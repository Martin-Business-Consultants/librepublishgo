package cms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// ---- the launch checklist ------------------------------------------------
//
// The CMS's own items of the site's launch checklist (docs/launch-checklist.md):
// what a site needs of its CMS to go live and stay live, each checked here
// and answered with the evidence. The Launch plugin (plugins/launch) shows
// them beside the other plugins' (Forms adds its forms'); a site's build
// will add its own. Nothing here changes anything, and nothing blocks
// publishing: the checklist only shows.

// evidenceDays is how long a sign of life is trusted: a test email, a
// build report, a token's use.
const evidenceDays = 30

func (e pluginEnv) SiteChecks(ctx context.Context) []plugin.Check { return e.a.launchChecks(ctx) }

func (a *App) launchChecks(ctx context.Context) []plugin.Check {
	l := &checklist{now: time.Now().UTC(), out: []plugin.Check{}}
	a.checkGeneral(ctx, l)
	a.checkEmail(ctx, l)
	a.checkDeploy(ctx, l)
	a.checkTokens(ctx, l)
	a.checkMedia(ctx, l)
	a.checkContent(ctx, l)
	a.checkGlobals(ctx, l)
	a.checkBackup(ctx, l)
	a.checkSiteHealth(ctx, l)
	a.checkBuild(ctx, l)
	return l.out
}

// checklist collects the items as they're checked.
type checklist struct {
	now time.Time
	out []plugin.Check
}

func (l *checklist) add(c plugin.Check, state, evidence string) {
	c.State, c.Evidence = state, evidence
	l.out = append(l.out, c)
}

func (l *checklist) couldnt(c plugin.Check, err error) {
	l.add(c, "unknown", "Couldn't check: "+err.Error())
}

// checkGeneral is the checklist's general items.
func (a *App) checkGeneral(ctx context.Context, l *checklist) {
	g := a.general(ctx)
	site := plugin.Check{Key: "general.site_url", Group: "general", Title: "The public site's address is set", Launch: true, Fix: "#/settings/general", FixLabel: "Settings › General"}
	switch u := str(g["site_base_url"]); {
	case u == "":
		l.add(site, "fails", "Settings › General has no public site URL: canonicals, the sitemap and links in email can only be relative.")
	case !strings.HasPrefix(u, "https://"):
		l.add(site, "warns", u+" isn't https.")
	default:
		l.add(site, "passes", u)
	}

	cms := plugin.Check{Key: "general.cms_url", Group: "general", Title: "The CMS has an address of its own", Launch: true, Fix: "#/settings/general", FixLabel: "Settings › General"}
	switch base := a.BaseURL(); {
	case !a.addressKnown():
		l.add(cms, "fails", "The CMS doesn't know its own address, so file URLs (and the forms' address, which visitors' browsers call) start with "+base+": set APP_URL or Settings › General's CMS address.")
	case !strings.HasPrefix(base, "https://"):
		l.add(cms, "warns", base+" isn't https: browsers on the live site won't load forms or files from it.")
	case provisionalHost(base):
		l.add(cms, "warns", base+" is a provisional address (an IP, or a wildcard DNS name): visitors' browsers call it for every form, so give it a name of its own (cms.<site>) before launch.")
	default:
		l.add(cms, "passes", base)
	}

	origins := plugin.Check{Key: "general.origins", Group: "general", Title: "Allowed origins are the site's own", Fix: "#/settings/general", FixLabel: "Settings › General › Allowed origins"}
	var temporary []string
	for _, o := range a.siteOrigins(ctx) {
		if o != strings.TrimRight(str(g["site_base_url"]), "/") && provisionalOrigin(o) {
			temporary = append(temporary, o)
		}
	}
	if len(temporary) > 0 {
		l.add(origins, "warns", fmt.Sprintf("Allowed besides the site: %s. A preview or a laptop left here can read and post the forms; remove them once they're done with.", strings.Join(temporary, ", ")))
	} else {
		l.add(origins, "passes", "Allowed: "+strOr(strings.Join(a.siteOrigins(ctx), ", "), "none (the site URL isn't set)"))
	}

	contact := plugin.Check{Key: "general.contact", Group: "general", Title: "The site has a contact email", Fix: "#/settings/general", FixLabel: "Settings › General"}
	if e := str(g["email"]); e != "" {
		l.add(contact, "passes", e)
	} else {
		l.add(contact, "warns", "Settings › General has no contact email: a form with no recipients of its own sends to it, and the contact_info block shows it.")
	}
}

