package plugin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// API is what one call of a plugin reaches of the host: the capabilities it
// was granted, scoped to its own key. One that wasn't granted answers an
// error.
type API struct {
	h      *Host
	key    string
	grants Grants
	events []string // what it may Emit: its manifest's events
}

func (a *API) need(name string, granted bool) error {
	if !granted {
		return fmt.Errorf("%s wasn't granted to this plugin", name)
	}
	return nil
}

// Key is the plugin's own key.
func (a *API) Key() string { return a.key }

// Log writes a message to the server log, under the plugin's key.
func (a *API) Log(msg string) {
	a.h.log.Info("plugin", "plugin", a.key, "message", truncate(msg, 2000))
}

// Site is the site the plugin runs on.
func (a *API) Site(ctx context.Context) (Site, error) { return a.h.env.Site(ctx) }

// Emit sends one of the plugin's events (its manifest's Events) to the
// webhooks that take it and the plugins that hook it, in the background.
func (a *API) Emit(ctx context.Context, event string, data any) error {
	if !slices.Contains(a.events, event) {
		return fmt.Errorf("%s isn't one of this plugin's events", event)
	}
	a.h.env.Deliver(ctx, event, data)
	return nil
}

// Content is live content, shaped as /api/v1 shapes it (content:read).
func (a *API) Content(ctx context.Context, q ContentQuery) (any, error) {
	if err := a.need("content:read", a.grants.ContentRead); err != nil {
		return nil, err
	}
	return a.h.env.Content(ctx, q)
}

// Mail sends m through Settings › Email, from its defaults where blank
// (mail): at most 50 recipients, 200 messages an hour.
func (a *API) Mail(ctx context.Context, m mail.Message) error {
	if err := a.need("mail", a.grants.Mail); err != nil {
		return err
	}
	if !a.h.mail.allow(a.key) {
		return errors.New("this plugin has sent 200 emails in the last hour; try later")
	}
	if err := a.h.env.SendMail(ctx, m); err != nil {
		return err
	}
	a.h.log.Info("plugin mail", "plugin", a.key, "to", len(m.To)+len(m.Cc)+len(m.Bcc), "subject", truncate(m.Subject, 120))
	return nil
}

// PutMedia adds a file of at most 10 MB to the media library (media:write),
// in Plugins/<key> unless it names a folder, and answers the asset as the
// media API shows it.
func (a *API) PutMedia(ctx context.Context, f Media) (map[string]any, error) {
	if err := a.need("media:write", a.grants.MediaWrite); err != nil {
		return nil, err
	}
	if f.Filename == "" || len(f.Data) == 0 {
		return nil, errors.New("a file needs a filename and data")
	}
	if len(f.Data) > maxBody {
		return nil, errors.New("a file from a plugin is at most 10 MB")
	}
	if f.Folder == "" {
		f.Folder = "Plugins/" + a.key
	}
	return a.h.env.PutMedia(ctx, f)
}

