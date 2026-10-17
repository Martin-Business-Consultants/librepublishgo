package restore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// The Go Forms plugin's records (plugins/forms), as it stores them in
// plugin_kv under its key:
//
//	form:<slug>           the form
//	sub:<slug>:<id>       a submission
//	all:<id>              the slug of submission id's form
//	stat:<slug>           its submissions counted by status
const formsPlugin = "forms"

type goForm struct {
	Slug              string    `json:"slug"`
	Name              string    `json:"name"`
	Status            string    `json:"status"`
	StoreSubmissions  bool      `json:"store_submissions"`
	Fields            []goField `json:"fields"`
	AdminEnabled      bool      `json:"admin_enabled"`
	AdminTo           []string  `json:"admin_to"`
	AdminCc           []string  `json:"admin_cc"`
	AdminBcc          []string  `json:"admin_bcc"`
	AdminSubject      string    `json:"admin_subject"`
	AdminMessage      string    `json:"admin_message"`
	AdminReplyToField string    `json:"admin_reply_to_field"`
	AdminFromName     string    `json:"admin_from_name"`
	AdminFromEmail    string    `json:"admin_from_email"`
	ClientEnabled     bool      `json:"client_enabled"`
	ClientToField     string    `json:"client_to_field"`
	ClientSubject     string    `json:"client_subject"`
	ClientMessage     string    `json:"client_message"`
	ClientReplyTo     string    `json:"client_reply_to"`
	ClientFromName    string    `json:"client_from_name"`
	ClientFromEmail   string    `json:"client_from_email"`
	SubmitLabel       string    `json:"submit_label"`
	SuccessMessage    string    `json:"success_message"`
	RedirectURL       string    `json:"redirect_url"`
	Honeypot          bool      `json:"honeypot"`
	MinSeconds        int       `json:"min_seconds"`
	Captcha           string    `json:"captcha"`
	TurnstileSiteKey  string    `json:"turnstile_site_key"`
	TurnstileSecret   string    `json:"turnstile_secret"`
	RecaptchaSiteKey  string    `json:"recaptcha_site_key"`
	RecaptchaSecret   string    `json:"recaptcha_secret"`
	CreatedAt         string    `json:"created_at"`
	UpdatedAt         string    `json:"updated_at"`
}