// checkEmail is the checklist's email items.
func (a *App) checkEmail(ctx context.Context, l *checklist) {
	smtp := plugin.Check{Key: "email.smtp", Group: "email", Title: "Email can be sent", Launch: true, Fix: "#/settings/email", FixLabel: "Settings › Email"}
	es := a.settingOr(ctx, "email")
	if str(es["smtp_host"]) == "" || str(es["from_email"]) == "" {
		l.add(smtp, "fails", "Settings › Email has no SMTP server or from address: no form email (notifications or confirmations) can be sent.")
		return
	}
	at, ok, err := a.lastAudit(ctx, "email.tested")
	switch {
	case err != nil:
		l.couldnt(smtp, err)
	case !ok:
		l.add(smtp, "warns", fmt.Sprintf("Set up (%s, from %s), but never tested: press Send test email in Settings › Email.", str(es["smtp_host"]), str(es["from_email"])))
	case l.now.Sub(at) > evidenceDays*24*time.Hour:
		l.add(smtp, "warns", fmt.Sprintf("The last test send that worked was %s: send another.", ago(l.now, at)))
	default:
		l.add(smtp, "passes", fmt.Sprintf("%s, from %s: a test send worked %s.", str(es["smtp_host"]), str(es["from_email"]), ago(l.now, at)))
	}
}

// checkDeploy is the checklist's deploy items.
func (a *App) checkDeploy(ctx context.Context, l *checklist) {
	deploy := a.settingOr(ctx, "deploy")
	front := a.settingOr(ctx, "frontend")
	build := a.settingOr(ctx, "frontend_build")
	hook := plugin.Check{Key: "deploy.hook", Group: "deploy", Title: "Publishing updates the site", Launch: true, Fix: "#/settings/deploy", FixLabel: "Settings › Deploy"}
	switch {
	case str(deploy["hook_url"]) != "" && str(front["webhook_url"]) != "":
		l.add(hook, "passes", "A deploy hook rebuilds the site, and its on-demand pages are purged.")
	case str(deploy["hook_url"]) != "":
		l.add(hook, "passes", "A deploy hook rebuilds the site when content is published.")
	case str(front["webhook_url"]) != "":
		l.add(hook, "passes", "The site's pages are purged when content is published (it renders on demand).")
	default:
		l.add(hook, "fails", "There's no deploy hook (Settings › Deploy): publishing changes nothing on a built site until someone rebuilds it.")
	}

	last := plugin.Check{Key: "deploy.last_build", Group: "deploy", Title: "The site has been built from this CMS lately", Fix: "#/settings/deploy", FixLabel: "Settings › Deploy"}
	if at, err := time.Parse(time.RFC3339, str(build["reported_at"])); err != nil {
		l.add(last, "unknown", "No build has reported to this CMS. A site on @librepublish/astro reports every build.")
	} else {
		evidence := fmt.Sprintf("Last build reported %s by %s, rendering %s.", ago(l.now, at), strOr(str(build["by"]), "a token"), strOr(str(build["render"]), "static"))
		if l.now.Sub(at) > evidenceDays*24*time.Hour {
			l.add(last, "warns", evidence+" Nothing since: is the site still built from here?")
		} else {
			l.add(last, "passes", evidence)
		}
	}

	pending := plugin.Check{Key: "deploy.pending", Group: "deploy", Title: "Nothing waits for approval", Fix: "#/settings/deploy", FixLabel: "Settings › Deploy"}
	if render := str(build["render"]); render != "" && render != "static" && (str(front["render"]) != render || str(front["webhook_url"]) != str(build["webhook_url"])) {
		l.add(pending, "warns", fmt.Sprintf("The last build renders %s and asks to be told of changes at %s: approve it in Settings › Deploy.", render, strOr(str(build["webhook_url"]), "(no address)")))
	} else {
		l.add(pending, "passes", "The site renders as the CMS has it.")
	}
}