// KVGet is the value stored under key, and whether there is one (storage).
func (a *API) KVGet(ctx context.Context, key string) (string, bool, error) {
	if err := a.need("storage", a.grants.Storage); err != nil {
		return "", false, err
	}
	var v []byte
	err := a.h.db.QueryRowContext(ctx, `SELECT value FROM plugin_kv WHERE plugin_key = ? AND key = ?`, a.key, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return string(v), err == nil, err
}

// KVSet stores value (at most 1 MB) under key, of at most 100,000 (storage).
func (a *API) KVSet(ctx context.Context, key, value string) error {
	if err := a.need("storage", a.grants.Storage); err != nil {
		return err
	}
	if key == "" {
		return errors.New("a key can't be blank")
	}
	if len(value) > 1<<20 {
		return errors.New("a value is at most 1 MB")
	}
	n, err := store.Count(ctx, a.h.db, `SELECT count(*) FROM plugin_kv WHERE plugin_key = ?`, a.key)
	if err != nil {
		return err
	}
	if n > 100000 {
		return errors.New("this plugin's storage is full (100,000 keys)")
	}
	_, err = a.h.db.ExecContext(ctx, `INSERT INTO plugin_kv (plugin_key, key, value, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(plugin_key, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, a.key, key, []byte(value), store.Now().DB())
	return err
}

// KVAdd adds delta to the number stored under key (0 when there's none, or
// it isn't a number) in one statement, so concurrent adds all count, and
// answers the sum (storage).
func (a *API) KVAdd(ctx context.Context, key string, delta int64) (int64, error) {
	if err := a.need("storage", a.grants.Storage); err != nil {
		return 0, err
	}
	if key == "" {
		return 0, errors.New("a key can't be blank")
	}
	n, err := store.Count(ctx, a.h.db, `SELECT count(*) FROM plugin_kv WHERE plugin_key = ?`, a.key)
	if err != nil {
		return 0, err
	}
	if n > 100000 {
		return 0, errors.New("this plugin's storage is full (100,000 keys)")
	}
	var sum int64
	err = a.h.db.QueryRowContext(ctx, `INSERT INTO plugin_kv (plugin_key, key, value, updated_at) VALUES (?,?,CAST(? AS TEXT),?)
		ON CONFLICT(plugin_key, key) DO UPDATE SET value = CAST(CAST(CAST(value AS TEXT) AS INTEGER) + ? AS TEXT), updated_at = excluded.updated_at
		RETURNING CAST(CAST(value AS TEXT) AS INTEGER)`, a.key, key, delta, store.Now().DB(), delta).Scan(&sum)
	return sum, err
}

// KVDelete removes key (storage).
func (a *API) KVDelete(ctx context.Context, key string) error {
	if err := a.need("storage", a.grants.Storage); err != nil {
		return err
	}
	_, err := a.h.db.ExecContext(ctx, `DELETE FROM plugin_kv WHERE plugin_key = ? AND key = ?`, a.key, key)
	return err
}

func likePrefix(prefix string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(prefix) + "%"
}

// KVList is a page of the keys under prefix after after, in order, at most
// limit (1000 when it's 0 or more), and the next page's after ("" at the
// end) (storage).
func (a *API) KVList(ctx context.Context, prefix, after string, limit int) (keys []string, next string, err error) {
	if err := a.need("storage", a.grants.Storage); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	keys, err = store.Strings(ctx, a.h.db, `SELECT key FROM plugin_kv WHERE plugin_key = ? AND key LIKE ? ESCAPE '\' AND key > ? ORDER BY key LIMIT ?`, a.key, likePrefix(prefix), after, limit+1)
	if err != nil {
		return nil, "", err
	}
	if len(keys) > limit {
		keys, next = keys[:limit], keys[limit-1]
	}
	return keys, next, nil
}

// KVAll is every key under prefix (storage).
func (a *API) KVAll(ctx context.Context, prefix string) ([]string, error) {
	var all []string
	after := ""
	for {
		keys, next, err := a.KVList(ctx, prefix, after, 1000)
		if err != nil {
			return nil, err
		}
		all = append(all, keys...)
		if next == "" {
			return all, nil
		}
		after = next
	}
}

// KVCount is how many keys there are under prefix (storage).
func (a *API) KVCount(ctx context.Context, prefix string) (int, error) {
	if err := a.need("storage", a.grants.Storage); err != nil {
		return 0, err
	}
	return store.Count(ctx, a.h.db, `SELECT count(*) FROM plugin_kv WHERE plugin_key = ? AND key LIKE ? ESCAPE '\'`, a.key, likePrefix(prefix))
}

// HTTP is a client that reaches only the hosts granted (http), redirects
// included, and reads at most 4 MB of a response. SiteHosts among them
// grants the site's own origins' hosts, as they are at each request.
func (a *API) HTTP() *http.Client {
	t := &hostsOnly{hosts: a.grants.HTTP, next: http.DefaultTransport}
	if slices.Contains(a.grants.HTTP, SiteHosts) && a.h != nil {
		t.site = func(ctx context.Context) []string {
			s, err := a.h.env.Site(ctx)
			if err != nil {
				return nil
			}
			return siteHosts(s)
		}
	}
	return &http.Client{Timeout: callTimeout, Transport: t}
}

// siteHosts is the hosts of the site's public URL and allowed origins.
func siteHosts(s Site) []string {
	out := []string{}
	for _, o := range append([]string{s.URL}, s.Origins...) {
		if u, err := url.Parse(strings.TrimSpace(o)); err == nil && u.Hostname() != "" {
			out = append(out, u.Hostname())
		}
	}
	return out
}

// hostsOnly refuses requests to hosts not granted, and caps responses.
type hostsOnly struct {
	hosts []string
	site  func(ctx context.Context) []string // the site's hosts, when SiteHosts is granted
	next  http.RoundTripper
}

const maxHTTPResponse = 4 << 20

func (t *hostsOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	host := r.URL.Hostname()
	if !slices.Contains(t.hosts, host) && (t.site == nil || !slices.Contains(t.site(r.Context()), host)) {
		return nil, fmt.Errorf("http to %s wasn't granted to this plugin", r.URL.Hostname())
	}
	res, err := t.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	res.Body = &capped{ReadCloser: res.Body, left: maxHTTPResponse}
	return res, nil
}

// capped errors once more than its limit has been read.
type capped struct {
	io.ReadCloser
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errors.New("the response is larger than a plugin may read (4 MB)")
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.ReadCloser.Read(p)
	c.left -= int64(n)
	return n, err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// mailLimiter caps what each plugin sends: 200 messages an hour.
type mailLimiter struct {
	mu    sync.Mutex
	sends map[string][]time.Time
}

func (l *mailLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sends == nil {
		l.sends = map[string][]time.Time{}
	}
	cut := time.Now().Add(-time.Hour)
	kept := l.sends[key][:0]
	for _, t := range l.sends[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 200 {
		l.sends[key] = kept
		return false
	}
	l.sends[key] = append(kept, time.Now())
	return true
}
