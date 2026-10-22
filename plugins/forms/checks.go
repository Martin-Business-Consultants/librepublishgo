package forms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

// What Forms adds to the site's launch checklist (docs/launch-checklist.md):
// that the open forms keep spam out with real keys, that their email goes
// somewhere, and that their hooks are the live site's and answer.

// hookEvidenceDays is how long a hook's last answer is trusted.
const hookEvidenceDays = 30

// The captcha providers' published test keys: they pass every visitor, so
// a live form on them has no captcha at all.
var (
	turnstileTestKey = regexp.MustCompile(`^[123]x0{20,}[A-F]{2}$`)
	recaptchaTestKey = map[string]bool{"6LeIxAcTAAAAAJcZVRqyHh4OPTjjzbP4Ow3Ki1kA": true, "6LeIxAcTAAAAAGG-vFI1TnRWxMZNFuojJ4WifJWe": true}
)

func checks(ctx context.Context, api *plugin.API) []plugin.Check {
	c := &call{ctx: ctx, api: api}
	site, _ := api.Site(ctx)
	var open []Form
	for _, f := range c.allForms() {
		if f.Status == "active" {
			full, _ := c.loadForm(f.Slug)
			open = append(open, full)
		}
	}
	captcha := plugin.Check{Key: "forms.captcha", Group: "forms", Title: "Forms keep spam out with real captcha keys", Launch: true, Fix: formsScreen, FixLabel: "Forms"}
	recipients := plugin.Check{Key: "forms.recipients", Group: "forms", Title: "Every form's email goes somewhere", Launch: true, Fix: formsScreen, FixLabel: "Forms"}
	hookSite := plugin.Check{Key: "forms.hook_site", Group: "forms", Title: "Form hooks call the live site", Launch: true, Fix: formsScreen, FixLabel: "Forms"}
	hookOK := plugin.Check{Key: "forms.hook_answers", Group: "forms", Title: "Form hooks answer", Fix: formsScreen, FixLabel: "Forms"}
	if len(open) == 0 {
		na := func(ch plugin.Check) plugin.Check {
			ch.State, ch.Evidence = "n_a", "No form is taking submissions."
			return ch
		}
		return []plugin.Check{na(captcha), na(recipients), na(hookSite), na(hookOK)}
	}
	hookSite, hookOK = checkHooks(c, open, site, hookSite, hookOK)
	out := []plugin.Check{checkCaptcha(open, captcha), checkRecipients(open, site, recipients), hookSite, hookOK}
	if secret := checkHookSecrets(c, open); secret != nil {
		out = append(out, *secret)
	}
	return out
}

const formsScreen = "#/plugins/forms/forms"

// formScreen is where a form is edited.
func formScreen(f Form) string { return "#/plugins/forms/form?id=" + url.QueryEscape(f.Slug) }

func checkCaptcha(open []Form, ch plugin.Check) plugin.Check {
	var weak []string
	for _, f := range open {
		if why := captchaProblem(f); why != "" {
			weak = append(weak, f.Name+": "+why)
			if ch.Fix == formsScreen {
				ch.Fix, ch.FixLabel = formScreen(f), f.Name+" › Spam"
			}
		}
	}
	if len(weak) > 0 {
		ch.State, ch.Evidence = "fails", strings.Join(weak, "; ")+"."
	} else {
		ch.State, ch.Evidence = "passes", fmt.Sprintf("All %d open forms check a captcha with their own keys.", len(open))
	}
	return ch
}

func checkRecipients(open []Form, site plugin.Site, ch plugin.Check) plugin.Check {
	var nowhere []string
	for _, f := range open {
		if f.AdminEnabled && len(f.AdminTo) == 0 && site.ContactEmail == "" {
			nowhere = append(nowhere, f.Name)
		}
		if !f.AdminEnabled && !f.ClientEnabled && f.HookURL == "" && !f.StoreSubmissions {
			nowhere = append(nowhere, f.Name+" (nothing is kept, sent or emailed)")
		}
	}
	if len(nowhere) > 0 {
		ch.State, ch.Evidence = "fails", "Submissions reach no one: "+strings.Join(nowhere, ", ")+". Give the form addresses to send to (Admin Confirmation), or Settings › General a contact email."
	} else {
		ch.State, ch.Evidence = "passes", "Every open form's submissions are sent or kept."
	}
	return ch
}