// checkTokens is the checklist's tokens items.
func (a *App) checkTokens(ctx context.Context, l *checklist) {
	prod := plugin.Check{Key: "tokens.production", Group: "tokens", Title: "The site reads the CMS with a read-only token", Launch: true, Fix: "#/settings/tokens", FixLabel: "Settings › Service tokens"}
	admin := plugin.Check{Key: "tokens.admin", Group: "tokens", Title: "No forgotten Administrator tokens", Fix: "#/settings/tokens", FixLabel: "Settings › Service tokens"}
	if tokens, err := a.liveServiceTokens(ctx); err != nil {
		l.couldnt(prod, err)
		l.couldnt(admin, err)
	} else {
		var used, unused, idle []string
		for _, t := range tokens {
			switch {
			case t.role == "Production site" && t.lastUsed.Valid && l.now.Sub(t.lastUsed.Time) <= evidenceDays*24*time.Hour:
				used = append(used, fmt.Sprintf("%s (last used %s)", t.name, ago(l.now, t.lastUsed.Time)))
			case t.role == "Production site":
				unused = append(unused, t.name)
			case t.role == "Administrator" && (!t.lastUsed.Valid || l.now.Sub(t.lastUsed.Time) > evidenceDays*24*time.Hour):
				idle = append(idle, t.name)
			}
		}
		switch {
		case len(used) > 0:
			l.add(prod, "passes", "On the Production site role: "+strings.Join(used, ", "))
		case len(unused) > 0:
			l.add(prod, "warns", fmt.Sprintf("%s on the Production site role, but not used in %d days: is the site built with an Administrator token instead?", strings.Join(unused, ", "), evidenceDays))
		default:
			l.add(prod, "fails", "No service token on the Production site role: the site's build needs one (Settings › Service tokens), not a person's or an administrator's.")
		}
		if len(idle) > 0 {
			l.add(admin, "warns", fmt.Sprintf("Administrator tokens unused for %d days: %s. Revoke the ones made for a setup.", evidenceDays, strings.Join(idle, ", ")))
		} else {
			l.add(admin, "passes", "Every Administrator token has been used lately.")
		}
	}
}

// checkMedia is the checklist's media items.
func (a *App) checkMedia(ctx context.Context, l *checklist) {
	files := plugin.Check{Key: "media.files", Group: "media", Title: "Every file the site uses is there", Launch: true, Fix: "#/media", FixLabel: "Media"}
	if missing, n, err := a.missingFiles(ctx); err != nil {
		l.couldnt(files, err)
	} else if len(missing) > 0 {
		l.add(files, "fails", fmt.Sprintf("%d of %d files in use have no file on disk (a lost upload, or an import without it): %s. Re-upload them to the same assets.", len(missing), n, strings.Join(firstN(missing, 8), ", ")))
	} else {
		l.add(files, "passes", fmt.Sprintf("All %d files in use are on disk.", n))
	}
}

