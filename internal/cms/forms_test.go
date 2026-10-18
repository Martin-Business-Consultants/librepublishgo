package cms

import (
	"bytes"
	"context"
	"io"
	"maps"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/url"
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

// A plugin is compiled on its first call, not at start, and let go when
// idle; the next call compiles it again (from the disk cache) and works.
func TestPluginLoadsWhenUsed(t *testing.T) {
	t.Parallel()
	s := newSiteWith(t, Config{}) // with the plugins the binary ships
	app := s.server.App()
	if app.plugins.Loaded("forms") {
		t.Fatal("compiled at start")
	}
	s.must(200, s.do(s.admin, "GET", "/api/x/forms/forms", nil))
	if !app.plugins.Loaded("forms") {
		t.Fatal("not compiled after a call")
	}
	if freed := app.plugins.UnloadIdle(t.Context(), time.Hour); len(freed) != 0 {
		t.Fatalf("let go of a plugin used just now: %v", freed)
	}
	if freed := app.plugins.UnloadIdle(t.Context(), 0); len(freed) != 1 || freed[0] != "forms" || app.plugins.Loaded("forms") {
		t.Fatalf("idle: let go of %v", freed)
	}
	s.must(200, s.do(s.admin, "GET", "/api/x/forms/forms", nil))
	if !app.plugins.Loaded("forms") {
		t.Fatal("not compiled again")
	}
}
