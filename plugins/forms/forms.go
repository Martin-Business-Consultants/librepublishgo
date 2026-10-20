// Package forms is the Forms plugin, the first default plugin: forms built
// visually in the admin, their submissions kept, and each one emailed to
// the site and a confirmation to the visitor. See docs/forms.md.
package forms

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

// Form is a form, as stored under "form:<slug>" and as the admin edits it: its
// fields (from the builder), who's emailed, what happens after, and how
// spam is kept out.
type Form struct {
	Slug             string  `json:"slug"`
	Name             string  `json:"name"`
	Status           string  `json:"status"`
	StoreSubmissions bool    `json:"store_submissions"`
	Fields           []Field `json:"fields"`

	AdminEnabled      bool     `json:"admin_enabled"`
	AdminTo           []string `json:"admin_to"`
	AdminCc           []string `json:"admin_cc"`
	AdminBcc          []string `json:"admin_bcc"`
	AdminSubject      string   `json:"admin_subject"`
	AdminMessage      string   `json:"admin_message"`
	AdminReplyToField string   `json:"admin_reply_to_field"`
	AdminFromName     string   `json:"admin_from_name"`
	AdminFromEmail    string   `json:"admin_from_email"`

	ClientEnabled   bool   `json:"client_enabled"`
	ClientToField   string `json:"client_to_field"`
	ClientSubject   string `json:"client_subject"`
	ClientMessage   string `json:"client_message"`
	ClientReplyTo   string `json:"client_reply_to"`
	ClientFromName  string `json:"client_from_name"`
	ClientFromEmail string `json:"client_from_email"`

	SubmitLabel    string `json:"submit_label"`
	SuccessMessage string `json:"success_message"`
	RedirectURL    string `json:"redirect_url"`

	Honeypot   bool `json:"honeypot"`
	MinSeconds int  `json:"min_seconds"`
	// Captcha is "none" (or ""), "turnstile" (Cloudflare) or "recaptcha"
	// (Google).
	Captcha           string  `json:"captcha"`
	TurnstileSiteKey  string  `json:"turnstile_site_key"`
	TurnstileSecret   string  `json:"turnstile_secret"`
	RecaptchaSiteKey  string  `json:"recaptcha_site_key"`
	RecaptchaSecret   string  `json:"recaptcha_secret"`
	RecaptchaMinScore float64 `json:"recaptcha_min_score,omitempty"`

	// HookURL, when set, is posted each submission (signed with
	// HookSecret) before it's kept and emailed; its answer can refuse it,
	// send the visitor elsewhere, and add values to it. See hook.go.
	HookURL    string `json:"hook_url,omitempty"`
	HookSecret string `json:"hook_secret,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// A Field is one row of the builder.
type Field struct {
	ID          string   `json:"id,omitempty"`
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Width       string   `json:"width,omitempty"`
	Options     []string `json:"options,omitempty"`
	Default     string   `json:"default,omitempty"`
	Text        string   `json:"text,omitempty"`
	Rows        int      `json:"rows,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	MaxLength   int      `json:"max_length,omitempty"`
	Accept      string   `json:"accept,omitempty"`
	MaxMB       float64  `json:"max_mb,omitempty"`
	ShowIf      *ShowIf  `json:"show_if,omitempty"`
}

func (f Field) layout() bool { return f.Type == "heading" }

// stats counts a form's submissions by status, under "stat:<slug>".
type stats struct {
	New   int `json:"new"`
	Read  int `json:"read"`
	Spam  int `json:"spam"`
	Total int `json:"total"`
}

func (s *stats) add(status string, n int) {
	switch status {
	case "new":
		s.New += n
	case "read":
		s.Read += n
	case "spam":
		s.Spam += n
	}
	s.Total += n
}