// checkContent is the checklist's content items.
func (a *App) checkContent(ctx context.Context, l *checklist) {
	live, err := a.liveContent(ctx)
	empty := plugin.Check{Key: "content.empty_blocks", Group: "content", Title: "No empty blocks", Fix: "#/pages", FixLabel: "Pages"}
	links := plugin.Check{Key: "content.redirect_links", Group: "content", Title: "Links go straight to their page", Fix: "#/redirects", FixLabel: "Redirects"}
	if err != nil {
		l.couldnt(empty, err)
		l.couldnt(links, err)
	} else {
		var blank []string
		for _, c := range live {
			for _, b := range c.Blocks.List() {
				if m, _ := b.(map[string]any); m != nil && emptyValue(m["data"]) {
					blank = append(blank, fmt.Sprintf("%s (%s)", c.where(), str(m["type"])))
				}
			}
		}
		if len(blank) > 0 {
			l.add(empty, "warns", fmt.Sprintf("%d blocks hold nothing, and draw as empty sections (or nothing): %s", len(blank), strings.Join(firstN(blank, 8), ", ")))
		} else {
			l.add(empty, "passes", fmt.Sprintf("Every block on %d live pages and entries holds something.", len(live)))
		}
		if hops, err := a.redirectedLinks(ctx, live); err != nil {
			l.couldnt(links, err)
		} else if len(hops) > 0 {
			l.add(links, "warns", fmt.Sprintf("%d links point at an address that redirects (one hop each, for visitors and search engines): %s. Point them at where it goes.", len(hops), strings.Join(firstN(hops, 8), ", ")))
		} else {
			l.add(links, "passes", "No link in live content or the globals points at a redirect.")
		}
	}
}

// checkGlobals is the checklist's globals items.
func (a *App) checkGlobals(ctx context.Context, l *checklist) {
	globals, err := a.globals(ctx, "")
	standard := plugin.Check{Key: "globals.standard", Group: "globals", Title: "The standard globals: nav, footer, scripts", Fix: "#/globals", FixLabel: "Globals"}
	jsonld := plugin.Check{Key: "globals.scripts_jsonld", Group: "globals", Title: "Structured data isn't in the scripts", Fix: "#/globals/scripts", FixLabel: "Globals › Scripts"}
	if err != nil {
		l.couldnt(standard, err)
		l.couldnt(jsonld, err)
	} else {
		bySlug := map[string]*Global{}
		for _, gl := range globals {
			bySlug[gl.Slug] = gl
		}
		var problems []string
		for _, want := range standardGlobals {
			gl := bySlug[want.slug]
			if gl == nil {
				problems = append(problems, want.slug+" is missing")
				continue
			}
			if miss := missingFields(gl.Schema, want.fields); len(miss) > 0 {
				problems = append(problems, fmt.Sprintf("%s has no %s", want.slug, strings.Join(miss, ", ")))
			}
		}
		if len(problems) > 0 {
			l.add(standard, "warns", strings.Join(problems, "; ")+". Every site has nav (items: label, link), footer (columns, address, copyright) and scripts (head and body: label, code).")
		} else {
			l.add(standard, "passes", "nav, footer and scripts are there, in the standard shape.")
		}
		if s := bySlug["scripts"]; s != nil && strings.Contains(string(s.Data), "application/ld+json") {
			l.add(jsonld, "warns", "The scripts global holds JSON-LD: it's put on every page, beside what the site emits itself. A page's structured data belongs in its SEO › JSON-LD (the business, on the home page).")
		} else {
			l.add(jsonld, "passes", "No JSON-LD in the scripts global.")
		}
	}
}

// checkBackup is the checklist's backup items.
func (a *App) checkBackup(ctx context.Context, l *checklist) {
	backup := plugin.Check{Key: "backup.recent", Group: "backup", Title: "There's a recent backup", Fix: "#/backup", FixLabel: "Tools › Backup"}
	if at, ok, err := a.lastAudit(ctx, "backup.downloaded"); err != nil {
		l.couldnt(backup, err)
	} else if !ok || l.now.Sub(at) > 7*24*time.Hour {
		evidence := "No backup has been downloaded from Tools › Backup"
		if ok {
			evidence = "The last backup downloaded from Tools › Backup was " + ago(l.now, at)
		}
		l.add(backup, "warns", evidence+". Scheduled backups on the server (the volume's) don't show here: if there are, this is yours to judge.")
	} else {
		l.add(backup, "passes", "A backup was downloaded "+ago(l.now, at)+".")
	}
}

