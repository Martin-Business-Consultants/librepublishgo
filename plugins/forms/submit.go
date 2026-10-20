package forms

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

// A Submission is one sending of a form, under "sub:<slug>:<id>"; its id
// sorts newest first. "all:<id>" names its form, for the list of every
// form's submissions.
type Submission struct {
	ID          string            `json:"id"`
	Form        string            `json:"form"`
	Status      string            `json:"status"`
	Values      map[string]any    `json:"values"`
	Files       map[string]upload `json:"files,omitempty"`
	SubmittedAt string            `json:"submitted_at"`
	IP          string            `json:"ip"`
	UserAgent   string            `json:"user_agent"`
	Referer     string            `json:"referer"`
	SpamReason  string            `json:"spam_reason,omitempty"`
	Mail        map[string]string `json:"mail,omitempty"`
	// Hook is what the form's hook added, HookStatus how calling it went.
	Hook       map[string]string `json:"hook,omitempty"`
	HookStatus string            `json:"hook_status,omitempty"`
	// HookSteps are what the hook said it did; HookCall is its call in the
	// hook log.
	HookSteps []HookStep `json:"hook_steps,omitempty"`
	HookCall  string     `json:"hook_call,omitempty"`
	// HookTables are the hook's values that are tables (rows of label and
	// value: a price breakdown), drawn as tables in HTML email; Hook has each
	// as text too, for everything else.
	HookTables map[string][]HookRow `json:"hook_tables,omitempty"`
}

func newID() string {
	inv := 9999999999999 - time.Now().UnixMilli()
	var r [3]byte
	rand.Read(r[:])
	return fmt.Sprintf("%013d-%x", inv, r)
}

// posted is what a request carried: the values, and files as base64.
type posted struct {
	values map[string][]string
	files  map[string]postedFile
}

type postedFile struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Data        string `json:"data"` // base64, as JSON posts it
	raw         []byte // as multipart posts it
	size        int
}

func parseBody(req request) (posted, error) {
	p := posted{values: map[string][]string{}, files: map[string]postedFile{}}
	ct := req.header("Content-Type")
	mt, params, _ := mime.ParseMediaType(ct)
	switch {
	case mt == "application/json" || (mt == "" && strings.HasPrefix(strings.TrimSpace(string(req.Body)), "{")):
		var raw map[string]any
		if err := json.Unmarshal(req.Body, &raw); err != nil {
			return p, fmt.Errorf("the body isn't JSON")
		}
		for k, v := range raw {
			switch x := v.(type) {
			case []any:
				for _, e := range x {
					p.values[k] = append(p.values[k], fmt.Sprint(e))
				}
			case map[string]any:
				var f postedFile
				b, _ := json.Marshal(x)
				_ = json.Unmarshal(b, &f) // anything else isn't a file: Data stays empty
				if f.Data != "" {
					f.size = base64.StdEncoding.DecodedLen(len(f.Data))
					p.files[k] = f
				}
			case nil:
			case bool:
				if x {
					p.values[k] = []string{"yes"}
				}
			default:
				p.values[k] = []string{fmt.Sprint(x)}
			}
		}
	case mt == "multipart/form-data":
		r := multipart.NewReader(bytes.NewReader(req.Body), params["boundary"])
		for {
			part, err := r.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return p, fmt.Errorf("the multipart body is broken")
			}
			data, _ := io.ReadAll(part)
			if part.FileName() != "" {
				if len(data) > 0 {
					p.files[part.FormName()] = postedFile{Filename: path.Base(part.FileName()), ContentType: part.Header.Get("Content-Type"),
						raw: data, size: len(data)}
				}
				continue
			}
			p.values[part.FormName()] = append(p.values[part.FormName()], string(data))
		}
	default:
		vals, err := url.ParseQuery(string(req.Body))
		if err != nil {
			return p, fmt.Errorf("the body isn't a form")
		}
		p.values = vals
	}
	// A field named "x[]" (PHP habit) is "x".
	for k, v := range p.values {
		if name, ok := strings.CutSuffix(k, "[]"); ok {
			p.values[name] = append(p.values[name], v...)
			delete(p.values, k)
		}
	}
	return p, nil
}