type goField struct {
	ID          string   `json:"id,omitempty"`
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Options     []string `json:"options,omitempty"`
	Default     string   `json:"default,omitempty"`
	Text        string   `json:"text,omitempty"`
	Rows        int      `json:"rows,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	MaxLength   int      `json:"max_length,omitempty"`
	Accept      string   `json:"accept,omitempty"`
}

type goSubmission struct {
	ID          string         `json:"id"`
	Form        string         `json:"form"`
	Status      string         `json:"status"`
	Values      map[string]any `json:"values"`
	SubmittedAt string         `json:"submitted_at"`
	IP          string         `json:"ip"`
	UserAgent   string         `json:"user_agent"`
	Referer     string         `json:"referer"`
	SpamReason  string         `json:"spam_reason,omitempty"`
}

type goStats struct {
	New   int `json:"new"`
	Read  int `json:"read"`
	Spam  int `json:"spam"`
	Total int `json:"total"`
}

// The Rails Forms plugin's field types, as the Go one names them.
var fieldTypes = map[string]string{
	"text": "text", "string": "text", "short_text": "text", "name": "text",
	"email": "email",
	"tel":   "tel", "phone": "tel", "telephone": "tel",
	"textarea": "textarea", "text_area": "textarea", "long_text": "textarea", "paragraph": "textarea", "message": "textarea",
	"number": "number", "integer": "number", "decimal": "number",
	"select": "select", "dropdown": "select",
	"radio": "radio", "radios": "radio", "choice": "radio",
	"checkboxes": "checkboxes", "multi_select": "checkboxes", "multiselect": "checkboxes", "check_boxes": "checkboxes",
	"checkbox": "checkbox", "boolean": "checkbox", "consent": "checkbox", "acceptance": "checkbox",
	"date": "date",
	"url":  "url", "website": "url",
	"file": "file", "upload": "file", "attachment": "file",
	"hidden":  "hidden",
	"heading": "heading", "section": "heading", "title": "heading", "html": "heading", "static": "heading",
}

var (
	formSlug  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	reserved  = []string{"_hp", "_ts", "_return", "form", "cf_turnstile_response"}
	mergeTag  = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
)

// convertForms turns the Rails Forms plugin's tables (forms,
// form_submissions, form_emails) into the Go Forms plugin's records: the
// fields, the emails, the submissions and their counts. What has nowhere
// to go (a form's own webhook, its submit URL) is reported. The Rails
// tables stay as they were.
func convertForms(ctx context.Context, db *store.DB, rep *Report) error {
	if !hasTable(ctx, db, "forms") {
		return nil
	}
	n, err := store.Count(ctx, db, `SELECT count(*) FROM plugin_kv WHERE plugin_key = ?`, formsPlugin)
	if err != nil {
		return err
	}
	if n > 0 {
		// A Go backup of a site that was itself imported from Rails: done.
		return nil
	}
	forms, err := tableMaps(ctx, db, "forms")
	if err != nil || len(forms) == 0 {
		return err
	}
	emails := map[int64][]map[string]any{}
	if hasTable(ctx, db, "form_emails") {
		rows, err := tableMaps(ctx, db, "form_emails")
		if err != nil {
			return err
		}
		for _, e := range rows {
			id, _ := asInt(e["form_id"])
			emails[id] = append(emails[id], e)
		}
	}
	c := &converter{rep: rep, kv: map[string]any{}, slugs: map[int64]string{}, renames: map[int64]map[string]string{}, taken: map[string]bool{}, stats: map[string]*goStats{}}
	for _, row := range forms {
		c.form(row, emails)
	}
	if hasTable(ctx, db, "form_submissions") {
		if err := c.submissions(ctx, db); err != nil {
			return err
		}
	}
	for slug, st := range c.stats {
		c.kv["stat:"+slug] = st
	}
	now := store.Now().DB()
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		for k, v := range c.kv {
			var raw []byte
			if s, ok := v.(string); ok {
				raw = []byte(s)
			} else if raw, err = json.Marshal(v); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_kv (plugin_key, key, value, updated_at) VALUES (?,?,?,?)`, formsPlugin, k, raw, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(c.kv) > 100000 {
		rep.warn("the Forms plugin keeps at most 100,000 records; this backup's forms make %d, so new submissions won't be kept until some are deleted", len(c.kv))
	}
	f := &rep.Forms
	slices.Sort(f.NotCarried)
	f.NotCarried = slices.Compact(f.NotCarried)
	if f.Forms > 0 {
		rep.warn("%s became Forms plugin forms (with %s, marked read): check each one's fields and emails before the site posts to it", plural(f.Forms, "Rails form", "Rails forms"), plural(f.Submissions, "submission", "submissions"))
	}
	return nil
}

type converter struct {
	rep     *Report
	kv      map[string]any
	slugs   map[int64]string            // Rails form id → slug
	renames map[int64]map[string]string // Rails field name → Go's, per form
	taken   map[string]bool
	stats   map[string]*goStats
}

func (c *converter) notCarried(format string, args ...any) {
	c.rep.Forms.NotCarried = append(c.rep.Forms.NotCarried, fmt.Sprintf(format, args...))
}