// checkSiteHealth is the checklist's Site health item.
func (a *App) checkSiteHealth(ctx context.Context, l *checklist) {
	health := plugin.Check{Key: "content.site_health", Group: "content", Title: "Site health passes", Fix: "#/", FixLabel: "Dashboard › Site health"}
	var failing []string
	for _, ch := range a.healthChecks(ctx) {
		if ch.Get("state") == "fails" {
			failing = append(failing, str(ch.Get("message")))
		}
	}
	if len(failing) > 0 {
		l.add(health, "warns", strings.Join(failing, " "))
	} else {
		l.add(health, "passes", "Every Site health check passes.")
	}
}

// checkBuild is what the site's last build reported about itself
// (@librepublish/astro's build checks, and the checks and manual items the
// site declares): shown as they were, with how old they are. A report
// older than evidenceDays is shown as unknown: the site may have changed.
// The live site is asked, too (askSite): whether it answers, and whether
// the variables it runs on are set, which turns the build's "set on the
// host" item from a person's tick into a check.
func (a *App) checkBuild(ctx context.Context, l *checklist) {
	build := a.settingOr(ctx, "frontend_build")
	at, err := time.Parse(time.RFC3339, str(build["reported_at"]))
	if err != nil {
		return // no build has reported: deploy.last_build says so
	}
	live := a.checkLiveSite(ctx, l, build)
	stale := l.now.Sub(at) > evidenceDays*24*time.Hour
	from := fmt.Sprintf(" (the build %s)", ago(l.now, at))
	list, _ := build["checks"].([]any)
	for _, item := range list {
		m, _ := item.(map[string]any)
		c := plugin.Check{Key: str(m["key"]), Group: strOr(str(m["group"]), "build"), Title: str(m["title"]), State: str(m["state"]),
			Evidence: str(m["evidence"]), Launch: m["launch"] == true, Manual: m["manual"] == true, Fix: str(m["fix"]), FixLabel: str(m["fix_label"]), Source: "build"}
		switch {
		case c.Key == "build.runtime_env" && live != nil && live.status == http.StatusOK:
			c.Manual, c.Source = false, "cms"
			var unset []string
			for name, set := range live.env {
				if !set {
					unset = append(unset, name)
				}
			}
			slices.Sort(unset)
			if len(unset) > 0 {
				c.State, c.Evidence = "fails", "The live site says these aren't set where it runs: "+strings.Join(unset, ", ")+"."
			} else {
				c.State, c.Evidence = "passes", fmt.Sprintf("The live site says all %d are set (asked %s).", len(live.env), ago(l.now, live.at))
			}
		case c.Manual:
			c.State, c.Evidence = "todo", strOr(c.Evidence, "Declared by the site: a person checks this one.")
		case stale:
			c.State, c.Evidence = "unknown", c.Evidence+from+": too old to go by. Build the site again."
		default:
			c.Evidence += from
		}
		l.out = append(l.out, c)
	}
}

