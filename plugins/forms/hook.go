package forms

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

// ---- fields shown only when another answer says so --------------------------

// ShowIf shows a field only when an earlier field's answer is one of Values
// (a consent checkbox's answer is "yes"). A field hidden by it isn't asked
// for: not required, and whatever was posted for it is dropped.
type ShowIf struct {
	Field  string   `json:"field"`
	Values []string `json:"values"`
}

// shownFields is, for each field that takes an answer, whether it's shown
// given what was posted. A field whose condition names a hidden field is
// hidden too.
func shownFields(fields []Field, p posted) map[string]bool {
	shown := map[string]bool{}
	for _, fl := range fields {
		if fl.layout() {
			continue
		}
		if fl.ShowIf == nil {
			shown[fl.Name] = true
			continue
		}
		on := shown[fl.ShowIf.Field]
		if on {
			on = slices.ContainsFunc(nonEmpty(p.values[fl.ShowIf.Field]), func(v string) bool { return slices.Contains(fl.ShowIf.Values, v) })
		}
		shown[fl.Name] = on
	}
	return shown
}

// checkShowIf is what's wrong with fields' conditions: each must name an
// earlier field that takes an answer, and the answers that show it.
func checkShowIf(fields []Field, add func(k, m string)) {
	earlier := map[string]bool{}
	for i := range fields {
		fl := &fields[i]
		if fl.ShowIf != nil {
			at := fmt.Sprintf("fields[%d].show_if", i)
			fl.ShowIf.Field = strings.TrimSpace(fl.ShowIf.Field)
			fl.ShowIf.Values = nonEmpty(fl.ShowIf.Values)
			switch {
			case fl.ShowIf.Field == "" && len(fl.ShowIf.Values) == 0:
				fl.ShowIf = nil // an emptied condition is none
			case !earlier[fl.ShowIf.Field]:
				add(at+".field", "must be a field above this one")
			case len(fl.ShowIf.Values) == 0:
				add(at+".values", "needs at least one answer that shows it")
			}
		}
		if !fl.layout() && fl.Name != "" {
			earlier[fl.Name] = true
		}
	}
}

// ---- the submit hook --------------------------------------------------------

// hookTimeout is how long a submission waits for the form's hook.
const hookTimeout = 6 * time.Second

// hookAnswer is what a form's hook may answer: where to send the visitor,
// a message for them, and values kept with the submission (and usable as
// merge tags in its emails). Or, with 422, why the submission is refused.
type hookAnswer struct {
	Redirect string              `json:"redirect"`
	Message  string              `json:"message"`
	Values   map[string]any      `json:"values"`
	Errors   map[string][]string `json:"errors"`
}

// hookResult is how calling the hook went: refused (the visitor sees why),
// or what to carry on with; status is what's noted on the submission.
type hookResult struct {
	refused  *plugin.Response
	redirect string
	message  string
	values   map[string]string
	status   string
}

// callHook posts a submission to the form's hook, signed with its secret
// (X-CMS-Signature: sha256=<hex HMAC-SHA256 of the body>, as webhooks are),
// and reads its answer. A hook that's down, slow or answers something else
// doesn't lose the submission: it's kept and emailed as usual, and the
// failure is noted on it.
func (c *call) callHook(f Form, sub Submission, site plugin.Site) hookResult {
	body, _ := json.Marshal(map[string]any{
		"event": "submission.created", "form": f.Slug, "form_name": f.Name,
		"submission_id": sub.ID, "submitted_at": sub.SubmittedAt, "values": sub.Values,
	})
	ctx, cancel := context.WithTimeout(c.ctx, hookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.HookURL, bytes.NewReader(body))
	if err != nil {
		return hookResult{status: "failed: " + err.Error()}
	}
	mac := hmac.New(sha256.New, []byte(f.HookSecret))
	mac.Write(body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CMS-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-CMS-Form", f.Slug)
	res, err := c.api.HTTP().Do(req)
	if err != nil {
		c.logf("forms: hook for " + f.Slug + " failed: " + err.Error())
		return hookResult{status: "failed: " + err.Error()}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var ans hookAnswer
	_ = json.Unmarshal(raw, &ans) // an answer that isn't JSON says nothing more than its status

	if res.StatusCode == http.StatusUnprocessableEntity {
		msg := strOr(ans.Message, "Some fields need attention.")
		errs := map[string][]string{}
		for k, v := range ans.Errors {
			if v = nonEmpty(v); len(v) > 0 {
				errs[k] = v
			}
		}
		return hookResult{refused: reply(422, map[string]any{"error": "invalid", "message": msg, "errors": errs})}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		c.logf(fmt.Sprintf("forms: hook for %s answered HTTP %d", f.Slug, res.StatusCode))
		return hookResult{status: fmt.Sprintf("failed: HTTP %d", res.StatusCode)}
	}

	out := hookResult{status: "done", message: strings.TrimSpace(ans.Message), values: map[string]string{}}
	if ans.Redirect != "" {
		if to, ok := hookRedirect(ans.Redirect, site); ok {
			out.redirect = to
		} else {
			out.status = "done, but its redirect isn't on the site: " + truncate(ans.Redirect, 200)
		}
	}
	for k, v := range ans.Values {
		if nameFormat.MatchString(k) && v != nil {
			out.values[k] = truncate(display(v), 2000)
		}
	}
	return out
}

// hookRedirect is where a hook may send the visitor: a path on the site, or
// an address on one of its origins.
func hookRedirect(to string, site plugin.Site) (string, bool) {
	to = strings.TrimSpace(to)
	if strings.HasPrefix(to, "/") && !strings.HasPrefix(to, "//") && !strings.HasPrefix(to, "/\\") {
		return to, true
	}
	return onSite(to, append([]string{site.URL}, site.Origins...))
}

// checkHook is what's wrong with a form's hook: an http(s) address on one
// of the site's origins (the only hosts the plugin may call), with a secret
// to sign its posts.
func checkHook(f *Form, site plugin.Site, add func(k, m string)) {
	f.HookURL = strings.TrimSpace(f.HookURL)
	if f.HookURL == "" {
		return
	}
	if _, ok := onSite(f.HookURL, append([]string{site.URL}, site.Origins...)); !ok {
		add("hook_url", "must be an address on the site (its public URL, or one of Settings › General › Allowed origins)")
	}
	if strings.TrimSpace(f.HookSecret) == "" {
		add("hook_secret", "can't be blank: the hook checks each post's signature with it")
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
