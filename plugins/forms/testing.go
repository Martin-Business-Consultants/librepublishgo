package forms

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

// testForm sends the form's emails as they'd go out for a submission, to
// its testing addresses only, so they can be seen without bothering the
// people who get the real ones. It takes the form as it is in the editor,
// saved or not; the answers are samples. When the form has a hook, it's
// called first as a test (Test hook's "test": true), and the values it
// adds (prices from a quote tool, say) go in the emails. Nothing is kept
// but that call, in the hook log, and webhooks don't hear of it.
func (c *call) testForm() *plugin.Response {
	var body struct {
		Form Form `json:"form"`
	}
	if err := json.Unmarshal(c.req.Body, &body); err != nil {
		return reply(400, map[string]string{"error": "bad_request", "message": "Send {\"form\": {…}}."})
	}
	f := c.withSavedSecret(body.Form)
	errs := validateForm(&f)
	if _, bad := errs["testing_to"]; len(f.TestingTo) == 0 && !bad {
		errs["testing_to"] = append(errs["testing_to"], "needs an address to send tests to")
	}
	if len(errs) > 0 {
		return invalid(errs)
	}
	if !f.AdminEnabled && !f.ClientEnabled {
		return reply(422, map[string]any{"error": "invalid", "message": "Neither email is on: turn on Admin Confirmation or User Confirmation first.", "errors": map[string][]string{}})
	}
	client := strOr(strings.TrimSpace(f.TestingClientTo), f.TestingTo[0])
	t := f
	t.AdminTo, t.AdminCc, t.AdminBcc = f.TestingTo, f.TestingCc, f.TestingBcc
	t.AdminSubject = "[Test] " + strOr(f.AdminSubject, "New {{ form_name }} submission")
	t.ClientSubject = "[Test] " + f.ClientSubject
	sub := sampleSubmission(f, client)
	parts := []string{}
	if f.HookURL != "" {
		sub.ID = newID()
		res := c.callHookAs(f, sub, c.siteInfo(), true)
		c.recordHookCall(res.call)
		switch res.call.Outcome {
		case "accepted":
			sub.Hook, sub.HookTables = res.values, res.tables
			parts = append(parts, fmt.Sprintf("the hook (as a test) added %d values", len(res.values)))
		case "refused":
			parts = append(parts, "the hook refused the sample answers ("+hookSummary(res.call)+"), so its values aren't in")
		default:
			parts = append(parts, "the hook failed ("+res.call.Error+"), so its values aren't in")
		}
	}
	sent := c.notify(t, sub)
	if r, ok := sent["admin"]; ok {
		parts = append(parts, fmt.Sprintf("the submission email %s (%s)", r, strings.Join(append(append(slices.Clone(t.AdminTo), t.AdminCc...), t.AdminBcc...), ", ")))
	}
	if r, ok := sent["client"]; ok {
		parts = append(parts, fmt.Sprintf("the confirmation %s (%s)", r, client))
	}
	msg := "Test: " + strings.Join(parts, "; ") + "."
	failed := strings.Contains(sent["admin"], "failed") || strings.Contains(sent["client"], "failed")
	return reply(map[bool]int{true: 502, false: 200}[failed], map[string]any{"ok": !failed, "message": msg, "mail": sent})
}

// sampleSubmission is a submission to f with an answer of each field's
// kind: its default or first choice where it has one, email in every email
// field. A field its condition hides with those answers is left out, as it
// would be.
func sampleSubmission(f Form, email string) Submission {
	values := map[string]any{}
	p := posted{values: map[string][]string{}}
	for _, fl := range f.Fields {
		if fl.layout() || fl.Type == "file" {
			continue
		}
		v := sampleAnswer(fl, email)
		values[fl.Name] = v
		switch x := v.(type) {
		case string:
			p.values[fl.Name] = []string{x}
		case []string:
			p.values[fl.Name] = x
		}
	}
	shown := shownFields(f.Fields, p)
	for name := range values {
		if !shown[name] {
			delete(values, name)
		}
	}
	return Submission{ID: "test", Form: f.Slug, Status: "new", Values: values, SubmittedAt: now()}
}

func sampleAnswer(fl Field, email string) any {
	label := strOr(fl.Label, fl.Name)
	first := fl.Default
	if first == "" && len(fl.Options) > 0 {
		first = fl.Options[0]
	}
	switch fl.Type {
	case "email":
		return email
	case "tel":
		return strOr(fl.Placeholder, "(555) 010-0100")
	case "number":
		if fl.Min != nil {
			return fmt.Sprint(*fl.Min)
		}
		return "1"
	case "select", "radio":
		return first
	case "checkboxes":
		if len(fl.Options) > 0 {
			return []string{fl.Options[0]}
		}
		return []string{}
	case "checkbox":
		return "yes"
	case "date":
		return time.Now().Format(time.DateOnly)
	case "url":
		return "https://example.com"
	case "hidden":
		return fl.Default
	case "textarea":
		return "A sample answer for " + label + "."
	}
	return strOr(fl.Default, strOr(fl.Placeholder, "Sample "+label))
}