// checkLiveSite asks the live site (its build reported where: checks_url,
// POST /_cms/checks in @librepublish/astro) whether it answers the CMS,
// signed with Settings › Deploy's purge secret, and adds deploy.live_site.
// The answer is nil when the site couldn't be asked.
func (a *App) checkLiveSite(ctx context.Context, l *checklist, build map[string]any) *siteAnswer {
	ch := plugin.Check{Key: "deploy.live_site", Group: "deploy", Title: "The live site answers the CMS", Fix: "#/settings/deploy", FixLabel: "Settings › Deploy"}
	addr := str(build["checks_url"])
	if addr == "" {
		l.add(ch, "n_a", "The site's build didn't say where to ask it: a static site (nothing runs on the host), or @librepublish/astro before 0.8.")
		return nil
	}
	secret := str(a.settingOr(ctx, "frontend")["secret"])
	if secret == "" {
		l.add(ch, "warns", "Settings › Deploy has no purge secret, so the CMS can't sign a question to "+addr+". Set one there, and the same as CMS_WEBHOOK_SECRET where the site runs.")
		return nil
	}
	ans := askSite(ctx, addr, secret)
	switch {
	case ans.err != "":
		l.add(ch, "unknown", "Couldn't reach "+addr+": "+ans.err)
	case ans.status == http.StatusOK:
		l.add(ch, "passes", fmt.Sprintf("%s verified the CMS's signature (CMS_WEBHOOK_SECRET matches Settings › Deploy), %s.", addr, ago(l.now, ans.at)))
	case ans.status == http.StatusUnauthorized:
		l.add(ch, "fails", addr+" refused the CMS's signature: CMS_WEBHOOK_SECRET where the site runs isn't Settings › Deploy's purge secret (or isn't set).")
	case ans.status == http.StatusNotFound || ans.status == http.StatusMethodNotAllowed:
		l.add(ch, "unknown", fmt.Sprintf("%s answered %d: the deployed site predates /_cms/checks (@librepublish/astro 0.8).", addr, ans.status))
	default:
		l.add(ch, "unknown", fmt.Sprintf("%s answered %d.", addr, ans.status))
	}
	return &ans
}

// siteAnswer is what the live site said to POST /_cms/checks.
type siteAnswer struct {
	status       int
	verifiedWith string
	env          map[string]bool
	err          string
	at           time.Time
}

// askedSites keeps each answer for a minute, so the checklist's screen and
// its badge don't ask the site twice a page view.
var askedSites sync.Map // url + secret's digest → siteAnswer