func (c *converter) form(row map[string]any, emails map[int64][]map[string]any) {
	id, _ := asInt(row["id"])
	title := strings.TrimSpace(text(row["title"]))
	if row["deleted_at"] != nil {
		c.rep.Forms.Skipped++
		c.notCarried("form %q: it was in the trash", title)
		return
	}
	slug := uniqueSlug(slugify(or(text(row["slug"]), title)), c.taken)
	c.taken[slug] = true
	f := goForm{Slug: slug, Name: or(title, slug), Status: "closed", StoreSubmissions: true, Honeypot: true, MinSeconds: 3, Captcha: "none",
		SubmitLabel: or(text(row["submit_label"]), "Send"), SuccessMessage: or(text(row["success_message"]), "Thanks! We'll be in touch soon."),
		CreatedAt: rfc3339(row["created_at"]), UpdatedAt: rfc3339(row["updated_at"]), AdminTo: []string{}, AdminCc: []string{}, AdminBcc: []string{}}
	switch strings.ToLower(text(row["status"])) {
	case "published", "active", "open", "live", "enabled":
		f.Status = "active"
	default:
		c.notCarried("form %q was %q there, so it's closed here", f.Name, text(row["status"]))
	}
	if u := text(row["submit_url"]); u != "" {
		c.notCarried("form %q: its submit URL (%s)", f.Name, u)
	}
	if u := text(row["notify_webhook_url"]); u != "" {
		c.notCarried("form %q: its webhook to %s (use a webhook on submission events instead)", f.Name, u)
	}
	renames := c.fields(&f, row["fields"])
	c.slugs[id], c.renames[id] = slug, renames
	var emailFields []string
	for _, fl := range f.Fields {
		if fl.Type == "email" {
			emailFields = append(emailFields, fl.Name)
		}
	}
	if len(emailFields) > 0 {
		f.AdminReplyToField, f.ClientToField = emailFields[0], emailFields[0]
	}
	// Before form_emails, a form's recipients were its own column.
	if to := text(row["notify_emails"]); to != "" {
		f.AdminEnabled, f.AdminTo = true, splitList(to)
	}
	for _, e := range emails[id] {
		c.email(&f, e, renames, emailFields)
	}
	c.kv["form:"+slug] = f
	c.stats[slug] = &goStats{}
	c.rep.Forms.Forms++
}

// fields converts a Rails form's fields, and says what each field's name
// became (names must be lowercase letters, digits and _ here).
func (c *converter) fields(f *goForm, raw any) map[string]string {
	renames, inputs := map[string]string{}, 0
	used := map[string]bool{}
	for i, el := range asList(raw) {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		kind := strings.ToLower(text(firstOf(m, "type", "kind", "as", "field_type")))
		typ, known := fieldTypes[kind]
		label := strings.TrimSpace(text(firstOf(m, "label", "title", "name", "key")))
		if !known {
			typ = "text"
			c.notCarried("form %q: field %q is a %q, which became a text field", f.Name, label, kind)
		}
		fl := goField{ID: fmt.Sprintf("f%d", i+1), Type: typ, Label: label,
			Placeholder: text(m["placeholder"]), Help: text(firstOf(m, "help", "hint", "description")), Required: truthy(m["required"]),
			Default: text(firstOf(m, "default", "default_value", "value")), Accept: text(m["accept"])}
		if typ == "heading" {
			fl.Text = or(text(firstOf(m, "text", "content", "html")), label)
			f.Fields = append(f.Fields, fl)
			continue
		}
		old := text(firstOf(m, "name", "key", "id"))
		if old == "" {
			old = label
		}
		fl.Name = uniqueName(fieldNameOf(old), used)
		used[fl.Name] = true
		renames[old] = fl.Name
		inputs++
		if fl.Label == "" && typ != "hidden" {
			fl.Label = old
		}
		for _, o := range asList(firstOf(m, "options", "choices", "values")) {
			switch x := o.(type) {
			case map[string]any:
				if v := text(firstOf(x, "value", "label")); v != "" {
					fl.Options = append(fl.Options, v)
				}
			default:
				if v := strings.TrimSpace(text(x)); v != "" {
					fl.Options = append(fl.Options, v)
				}
			}
		}
		if s, ok := m["options"].(string); ok {
			fl.Options = splitList(s)
		}
		if (typ == "select" || typ == "radio" || typ == "checkboxes") && len(fl.Options) == 0 {
			fl.Type = "text"
			c.notCarried("form %q: field %q had no choices, so it became a text field", f.Name, fl.Label)
		}
		if n, ok := asInt(firstOf(m, "rows")); ok {
			fl.Rows = int(n)
		}
		if n, ok := asInt(firstOf(m, "max_length", "maxlength", "maxLength")); ok {
			fl.MaxLength = int(n)
		}
		fl.Min, fl.Max = asFloat(m["min"]), asFloat(m["max"])
		if typ == "checkbox" {
			fl.Text = text(firstOf(m, "text", "content"))
		}
		f.Fields = append(f.Fields, fl)
	}
	if inputs == 0 {
		c.notCarried("form %q: it has no fields a visitor fills in", f.Name)
	}
	return renames
}

