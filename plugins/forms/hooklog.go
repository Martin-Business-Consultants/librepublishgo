package forms

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

// A HookCall is one call of a form's hook, kept under "hook:<slug>:<id>"
// (and indexed under "hookall:<id>"), newest first: what was sent, what
// came back, the steps the hook said it took, and the submission it led to.
// Refusals are kept too, though their submissions aren't: they're how an
// out-of-area lead, say, is seen at all.
type HookCall struct {
	ID         string              `json:"id"`
	Form       string              `json:"form"`
	At         string              `json:"at"`
	Test       bool                `json:"test,omitempty"`
	Outcome    string              `json:"outcome"` // accepted, refused or failed
	Status     int                 `json:"status"`  // the HTTP status, 0 for none
	DurationMS int64               `json:"duration_ms"`
	Error      string              `json:"error,omitempty"`
	Sent       map[string]any      `json:"sent"` // the answers posted
	Redirect   string              `json:"redirect,omitempty"`
	Message    string              `json:"message,omitempty"`
	Values     map[string]any      `json:"values,omitempty"`
	Errors     map[string][]string `json:"errors,omitempty"`
	Steps      []HookStep          `json:"steps,omitempty"`
	Submission string              `json:"submission,omitempty"` // its id, when it was kept
}

// A HookStep is something a hook says it did, shown as it said it: the CMS
// doesn't know what a zip check or a quote is. Status is ok, failed,
// skipped, warning or info.
type HookStep struct {
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

var stepStatuses = map[string]bool{"ok": true, "failed": true, "skipped": true, "warning": true, "info": true}

// cleanSteps is a hook's steps as kept: at most 50, each labelled, its
// status one there is (else info), its text bounded.
func cleanSteps(in []HookStep) []HookStep {
	out := []HookStep{}
	for _, s := range in {
		s.Label, s.Detail, s.Status = truncate(strings.TrimSpace(s.Label), 120), truncate(strings.TrimSpace(s.Detail), 1000), strings.ToLower(strings.TrimSpace(s.Status))
		if s.Label == "" {
			continue
		}
		if !stepStatuses[s.Status] {
			s.Status = "info"
		}
		out = append(out, s)
		if len(out) == 50 {
			break
		}
	}
	return out
}

// hookLogKeep is how many calls are kept per form; past it, the oldest go.
const hookLogKeep = 1000

// recordHookCall keeps hc, and lets the oldest of its form's go past
// hookLogKeep.
func (c *call) recordHookCall(hc HookCall) {
	if hc.ID == "" {
		hc.ID = newID()
	}
	c.kvSetJSON("hook:"+hc.Form+":"+hc.ID, hc)
	c.kvSet("hookall:"+hc.ID, hc.Form)
	if n, err := c.api.KVCount(c.ctx, "hook:"+hc.Form+":"); err != nil || n <= hookLogKeep+100 {
		return
	}
	keys := c.kvAll("hook:" + hc.Form + ":") // newest first
	for _, k := range keys[hookLogKeep:] {
		c.kvDelete(k)
		c.kvDelete("hookall:" + k[strings.LastIndex(k, ":")+1:])
	}
}

func (c *call) loadHookCall(id string) (HookCall, bool) {
	var hc HookCall
	slug, ok := c.kvGet("hookall:" + id)
	if !ok {
		return hc, false
	}
	return hc, c.kvGetJSON("hook:"+slug+":"+id, &hc)
}

// hookSummary is a call in a line: where it sent the visitor, or why it
// refused them, or how it failed.
func hookSummary(hc HookCall) string {
	switch hc.Outcome {
	case "refused":
		parts := []string{}
		for _, k := range sortedKeys(hc.Errors) {
			parts = append(parts, k+": "+strings.Join(hc.Errors[k], ", "))
		}
		return strOr(strings.Join(parts, "; "), hc.Message)
	case "failed":
		return hc.Error
	}
	return strOr(hc.Redirect, strOr(hc.Message, "accepted"))
}

// hookWho is who a call was about, from the answers sent: a name, else an
// email address.
func hookWho(f Form, hc HookCall) string {
	name, _ := summary(f, Submission{Values: hc.Sent})
	return name
}

// listHookCalls is the hook log: newest first, by form and outcome.
func (c *call) listHookCalls() *plugin.Response {
	req := c.req
	slug, outcome := req.q("form"), req.q("outcome")
	forms := c.allForms()
	byslug := map[string]Form{}
	options := []map[string]string{{"value": "", "label": "Every form"}}
	for _, f := range forms {
		byslug[f.Slug] = f
		if f.HookURL != "" {
			options = append(options, map[string]string{"value": f.Slug, "label": f.Name})
		}
	}
	prefix := "hookall:"
	if slug != "" {
		prefix = "hook:" + slug + ":"
	}
	const per = 25
	rows := []map[string]any{}
	after, next := req.q("after"), ""
	for len(rows) < per {
		keys, more := c.kvKeys(prefix, after, 100)
		for i, k := range keys {
			hc, ok := c.loadHookCall(k[strings.LastIndex(k, ":")+1:])
			if !ok || (outcome == "test" && !hc.Test) || (outcome != "" && outcome != "test" && hc.Outcome != outcome) {
				continue
			}
			who := hookWho(byslug[hc.Form], hc)
			if hc.Test {
				who += " (test)"
			}
			rows = append(rows, map[string]any{"id": hc.ID, "who": who, "summary": truncate(hookSummary(hc), 140),
				"form_name": strOr(byslug[hc.Form].Name, hc.Form), "outcome": hc.Outcome, "duration": fmt.Sprintf("%d ms", hc.DurationMS), "at": hc.At})
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
	outcomes := []map[string]any{{"value": "", "label": "All"}, {"value": "accepted", "label": "Accepted"},
		{"value": "refused", "label": "Refused"}, {"value": "failed", "label": "Failed"}, {"value": "test", "label": "Tests"}}
	return reply(200, map[string]any{"calls": rows, "next": next, "forms": options, "outcomes": outcomes})
}

// showHookCall is one call: what was sent, what came back, its steps.
func (c *call) showHookCall() *plugin.Response {
	hc, ok := c.loadHookCall(c.req.q("id"))
	if !ok {
		return notFound("No hook call " + c.req.q("id") + ".")
	}
	f, _ := c.loadForm(hc.Form)
	details := []map[string]any{
		{"label": "Form", "value": strOr(f.Name, hc.Form), "screen": "form", "params": map[string]string{"id": hc.Form}},
		{"label": "When", "value": hc.At, "time": true},
		{"label": "Outcome", "value": hc.Outcome, "badge": true},
		{"label": "HTTP status", "value": map[bool]string{true: "—", false: strconv.Itoa(hc.Status)}[hc.Status == 0]},
		{"label": "Took", "value": fmt.Sprintf("%d ms", hc.DurationMS)},
	}
	if hc.Test {
		details = append(details, map[string]any{"label": "Test", "value": "Sent with \"test\": true, from Send test hook"})
	}
	if hc.Submission != "" {
		details = append(details, map[string]any{"label": "Submission", "value": "View it", "screen": "submission", "params": map[string]string{"id": hc.Submission}})
	}
	answer := []map[string]any{}
	if hc.Error != "" {
		answer = append(answer, map[string]any{"label": "Error", "value": hc.Error, "tone": "danger"})
	}
	if hc.Redirect != "" {
		answer = append(answer, map[string]any{"label": "Sent the visitor to", "value": hc.Redirect})
	}
	if hc.Message != "" {
		answer = append(answer, map[string]any{"label": "Message", "value": hc.Message})
	}
	for _, k := range sortedKeys(hc.Errors) {
		answer = append(answer, map[string]any{"label": "Refused: " + k, "value": strings.Join(hc.Errors[k], ", "), "tone": "danger"})
	}
	for _, k := range sortedKeys(hc.Values) {
		answer = append(answer, map[string]any{"label": k + " (added)", "value": display(hc.Values[k])})
	}
	if len(answer) == 0 {
		answer = append(answer, map[string]any{"label": "Answer", "value": "Nothing more than its status"})
	}
	sent := []map[string]any{}
	for _, k := range sortedKeys(hc.Sent) {
		sent = append(sent, map[string]any{"label": k, "value": display(hc.Sent[k])})
	}
	return reply(200, map[string]any{"call": map[string]any{"id": hc.ID, "title": hookWho(f, hc) + " · " + hc.Outcome, "outcome": hc.Outcome,
		"steps": stepItems(hc.Steps), "answer": answer, "sent": sent, "details": details}})
}

// stepItems is a hook's steps as a screen shows them.
func stepItems(steps []HookStep) []map[string]any {
	out := []map[string]any{}
	tones := map[string]string{"ok": "success", "failed": "danger", "warning": "danger"}
	for _, s := range steps {
		parts := []string{}
		for _, p := range []string{stepWord(s.Status), s.Detail} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		item := map[string]any{"label": s.Label, "value": strings.Join(parts, " · ")}
		if t := tones[s.Status]; t != "" {
			item["tone"] = t
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"label": "Steps", "value": "The hook didn't report any (its answer's steps)."})
	}
	return out
}

func stepWord(status string) string {
	return map[string]string{"ok": "OK", "failed": "Failed", "skipped": "Skipped", "warning": "Warning", "info": ""}[status]
}

// testHook posts sample answers to the form's hook, as it's set in the
// editor, marked "test": true (and X-CMS-Test: 1) so the site can skip what
// it shouldn't do for one, and shows what came back. Nothing is kept but
// the call, in the hook log, as a test.
func (c *call) testHook() *plugin.Response {
	var body struct {
		Form Form `json:"form"`
	}
	if err := json.Unmarshal(c.req.Body, &body); err != nil {
		return reply(400, map[string]string{"error": "bad_request", "message": "Send {\"form\": {…}}."})
	}
	f := body.Form
	if strings.HasPrefix(f.HookSecret, "***") {
		if old, ok := c.loadForm(c.req.q("id")); ok {
			f.HookSecret = old.HookSecret
		}
	}
	errs := validateForm(&f)
	site := c.siteInfo()
	checkHook(&f, site, func(k, m string) { errs[k] = append(errs[k], m) })
	if f.HookURL == "" {
		errs["hook_url"] = append(errs["hook_url"], "needs an address to test: set it in the Hook tab")
	}
	if len(errs) > 0 {
		return invalid(errs)
	}
	email := strOr(strings.TrimSpace(f.TestingClientTo), "test@example.com")
	if len(f.TestingTo) > 0 && strings.TrimSpace(f.TestingClientTo) == "" {
		email = f.TestingTo[0]
	}
	sub := sampleSubmission(f, email)
	sub.ID = newID()
	res := c.callHookAs(f, sub, site, true)
	c.recordHookCall(res.call)
	hc := res.call
	took := fmt.Sprintf(" · %d ms", hc.DurationMS)
	if hc.Status != 0 {
		took = fmt.Sprintf(" · HTTP %d", hc.Status) + took
	}
	items := []map[string]any{{"label": "Outcome", "value": hc.Outcome + took,
		"tone": map[string]string{"accepted": "success", "refused": "danger", "failed": "danger"}[hc.Outcome]}}
	if s := hookSummary(hc); s != "" {
		items = append(items, map[string]any{"label": map[string]string{"accepted": "Sent the visitor to", "refused": "Refused", "failed": "Error"}[hc.Outcome], "value": s})
	}
	for _, k := range sortedKeys(hc.Values) {
		items = append(items, map[string]any{"label": k + " (added)", "value": display(hc.Values[k])})
	}
	if len(hc.Steps) > 0 {
		items = append(items, stepItems(hc.Steps)...)
	}
	for _, k := range sortedKeys(hc.Sent) {
		items = append(items, map[string]any{"label": "Sent: " + k, "value": display(hc.Sent[k])})
	}
	return reply(200, map[string]any{"ok": hc.Outcome != "failed", "message": "Test hook: " + hc.Outcome + ".",
		"result": map[string]any{"title": "Test hook · " + f.Name, "items": items}})
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
