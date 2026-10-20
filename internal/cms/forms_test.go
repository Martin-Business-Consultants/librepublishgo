package cms

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
)

// captureMailer keeps what would have been sent.
type captureMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

type sentMail struct {
	cfg  mail.Config
	from string
	to   []string
	raw  string
}

func (m *captureMailer) Send(ctx context.Context, cfg mail.Config, from string, to []string, msg []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMail{cfg, from, to, string(msg)})
	return nil
}

func (m *captureMailer) take() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sent
	m.sent = nil
	return out
}

func header(raw, name string) string {
	for line := range strings.SplitSeq(raw, "\r\n") {
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ": "); ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// decodeQP is a message with its quoted-printable parts decoded.
func decodeQP(raw string) string {
	b, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(raw)))
	return string(b)
}

func TestEmailSettings(t *testing.T) {
	t.Parallel()
	mailer := &captureMailer{}
	s := newSiteWith(t, Config{NoDefaultPlugins: true, Mailer: mailer})
	r := s.must(422, s.do(s.admin, "POST", "/api/settings/email/test", map[string]any{"to": "me@acme.test"}))
	if !strings.Contains(str(r.Body["message"]), "Settings › Email") {
		t.Fatalf("unconfigured: %s", r.Raw)
	}
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/email", map[string]any{"setting": map[string]any{"data": map[string]any{
		"smtp_host": "smtp.acme.test", "smtp_username": "acme", "smtp_password": "hunter22", "from_name": "Acme", "from_email": "hello@acme.test"}}}))
	// The password is sealed at rest and masked when read.
	var raw string
	s.app.store.QueryRowContext(t.Context(), `SELECT data FROM settings WHERE key = 'email'`).Scan(&raw)
	if strings.Contains(raw, "hunter22") || !strings.Contains(raw, secret.SealedPrefix) {
		t.Fatalf("password at rest: %s", raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/settings/email", nil))
	if pw := str(dig(r.Body, "setting", "data", "smtp_password")); !strings.HasPrefix(pw, "***") {
		t.Fatalf("password shown: %s", r.Raw)
	}
	// Saving the masked value back keeps the password.
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/email", map[string]any{"setting": map[string]any{"data": map[string]any{"smtp_password": dig(r.Body, "setting", "data", "smtp_password")}}}))
	if cfg := s.app.mailConfig(t.Context()); cfg.Password != "hunter22" || cfg.Port != 587 || cfg.Security != "starttls" {
		t.Fatalf("config: %+v", cfg)
	}
	s.must(200, s.do(s.admin, "POST", "/api/settings/email/test", map[string]any{"to": "me@acme.test"}))
	got := mailer.take()
	if len(got) != 1 || got[0].from != "hello@acme.test" || header(got[0].raw, "From") != `"Acme" <hello@acme.test>` || got[0].to[0] != "me@acme.test" {
		t.Fatalf("test mail: %+v", got)
	}
	// No header injection through a name or a subject.
	err := s.app.sendMail(t.Context(), mail.Message{FromName: "x\r\nBcc: evil@x.test", To: []string{"a@acme.test"}, Subject: "hi\r\nBcc: evil@x.test", Text: "."})
	if err != nil {
		t.Fatal(err)
	}
	if m := mailer.take()[0]; strings.Contains(m.raw, "\r\nBcc:") || len(m.to) != 1 {
		t.Fatalf("injection: %q", m.raw)
	}
	if err := s.app.sendMail(t.Context(), mail.Message{To: []string{"not an address"}, Subject: "x", Text: "x"}); err == nil {
		t.Fatal("a bad address was sent to")
	}
}

func TestFormsPlugin(t *testing.T) {
	t.Parallel()
	mail := &captureMailer{}
	s := newSiteWith(t, Config{Mailer: mail})

	// It came with the binary: installed, granted and on.
	r := s.must(200, s.do(s.admin, "GET", "/api/plugins/forms", nil))
	if dig(r.Body, "plugin", "enabled") != true || dig(r.Body, "plugin", "granted", "mail") != true || dig(r.Body, "plugin", "granted", "media:write") != true {
		t.Fatalf("default plugin: %s", r.Raw)
	}
	// Its capabilities join the role editor's catalog.
	r = s.must(200, s.do(s.admin, "GET", "/api/roles", nil))
	if !strings.Contains(string(r.Raw), `"forms:write"`) {
		t.Fatalf("catalog: %s", r.Raw)
	}
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/email", map[string]any{"setting": map[string]any{"data": map[string]any{
		"smtp_host": "smtp.acme.test", "from_name": "Acme", "from_email": "hello@acme.test"}}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/general", map[string]any{"setting": map[string]any{"data": map[string]any{"title": "Acme", "site_base_url": "https://acme.test"}}}))

	// A new form starts from a sensible draft.
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/form", nil))
	if dig(r.Body, "form", "fields", 1, "type") != "email" || !strings.Contains(str(dig(r.Body, "form", "client_message")), "{{ all_fields }}") || dig(r.Body, "form", "captcha") != "none" {
		t.Fatalf("draft: %s", r.Raw)
	}
	// The agent has no forms capability.
	s.must(403, s.do(s.agent, "GET", "/api/x/forms/forms", nil))

	form := map[string]any{
		"name": "Contact us", "status": "active", "store_submissions": true, "honeypot": true, "min_seconds": 2,
		"fields": []any{
			map[string]any{"type": "heading", "text": "Say hello"},
			map[string]any{"type": "text", "name": "name", "label": "Name", "required": true},
			map[string]any{"type": "email", "name": "email", "label": "Email", "required": true},
			map[string]any{"type": "select", "name": "topic", "label": "Topic", "options": []any{"Sales", "Support"}},
			map[string]any{"type": "textarea", "name": "message", "label": "Message"},
			map[string]any{"type": "file", "name": "brief", "label": "Brief", "accept": ".txt,.pdf", "max_mb": 1},
		},
		"admin_enabled": true, "admin_to": []any{"owner@acme.test"}, "admin_cc": []any{"team@acme.test"},
		"admin_subject": "{{form_name}}: {{name}}", "admin_reply_to_field": "email", "admin_from_name": "Acme Forms",
		"client_enabled": true, "client_to_field": "email", "client_subject": "Thanks, {{name}}",
		"client_message": "Hi {{name}},\n\n{{all_fields}}", "client_from_email": "noreply@acme.test", "client_reply_to": "help@acme.test",
		"success_message": "Thanks!",
	}
	bad := map[string]any{}
	maps.Copy(bad, form)
	bad["name"], bad["admin_to"], bad["captcha"] = "", []any{"nope"}, "recaptcha"
	r = s.must(422, s.do(s.admin, "POST", "/api/x/forms/form", map[string]any{"form": bad}))
	if dig(r.Body, "errors", "name", 0) != "can't be blank" || dig(r.Body, "errors", "admin_to", 0) != "nope isn't an email address" || dig(r.Body, "errors", "recaptcha_secret", 0) != "can't be blank" {
		t.Fatalf("validation: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "POST", "/api/x/forms/form", map[string]any{"form": form}))
	if dig(r.Body, "form", "slug") != "contact-us" {
		t.Fatalf("save: %s", r.Raw)
	}

	// What the site draws it from.
	r = s.must(200, s.do("", "GET", "/api/x/forms/definition?form=contact-us", nil))
	if dig(r.Body, "form", "fields", 1, "name") != "name" || dig(r.Body, "form", "spam", "honeypot_field") != "_hp" || dig(r.Body, "form", "fields", 0, "id") != nil {
		t.Fatalf("definition: %s", r.Raw)
	}

	ts := strconv.FormatInt(time.Now().Add(-10*time.Second).UnixMilli(), 10)
	// Invalid answers come back per field.
	r = s.must(422, s.do("", "POST", "/api/x/forms/submit?form=contact-us", map[string]any{"name": "Ada", "email": "not-an-email", "topic": "Other", "_ts": ts}))
	if dig(r.Body, "errors", "email", 0) != "isn't an email address" || dig(r.Body, "errors", "topic", 0) != "isn't one of the choices" {
		t.Fatalf("submit errors: %s", r.Raw)
	}
	mail.take()

	r = s.must(200, s.do("", "POST", "/api/x/forms/submit?form=contact-us", map[string]any{
		"name": "Ada", "email": "ada@example.test", "topic": "Sales", "message": "=1+1 hello <script>x</script>", "_ts": ts, "_hp": ""}))
	if r.Body["message"] != "Thanks!" {
		t.Fatalf("submit: %s", r.Raw)
	}
	sent := mail.take()
	if len(sent) != 2 {
		t.Fatalf("want 2 emails, got %d", len(sent))
	}
	admin, client := sent[0], sent[1]
	adminBody := decodeQP(admin.raw)
	_, adminHTML, ok := strings.Cut(adminBody, "text/html")
	if !ok {
		t.Fatalf("admin email has no HTML part:\n%s", adminBody)
	}
	if header(admin.raw, "From") != `"Acme Forms" <hello@acme.test>` || header(admin.raw, "Reply-To") != "ada@example.test" ||
		header(admin.raw, "To") != "owner@acme.test" || header(admin.raw, "Cc") != "team@acme.test" || !strings.Contains(admin.raw, "Contact us: Ada") {
		t.Fatalf("admin email:\n%s", admin.raw)
	}
	// The admin's message is the default, HTML: answers escaped, as a
	// table, with a plain-text copy.
	if !strings.Contains(admin.raw, "multipart/alternative") || !strings.Contains(adminHTML, "&lt;script&gt;") || strings.Contains(adminHTML, "<script>x") ||
		!strings.Contains(adminHTML, "<table") || !strings.Contains(adminBody, "Topic: Sales") {
		t.Fatalf("admin html:\n%s", adminBody)
	}
	if client.from != "noreply@acme.test" || header(client.raw, "From") != `"Acme" <noreply@acme.test>` || header(client.raw, "To") != "ada@example.test" ||
		header(client.raw, "Reply-To") != "help@acme.test" || !strings.Contains(client.raw, "Thanks, Ada") || !strings.Contains(client.raw, "Topic: Sales") {
		t.Fatalf("client email:\n%s", client.raw)
	}

	// Spam is kept, flagged, and not emailed; the bot is told it worked.
	s.must(200, s.do("", "POST", "/api/x/forms/submit?form=contact-us", map[string]any{"name": "Bot", "email": "bot@spam.test", "_hp": "http://spam", "_ts": ts}))
	s.must(200, s.do("", "POST", "/api/x/forms/submit?form=contact-us", map[string]any{"name": "Fast", "email": "fast@spam.test", "_ts": strconv.FormatInt(time.Now().UnixMilli(), 10)}))
	if n := len(mail.take()); n != 0 {
		t.Fatalf("spam was emailed (%d)", n)
	}

	// A plain HTML form is answered with a redirect back to its page.
	body := url.Values{"name": {"Grace"}, "email": {"grace@example.test"}, "_ts": {ts}}.Encode()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.srv.URL+"/api/x/forms/submit?form=contact-us", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Referer", "https://acme.test/contact")
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "https://acme.test/contact?form=contact-us&form_status=sent" {
		t.Fatalf("html post: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	mail.take()

	// A file goes into the media library; one too large or of the wrong
	// type is refused.
	var mp bytes.Buffer
	w := multipart.NewWriter(&mp)
	w.WriteField("name", "Linus")
	w.WriteField("email", "linus@example.test")
	w.WriteField("_ts", ts)
	fw, _ := w.CreateFormFile("brief", "brief.txt")
	fw.Write([]byte("the brief"))
	w.Close()
	r = s.must(200, s.do("", "POST", "/api/x/forms/submit?form=contact-us", mp.Bytes(), "Content-Type", w.FormDataContentType()))
	r = s.must(200, s.do(s.admin, "GET", "/api/assets?folder="+url.QueryEscape("Form uploads/contact-us"), nil))
	if !strings.Contains(string(r.Raw), "brief.txt") {
		t.Fatalf("upload not in media: %s", r.Raw)
	}
	mp.Reset()
	w = multipart.NewWriter(&mp)
	w.WriteField("name", "Eve")
	w.WriteField("email", "eve@example.test")
	fw, _ = w.CreateFormFile("brief", "brief.exe")
	fw.Write([]byte("MZ"))
	w.Close()
	r = s.must(422, s.do("", "POST", "/api/x/forms/submit?form=contact-us", mp.Bytes(), "Content-Type", w.FormDataContentType()))
	if !strings.Contains(str(dig(r.Body, "errors", "brief", 0)), "allowed type") {
		t.Fatalf("file type: %s", r.Raw)
	}
	mail.take()

	// The admin's lists: newest first, spam apart, counted.
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submissions?form=contact-us", nil))
	rows, _ := r.Body["submissions"].([]any)
	if len(rows) != 3 || dig(rows, 0, "summary") != "Linus" || dig(r.Body, "statuses", 3, "count") != float64(2) {
		t.Fatalf("submissions: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submissions?status=spam", nil))
	if rows, _ := r.Body["submissions"].([]any); len(rows) != 2 {
		t.Fatalf("spam: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/forms", nil))
	if dig(r.Body, "forms", 0, "submissions") != float64(3) || dig(r.Body, "forms", 0, "new") != float64(3) {
		t.Fatalf("forms: %s", r.Raw)
	}
	id := str(dig(rows, 2, "id"))
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submission?id="+id, nil))
	if dig(r.Body, "submission", "answers", 0, "value") != "Ada" || dig(r.Body, "submission", "mail", 0, "value") != "sent to 2" {
		t.Fatalf("submission: %s", r.Raw)
	}
	// Opening it read it.
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submissions?count=new", nil))
	if r.Body["count"] != float64(2) {
		t.Fatalf("unread count: %s", r.Raw)
	}

	// CSV: a column per field, and a formula a visitor typed stays text.
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submissions.csv?form=contact-us", nil))
	csv := string(r.Raw)
	if !strings.Contains(csv, "Submitted,Form,Status,Name,Email,Topic,Message,Brief") || !strings.Contains(csv, "'=1+1 hello") ||
		!strings.Contains(r.Header.Get("Content-Disposition"), "contact-us-submissions.csv") {
		t.Fatalf("csv: %s", csv)
	}

	// Or to the _return it sent, when that's on the site; never elsewhere.
	plainPost := func(ret, referer string) string {
		t.Helper()
		body := url.Values{"name": {"Grace"}, "email": {"grace@example.test"}, "_ts": {ts}, "_return": {ret}}.Encode()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.srv.URL+"/api/x/forms/submit?form=contact-us", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Referer", referer)
		res, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.Header.Get("Location")
	}
	if got := plainPost("https://acme.test/contact?step=2#top", "https://cms.example/preview"); got != "https://acme.test/contact?form=contact-us&form_status=sent&step=2" {
		t.Fatalf("_return: %s", got)
	}
	for _, ret := range []string{"https://evil.test/contact", "//evil.test/x", "javascript:alert(1)", "https://acme.test@evil.test/", "/contact"} {
		if got := plainPost(ret, "https://acme.test/from"); got != "https://acme.test/from?form=contact-us&form_status=sent" {
			t.Fatalf("_return %q went to %s", ret, got)
		}
	}
	mail.take()

	// Closed forms take nothing.
	form["status"] = "closed"
	s.must(200, s.do(s.admin, "POST", "/api/x/forms/form?id=contact-us", map[string]any{"form": form}))
	s.must(410, s.do("", "POST", "/api/x/forms/submit?form=contact-us", map[string]any{"name": "Late", "email": "late@example.test"}))

	// Removing a default plugin removes it for good: it isn't put back.
	s.must(204, s.do(s.admin, "DELETE", "/api/plugins/forms", nil))
	s.app.plugins.EnsureDefaults(t.Context())
	s.must(404, s.do(s.admin, "GET", "/api/plugins/forms", nil))
}

// A field can be shown only when another's answer says so, and a form's
// hook hears of each submission first: it may refuse it, send the visitor
// on, and add values that are kept and mailed. Then webhooks hear of it.
func TestFormsShowIfAndHook(t *testing.T) {
	t.Parallel()
	mailer := &captureMailer{}
	s := newSiteWith(t, Config{Mailer: mailer})

	var mu sync.Mutex
	var hookBodies [][]byte
	var hookSigs []string
	hookAnswer := func(w http.ResponseWriter, values map[string]any) {
		if values["zip"] == "00000" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"message":"We don't serve that zip yet.","errors":{"zip":["isn't in our service area"]}}`)
			return
		}
		if values["zip"] == "99999" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, `{"redirect":"/thanks?q=Q-42","values":{"quote_id":"Q-42","bad key":"x"}}`)
	}
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hookBodies, hookSigs = append(hookBodies, b), append(hookSigs, r.Header.Get("X-CMS-Signature"))
		mu.Unlock()
		var m struct {
			Values map[string]any `json:"values"`
		}
		json.Unmarshal(b, &m)
		hookAnswer(w, m.Values)
	}))
	defer hook.Close()
	var events []map[string]any
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		events = append(events, m)
		mu.Unlock()
	}))
	defer recv.Close()
	// An external webhook is sent the body its fields build, unsigned.
	var crmBodies [][]byte
	var crmSigs []string
	crm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		crmBodies, crmSigs = append(crmBodies, b), append(crmSigs, r.Header.Get("X-CMS-Signature")+r.Header.Get("X-CMS-Event"))
		mu.Unlock()
	}))
	defer crm.Close()
	var otherForm int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		otherForm++
		mu.Unlock()
	}))
	defer other.Close()

	s.must(200, s.do(s.admin, "PATCH", "/api/settings/email", map[string]any{"setting": map[string]any{"data": map[string]any{
		"smtp_host": "smtp.acme.test", "from_name": "Acme", "from_email": "hello@acme.test"}}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/general", map[string]any{"setting": map[string]any{"data": map[string]any{
		"title": "Acme", "site_base_url": "https://acme.test", "cors_origins": hook.URL}}}))
	// Webhooks are offered the plugin's event, and one takes it.
	r := s.must(200, s.do(s.admin, "GET", "/api/webhooks", nil))
	if !slices.Contains(toStrings(r.Body["events"]), "submission.created") {
		t.Fatalf("events: %s", r.Raw)
	}
	s.must(201, s.do(s.admin, "POST", "/api/webhooks", map[string]any{"webhook": map[string]any{"name": "crm", "url": recv.URL, "events": []any{"submission.created"}}}))
	stella := map[string]any{"name": "Stella", "url": crm.URL, "kind": "external", "events": []any{"submission.created"},
		"event_filters": map[string]any{"submission.created": map[string]any{"form": "quote"}},
		"body_map": []any{
			map[string]any{"key": "service_type", "value": "values.service", "transform": "slug"},
			map[string]any{"key": "initial_zip", "value": "values.zip"},
			map[string]any{"key": "fd_zip", "value": "values.to_zip"},
			map[string]any{"key": "email", "value": "values.email"},
			map[string]any{"key": "quote_id", "value": "hook.quote_id"},
			map[string]any{"key": "source", "value": "website", "transform": "text"},
		}}
	// Each field has a key of its own, a value and a transform there is.
	badHook := map[string]any{"name": "x", "url": crm.URL, "kind": "outside", "body_map": []any{
		map[string]any{"key": "a", "value": "values.a"}, map[string]any{"key": "a", "value": "", "transform": "shout"}}}
	r = s.must(422, s.do(s.admin, "POST", "/api/webhooks", map[string]any{"webhook": badHook}))
	for _, k := range []string{"kind", "body_map[1].key", "body_map[1].value", "body_map[1].transform"} {
		if dig(r.Body, "errors", k, 0) == nil {
			t.Fatalf("%s: %s", k, r.Raw)
		}
	}
	r = s.must(201, s.do(s.admin, "POST", "/api/webhooks", map[string]any{"webhook": stella}))
	stellaID := str(dig(r.Body, "webhook", "id"))
	if dig(r.Body, "webhook", "kind") != "external" || dig(r.Body, "webhook", "event_filters", "submission.created", "form", 0) != "quote" ||
		dig(r.Body, "webhook", "body_map", 0, "transform") != "slug" {
		t.Fatalf("external webhook: %s", r.Raw)
	}
	s.must(201, s.do(s.admin, "POST", "/api/webhooks", map[string]any{"webhook": map[string]any{"name": "contact only", "url": other.URL,
		"events": []any{"submission.created"}, "event_filters": map[string]any{"submission.created": map[string]any{"form": []any{"contact"}}}}}))

	form := map[string]any{
		"name": "Quote", "slug": "quote", "status": "active", "store_submissions": true, "min_seconds": 0,
		"fields": []any{
			map[string]any{"type": "radio", "name": "service", "label": "Service", "options": []any{"Keep It", "Move It"}, "required": true},
			map[string]any{"type": "email", "name": "email", "label": "Email", "required": true},
			map[string]any{"type": "text", "name": "zip", "label": "Zip", "required": true},
			map[string]any{"type": "text", "name": "to_zip", "label": "Destination zip", "required": true,
				"show_if": map[string]any{"field": "service", "values": []any{"Move It"}}},
			map[string]any{"type": "checkbox", "name": "in_state", "label": "In state", "text": "Moving within the state",
				"show_if": map[string]any{"field": "service", "values": []any{"Move It"}}},
			map[string]any{"type": "text", "name": "note", "label": "Note", "show_if": map[string]any{"field": "in_state", "values": []any{"yes"}}},
		},
		"admin_enabled": true, "admin_to": []any{"owner@acme.test"}, "admin_subject": "Quote {{ quote_id }}",
		"success_message": "Thanks!", "redirect_url": "/thank-you",
		"hook_url": "https://evil.test/hook", "hook_secret": "s3cret",
	}
	// A condition names a field above it; a hook is on the site, with a secret.
	bad := map[string]any{}
	maps.Copy(bad, form)
	bad["fields"] = []any{
		map[string]any{"type": "text", "name": "a", "label": "A", "show_if": map[string]any{"field": "b", "values": []any{"x"}}},
		map[string]any{"type": "text", "name": "b", "label": "B", "show_if": map[string]any{"field": "a"}},
	}
	r = s.must(422, s.do(s.admin, "POST", "/api/x/forms/form", map[string]any{"form": bad}))
	if dig(r.Body, "errors", "fields[0].show_if.field", 0) == nil || dig(r.Body, "errors", "fields[1].show_if.values", 0) == nil ||
		!strings.Contains(str(dig(r.Body, "errors", "hook_url", 0)), "on the site") {
		t.Fatalf("validation: %s", r.Raw)
	}
	form["hook_url"] = hook.URL + "/hook"
	s.must(200, s.do(s.admin, "POST", "/api/x/forms/form", map[string]any{"form": form}))
	// The secret is masked, and saving the mask keeps it.
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/form?id=quote", nil))
	if dig(r.Body, "form", "hook_secret") != "***cret" {
		t.Fatalf("secret: %s", r.Raw)
	}
	form["hook_secret"] = "***cret"
	s.must(200, s.do(s.admin, "POST", "/api/x/forms/form?id=quote", map[string]any{"form": form}))
	// The site sees each field's condition.
	r = s.must(200, s.do("", "GET", "/api/x/forms/definition?form=quote", nil))
	if dig(r.Body, "form", "fields", 3, "show_if", "field") != "service" || dig(r.Body, "form", "fields", 4, "show_if", "values", 0) != "Move It" {
		t.Fatalf("definition: %s", r.Raw)
	}

	// Hidden, a required field isn't asked for, and what was posted for it
	// is dropped; shown, it's required.
	r = s.must(422, s.do("", "POST", "/api/x/forms/submit?form=quote", map[string]any{"service": "Move It", "email": "a@x.test", "zip": "32207"}))
	if dig(r.Body, "errors", "to_zip", 0) != "can't be blank" {
		t.Fatalf("shown and required: %s", r.Raw)
	}
	r = s.must(200, s.do("", "POST", "/api/x/forms/submit?form=quote", map[string]any{
		"service": "Keep It", "email": "a@x.test", "zip": "32207", "to_zip": "", "in_state": true, "note": "dropped"}))
	// The hook's answer sends the visitor on, and its values go with it.
	if r.Body["redirect"] != "/thanks?q=Q-42" || r.Body["message"] != "Thanks!" {
		t.Fatalf("hook answer: %s", r.Raw)
	}
	if len(hookBodies) != 1 {
		t.Fatalf("hook calls: %d", len(hookBodies))
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(hookBodies[0])
	if hookSigs[0] != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature %s", hookSigs[0])
	}
	var posted map[string]any
	json.Unmarshal(hookBodies[0], &posted)
	if dig(posted, "form") != "quote" || dig(posted, "values", "zip") != "32207" || dig(posted, "values", "note") != nil || dig(posted, "values", "in_state") != nil {
		t.Fatalf("hook body: %s", hookBodies[0])
	}
	if got := mailer.take(); len(got) != 1 || !strings.Contains(got[0].raw, "Quote Q-42") {
		t.Fatalf("mail: %+v", got)
	}
	s.app.hooks.Wait()
	if len(events) != 1 || dig(events[0], "event") != "submission.created" || dig(events[0], "data", "values", "zip") != "32207" || dig(events[0], "data", "hook", "quote_id") != "Q-42" {
		t.Fatalf("webhook: %v", events)
	}
	var sent map[string]any
	if len(crmBodies) != 1 || json.Unmarshal(crmBodies[0], &sent) != nil || crmSigs[0] != "" {
		t.Fatalf("external: %q %q", crmBodies, crmSigs)
	}
	if want := map[string]any{"service_type": "keep-it", "initial_zip": "32207", "fd_zip": "", "email": "a@x.test", "quote_id": "Q-42", "source": "website"}; !maps.Equal(sent, want) {
		t.Fatalf("external body %v, want %v", sent, want)
	}
	if otherForm != 0 {
		t.Fatalf("another form's webhook heard of it")
	}
	// Its deliveries show what it was sent.
	r = s.must(200, s.do(s.admin, "GET", "/api/webhooks/"+stellaID+"/deliveries", nil))
	if dig(r.Body, "deliveries", 0, "payload", "service_type") != "keep-it" {
		t.Fatalf("deliveries: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submissions?form=quote", nil))
	id := str(dig(r.Body, "submissions", 0, "id"))
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submission?id="+id, nil))
	if !strings.Contains(string(r.Raw), `"quote_id (from the hook)"`) || !strings.Contains(string(r.Raw), `"value":"done"`) {
		t.Fatalf("submission: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submissions.csv?form=quote", nil))
	if !strings.Contains(string(r.Raw), ",quote_id") || !strings.Contains(string(r.Raw), ",Q-42") {
		t.Fatalf("csv: %s", r.Raw)
	}

	// A refusal reaches the visitor, per field, and nothing is kept.
	r = s.must(422, s.do("", "POST", "/api/x/forms/submit?form=quote", map[string]any{"service": "Keep It", "email": "a@x.test", "zip": "00000"}))
	if r.Body["message"] != "We don't serve that zip yet." || dig(r.Body, "errors", "zip", 0) != "isn't in our service area" {
		t.Fatalf("refusal: %s", r.Raw)
	}
	// A hook that fails doesn't lose the submission: it goes through as
	// the form says, noted.
	r = s.must(200, s.do("", "POST", "/api/x/forms/submit?form=quote", map[string]any{"service": "Keep It", "email": "a@x.test", "zip": "99999"}))
	if r.Body["redirect"] != "/thank-you" {
		t.Fatalf("failed hook: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submissions?form=quote", nil))
	if rows, _ := r.Body["submissions"].([]any); len(rows) != 2 {
		t.Fatalf("kept: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/x/forms/submission?id="+str(dig(r.Body, "submissions", 0, "id")), nil))
	if !strings.Contains(string(r.Raw), "failed: HTTP 502") {
		t.Fatalf("noted: %s", r.Raw)
	}
}
