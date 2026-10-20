// Package webhook posts events to the URLs people registered, signed the
// way the webhook contract says (X-CMS-Signature: sha256=<HMAC of the
// body>), and records each delivery.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/jsonv"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// Spawn runs fn in the background for as long as the app lives; false when
// it's closed (fn doesn't run).
type Spawn func(ctx context.Context, name string, fn func(context.Context)) bool

// Dispatcher posts events to the URLs people registered.
type Dispatcher struct {
	db     *store.DB
	key    *secret.Key
	log    *slog.Logger
	tenant string // the site's name in every delivery
	spawn  Spawn
	// pending is the deliveries under way, for Wait.
	pending sync.WaitGroup
}

// New is a dispatcher reading webhooks from db, opening their secrets with
// key, and delivering in the background with spawn.
func New(db *store.DB, key *secret.Key, log *slog.Logger, tenant string, spawn Spawn) *Dispatcher {
	return &Dispatcher{db: db, key: key, log: log, tenant: tenant, spawn: spawn}
}

// Hook is one registered receiver.
type Hook struct {
	ID                   int64
	Name, URL, Secret    string
	Kind                 string // Internal or External
	TestingURL           string // where a test goes instead of URL; "" for none
	Events, Headers      store.JSON
	EventFilters         store.JSON
	BodyMap              store.JSON // an external one's Fields
	Active               bool
	FailureCount         int
	LastDeliveryAt       store.Time
	LastStatus           string
	CreatedAt, UpdatedAt store.Time
}

const cols = `id, name, url, testing_url, secret, kind, body_map, events, headers, event_filters, active, failure_count, last_delivery_at, COALESCE(last_status, ''), created_at, updated_at`