var (
	slugFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	nameFormat = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	reserved   = []string{"_hp", "_ts", "_return", "form", "cf_turnstile_response"}
	// fieldTypes is the palette's types.
	fieldTypes = func() map[string]bool {
		types := map[string]bool{}
		for _, p := range palette {
			types[p["type"].(string)] = true //nolint:forcetypeassert,errcheck // the palette is ours, each type a string
		}
		return types
	}()
)

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func newForm() Form {
	return Form{
		Status: "active", StoreSubmissions: true, Honeypot: true, MinSeconds: 3, Captcha: "none",
		AdminEnabled: true, AdminSubject: "New {{ form_name }} submission", ClientSubject: "Thanks for getting in touch",
		AdminMessage: defaultAdminMessage, ClientMessage: defaultClientMessage,
		SubmitLabel: "Send", SuccessMessage: "Thanks! We'll be in touch soon.",
		Fields: []Field{
			{ID: "f1", Type: "text", Name: "name", Label: "Name", Required: true, Width: "half"},
			{ID: "f2", Type: "email", Name: "email", Label: "Email", Required: true, Width: "half"},
			{ID: "f3", Type: "textarea", Name: "message", Label: "Message", Required: true, Rows: 5},
		},
		AdminReplyToField: "email", ClientToField: "email",
	}
}

// The messages a form starts with (and goes back to when one's emptied).
const (
	defaultAdminMessage  = "<p>You have a new submission to <strong>{{ form_name }}</strong>.</p><p>{{ all_fields }}</p><p>Received {{ date }} on {{ site_name }}. Reply to this email to answer them.</p>"
	defaultClientMessage = "<p>Hi {{ name }},</p><p>Thanks for getting in touch. We've received your message and will get back to you soon.</p><p>Here's a copy of what you sent:</p><p>{{ all_fields }}</p><p>{{ site_name }}</p>"
)

func (c *call) loadForm(slug string) (Form, bool) {
	var f Form
	ok := c.kvGetJSON("form:"+slug, &f)
	// Forms saved before the captcha choice used Turnstile when keyed.
	if f.Captcha == "" && f.TurnstileSecret != "" {
		f.Captcha = "turnstile"
	}
	return f, ok
}

func (c *call) formStats(slug string) stats {
	var s stats
	c.kvGetJSON("stat:"+slug, &s)
	return s
}

func (c *call) allForms() []Form {
	out := []Form{}
	for _, k := range c.kvAll("form:") {
		var f Form
		if c.kvGetJSON(k, &f) {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b Form) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

// asHTML is a message as the rich-text editor edits it: the default when
// blank, and an older plain-text one as paragraphs.
func asHTML(msg, fallback string) string {
	if strings.TrimSpace(msg) == "" {
		return fallback
	}
	if htmlTag.MatchString(msg) {
		return msg
	}
	var b strings.Builder
	for para := range strings.SplitSeq(strings.ReplaceAll(msg, "\r\n", "\n"), "\n\n") {
		if para = strings.TrimSpace(para); para != "" {
			b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(para), "\n", "<br>") + "</p>")
		}
	}
	return b.String()
}

func mask(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) > 4 {
		return "***" + secret[len(secret)-4:]
	}
	return "***"
}

// forAdmin is a form as the admin edits it: the secret masked, and how the
// site uses it.
func (c *call) forAdmin(f Form) map[string]any {
	b, _ := json.Marshal(f)
	var m map[string]any
	_ = json.Unmarshal(b, &m) // a Form, just marshalled
	m["turnstile_secret"] = mask(f.TurnstileSecret)
	m["recaptcha_secret"] = mask(f.RecaptchaSecret)
	m["hook_secret"] = mask(f.HookSecret)
	m["admin_message"] = asHTML(f.AdminMessage, defaultAdminMessage)
	m["client_message"] = asHTML(f.ClientMessage, defaultClientMessage)
	if f.Slug != "" {
		s := c.siteInfo()
		base := strings.TrimRight(s.CMSURL, "/") + "/api/x/forms"
		m["embed"] = []map[string]string{
			{"label": "Definition (GET)", "value": base + "/definition?form=" + f.Slug, "copy": "true"},
			{"label": "Submit (POST)", "value": base + "/submit?form=" + f.Slug, "copy": "true"},
			{"label": "How to post", "value": "JSON, a urlencoded form or multipart (for files). Send _ts (when the page loaded, in ms), an empty _hp field and, from a plain HTML form, _return (the page's address)."},
		}
	}
	return m
}

// ---- admin routes ---------------------------------------------------------

