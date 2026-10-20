package cms

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/webhook"
)

func (a *App) listWebhooks(c *Ctx) error {
	if err := c.Need("webhooks:read"); err != nil {
		return err
	}
	list, err := a.hooks.List(c.R.Context(), "")
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, h := range list {
		out = append(out, webhookJSON(h))
	}
	return c.JSON(200, O("webhooks", out, "events", a.webhookEvents()))
}

// coreEvents are the events the CMS itself sends to webhooks.
var coreEvents = []string{"page.published", "page.updated", "page.unpublished", "page.deleted",
	"entry.published", "entry.updated", "entry.unpublished", "entry.deleted", "global.updated", schemaChangedEvent}

// webhookEvents is every event a webhook can take: the CMS's own, then the
// enabled plugins' (their manifests' events).
func (a *App) webhookEvents() []string {
	out := slices.Clone(coreEvents)
	for _, p := range a.plugins.List() {
		if !p.Enabled {
			continue
		}
		for _, e := range p.Manifest.Events {
			if !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
	}
	return out
}

func (a *App) webhookByID(c *Ctx) (*webhook.Hook, error) {
	id, err := c.IntParam("id")
	if err != nil {
		return nil, err
	}
	list, err := a.hooks.List(c.R.Context(), "id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apierr.NotFound("Couldn't find webhook " + c.Param("id") + ".")
	}
	return list[0], nil
}

func (a *App) showWebhook(c *Ctx) error {
	if err := c.Need("webhooks:read"); err != nil {
		return err
	}
	h, err := a.webhookByID(c)
	if err != nil {
		return err
	}
	return c.JSON(200, O("webhook", webhookJSON(h)))
}

func (a *App) saveWebhook(c *Ctx, h *webhook.Hook, p map[string]any) (string, error) {
	creating := h.ID == 0
	plain := ""
	if creating {
		h.Active, h.Kind, h.Events, h.Headers, h.EventFilters, h.BodyMap = true, webhook.Internal, store.JSON("[]"), store.JSON("{}"), store.JSON("{}"), store.JSON("[]")
		plain = str(p["secret"])
		if plain == "" {
			plain = secret.Token(32)
		}
		h.Secret = a.secrets.Seal(plain)
	}
	if has(p, "name") {
		h.Name = strings.TrimSpace(str(p["name"]))
	}
	if has(p, "url") {
		h.URL = strings.TrimSpace(str(p["url"]))
	}
	if has(p, "testing_url") {
		h.TestingURL = strings.TrimSpace(str(p["testing_url"]))
	}
	if has(p, "kind") {
		h.Kind = strings.TrimSpace(str(p["kind"]))
	}
	if has(p, "active") {
		h.Active = boolOf(p["active"])
	}
	if has(p, "events") {
		h.Events = store.MustJSON(p["events"]).Or("[]")
	}
	if has(p, "headers") {
		old := h.Headers.Map()
		next := map[string]any{}
		if m, ok := p["headers"].(map[string]any); ok {
			keepMasked(next, old, m)
		}
		h.Headers = store.MustJSON(next)
	}
	inv := apierr.Invalid{}
	if has(p, "event_filters") {
		h.EventFilters = store.MustJSON(eventFilters(p["event_filters"], inv)).Or("{}")
	}
	if has(p, "body_map") {
		h.BodyMap = store.MustJSON(bodyMap(p["body_map"], inv)).Or("[]")
	}
	if h.Name == "" {
		inv.Add("name", "can't be blank")
	}
	if outbound.CheckURL(h.URL) != nil {
		inv.Add("url", "must be an http(s) URL")
	}
	if h.TestingURL != "" && outbound.CheckURL(h.TestingURL) != nil {
		inv.Add("testing_url", "must be an http(s) URL")
	}
	if h.Kind != webhook.Internal && h.Kind != webhook.External {
		inv.Add("kind", "must be internal or external")
	}
	if err := inv.Err(); err != nil {
		return "", err
	}
	now := store.Now()
	if creating {
		res, err := a.store.ExecContext(c.R.Context(), `INSERT INTO webhooks (name, url, testing_url, secret, kind, body_map, events, headers, event_filters, active, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			h.Name, h.URL, h.TestingURL, h.Secret, h.Kind, string(h.BodyMap), string(h.Events), string(h.Headers), string(h.EventFilters), h.Active, now.DB(), now.DB())
		if err != nil {
			return "", err
		}
		h.ID, _ = res.LastInsertId()
		h.CreatedAt = now
	} else if _, err := a.store.ExecContext(c.R.Context(), `UPDATE webhooks SET name=?, url=?, testing_url=?, kind=?, body_map=?, events=?, headers=?, event_filters=?, active=?, updated_at=? WHERE id=?`,
		h.Name, h.URL, h.TestingURL, h.Kind, string(h.BodyMap), string(h.Events), string(h.Headers), string(h.EventFilters), h.Active, now.DB(), h.ID); err != nil {
		return "", err
	}
	h.UpdatedAt = now
	a.audit(c.R.Context(), c.Actor, map[bool]string{true: "webhook.created", false: "webhook.updated"}[creating], "Webhook", h.ID, h.Name, nil)
	return plain, nil
}

// eventFilters is a webhook's event_filters as {event: {path: [values]}},
// a value given alone made a list and an empty one dropped; what isn't
// that shape is an error on inv.
func eventFilters(v any, inv apierr.Invalid) map[string]any {
	out := map[string]any{}
	if v == nil {
		return out
	}
	m, ok := v.(map[string]any)
	if !ok {
		inv.Add("event_filters", "must be {event: {field: [values]}}")
		return out
	}
	for event, f := range m {
		fm, ok := f.(map[string]any)
		if !ok {
			inv.Add("event_filters."+event, "must be {field: [values]}")
			continue
		}
		next := map[string]any{}
		for path, want := range fm {
			list, ok := want.([]any)
			if !ok {
				list = []any{want}
			}
			vals := []string{}
			for _, x := range list {
				if s := strings.TrimSpace(str(x)); s != "" {
					vals = append(vals, s)
				}
			}
			if path = strings.TrimSpace(path); path != "" && len(vals) > 0 {
				next[path] = vals
			}
		}
		if len(next) > 0 {
			out[event] = next
		}
	}
	return out
}

// bodyMap is an external webhook's fields, checked: each with a key of its
// own, a value (a path, or the text to send), and a transform there is.
func bodyMap(v any, inv apierr.Invalid) []webhook.Field {
	out := []webhook.Field{}
	list, ok := v.([]any)
	if v != nil && !ok {
		inv.Add("body_map", "must be a list of {key, value, transform}")
		return out
	}
	seen := map[string]bool{}
	for i, x := range list {
		m, _ := x.(map[string]any)
		f := webhook.Field{Key: strings.TrimSpace(str(m["key"])), Value: strings.TrimSpace(str(m["value"])), Transform: strings.TrimSpace(str(m["transform"]))}
		at := fmt.Sprintf("body_map[%d].", i)
		switch {
		case f.Key == "":
			inv.Add(at+"key", "can't be blank")
		case seen[f.Key]:
			inv.Add(at+"key", "is already in the body")
		}
		seen[f.Key] = true
		if f.Value == "" && f.Transform != "text" {
			inv.Add(at+"value", "can't be blank")
		}
		if !slices.Contains(webhook.Transforms, f.Transform) {
			inv.Add(at+"transform", "isn't one there is")
		}
		out = append(out, f)
	}
	return out
}

func (a *App) createWebhook(c *Ctx) error {
	if err := c.Need("webhooks:write"); err != nil {
		return err
	}
	p, err := c.Wrapped("webhook")
	if err != nil {
		return err
	}
	h := &webhook.Hook{}
	secret, err := a.saveWebhook(c, h, p)
	if err != nil {
		return err
	}
	c.Summary = "Created. The signing secret is shown once: keep it with the receiver."
	return c.JSON(201, O("webhook", append(webhookJSON(h), kv{"secret", secret})))
}

func (a *App) updateWebhook(c *Ctx) error {
	if err := c.Need("webhooks:write"); err != nil {
		return err
	}
	h, err := a.webhookByID(c)
	if err != nil {
		return err
	}
	p, err := c.Wrapped("webhook")
	if err != nil {
		return err
	}
	if _, err := a.saveWebhook(c, h, p); err != nil {
		return err
	}
	return c.JSON(200, O("webhook", webhookJSON(h)))
}

func (a *App) destroyWebhook(c *Ctx) error {
	if err := c.Need("webhooks:delete"); err != nil {
		return err
	}
	h, err := a.webhookByID(c)
	if err != nil {
		return err
	}
	if _, err := a.store.ExecContext(c.R.Context(), `DELETE FROM webhook_deliveries WHERE webhook_id = ?`, h.ID); err != nil {
		return err
	}
	if _, err := a.store.ExecContext(c.R.Context(), `DELETE FROM webhooks WHERE id = ?`, h.ID); err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "webhook.deleted", "Webhook", h.ID, h.Name, nil)
	return c.NoContent()
}

func (a *App) rotateWebhookSecret(c *Ctx) error {
	if err := c.Need("webhooks:write"); err != nil {
		return err
	}
	h, err := a.webhookByID(c)
	if err != nil {
		return err
	}
	plain := secret.Token(32)
	if _, err := a.store.ExecContext(c.R.Context(), `UPDATE webhooks SET secret = ?, updated_at = ? WHERE id = ?`, a.secrets.Seal(plain), store.Now().DB(), h.ID); err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "webhook.secret_rotated", "Webhook", h.ID, h.Name, nil)
	return c.JSON(200, O("webhook", append(webhookJSON(h), kv{"secret", plain})))
}

// webhookTry is a test's or a preview's request: which event, with what
// data (blank: the latest one delivered that this webhook takes, else a
// sample), and, for a test, where to ("testing", its testing URL, or
// "live", its own).
type webhookTry struct {
	Event string         `json:"event"`
	Data  map[string]any `json:"data"`
	To    string         `json:"to"`
}

// tryData is the event and data a test or preview uses, and where the data
// came from: "given", "delivery" (the latest of that event delivered that
// h takes, for a real look) or "sample".
func (a *App) tryData(c *Ctx, h *webhook.Hook, t webhookTry) (event string, data any, from string, err error) {
	event = strings.TrimSpace(t.Event)
	if event == "" {
		event = "webhook.test"
		for _, e := range h.Events.List() {
			if s := str(e); s != "*" {
				event = s
				break
			}
		}
	}
	if t.Data != nil {
		return event, t.Data, "given", nil
	}
	rows, err := a.store.QueryContext(c.R.Context(), `SELECT event_data FROM webhook_deliveries WHERE event = ? AND test = 0 AND event_data IS NOT NULL AND event_data != 'null' ORDER BY id DESC LIMIT 50`, event)
	if err != nil {
		return "", nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return "", nil, "", err
		}
		var d any
		if json.Unmarshal([]byte(raw), &d) == nil && h.Takes(event, d) {
			return event, d, "delivery", nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", nil, "", err
	}
	if event == "webhook.test" {
		return event, map[string]any{"message": "A test delivery from Libre Publish.", "webhook_id": h.ID}, "sample", nil
	}
	return event, sampleFor(h, event), "sample", nil
}

// sampleFor is sample data for event shaped to h: each path its body reads
// that the sample lacks gets a value (a date for a date field), and each of
// its conditions on the event is met, so the preview shows what h sends.
func sampleFor(h *webhook.Hook, event string) map[string]any {
	d := sampleEventData(event)
	if h.Kind == webhook.External {
		for _, f := range webhook.Fields(h.BodyMap) {
			if f.Transform == "text" || webhook.Lookup(d, f.Value) != nil {
				continue
			}
			v := "Sample " + f.Value[strings.LastIndex(f.Value, ".")+1:]
			switch f.Transform {
			case "date_us":
				v = store.Now().Format(time.DateOnly)
			case "number":
				v = "1"
			}
			setPath(d, f.Value, v)
		}
	}
	filters, _ := h.EventFilters.Map()[event].(map[string]any)
	for path, want := range filters {
		if list, _ := want.([]any); len(list) > 0 {
			setPath(d, path, list[0])
		}
	}
	return d
}

// setPath puts v at a dotted path in m, making the maps on the way.
func setPath(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}

// sampleEventData is made-up data in the shape event sends (the contract's).
func sampleEventData(event string) map[string]any {
	now := store.Now().UTC().Format(time.RFC3339)
	kind, _, _ := strings.Cut(event, ".")
	switch kind {
	case "page":
		return map[string]any{"id": 1, "slug": "about", "path": "about", "url": "https://example.com/about", "title": "About us", "status": "published", "locale": "en", "published_at": now, "updated_at": now}
	case "entry":
		return map[string]any{"id": 1, "collection_slug": "posts", "slug": "first-post", "url": "https://example.com/posts/first-post", "title": "First post", "status": "published", "locale": "en", "published_at": now, "updated_at": now}
	case "global":
		return map[string]any{"id": 1, "slug": "nav", "name": "Navigation", "updated_at": now}
	case "schema":
		return map[string]any{"schema_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"}
	case "submission":
		return map[string]any{"form": "contact", "form_name": "Contact", "submission_id": "sample", "submitted_at": now, "hook": map[string]any{},
			"values": map[string]any{"name": "Ada Lovelace", "first_name": "Ada", "last_name": "Lovelace", "email": "ada@example.com", "phone": "(555) 010-0100", "message": "A sample message."}}
	}
	return map[string]any{}
}

// tryJSON is a delivery as a preview or test shows it: where it goes, its
// headers (h's own masked), and its body exactly.
func tryJSON(h *webhook.Hook, out *webhook.Outgoing, event string, data any, from string) Obj {
	headers := map[string]string{}
	masked, _ := redact(h.Headers.Map()).(map[string]any) // a map, as given
	for k := range out.Header {
		headers[k] = out.Header.Get(k)
	}
	for k, v := range masked {
		headers[http.CanonicalHeaderKey(k)] = str(v)
	}
	return O("event", event, "data", data, "data_from", from, "url", h.URL, "testing_url", h.TestingURL, "headers", headers,
		"body", rawJSON(string(out.Body)), "body_text", string(out.Body))
}

// previewWebhook is what a delivery of an event would be, exactly, without
// sending it.
func (a *App) previewWebhook(c *Ctx) error {
	if err := c.Need("webhooks:read"); err != nil {
		return err
	}
	h, err := a.webhookByID(c)
	if err != nil {
		return err
	}
	var t webhookTry
	if err := c.OptionalBody(&t); err != nil {
		return err
	}
	event, data, from, err := a.tryData(c, h, t)
	if err != nil {
		return err
	}
	out, err := a.hooks.Build(h, event, data)
	if err != nil {
		return &apierr.Error{Status: 422, Code: "invalid", Message: err.Error()}
	}
	return c.JSON(200, tryJSON(h, out, event, data, from))
}

// testWebhook sends a test delivery: to the testing URL ("to": "testing"),
// or the webhook's own. It's recorded as a test, and doesn't change how the
// webhook is shown to be doing. With no body, the webhook.test event.
func (a *App) testWebhook(c *Ctx) error {
	if err := c.Need("webhooks:write"); err != nil {
		return err
	}
	h, err := a.webhookByID(c)
	if err != nil {
		return err
	}
	t := webhookTry{Event: "webhook.test"}
	if err := c.OptionalBody(&t); err != nil {
		return err
	}
	url := h.URL
	if t.To == "testing" {
		if h.TestingURL == "" {
			return &apierr.Error{Status: 422, Code: "invalid", Message: "This webhook has no testing URL: add one, or send to its own.", Errors: map[string][]string{"testing_url": {"can't be blank to send a test there"}}}
		}
		url = h.TestingURL
	}
	event, data, from, err := a.tryData(c, h, t)
	if err != nil {
		return err
	}
	status, err := a.hooks.SendTest(c.R.Context(), h, event, data, url)
	body := O("ok", err == nil && status >= 200 && status < 300, "response_status", status, "sent_to", url)
	if out, berr := a.hooks.Build(h, event, data); berr == nil {
		body = append(body, tryJSON(h, out, event, data, from)...)
	}
	if err != nil {
		body = append(body, kv{"error", err.Error()})
	}
	return c.JSON(200, body)
}

func (a *App) webhookDeliveries(c *Ctx) error {
	if err := c.Need("webhooks:read"); err != nil {
		return err
	}
	h, err := a.webhookByID(c)
	if err != nil {
		return err
	}
	rows, err := a.store.QueryContext(c.R.Context(), `SELECT id, event, payload, test, response_status, success, duration_ms, error, created_at FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT 100`, h.ID)
	if err != nil {
		return err
	}
	out, err := store.ScanAll(rows, func(r *sql.Rows) (Obj, error) {
		var id int64
		var event, payload string
		var status, ms store.NullInt
		var ok, test bool
		var errText sql.NullString
		var at store.Time
		if err := r.Scan(&id, &event, &payload, &test, &status, &ok, &ms, &errText, &at); err != nil {
			return nil, err
		}
		return O("id", id, "event", event, "payload", rawJSON(payload), "test", test, "response_status", status, "success", ok, "duration_ms", ms, "error", nullStr(errText), "created_at", at), nil
	})
	if err != nil {
		return err
	}
	if out == nil {
		out = []Obj{}
	}
	return c.JSON(200, O("deliveries", out))
}

// webhookJSON is a webhook as the API shows it, its headers redacted.
func webhookJSON(h *webhook.Hook) Obj {
	return O("id", h.ID, "name", h.Name, "url", h.URL, "testing_url", h.TestingURL, "kind", h.Kind, "events", h.Events, "event_filters", h.EventFilters, "body_map", h.BodyMap, "headers", redact(h.Headers.Map()),
		"active", h.Active, "failure_count", h.FailureCount, "last_delivery_at", h.LastDeliveryAt, "last_status", h.LastStatus,
		"created_at", h.CreatedAt, "updated_at", h.UpdatedAt)
}

// rawJSON is a delivery's recorded body as JSON, nil when it isn't (one
// that couldn't be made).
func rawJSON(s string) any {
	if !json.Valid([]byte(s)) {
		return nil
	}
	return json.RawMessage(s)
}