func (p posted) one(name string) string {
	if v := p.values[name]; len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

// wantsJSON tells a script's post (answered with JSON) from a plain HTML
// form's (answered with a redirect).
func wantsJSON(req request) bool {
	accept := req.header("Accept")
	return strings.Contains(accept, "application/json") || req.header("X-Requested-With") != "" ||
		strings.HasPrefix(req.header("Content-Type"), "application/json") || !strings.Contains(accept, "text/html")
}

func (c *call) submit() *plugin.Response { //nolint:gocyclo // the steps of a submission, in order
	req := c.req
	slug := req.q("form")
	f, ok := c.loadForm(slug)
	if !ok {
		return notFound("No form " + slug + ".")
	}
	if f.Status != "active" {
		return reply(410, map[string]string{"error": "closed", "message": "This form isn't taking submissions."})
	}
	p, err := parseBody(req)
	if err != nil {
		return reply(400, map[string]string{"error": "bad_request", "message": err.Error()})
	}
	sub := Submission{ID: newID(), Form: f.Slug, Status: "new", Values: map[string]any{}, Files: map[string]upload{},
		SubmittedAt: now(), IP: req.IP, UserAgent: req.header("User-Agent"), Referer: req.header("Referer")}

	// Spam: the honeypot, the time taken, Turnstile. A bot caught by the
	// first two is told it worked, so it doesn't learn to dodge them.
	quiet := false
	if f.Honeypot && p.one("_hp") != "" {
		sub.Status, sub.SpamReason, quiet = "spam", "filled the honeypot", true
	}
	if ts, err := strconv.ParseInt(p.one("_ts"), 10, 64); err == nil && f.MinSeconds > 0 && sub.Status != "spam" {
		if took := time.Now().UnixMilli() - ts; took >= 0 && took < int64(f.MinSeconds)*1000 {
			sub.Status, sub.SpamReason, quiet = "spam", fmt.Sprintf("sent %.1fs after the page loaded", float64(took)/1000), true
		}
	}
	if f.Captcha != "" && f.Captcha != "none" && sub.Status != "spam" {
		var why string
		switch f.Captcha {
		case "turnstile":
			why = c.verifyCaptcha("https://challenges.cloudflare.com/turnstile/v0/siteverify", f.TurnstileSecret, strOr(p.one("cf-turnstile-response"), p.one("cf_turnstile_response")), req.IP, 0)
		case "recaptcha":
			why = c.verifyCaptcha("https://www.google.com/recaptcha/api/siteverify", f.RecaptchaSecret, strOr(p.one("g-recaptcha-response"), p.one("g_recaptcha_response")), req.IP, f.RecaptchaMinScore)
		}
		if why != "" {
			return c.answer(req, f, p.one("_return"), 422, map[string]any{"error": "invalid", "message": why, "errors": map[string][]string{"captcha": {why}}})
		}
	}

	errs := map[string][]string{}
	shown := shownFields(f.Fields, p)
	for _, fl := range f.Fields {
		if fl.layout() || !shown[fl.Name] {
			continue
		}
		if fl.Type == "file" {
			pf, has := p.files[fl.Name]
			if !has {
				if fl.Required {
					errs[fl.Name] = append(errs[fl.Name], blankMessage(fl))
				}
				continue
			}
			if msg := checkFile(fl, pf); msg != "" {
				errs[fl.Name] = append(errs[fl.Name], msg)
				continue
			}
			if sub.Status == "spam" {
				continue
			}
			u, e := c.putMedia(pf, "Form uploads/"+f.Slug)
			if e != "" {
				errs[fl.Name] = append(errs[fl.Name], "couldn't be saved: "+e)
				continue
			}
			sub.Files[fl.Name] = u
			sub.Values[fl.Name] = u.URL
			continue
		}
		vals := nonEmpty(p.values[fl.Name])
		if fl.Type == "hidden" && len(vals) == 0 && fl.Default != "" {
			vals = []string{fl.Default}
		}
		if len(vals) == 0 {
			if fl.Required {
				errs[fl.Name] = append(errs[fl.Name], blankMessage(fl))
			}
			continue
		}
		if msg := checkValue(fl, vals); msg != "" {
			errs[fl.Name] = append(errs[fl.Name], msg)
			continue
		}
		switch fl.Type {
		case "checkboxes":
			sub.Values[fl.Name] = vals
		case "checkbox":
			sub.Values[fl.Name] = "yes"
		default:
			sub.Values[fl.Name] = vals[0]
		}
	}
	if len(errs) > 0 && !quiet {
		return c.answer(req, f, p.one("_return"), 422, map[string]any{"error": "invalid", "message": "Some fields need attention.", "errors": errs})
	}

	// The form's hook hears of it first: it may refuse it (the visitor sees
	// why, and nothing is kept), and what it answers goes with it.
	site := c.siteInfo()
	var hook hookResult
	if f.HookURL != "" && sub.Status != "spam" {
		hook = c.callHook(f, sub, site)
		if hook.refused != nil {
			c.recordHookCall(hook.call)
			return c.answerWith(req, f, p.one("_return"), hook.refused)
		}
		sub.HookStatus, sub.HookSteps, sub.HookCall = hook.status, hook.call.Steps, hook.call.ID
		if len(hook.values) > 0 {
			sub.Hook = hook.values
		}
		if len(hook.tables) > 0 {
			sub.HookTables = hook.tables
		}
	}

	// Kept first, then emailed: a slow mail server can't lose a submission.
	keep := f.StoreSubmissions || sub.Status == "spam"
	if hook.call.ID != "" {
		if keep {
			hook.call.Submission = sub.ID
		}
		c.recordHookCall(hook.call)
	}
	if keep {
		if e := c.kvSetJSON("sub:"+f.Slug+":"+sub.ID, sub); e != "" {
			return c.answer(req, f, p.one("_return"), 503, map[string]any{"error": "storage", "message": "The submission couldn't be saved; try again."})
		}
		c.kvSet("all:"+sub.ID, f.Slug)
		st := c.formStats(f.Slug)
		st.add(sub.Status, 1)
		c.kvSetJSON("stat:"+f.Slug, st)
	}
	if sub.Status != "spam" {
		sub.Mail = c.notify(f, sub, false)
		if keep {
			c.kvSetJSON("sub:"+f.Slug+":"+sub.ID, sub)
		}
		if err := c.api.Emit(c.ctx, "submission.created", submissionEvent(f, sub)); err != nil {
			c.logf("forms: " + err.Error())
		}
	}
	redirect := strOr(hook.redirect, f.RedirectURL)
	return c.answer(req, f, p.one("_return"), 200, map[string]any{"ok": true, "message": strOr(hook.message, f.SuccessMessage), "redirect": redirect})
}

// submissionEvent is the submission.created webhook's data: the answers,
// and what the form's hook added ({} when nothing).
func submissionEvent(f Form, s Submission) map[string]any {
	hook := map[string]any{}
	for k, v := range s.Hook {
		hook[k] = v
	}
	for k, rows := range s.HookTables {
		hook[k] = rows // a webhook gets the rows, not their text
	}
	return map[string]any{"form": f.Slug, "form_name": f.Name, "submission_id": s.ID, "submitted_at": s.SubmittedAt,
		"values": s.Values, "hook": hook}
}

// answerWith answers with a response already made, the way answer does: as
// it is to a script, and as a redirect back to the page to a plain form.
func (c *call) answerWith(req request, f Form, ret string, res *plugin.Response) *plugin.Response {
	var body map[string]any
	_ = json.Unmarshal(res.Body, &body) // ours, just made
	return c.answer(req, f, ret, res.Status, body)
}

// answer replies to a script with JSON, and to a plain HTML form with a
// redirect: to the thank-you page, or back to the page it came from — the
// _return it sent when that's on one of the site's origins, else the
// Referer.
func (c *call) answer(req request, f Form, ret string, status int, body map[string]any) *plugin.Response {
	if wantsJSON(req) {
		return reply(status, body)
	}
	s := c.siteInfo()
	to := req.header("Referer")
	if back, ok := onSite(ret, s.Origins); ok {
		to = back
	}
	if redirect, _ := body["redirect"].(string); status == 200 && redirect != "" {
		to = redirect
		if strings.HasPrefix(to, "/") && s.URL != "" {
			to = s.URL + to
		}
	} else if to != "" {
		u, err := url.Parse(to)
		if err == nil {
			q := u.Query()
			q.Set("form", f.Slug)
			q.Set("form_status", map[bool]string{true: "sent", false: "error"}[status == 200])
			u.RawQuery = q.Encode()
			to = u.String()
		}
	}
	if to == "" {
		return reply(status, body)
	}
	return replyRaw(303, map[string]string{"Location": to, "Content-Type": "text/plain; charset=utf-8"}, "See "+to)
}

// onSite is raw when it's an absolute http(s) address on one of origins,
// so a form's return can't send anyone elsewhere.
func onSite(raw string, origins []string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if raw == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	origin := u.Scheme + "://" + u.Host
	for _, o := range origins {
		if strings.EqualFold(strings.TrimRight(o, "/"), origin) {
			u.Fragment = ""
			return u.String(), true
		}
	}
	return "", false
}

// verifyCaptcha checks a token with Turnstile or reCAPTCHA (the same
// siteverify call); minScore applies to reCAPTCHA v3's score.
func (c *call) verifyCaptcha(endpoint, secret, token, ip string, minScore float64) string {
	if token == "" {
		return "Complete the captcha."
	}
	body := url.Values{"secret": {secret}, "response": {token}, "remoteip": {ip}}.Encode()
	r, err := http.NewRequestWithContext(c.ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return "The captcha couldn't be checked; try again."
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.api.HTTP().Do(r)
	if err != nil {
		return "The captcha couldn't be checked; try again."
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "The captcha couldn't be checked; try again."
	}
	var out struct {
		Success bool     `json:"success"`
		Score   *float64 `json:"score"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out) // an answer that isn't JSON doesn't pass
	if !out.Success || (minScore > 0 && out.Score != nil && *out.Score < minScore) {
		return "The captcha didn't pass; try again."
	}
	return ""
}

// ---- email ----------------------------------------------------------------

// notify sends the notification and the confirmation, and says how each
// went. The confirmation goes to an address anyone posting may type, so it
// counts against the visitors' budget and that address's (MailVisitor),
// never the notification's; a test's goes to the form's own testing
// address, so it doesn't.
func (c *call) notify(f Form, s Submission, test bool) map[string]string {
	out := map[string]string{}
	site := c.siteInfo()
	tags := mergeTags(f, s, site, false)
	if f.AdminEnabled { //nolint:nestif // who to send to, then sending
		to := f.AdminTo
		if len(to) == 0 && site.ContactEmail != "" {
			to = []string{site.ContactEmail}
		}
		if len(to) == 0 {
			out["admin"] = "not sent: no address to send to"
		} else {
			text, html := render(strOr(f.AdminMessage, defaultAdminMessage), f, s, site, false)
			m := mail.Message{To: to, Cc: f.AdminCc, Bcc: f.AdminBcc, FromName: f.AdminFromName, FromEmail: strings.TrimSpace(f.AdminFromEmail),
				Subject: fill(strOr(f.AdminSubject, "New {{ form_name }} submission"), tags), Text: text, HTML: html}
			if f.AdminReplyToField != "" {
				if v, ok := s.Values[f.AdminReplyToField].(string); ok && emailFormat.MatchString(v) {
					m.ReplyTo = v
				}
			}
			out["admin"] = c.result(c.sendMail(m), len(to)+len(f.AdminCc)+len(f.AdminBcc))
		}
	}
	if f.ClientEnabled {
		to, _ := s.Values[f.ClientToField].(string)
		if !emailFormat.MatchString(to) {
			out["client"] = "not sent: no email address in " + f.ClientToField
		} else {
			text, html := render(strOr(f.ClientMessage, defaultClientMessage), f, s, site, true)
			m := mail.Message{To: []string{to}, FromName: f.ClientFromName, FromEmail: strings.TrimSpace(f.ClientFromEmail), ReplyTo: strings.TrimSpace(f.ClientReplyTo),
				Subject: fill(f.ClientSubject, tags), Text: text, HTML: html}
			send := c.sendVisitorMail
			if test {
				send = c.sendMail
			}
			out["client"] = c.result(send(m), 1)
		}
	}
	return out
}

func (c *call) result(err string, n int) string {
	if err != "" {
		c.logf("forms: email failed: " + err)
		return "failed: " + err
	}
	return fmt.Sprintf("sent to %d", n)
}

func strOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func display(v any) string {
	switch x := v.(type) {
	case []string:
		return strings.Join(x, ", ")
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = fmt.Sprint(e)
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// longDate is when a submission came in as {{ date }} shows it, October
// 20, 2026: in the server's time zone (TZ), which is UTC unless it's set.
func longDate(submittedAt string) string {
	t, err := time.Parse(time.RFC3339, submittedAt)
	if err != nil {
		return submittedAt
	}
	return t.In(time.Local).Format("January 2, 2006")
}

// answer is a field's answer as an email shows it: a date in words
// (October 20, 2026), the rest as display has it. What's kept, exported
// and sent to webhooks is the answer as it came (2026-10-20).
func answer(fl Field, v any) string {
	s := display(v)
	if fl.Type == "date" {
		if t, err := time.Parse(time.DateOnly, s); err == nil {
			return t.Format("January 2, 2006")
		}
	}
	return s
}

// echoed is whether a field's answer is in {{ all_fields }}. In the
// visitor's confirmation a paragraph field's isn't: it's free text anyone
// can post, and the site would be mailing it, under its own name, to
// whatever address was typed beside it. {{ <its name> }} still has it.
func echoed(fl Field, visitor bool) bool {
	return !visitor || fl.Type != "textarea"
}

// mergeTags are the values a message's {{ tags }} stand for; visitor for
// the confirmation (see echoed).
func mergeTags(f Form, s Submission, site plugin.Site, visitor bool) map[string]string {
	tags := map[string]string{"form_name": f.Name, "site_name": site.Name, "submission_id": s.ID,
		"date": longDate(s.SubmittedAt)}
	// What the form's hook added, under the fields' own.
	maps.Copy(tags, s.Hook)
	var all bytes.Buffer
	for _, fl := range f.Fields {
		if fl.layout() || fl.Type == "hidden" {
			if fl.Type == "hidden" {
				tags[fl.Name] = display(s.Values[fl.Name])
			}
			continue
		}
		v := answer(fl, s.Values[fl.Name])
		tags[fl.Name] = v
		if v == "" && fl.ShowIf != nil {
			continue // a field its condition hid: not asked
		}
		if !echoed(fl, visitor) {
			continue
		}
		if v == "" {
			v = "—"
		}
		label := strOr(fl.Label, fl.Name)
		if strings.Contains(v, "\n") {
			fmt.Fprintf(&all, "%s:\n%s\n\n", label, v)
		} else {
			fmt.Fprintf(&all, "%s: %s\n", label, v)
		}
	}
	tags["all_fields"] = strings.TrimSpace(all.String())
	return tags
}

var (
	htmlTag      = regexp.MustCompile(`<[a-zA-Z/][^>]*>`)
	allFieldsPar = regexp.MustCompile(`<p>\s*\{\{\s*all_fields\s*\}\}\s*</p>`)
	blockEnd     = regexp.MustCompile(`(?i)</(p|h[1-6]|li|blockquote|tr)>|<br\s*/?>|<hr\s*/?>`)
	listItem     = regexp.MustCompile(`(?i)<li[^>]*>`)
	blankLines   = regexp.MustCompile(`\n{3,}`)
)

// render makes a message's plain text and HTML. A message written in the
// rich-text editor is HTML; an older plain one is text, sent as text. In
// HTML the answers are escaped, and {{ all_fields }} is a table.
func render(tpl string, f Form, s Submission, st plugin.Site, visitor bool) (text, htmlOut string) {
	plain := mergeTags(f, s, st, visitor)
	if !htmlTag.MatchString(tpl) {
		return fill(tpl, plain), ""
	}
	esc := map[string]string{}
	for k, v := range plain {
		esc[k] = strings.ReplaceAll(html.EscapeString(v), "\n", "<br>")
	}
	esc["all_fields"] = answersTable(f, s, visitor)
	body := allFieldsPar.ReplaceAllString(tpl, esc["all_fields"])
	for name, rows := range s.HookTables {
		esc[name] = rowsTable(rows)
		// A table on its own line replaces its paragraph: a table can't be in one.
		par := regexp.MustCompile(`<p>\s*\{\{\s*` + regexp.QuoteMeta(name) + `\s*\}\}\s*</p>`)
		body = par.ReplaceAllLiteralString(body, esc[name])
	}
	body = fill(body, esc)
	return fill(htmlToText(tpl), plain), frame(body, st)
}

// frame is a message's HTML in the email around it, in the site's brand:
// its logo at the top (linked to the site), a band of its primary color
// above the card, its links in that color, and its text in the secondary.
// The colors are hex, as Settings › Brand keeps them.
func frame(body string, st plugin.Site) string {
	primary, text := strOr(st.PrimaryColor, "#18181b"), strOr(st.SecondaryColor, "#18181b")
	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html><html><head><meta charset="utf-8"><style>a{color:%s}</style></head><body style="margin:0;padding:24px;background:#f6f6f7">`, primary)
	b.WriteString(`<div style="max-width:600px;margin:0 auto">`)
	if st.LogoURL != "" {
		img := fmt.Sprintf(`<img src="%s" alt="%s" style="display:inline-block;max-width:200px;max-height:64px;width:auto;height:auto;border:0">`, html.EscapeString(st.LogoURL), html.EscapeString(st.Name))
		if st.URL != "" {
			img = `<a href="` + html.EscapeString(st.URL) + `">` + img + `</a>`
		}
		b.WriteString(`<div style="text-align:center;padding:8px 0 20px">` + img + `</div>`)
	}
	fmt.Fprintf(&b, `<div style="background:#fff;border:1px solid #e5e5e7;border-top:4px solid %s;border-radius:8px;padding:24px;font:15px/1.6 -apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:%s">`, primary, text)
	b.WriteString(body)
	b.WriteString(`</div></div></body></html>`)
	return b.String()
}

// rowsTable is a hook's table value in HTML, styled as the answers are; a
// row marked strong (a total) is bold.
func rowsTable(rows []HookRow) string {
	var b strings.Builder
	b.WriteString(`<table role="presentation" style="width:100%;border-collapse:collapse;margin:8px 0">`)
	for _, r := range rows {
		weight := ""
		if r.Strong {
			weight = "font-weight:600;"
		}
		fmt.Fprintf(&b, `<tr><td style="padding:6px 12px 6px 0;vertical-align:top;border-top:1px solid #f0f0f1;%s">%s</td><td style="padding:6px 0;vertical-align:top;text-align:right;white-space:nowrap;border-top:1px solid #f0f0f1;%s">%s</td></tr>`,
			weight, html.EscapeString(r.Label), weight, html.EscapeString(r.Value))
	}
	b.WriteString(`</table>`)
	return b.String()
}

func answersTable(f Form, s Submission, visitor bool) string {
	var b strings.Builder
	b.WriteString(`<table role="presentation" style="width:100%;border-collapse:collapse;margin:8px 0">`)
	for _, fl := range f.Fields {
		if fl.layout() || fl.Type == "hidden" || !echoed(fl, visitor) {
			continue
		}
		v := answer(fl, s.Values[fl.Name])
		if v == "" && fl.ShowIf != nil {
			continue // a field its condition hid: not asked
		}
		if v == "" {
			v = "—"
		}
		cell := strings.ReplaceAll(html.EscapeString(v), "\n", "<br>")
		if u, ok := s.Files[fl.Name]; ok {
			cell = `<a href="` + html.EscapeString(u.URL) + `">` + html.EscapeString(u.Filename) + `</a>`
		}
		fmt.Fprintf(&b, `<tr><td style="padding:6px 12px 6px 0;color:#71717a;vertical-align:top;white-space:nowrap;border-top:1px solid #f0f0f1">%s</td><td style="padding:6px 0;vertical-align:top;border-top:1px solid #f0f0f1">%s</td></tr>`,
			html.EscapeString(strOr(fl.Label, fl.Name)), cell)
	}
	b.WriteString(`</table>`)
	return b.String()
}

// htmlToText is a message's HTML as plain text, its tags left in.
func htmlToText(h string) string {
	t := listItem.ReplaceAllString(h, "- ")
	t = blockEnd.ReplaceAllString(t, "\n\n")
	t = htmlTag.ReplaceAllString(t, "")
	t = html.UnescapeString(t)
	lines := strings.Split(t, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// fill replaces {{tag}}s, leaving unknown ones as they were.
func fill(tpl string, tags map[string]string) string {
	var b strings.Builder
	for {
		i := strings.Index(tpl, "{{")
		if i < 0 {
			b.WriteString(tpl)
			return b.String()
		}
		j := strings.Index(tpl[i:], "}}")
		if j < 0 {
			b.WriteString(tpl)
			return b.String()
		}
		b.WriteString(tpl[:i])
		name := strings.TrimSpace(tpl[i+2 : i+j])
		if v, ok := tags[name]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(tpl[i : i+j+2])
		}
		tpl = tpl[i+j+2:]
	}
}