// checkHooks is whether the open forms' hooks are the live site's, and
// whether each answered the last time it was called.
func checkHooks(c *call, open []Form, site plugin.Site, hookSite, hookOK plugin.Check) (plugin.Check, plugin.Check) {
	var hooked, elsewhere, failing, silent, stale, fine []string
	live := host(site.URL)
	now := time.Now().UTC()
	for _, f := range open {
		if strings.TrimSpace(f.HookURL) == "" {
			continue
		}
		hooked = append(hooked, f.Name)
		if host(f.HookURL) != live {
			elsewhere = append(elsewhere, fmt.Sprintf("%s calls %s", f.Name, f.HookURL))
			if hookSite.Fix == formsScreen {
				hookSite.Fix, hookSite.FixLabel = formScreen(f), f.Name+" › Hook"
			}
		}
		keys, _ := c.kvKeys("hook:"+f.Slug+":", "", 1) // newest first
		var hc HookCall
		switch {
		case len(keys) == 0 || !c.kvGetJSON(keys[0], &hc):
			silent = append(silent, f.Name)
		case hc.Outcome == "failed":
			failing = append(failing, fmt.Sprintf("%s (%s: %s)", f.Name, hc.At, strOr(hc.Error, fmt.Sprintf("HTTP %d", hc.Status))))
		default:
			at, err := time.Parse(time.RFC3339, hc.At)
			kind := "a submission"
			if hc.Test {
				kind = "a test"
			}
			if err != nil || now.Sub(at) > hookEvidenceDays*24*time.Hour {
				stale = append(stale, fmt.Sprintf("%s (last %s)", f.Name, hc.At))
			} else {
				fine = append(fine, fmt.Sprintf("%s: %s %s for %s, %d ms", f.Name, hc.Outcome, at.Format("2 Jan 15:04"), kind, hc.DurationMS))
			}
		}
	}
	switch {
	case len(hooked) == 0:
		hookSite.State, hookSite.Evidence = "n_a", "No open form has a hook."
		hookOK.State, hookOK.Evidence = "n_a", "No open form has a hook."
		return hookSite, hookOK
	case len(elsewhere) > 0:
		hookSite.State, hookSite.Evidence = "fails", strings.Join(elsewhere, "; ")+fmt.Sprintf(", not the live site (%s): a preview's or a laptop's. Point it at the live site before launch.", strOr(site.URL, "no public site URL set"))
	default:
		hookSite.State, hookSite.Evidence = "passes", fmt.Sprintf("%s call %s.", strings.Join(hooked, ", "), site.URL)
	}
	switch {
	case len(failing) > 0:
		hookOK.State, hookOK.Evidence = "fails", "The last call failed: "+strings.Join(failing, "; ")+". See Forms › Hook log; the submissions went through without it."
	case len(silent) > 0:
		hookOK.State, hookOK.Evidence = "unknown", "Never called: "+strings.Join(silent, ", ")+". Press Test hook on the form's Testing tab."
	case len(stale) > 0:
		hookOK.State, hookOK.Evidence = "warns", fmt.Sprintf("Not called in %d days: %s. Press Test hook to see it still answers.", hookEvidenceDays, strings.Join(stale, ", "))
	default:
		hookOK.State, hookOK.Evidence = "passes", strings.Join(fine, "; ")+"."
	}
	return hookSite, hookOK
}

// captchaProblem is what's wrong with a form's captcha, "" when nothing.
func captchaProblem(f Form) string {
	switch f.Captcha {
	case "turnstile":
		switch {
		case f.TurnstileSiteKey == "" || f.TurnstileSecret == "":
			return "Turnstile without its keys"
		case turnstileTestKey.MatchString(f.TurnstileSiteKey) || turnstileTestKey.MatchString(f.TurnstileSecret):
			return "Turnstile's test keys, which pass everyone"
		}
	case "recaptcha":
		switch {
		case f.RecaptchaSiteKey == "" || f.RecaptchaSecret == "":
			return "reCAPTCHA without its keys"
		case recaptchaTestKey[f.RecaptchaSiteKey] || recaptchaTestKey[f.RecaptchaSecret]:
			return "reCAPTCHA's test keys, which pass everyone"
		}
	default:
		return "no captcha (only the honeypot and timing)"
	}
	return ""
}