// email is a form_emails row: "notification" (to the site's people) or
// "confirmation" (to whoever sent it).
func (c *converter) email(f *goForm, e map[string]any, renames map[string]string, emailFields []string) {
	enabled := e["enabled"] == nil || truthy(e["enabled"])
	subject := c.tags(f.Name, text(e["subject"]), renames)
	body := asHTML(c.tags(f.Name, text(e["body"]), renames))
	if text(e["site_template"]) != "" {
		c.notCarried("form %q: its %s email's design from the site (the Go core sends its own)", f.Name, text(e["kind"]))
	}
	switch text(e["kind"]) {
	case "notification", "admin":
		f.AdminTo = splitList(text(e["recipients"]))
		f.AdminEnabled = enabled && len(f.AdminTo) > 0
		f.AdminSubject, f.AdminMessage = or(subject, "New {{ form_name }} submission"), body
	case "confirmation", "client":
		to := renames[text(e["from_field"])]
		if to == "" || !slices.Contains(emailFields, to) {
			to = ""
			if len(emailFields) > 0 {
				to = emailFields[0]
			}
		}
		f.ClientToField = to
		f.ClientEnabled = enabled && to != ""
		if enabled && to == "" {
			c.notCarried("form %q: its confirmation email (it has no email field to send it to)", f.Name)
		}
		f.ClientSubject, f.ClientMessage = or(subject, "Thanks for getting in touch"), body
	default:
		c.notCarried("form %q: its %q email", f.Name, text(e["kind"]))
		return
	}
	c.rep.Forms.Emails++
}

// tags rewrites the Rails plugin's merge tags as the Go one's.
func (c *converter) tags(form, s string, renames map[string]string) string {
	return mergeTag.ReplaceAllStringFunc(s, func(m string) string {
		name := mergeTag.FindStringSubmatch(m)[1]
		switch name {
		case "form_title", "form_name", "form":
			return "{{ form_name }}"
		case "submitted_at", "date", "created_at":
			return "{{ date }}"
		case "submission_id", "id":
			return "{{ submission_id }}"
		case "site_name", "site_title":
			return "{{ site_name }}"
		case "all_fields", "fields", "data", "answers":
			return "{{ all_fields }}"
		}
		if to, ok := renames[name]; ok {
			return "{{ " + to + " }}"
		}
		c.notCarried("form %q: the merge tag {{%s}}, which the Forms plugin doesn't have", form, name)
		return m
	})
}

func (c *converter) submissions(ctx context.Context, db *store.DB) error {
	rows, err := tableMaps(ctx, db, "form_submissions")
	if err != nil {
		return err
	}
	files := c.attachments(ctx, db)
	for _, row := range rows {
		id, _ := asInt(row["id"])
		formID, _ := asInt(row["form_id"])
		slug, ok := c.slugs[formID]
		if !ok {
			continue // its form was in the trash, or is gone
		}
		renames := c.renames[formID]
		var data, meta map[string]any
		_ = json.Unmarshal([]byte(text(row["data"])), &data)
		_ = json.Unmarshal([]byte(text(row["meta"])), &meta)
		values := map[string]any{}
		for k, v := range data {
			name, ok := renames[k]
			if !ok {
				name = fieldNameOf(k) // an answer to a field since removed: kept, as the plugin keeps them
			}
			if v != nil && v != "" {
				values[name] = v
			}
		}
		for field, filename := range files[id] {
			if name, ok := renames[field]; ok {
				values[name] = filename
			}
		}
		at := store.Time{}
		_ = at.Scan(row["created_at"])
		if !at.Valid {
			at = store.TimeOf(time.Unix(0, 0))
		}
		status := "read"
		switch s := strings.ToLower(text(firstOf(meta, "status", "state"))); {
		case s == "new" || s == "read" || s == "spam":
			status = s
		case truthy(meta["spam"]):
			status = "spam"
		}
		// Newest first, as the plugin's ids sort; the Rails id keeps it unique.
		subID := fmt.Sprintf("%013d-%06x", max(9999999999999-at.UnixMilli(), 0), id)
		s := goSubmission{ID: subID, Form: slug, Status: status, Values: values, SubmittedAt: at.Time.UTC().Format(time.RFC3339),
			IP: text(row["ip"]), UserAgent: text(firstOf(meta, "user_agent", "ua")), Referer: text(firstOf(meta, "referer", "referrer", "page_url", "url")),
			SpamReason: text(meta["spam_reason"])}
		c.kv["sub:"+slug+":"+subID] = s
		c.kv["all:"+subID] = slug
		st := c.stats[slug]
		switch status {
		case "new":
			st.New++
		case "read":
			st.Read++
		case "spam":
			st.Spam++
		}
		st.Total++
		c.rep.Forms.Submissions++
	}
	if len(files) > 0 {
		c.notCarried("files uploaded with submissions: the files are kept in the media storage, and a submission shows the file's name")
	}
	return nil
}

