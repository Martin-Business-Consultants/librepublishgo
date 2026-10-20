package forms

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

func (c *call) loadSubmission(id string) (Submission, bool) {
	slug, ok := c.kvGet("all:" + id)
	if !ok {
		return Submission{}, false
	}
	var s Submission
	return s, c.kvGetJSON("sub:"+slug+":"+id, &s)
}

// summary is how a submission is named in lists: its first and last names
// when it has both, else its first name- or email-like answer, else its
// first answer.
func summary(f Form, s Submission) (string, string) {
	var first, best, excerpt string
	if full := strings.TrimSpace(display(s.Values["first_name"]) + " " + display(s.Values["last_name"])); display(s.Values["first_name"]) != "" && display(s.Values["last_name"]) != "" {
		best = full
	}
	for _, fl := range f.Fields {
		v := display(s.Values[fl.Name])
		if v == "" || fl.layout() || fl.Type == "hidden" || fl.Type == "file" {
			continue
		}
		if first == "" {
			first = v
		}
		if best == "" && (fl.Type == "email" || strings.Contains(fl.Name, "name")) {
			best = v
		}
		if excerpt == "" && fl.Type == "textarea" {
			excerpt = v
		}
	}
	if best == "" {
		best = first
	}
	if best == "" {
		best = "Submission " + s.ID
	}
	if r := []rune(excerpt); len(r) > 90 {
		excerpt = string(r[:90]) + "…"
	}
	return best, excerpt
}

func (c *call) listSubmissions() *plugin.Response {
	req := c.req
	slug, status := req.q("form"), req.q("status")
	forms := c.allForms()
	byslug := map[string]Form{}
	options := []map[string]string{{"value": "", "label": "Every form"}}
	total := stats{}
	for _, f := range forms {
		byslug[f.Slug] = f
		options = append(options, map[string]string{"value": f.Slug, "label": f.Name})
		if slug == "" || slug == f.Slug {
			st := c.formStats(f.Slug)
			total.New, total.Read, total.Spam, total.Total = total.New+st.New, total.Read+st.Read, total.Spam+st.Spam, total.Total+st.Total
		}
	}
	if req.q("count") == "new" {
		return reply(200, map[string]any{"count": total.New})
	}
	statuses := []map[string]any{
		{"value": "", "label": "All", "count": total.Total - total.Spam},
		{"value": "new", "label": "Unread", "count": total.New},
		{"value": "read", "label": "Read", "count": total.Read},
		{"value": "spam", "label": "Spam", "count": total.Spam},
	}
	prefix := "all:"
	if slug != "" {
		prefix = "sub:" + slug + ":"
	}
	const per = 25
	rows := []map[string]any{}
	after, next := req.q("after"), ""
	for len(rows) < per {
		keys, more := c.kvKeys(prefix, after, 100)
		for i, k := range keys {
			id := k[strings.LastIndex(k, ":")+1:]
			s, ok := c.loadSubmission(id)
			if !ok || (status == "" && s.Status == "spam") || (status != "" && s.Status != status) {
				continue
			}
			name, excerpt := summary(byslug[s.Form], s)
			rows = append(rows, map[string]any{"id": s.ID, "summary": name, "excerpt": excerpt, "form": s.Form,
				"form_name": strOr(byslug[s.Form].Name, s.Form), "status": s.Status, "submitted_at": s.SubmittedAt})
			if len(rows) == per {
				if i < len(keys)-1 || more != "" {
					next = k
				}
				break
			}
		}
		if more == "" || len(rows) == per {
			break
		}
		after = more
	}
	return reply(200, map[string]any{"submissions": rows, "next": next, "forms": options, "statuses": statuses})
}