// List is the webhooks matching where (all of them when it's "").
func (d *Dispatcher) List(ctx context.Context, where string, args ...any) ([]*Hook, error) {
	if where == "" {
		where = "1=1"
	}
	rows, err := d.db.QueryContext(ctx, "SELECT "+cols+" FROM webhooks WHERE "+where+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Hook{}
	for rows.Next() {
		h := &Hook{}
		if err := rows.Scan(&h.ID, &h.Name, &h.URL, &h.TestingURL, &h.Secret, &h.Kind, &h.BodyMap, &h.Events, &h.Headers, &h.EventFilters, &h.Active, &h.FailureCount, &h.LastDeliveryAt, &h.LastStatus, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		h.Events, h.Headers, h.EventFilters, h.BodyMap = h.Events.Or("[]"), h.Headers.Or("{}"), h.EventFilters.Or("{}"), h.BodyMap.Or("[]")
		out = append(out, h)
	}
	return out, rows.Err()
}

// secret reads the signing key. One Rails encrypted, one sealed with
// another install's key, or one an import cleared can't be read here and
// has to be rotated.
func (h *Hook) secret(s *secret.Key) (string, bool) {
	if plain, ok := s.Open(h.Secret); ok {
		return plain, true
	}
	if h.Secret == "" || strings.HasPrefix(h.Secret, "{") || strings.HasPrefix(h.Secret, secret.SealedPrefix) {
		return "", false
	}
	return h.Secret, true
}

// Deliver sends event to every active webhook that takes it (its events
// and filters), in the background: the request that caused it can end
// first.
func (d *Dispatcher) Deliver(ctx context.Context, event string, data any) {
	hooks, err := d.List(ctx, "active = 1")
	if err != nil {
		d.log.Warn("webhooks", "err", err)
		return
	}
	for _, h := range hooks {
		if !h.Takes(event, data) {
			continue
		}
		d.pending.Add(1)
		if !d.spawn(ctx, "webhook", func(ctx context.Context) {
			defer d.pending.Done()
			_, _ = d.Send(ctx, h, event, data) // the delivery's outcome is recorded
		}) {
			d.pending.Done()
		}
	}
}

// Wait is for tests: every delivery started has finished.
func (d *Dispatcher) Wait() { d.pending.Wait() }

// envelope is a delivery's body.
type envelope struct {
	Event       string `json:"event"`
	Tenant      string `json:"tenant"`
	DeliveredAt string `json:"delivered_at"`
	Data        any    `json:"data"`
}

// Outgoing is a delivery as it's sent: where, with which headers, and its
// body, byte for byte.
type Outgoing struct {
	URL    string
	Header http.Header
	Body   []byte
}

// Build is the delivery of event to h, made but not sent: an internal
// webhook's is the envelope, signed; an external one's is the body its
// fields build, unsigned. Its headers include h's own.
func (d *Dispatcher) Build(h *Hook, event string, data any) (*Outgoing, error) {
	external := h.Kind == External
	var body []byte
	var err error
	if external {
		body, err = json.Marshal(Body(Fields(h.BodyMap), data))
	} else {
		body, err = json.Marshal(envelope{event, d.tenant, time.Now().UTC().Format(time.RFC3339), data})
	}
	if err != nil {
		return nil, err
	}
	out := &Outgoing{URL: h.URL, Header: http.Header{}, Body: body}
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("User-Agent", "librepublish-webhooks/1")
	if !external {
		secret, ok := h.secret(d.key)
		if !ok {
			return out, fmt.Errorf("the signing secret can't be read here (another install's key, or cleared by an import): rotate it")
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		out.Header.Set("X-CMS-Event", event)
		out.Header.Set("X-CMS-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	for k, v := range h.Headers.Map() {
		out.Header.Set(k, jsonv.String(v))
	}
	return out, nil
}

// Send makes one delivery to h's URL and records it.
func (d *Dispatcher) Send(ctx context.Context, h *Hook, event string, data any) (status int, err error) {
	return d.send(ctx, h, event, data, h.URL, false)
}

// SendTest makes a test delivery to url (h's testing URL, or its own): it's
// recorded as a test, and doesn't change how h is shown to be doing.
func (d *Dispatcher) SendTest(ctx context.Context, h *Hook, event string, data any, url string) (status int, err error) {
	return d.send(ctx, h, event, data, url, true)
}

func (d *Dispatcher) send(ctx context.Context, h *Hook, event string, data any, url string, test bool) (status int, err error) {
	out, berr := d.Build(h, event, data)
	var body []byte
	if out != nil {
		body = out.Body
	}
	start := time.Now()
	defer func() {
		if rerr := d.record(ctx, h, event, data, body, test, status, time.Since(start), err); rerr != nil {
			d.log.Warn("webhook delivery record", "webhook", h.ID, "err", rerr)
		}
	}()
	if berr != nil {
		return 0, berr
	}
	if err := outbound.CheckURL(url); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header = out.Header.Clone()
	resp, err := outbound.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Read some of the answer, so the connection can be reused; it isn't kept.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}

// record writes down one delivery, with the event's data (what a test can
// replay), and, unless it was a test, the webhook's last status.
func (d *Dispatcher) record(ctx context.Context, h *Hook, event string, data any, body []byte, test bool, status int, took time.Duration, err error) error {
	success := err == nil && status >= 200 && status < 300
	var errText any
	if err != nil {
		errText = err.Error()
	} else if !success {
		errText = fmt.Sprintf("HTTP %d", status)
	}
	eventData, _ := json.Marshal(data) // nil when it can't be: then null
	_, ierr := d.db.ExecContext(ctx, `INSERT INTO webhook_deliveries (webhook_id, event, payload, event_data, test, response_status, success, duration_ms, error, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		h.ID, event, string(body), string(eventData), test, status, success, took.Milliseconds(), errText, store.Now().DB())
	var uerr error
	if test {
		return ierr
	}
	if success {
		_, uerr = d.db.ExecContext(ctx, `UPDATE webhooks SET last_delivery_at = ?, last_status = ?, failure_count = 0 WHERE id = ?`, store.Now().DB(), strconv.Itoa(status), h.ID)
	} else {
		_, uerr = d.db.ExecContext(ctx, `UPDATE webhooks SET last_delivery_at = ?, last_status = ?, failure_count = failure_count + 1 WHERE id = ?`, store.Now().DB(), fmt.Sprint(errText), h.ID)
	}
	return errors.Join(ierr, uerr)
}