// attachments are submissions' uploaded files' names, by submission and
// field.
func (c *converter) attachments(ctx context.Context, db *store.DB) map[int64]map[string]string {
	out := map[int64]map[string]string{}
	type file struct {
		id             int64
		name, filename string
	}
	rows, err := db.QueryContext(ctx, `SELECT a.record_id, a.name, b.filename FROM active_storage_attachments a JOIN active_storage_blobs b ON b.id = a.blob_id WHERE a.record_type = 'FormSubmission'`)
	if err != nil {
		return out
	}
	files, err := store.ScanAll(rows, func(r *sql.Rows) (file, error) {
		var f file
		err := r.Scan(&f.id, &f.name, &f.filename)
		return f, err
	})
	if err != nil {
		return out
	}
	for _, f := range files {
		if out[f.id] == nil {
			out[f.id] = map[string]string{}
		}
		out[f.id][f.name] = f.filename
	}
	return out
}

// ---- helpers -------------------------------------------------------------

func hasTable(ctx context.Context, db store.Querier, name string) bool {
	n, err := store.Count(ctx, db, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name)
	return err == nil && n > 0
}

// tableMaps is every row of table, as column → value, whatever columns
// that version of the table has.
func tableMaps(ctx context.Context, db store.Querier, table string) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, `SELECT * FROM `+quote(table)+` ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	}
	return fmt.Sprint(v)
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			return v
		}
	}
	return nil
}

func asList(v any) []any {
	if s, ok := v.(string); ok {
		var l []any
		if json.Unmarshal([]byte(s), &l) == nil {
			return l
		}
		return nil
	}
	l, _ := v.([]any)
	return l
}

func asInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		return int64(x), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	}
	return 0, false
}

func asFloat(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case int64:
		f := float64(x)
		return &f
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return &f
		}
	}
	return nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "t", "yes", "on":
			return true
		}
	}
	return false
}

func rfc3339(v any) string {
	t := store.Time{}
	if t.Scan(v) != nil || !t.Valid {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return t.Time.UTC().Format(time.RFC3339)
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
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
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-")
	}
	if out == "" {
		out = "form"
	}
	return out
}

func uniqueSlug(base string, taken map[string]bool) string {
	slug := base
	for i := 2; taken[slug] || !formSlug.MatchString(slug); i++ {
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	return slug
}

// fieldNameOf is s as a field name: lowercase letters, digits and _,
// starting with a letter, and not one the plugin keeps for itself.
func fieldNameOf(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		name = "field_" + name
	}
	if len(name) > 56 {
		name = name[:56]
	}
	if slices.Contains(reserved, name) {
		name += "_field"
	}
	return name
}

func uniqueName(base string, used map[string]bool) string {
	name := base
	for i := 2; used[name] || !fieldName.MatchString(name); i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	return name
}

// asHTML is a Rails email body (Markdown, mostly paragraphs and **bold**)
// as the HTML the Go plugin's editor keeps.
func asHTML(md string) string {
	md = strings.TrimSpace(strings.ReplaceAll(md, "\r\n", "\n"))
	if md == "" {
		return ""
	}
	bold := regexp.MustCompile(`\*\*([^*]+)\*\*`)
	var b strings.Builder
	for para := range strings.SplitSeq(md, "\n\n") {
		if para = strings.TrimSpace(para); para == "" {
			continue
		}
		p := bold.ReplaceAllString(html.EscapeString(para), "<strong>$1</strong>")
		b.WriteString("<p>" + strings.ReplaceAll(p, "\n", "<br>") + "</p>")
	}
	return b.String()
}