// askSite posts a signed question to the live site's /_cms/checks (or a
// form hook's site, with the form's secret): which secret verified it, and
// which of its runtime variables are set.
func askSite(ctx context.Context, url, secret string) siteAnswer {
	sum := sha256.Sum256([]byte(secret))
	key := url + " " + hex.EncodeToString(sum[:8])
	if v, ok := askedSites.Load(key); ok {
		if kept, ok := v.(siteAnswer); ok && time.Since(kept.at) < time.Minute {
			return kept
		}
	}
	ans := siteAnswer{at: time.Now().UTC()}
	defer func() { askedSites.Store(key, ans) }()
	if err := outbound.CheckURL(url); err != nil {
		ans.err = err.Error()
		return ans
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"purpose": "launch_check", "nonce": outbound.NewDeliveryID()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		ans.err = err.Error()
		return ans
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	outbound.Sign(req.Header, secret, body, outbound.NewDeliveryID())
	res, err := outbound.Client.Do(req)
	if err != nil {
		ans.err = err.Error()
		return ans
	}
	defer res.Body.Close()
	ans.status = res.StatusCode
	var out struct {
		VerifiedWith string          `json:"verified_with"`
		Env          map[string]bool `json:"env"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out)
	ans.verifiedWith, ans.env = out.VerifiedWith, out.Env
	return ans
}

// httpURL is u when it's an http(s) address, else "".
func httpURL(u string) string {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
		return ""
	}
	return p.String()
}

// buildChecks are a build report's checks, as kept: at most 100, each with
// a key, a known state and bounded text; anything else is dropped.
func buildChecks(v any) []any {
	in, _ := v.([]any)
	out := []any{}
	states := map[string]bool{"passes": true, "fails": true, "warns": true, "unknown": true, "n_a": true}
	for _, item := range in {
		m, _ := item.(map[string]any)
		key := str(m["key"])
		manualItem := m["manual"] == true
		if !buildCheckKey.MatchString(key) || (!manualItem && !states[str(m["state"])]) || strings.TrimSpace(str(m["title"])) == "" {
			continue
		}
		fix := str(m["fix"])
		if fix != "" && !strings.HasPrefix(fix, "#/") && !strings.HasPrefix(fix, "https://") {
			fix = ""
		}
		out = append(out, map[string]any{"key": key, "group": clip(strOr(str(m["group"]), "build"), 40), "title": clip(str(m["title"]), 200),
			"state": str(m["state"]), "evidence": clip(str(m["evidence"]), 2000), "launch": m["launch"] == true, "manual": manualItem,
			"fix": clip(fix, 500), "fix_label": clip(str(m["fix_label"]), 100)})
		if len(out) == 100 {
			break
		}
	}
	return out
}

// A build check's key: build.<name> (the integration's) or site.<name> (the
// site's own), lowercase.
var buildCheckKey = regexp.MustCompile(`^(build|site)\.[a-z0-9_.-]{1,60}$`)

// standardGlobals are the globals every site has (docs/launch-checklist.md),
// with the fields each is read by.
var standardGlobals = []struct {
	slug   string
	fields []string
}{
	{"nav", []string{"items"}},
	{"footer", []string{"columns", "address", "copyright"}},
	{"scripts", []string{"head", "body"}},
}

// missingFields are the names a global's schema lacks of want.
func missingFields(schema store.JSON, want []string) []string {
	var s struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	}
	_ = json.Unmarshal(schema, &s)
	var miss []string
	for _, w := range want {
		if !slices.ContainsFunc(s.Fields, func(f struct {
			Name string `json:"name"`
		}) bool {
			return f.Name == w
		}) {
			miss = append(miss, w)
		}
	}
	return miss
}

// provisionalHost is an address no site should keep: an IP, or a wildcard
// DNS service's name for one.
func provisionalHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return net.ParseIP(h) != nil || strings.HasSuffix(h, ".sslip.io") || strings.HasSuffix(h, ".nip.io")
}

// provisionalOrigin is an origin that's a preview, a laptop or a stopgap.
func provisionalOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".workers.dev") || strings.HasSuffix(h, ".pages.dev") ||
		strings.HasSuffix(h, ".netlify.app") || strings.HasSuffix(h, ".vercel.app") || provisionalHost(raw)
}

// lastAudit is when action was last recorded.
func (a *App) lastAudit(ctx context.Context, action string) (time.Time, bool, error) {
	var at store.Time
	err := a.store.QueryRowContext(ctx, `SELECT created_at FROM audit_logs WHERE action = ? ORDER BY id DESC LIMIT 1`, action).Scan(&at)
	if err != nil {
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return at.Time, at.Valid, nil
}

type serviceToken struct {
	name, role string
	lastUsed   store.Time
}

// liveServiceTokens are the tokens not revoked, with their role.
func (a *App) liveServiceTokens(ctx context.Context) ([]serviceToken, error) {
	rows, err := a.store.QueryContext(ctx, `SELECT t.name, COALESCE(r.name, ''), t.last_used_at FROM service_tokens t LEFT JOIN roles r ON r.id = t.role_id
		WHERE t.revoked_at IS NULL ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	return store.ScanAll(rows, func(r *sql.Rows) (serviceToken, error) {
		var t serviceToken
		err := r.Scan(&t.name, &t.role, &t.lastUsed)
		return t, err
	})
}

// missingFiles are the assets live content uses whose file isn't on disk,
// as "<id> (<filename>)", and how many files in use there are.
func (a *App) missingFiles(ctx context.Context) ([]string, int, error) {
	rows, err := a.store.QueryContext(ctx, `SELECT DISTINCT a.id, b.key, b.filename FROM assets a
		JOIN content_references r ON r.ref_type = 'asset' AND r.ref_id = CAST(a.id AS TEXT)
		JOIN active_storage_attachments att ON att.record_type = 'Asset' AND att.record_id = a.id
		JOIN active_storage_blobs b ON b.id = att.blob_id
		WHERE a.deleted_at IS NULL ORDER BY a.id`)
	if err != nil {
		return nil, 0, err
	}
	type used struct {
		id            int64
		key, filename string
	}
	all, err := store.ScanAll(rows, func(r *sql.Rows) (used, error) {
		var u used
		err := r.Scan(&u.id, &u.key, &u.filename)
		return u, err
	})
	if err != nil {
		return nil, 0, err
	}
	var missing []string
	for _, u := range all {
		p, ok := a.blobPath(u.key)
		if !ok {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, fmt.Sprintf("%d (%s)", u.id, u.filename))
		}
	}
	return missing, len(all), nil
}

// liveContent is every published page and entry.
func (a *App) liveContent(ctx context.Context) ([]*Content, error) {
	pages, err := a.pages(ctx, "status = 'published'")
	if err != nil {
		return nil, err
	}
	entries, err := a.entries(ctx, "e.status = 'published'")
	if err != nil {
		return nil, err
	}
	return append(pages, entries...), nil
}

// where names a page or an entry in the checklist: its path, or
// <collection>/<slug>.
func (c *Content) where() string {
	if c.Kind == "entry" {
		return c.CollectionSlug + "/" + c.Slug
	}
	return c.Path
}

// emptyValue is a value that holds nothing: null, "", and lists and objects
// of nothing but those.
func emptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		for _, x := range t {
			if !emptyValue(x) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, x := range t {
			if !emptyValue(x) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// redirectedLinks are the links in live content and the globals whose
// address is an active redirect's source, as "<where>: <address>".
func (a *App) redirectedLinks(ctx context.Context, live []*Content) ([]string, error) {
	rules, err := a.redirects(ctx, "active = 1 AND wildcard = 0")
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, nil
	}
	sources := map[string]bool{}
	for _, r := range rules {
		sources[strings.TrimRight(r.Source, "/")] = true
	}
	var hops []string
	seen := map[string]bool{}
	note := func(where, addr string) {
		if k := where + " " + addr; !seen[k] {
			seen[k] = true
			hops = append(hops, where+": "+addr)
		}
	}
	var walk func(where string, v any)
	walk = func(where string, v any) {
		switch t := v.(type) {
		case map[string]any:
			// A link field: {kind: url|page|entry, value, collection}.
			if kind, ok := t["kind"].(string); ok {
				addr := ""
				switch kind {
				case "url":
					addr = str(t["value"])
				case "page":
					addr = "/" + strings.Trim(str(t["value"]), "/")
				case "entry":
					addr = "/" + str(t["collection"]) + "/" + str(t["value"])
				}
				if sources[strings.TrimRight(addr, "/")] {
					note(where, addr)
				}
			}
			for _, x := range t {
				walk(where, x)
			}
		case []any:
			for _, x := range t {
				walk(where, x)
			}
		case string:
			// A plain field holding an address (a button's link).
			if strings.HasPrefix(t, "/") && sources[strings.TrimRight(t, "/")] {
				note(where, t)
			}
		}
	}
	for _, c := range live {
		var v any
		_ = json.Unmarshal(c.Blocks, &v)
		walk(c.where(), v)
		_ = json.Unmarshal(c.Frontmatter, &v)
		walk(c.where(), v)
	}
	globals, err := a.globals(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, gl := range globals {
		var v any
		_ = json.Unmarshal(gl.Data, &v)
		walk("global "+gl.Slug, v)
	}
	return hops, nil
}

// ago is how long before now t was, in words.
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return count(int(d.Minutes()), "minute") + " ago"
	case d < 48*time.Hour:
		return count(int(d.Hours()), "hour") + " ago"
	default:
		return count(int(d.Hours()/24), "day") + " ago (" + t.Format("2 Jan 2006") + ")"
	}
}

// count is "1 day", "3 days".
func count(n int, unit string) string { return fmt.Sprintf("%d %s", n, plural(n, unit)) }