func (c *call) listForms() *plugin.Response {
	out := []map[string]any{}
	for _, f := range c.allForms() {
		count := 0
		for _, fl := range f.Fields {
			if !fl.layout() {
				count++
			}
		}
		st := c.formStats(f.Slug)
		out = append(out, map[string]any{"slug": f.Slug, "name": f.Name, "status": f.Status, "field_count": count,
			"submissions": st.Total - st.Spam, "new": st.New, "updated_at": f.UpdatedAt})
	}
	return reply(200, map[string]any{"forms": out})
}

func (c *call) showForm() *plugin.Response {
	req := c.req
	id := req.q("id")
	if id == "" {
		return reply(200, map[string]any{"form": c.forAdmin(newForm())})
	}
	f, ok := c.loadForm(id)
	if !ok {
		return notFound("No form " + id + ".")
	}
	return reply(200, map[string]any{"form": c.forAdmin(f)})
}

func (c *call) saveForm() *plugin.Response {
	req := c.req
	var body struct {
		Form Form `json:"form"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return reply(400, map[string]string{"error": "bad_request", "message": "Send {\"form\": {…}}."})
	}
	f := body.Form
	id := req.q("id")
	var old Form
	existing := false
	if id != "" {
		if old, existing = c.loadForm(id); !existing {
			return notFound("No form " + id + ".")
		}
	}
	f.Name = strings.TrimSpace(f.Name)
	f.Slug = strings.TrimSpace(strings.ToLower(f.Slug))
	if f.Slug == "" {
		f.Slug = slugify(f.Name)
	}
	if f.Status == "" {
		f.Status = "active"
	}
	if strings.HasPrefix(f.TurnstileSecret, "***") {
		f.TurnstileSecret = old.TurnstileSecret
	}
	if strings.HasPrefix(f.RecaptchaSecret, "***") {
		f.RecaptchaSecret = old.RecaptchaSecret
	}
	if strings.HasPrefix(f.HookSecret, "***") {
		f.HookSecret = old.HookSecret
	}
	errs := validateForm(&f)
	checkHook(&f, c.siteInfo(), func(k, m string) { errs[k] = append(errs[k], m) })
	if len(errs) > 0 {
		return invalid(errs)
	}
	if f.Slug != id {
		if _, taken := c.loadForm(f.Slug); taken {
			return invalid(map[string][]string{"slug": {"is already used by another form"}})
		}
	}
	f.CreatedAt = old.CreatedAt
	if f.CreatedAt == "" {
		f.CreatedAt = now()
	}
	f.UpdatedAt = now()
	if e := c.kvSetJSON("form:"+f.Slug, f); e != "" {
		return reply(500, map[string]string{"error": "storage", "message": e})
	}
	if existing && f.Slug != id {
		c.renameForm(id, f.Slug)
	}
	return reply(200, map[string]any{"form": c.forAdmin(f), "redirect": map[string]any{"screen": "form", "params": map[string]string{"id": f.Slug}}})
}

func validateForm(f *Form) map[string][]string { //nolint:gocyclo // one check after another, each simple
	errs := map[string][]string{}
	add := func(k, m string) { errs[k] = append(errs[k], m) }
	if f.Name == "" {
		add("name", "can't be blank")
	}
	if !slugFormat.MatchString(f.Slug) {
		add("slug", "is lowercase letters, digits and dashes")
	}
	if f.Status != "active" && f.Status != "closed" {
		add("status", "is active or closed")
	}
	seen := map[string]bool{}
	inputs := 0
	for i := range f.Fields {
		fl := &f.Fields[i]
		at := fmt.Sprintf("fields[%d]", i)
		fl.Name = strings.TrimSpace(fl.Name)
		fl.Label = strings.TrimSpace(fl.Label)
		if !fieldTypes[fl.Type] {
			add(at+".type", "isn't a field type")
			continue
		}
		if fl.ID == "" {
			fl.ID = fmt.Sprintf("f%d", i+1)
		}
		if fl.layout() {
			continue
		}
		inputs++
		if fl.Name == "" {
			fl.Name = strings.ReplaceAll(slugify(fl.Label), "-", "_")
		}
		switch {
		case !nameFormat.MatchString(fl.Name):
			add(at+".name", "is lowercase letters, digits and _, starting with a letter")
		case slices.Contains(reserved, fl.Name):
			add(at+".name", "is reserved")
		case seen[fl.Name]:
			add(at+".name", "is used by another field")
		}
		seen[fl.Name] = true
		if fl.Label == "" && fl.Type != "hidden" {
			add(at+".label", "can't be blank")
		}
		if (fl.Type == "select" || fl.Type == "radio" || fl.Type == "checkboxes") && len(nonEmpty(fl.Options)) == 0 {
			add(at+".options", "needs at least one choice")
		}
		fl.Options = nonEmpty(fl.Options)
	}
	if inputs == 0 {
		add("fields", "needs at least one field")
	}
	checkShowIf(f.Fields, add)
	checkList := func(k string, list []string) []string {
		out := nonEmpty(list)
		for _, e := range out {
			if !emailFormat.MatchString(e) {
				add(k, e+" isn't an email address")
			}
		}
		return out
	}
	f.AdminTo, f.AdminCc, f.AdminBcc = checkList("admin_to", f.AdminTo), checkList("admin_cc", f.AdminCc), checkList("admin_bcc", f.AdminBcc)
	for k, v := range map[string]string{"admin_from_email": f.AdminFromEmail, "client_from_email": f.ClientFromEmail, "client_reply_to": f.ClientReplyTo} {
		if strings.TrimSpace(v) != "" && !emailFormat.MatchString(strings.TrimSpace(v)) {
			add(k, "isn't an email address")
		}
	}
	emailField := func(name string) bool {
		return slices.ContainsFunc(f.Fields, func(x Field) bool { return x.Name == name && x.Type == "email" })
	}
	if f.AdminReplyToField != "" && !emailField(f.AdminReplyToField) {
		add("admin_reply_to_field", "must be one of the form's email fields")
	}
	if f.ClientEnabled {
		if !emailField(f.ClientToField) {
			add("client_to_field", "choose the email field the confirmation goes to")
		}
		if strings.TrimSpace(f.ClientSubject) == "" {
			add("client_subject", "can't be blank")
		}
	}
	if f.MinSeconds < 0 || f.MinSeconds > 600 {
		add("min_seconds", "is between 0 and 600")
	}
	switch f.Captcha {
	case "", "none":
	case "turnstile":
		if strings.TrimSpace(f.TurnstileSiteKey) == "" {
			add("turnstile_site_key", "can't be blank")
		}
		if strings.TrimSpace(f.TurnstileSecret) == "" {
			add("turnstile_secret", "can't be blank")
		}
	case "recaptcha":
		if strings.TrimSpace(f.RecaptchaSiteKey) == "" {
			add("recaptcha_site_key", "can't be blank")
		}
		if strings.TrimSpace(f.RecaptchaSecret) == "" {
			add("recaptcha_secret", "can't be blank")
		}
		if f.RecaptchaMinScore < 0 || f.RecaptchaMinScore > 1 {
			add("recaptcha_min_score", "is between 0 and 1")
		}
	default:
		add("captcha", "is none, Cloudflare Turnstile or Google reCAPTCHA")
	}
	return errs
}

func nonEmpty(list []string) []string {
	out := []string{}
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// renameForm moves a form's submissions and counts to its new slug.
func (c *call) renameForm(from, to string) {
	c.kvDelete("form:" + from)
	for _, k := range c.kvAll("sub:" + from + ":") {
		if v, ok := c.kvGet(k); ok {
			var s Submission
			if json.Unmarshal([]byte(v), &s) == nil {
				s.Form = to
				c.kvSetJSON("sub:"+to+":"+strings.TrimPrefix(k, "sub:"+from+":"), s)
				c.kvSet("all:"+s.ID, to)
			}
		}
		c.kvDelete(k)
	}
	if v, ok := c.kvGet("stat:" + from); ok {
		c.kvSet("stat:"+to, v)
		c.kvDelete("stat:" + from)
	}
}

func (c *call) duplicateForm() *plugin.Response {
	req := c.req
	f, ok := c.loadForm(req.q("id"))
	if !ok {
		return notFound("No form " + req.q("id") + ".")
	}
	f.Name += " (copy)"
	base := f.Slug + "-copy"
	f.Slug = base
	for i := 2; ; i++ {
		if _, taken := c.loadForm(f.Slug); !taken {
			break
		}
		f.Slug = fmt.Sprintf("%s-%d", base, i)
	}
	f.CreatedAt, f.UpdatedAt = now(), now()
	c.kvSetJSON("form:"+f.Slug, f)
	return reply(200, map[string]any{"ok": true, "message": "Duplicated as " + f.Name + ".", "form": c.forAdmin(f)})
}

func (c *call) deleteForm() *plugin.Response {
	req := c.req
	slug := req.q("id")
	if _, ok := c.loadForm(slug); !ok {
		return notFound("No form " + slug + ".")
	}
	for _, k := range c.kvAll("sub:" + slug + ":") {
		c.kvDelete("all:" + strings.TrimPrefix(k, "sub:"+slug+":"))
		c.kvDelete(k)
	}
	c.kvDelete("stat:" + slug)
	c.kvDelete("form:" + slug)
	return reply(200, map[string]any{"ok": true, "message": "Deleted."})
}

// ---- for the site ---------------------------------------------------------

// publishedField is a field as a site draws it: the builder's settings and
// the rules its answers must meet (none for a heading).
type publishedField struct {
	Field
	Rules *Rules `json:"rules,omitempty"`
}

// published is what a site needs to draw a form: its fields and their
// rules, the labels, where to post, the spam checks it should send, and a
// digest of all of it ("sha256:<hex>" of the rest in canonical JSON), so a
// site can tell the form it was built against from the one it's given.
func published(f Form, s plugin.Site) map[string]any {
	fields := []publishedField{}
	for _, fl := range f.Fields {
		fl.ID = ""
		fields = append(fields, publishedField{Field: fl, Rules: rulesFor(fl)})
	}
	out := map[string]any{
		"slug": f.Slug, "name": f.Name, "open": f.Status == "active", "fields": fields,
		"submit_label": f.SubmitLabel, "success_message": f.SuccessMessage, "redirect_url": f.RedirectURL,
		"action": strings.TrimRight(s.CMSURL, "/") + "/api/x/forms/submit?form=" + f.Slug,
		"spam": map[string]any{"honeypot_field": map[bool]string{true: "_hp"}[f.Honeypot], "timestamp_field": map[bool]string{true: "_ts"}[f.MinSeconds > 0],
			"min_seconds": f.MinSeconds, "captcha": captchaFor(f)},
	}
	out["digest"] = digestOf(canonicalJSON(out))
	return out
}

// definition answers one form's published definition.
func (c *call) definition() *plugin.Response {
	req := c.req
	f, ok := c.loadForm(req.q("form"))
	if !ok {
		return notFound("No form " + req.q("form") + ".")
	}
	return replyPublic(map[string]any{"form": published(f, c.siteInfo())})
}

// listPublic is every form a site can draw: its slug, name, whether it's
// taking submissions, and its definition's digest.
func (c *call) listPublic() *plugin.Response {
	s := c.siteInfo()
	out := []map[string]any{}
	for _, f := range c.allForms() {
		p := published(f, s)
		out = append(out, map[string]any{"slug": f.Slug, "name": f.Name, "open": p["open"], "digest": p["digest"]})
	}
	return replyPublic(map[string]any{"forms": out})
}

// replyPublic answers a site's read: JSON a cache may keep a minute.
func replyPublic(v any) *plugin.Response {
	return replyRaw(200, map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "public, max-age=60"}, mustJSON(v))
}

// captchaFor is what a site needs to show the form's captcha: the
// provider, its public site key, and the field its token is posted in.
func captchaFor(f Form) any {
	switch f.Captcha {
	case "turnstile":
		return map[string]string{"provider": "turnstile", "site_key": f.TurnstileSiteKey, "field": "cf-turnstile-response", "script": "https://challenges.cloudflare.com/turnstile/v0/api.js"}
	case "recaptcha":
		return map[string]string{"provider": "recaptcha", "site_key": f.RecaptchaSiteKey, "field": "g-recaptcha-response", "script": "https://www.google.com/recaptcha/api.js"}
	}
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// canonicalJSON is v as digests hash it, the same form as the CMS's
// schema digest: compact JSON with every object's keys sorted, strings
// escaped as encoding/json does by default, numbers as first written.
func canonicalJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	_ = dec.Decode(&generic) // v, just marshalled
	out, _ := json.Marshal(generic)
	return out
}

func digestOf(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}