// host is an address's host, lowercased; "" for none.
func host(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// checkHookSecrets asks each hooked form's site whether it shares the form's
// secret: a question to its /_cms/checks (@librepublish/astro 0.8), signed
// with the form's hook secret, verifies only when the site's
// CMS_FORM_HOOK_SECRET is the same. A mismatch is what makes every hook
// call fail its signature, and the leads go without what the hook does.
// nil when no open form has a hook.
func checkHookSecrets(c *call, open []Form) *plugin.Check {
	ch := plugin.Check{Key: "forms.hook_secret", Group: "forms", Title: "Each form's hook secret is the site's", Launch: true, Fix: formsScreen, FixLabel: "Forms"}
	var hooked, matched, refused, unknown []string
	for _, f := range open {
		if strings.TrimSpace(f.HookURL) == "" {
			continue
		}
		hooked = append(hooked, f.Name)
		u, err := url.Parse(f.HookURL)
		if err != nil || u.Host == "" {
			unknown = append(unknown, f.Name+" (its hook address doesn't parse)")
			continue
		}
		at := u.Scheme + "://" + u.Host + "/_cms/checks"
		ans := askSite(c, at, f.HookSecret)
		switch {
		case ans.err != "":
			unknown = append(unknown, fmt.Sprintf("%s (couldn't reach %s: %s)", f.Name, at, ans.err))
		case ans.status == http.StatusOK && ans.verifiedWith == "form_hook":
			matched = append(matched, f.Name)
		case ans.status == http.StatusOK:
			matched = append(matched, f.Name+" (the site verified it as its purge secret)")
		case ans.status == http.StatusUnauthorized:
			refused = append(refused, f.Name)
			if ch.Fix == formsScreen {
				ch.Fix, ch.FixLabel = formScreen(f), f.Name+" › Hook"
			}
		default:
			unknown = append(unknown, fmt.Sprintf("%s (%s answered %d: the deployed site predates /_cms/checks, @librepublish/astro 0.8)", f.Name, at, ans.status))
		}
	}
	switch {
	case len(hooked) == 0:
		return nil
	case len(refused) > 0:
		ch.State, ch.Evidence = "fails", "The site refused a question signed with the hook secret of "+strings.Join(refused, ", ")+": its CMS_FORM_HOOK_SECRET isn't the form's (Hook tab), so every hook call fails its signature and the leads go without what the hook does."
	case len(unknown) > 0:
		ch.State, ch.Evidence = "unknown", "Couldn't ask: "+strings.Join(unknown, "; ")+"."
	default:
		ch.State, ch.Evidence = "passes", "The site verified the hook secret of "+strings.Join(matched, ", ")+"."
	}
	return &ch
}

type siteAnswer struct {
	status       int
	verifiedWith string
	err          string
	at           time.Time
}

// asked keeps each answer a minute, so the checklist's screen and its badge
// don't ask the site twice a page view.
var asked sync.Map

// askSite posts a question to a site's /_cms/checks, signed with secret.
func askSite(c *call, at, secret string) siteAnswer {
	sum := sha256.Sum256([]byte(secret))
	key := at + " " + hex.EncodeToString(sum[:8])
	if v, ok := asked.Load(key); ok {
		if kept, ok := v.(siteAnswer); ok && time.Since(kept.at) < time.Minute {
			return kept
		}
	}
	ans := siteAnswer{at: time.Now().UTC()}
	defer func() { asked.Store(key, ans) }()
	ctx, cancel := context.WithTimeout(c.ctx, 4*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"purpose": "launch_check", "nonce": newID()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, at, bytes.NewReader(body))
	if err != nil {
		ans.err = err.Error()
		return ans
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	outbound.Sign(req.Header, secret, body, newID())
	res, err := c.api.HTTP().Do(req)
	if err != nil {
		ans.err = err.Error()
		return ans
	}
	defer res.Body.Close()
	ans.status = res.StatusCode
	var out struct {
		VerifiedWith string `json:"verified_with"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out)
	ans.verifiedWith = out.VerifiedWith
	return ans
}