func (c *call) showSubmission() *plugin.Response {
	req := c.req
	s, ok := c.loadSubmission(req.q("id"))
	if !ok {
		return notFound("No submission " + req.q("id") + ".")
	}
	f, _ := c.loadForm(s.Form)
	// Opening an unread one reads it.
	if s.Status == "new" {
		c.setStatus(&s, "read")
	}
	answers := []map[string]any{}
	seen := map[string]bool{}
	for _, fl := range f.Fields {
		if fl.layout() {
			continue
		}
		seen[fl.Name] = true
		item := map[string]any{"label": strOr(fl.Label, fl.Name), "value": display(s.Values[fl.Name])}
		if u, ok := s.Files[fl.Name]; ok {
			item["value"], item["href"] = u.Filename, u.URL
		}
		if fl.Type == "email" {
			item["href"] = "mailto:" + display(s.Values[fl.Name])
		}
		if fl.Type == "textarea" {
			item["multiline"] = true
		}
		answers = append(answers, item)
	}
	// Answers to fields the form no longer has are kept, and shown.
	var extra []string
	for k := range s.Values {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	slices.Sort(extra)
	for _, k := range extra {
		answers = append(answers, map[string]any{"label": k + " (removed field)", "value": display(s.Values[k])})
	}
	name, _ := summary(f, s)
	details := []map[string]any{
		{"label": "Form", "value": strOr(f.Name, s.Form), "screen": "form", "params": map[string]string{"id": s.Form}},
		{"label": "Received", "value": s.SubmittedAt, "time": true},
		{"label": "Status", "value": s.Status, "badge": true},
		{"label": "From address", "value": s.IP},
		{"label": "Browser", "value": s.UserAgent},
		{"label": "Page", "value": s.Referer, "href": s.Referer},
	}
	if s.SpamReason != "" {
		details = append(details, map[string]any{"label": "Why it's spam", "value": s.SpamReason})
	}
	mail := []map[string]any{}
	for _, k := range []string{"admin", "client"} {
		if v, ok := s.Mail[k]; ok {
			mail = append(mail, map[string]any{"label": map[string]string{"admin": "To you", "client": "Confirmation"}[k], "value": v,
				"tone": map[bool]string{true: "danger", false: "success"}[!strings.HasPrefix(v, "sent")]})
		}
	}
	if len(mail) == 0 {
		mail = append(mail, map[string]any{"label": "Email", "value": "None sent"})
	}
	if s.HookStatus != "" {
		details = append(details, map[string]any{"label": "Hook", "value": s.HookStatus,
			"tone": map[bool]string{true: "success", false: "danger"}[strings.HasPrefix(s.HookStatus, "done")]})
	}
	hookKeys := slices.Sorted(maps.Keys(s.Hook))
	for _, k := range hookKeys {
		answers = append(answers, map[string]any{"label": k + " (from the hook)", "value": s.Hook[k], "multiline": strings.Contains(s.Hook[k], "\n")})
	}
	hook := []map[string]any{}
	if s.HookStatus != "" {
		hook = stepItems(s.HookSteps)
		if s.HookCall != "" {
			hook = append(hook, map[string]any{"label": "Hook log", "value": "This call", "screen": "hook_call", "params": map[string]string{"id": s.HookCall}})
		}
	}
	return reply(200, map[string]any{"submission": map[string]any{"id": s.ID, "summary": name, "status": s.Status, "form": s.Form,
		"answers": answers, "details": details, "mail": mail, "hook": hook}})
}

func (c *call) setStatus(s *Submission, status string) {
	if s.Status == status {
		return
	}
	st := c.formStats(s.Form)
	st.add(s.Status, -1)
	st.add(status, 1)
	s.Status = status
	c.kvSetJSON("stat:"+s.Form, st)
	c.kvSetJSON("sub:"+s.Form+":"+s.ID, *s)
}

func (c *call) setSubmissionStatus() *plugin.Response {
	req := c.req
	var body struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(req.Body, &body) // a bad body has no status, refused below
	if body.Status != "new" && body.Status != "read" && body.Status != "spam" {
		return invalid(map[string][]string{"status": {"is new, read or spam"}})
	}
	s, ok := c.loadSubmission(req.q("id"))
	if !ok {
		return notFound("No submission " + req.q("id") + ".")
	}
	c.setStatus(&s, body.Status)
	return reply(200, map[string]any{"ok": true, "message": map[string]string{"spam": "Marked as spam.", "read": "Marked as read.", "new": "Marked as unread."}[body.Status]})
}

func (c *call) deleteSubmission() *plugin.Response {
	req := c.req
	s, ok := c.loadSubmission(req.q("id"))
	if !ok {
		return notFound("No submission " + req.q("id") + ".")
	}
	st := c.formStats(s.Form)
	st.add(s.Status, -1)
	c.kvSetJSON("stat:"+s.Form, st)
	c.kvDelete("sub:" + s.Form + ":" + s.ID)
	c.kvDelete("all:" + s.ID)
	return reply(200, map[string]any{"ok": true, "message": "Deleted."})
}

// export_submissions is a form's submissions (or every form's) as CSV, a
// column per field, newest first.
func (c *call) exportSubmissions() *plugin.Response {
	req := c.req
	slug, status := req.q("form"), req.q("status")
	forms := []Form{}
	if slug != "" {
		f, ok := c.loadForm(slug)
		if !ok {
			return notFound("No form " + slug + ".")
		}
		forms = append(forms, f)
	} else {
		forms = c.allForms()
	}
	var cols []Field
	seen := map[string]bool{}
	for _, f := range forms {
		for _, fl := range f.Fields {
			if !fl.layout() && !seen[fl.Name] {
				seen[fl.Name] = true
				cols = append(cols, fl)
			}
		}
	}
	type found struct {
		form string
		sub  Submission
	}
	var subs []found
	hookCols := map[string]bool{}
	for _, f := range forms {
		for _, k := range c.kvAll("sub:" + f.Slug + ":") {
			var s Submission
			if !c.kvGetJSON(k, &s) || (status == "" && s.Status == "spam") || (status != "" && s.Status != status) {
				continue
			}
			subs = append(subs, found{f.Name, s})
			for k := range s.Hook {
				hookCols[k] = true
			}
		}
	}
	hookKeys := slices.Sorted(maps.Keys(hookCols))
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	head := []string{"Submitted", "Form", "Status"}
	for _, c := range cols {
		head = append(head, strOr(c.Label, c.Name))
	}
	head = append(head, hookKeys...)
	_ = w.Write(head) // to a buffer; Flush reports
	for _, x := range subs {
		row := []string{x.sub.SubmittedAt, x.form, x.sub.Status}
		for _, c := range cols {
			row = append(row, csvSafe(display(x.sub.Values[c.Name])))
		}
		for _, k := range hookKeys {
			row = append(row, csvSafe(x.sub.Hook[k]))
		}
		_ = w.Write(row)
	}
	w.Flush()
	name := "submissions"
	if slug != "" {
		name = slug + "-submissions"
	}
	return replyRaw(200, map[string]string{"Content-Type": "text/csv; charset=utf-8",
		"Content-Disposition": `attachment; filename="` + name + `.csv"`}, "\ufeff"+buf.String())
}

// csvSafe keeps a spreadsheet from running what a visitor typed as a
// formula.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
