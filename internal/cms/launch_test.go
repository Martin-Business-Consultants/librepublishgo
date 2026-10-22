package cms

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The launch checklist (plugins/launch, the CMS's items in launch.go, Forms'
// in plugins/forms/checks.go): what a fresh site lacks, each item going
// green as it's set up, the evidence each gives, and the manual ticks.
func TestLaunchChecklist(t *testing.T) {
	t.Parallel()
	mail := &captureMailer{}
	s := newSiteWith(t, Config{Mailer: mail})

	state := func() map[string]map[string]any {
		t.Helper()
		r := s.must(200, s.do(s.admin, "GET", "/api/x/launch/checks?show=all", nil))
		out := map[string]map[string]any{}
		for _, row := range r.Body["checks"].([]any) {
			m := row.(map[string]any)
			out[m["key"].(string)] = m
		}
		return out
	}
	expect := func(c map[string]map[string]any, key, want string, evidence ...string) {
		t.Helper()
		got := c[key]
		if got == nil {
			t.Fatalf("no item %s", key)
		}
		if got["state"] != want {
			t.Fatalf("%s: %v, want %s (%v)", key, got["state"], want, got["evidence"])
		}
		for _, e := range evidence {
			if !strings.Contains(got["evidence"].(string), e) {
				t.Fatalf("%s's evidence %q doesn't say %q", key, got["evidence"], e)
			}
		}
	}

	// It came with the binary: installed, granted site:read, and on.
	r := s.must(200, s.do(s.admin, "GET", "/api/plugins/launch", nil))
	if dig(r.Body, "plugin", "enabled") != true || dig(r.Body, "plugin", "granted", "site:read") != true {
		t.Fatalf("default plugin: %s", r.Raw)
	}
	// The agent's role doesn't see it unless granted.
	s.must(403, s.do(s.agent, "GET", "/api/x/launch/checks", nil))

	// A fresh site isn't ready.
	c := state()
	expect(c, "verdict", "not ready")
	expect(c, "general.site_url", "fails", "no public site URL")
	expect(c, "email.smtp", "fails")
	expect(c, "deploy.hook", "fails")
	expect(c, "deploy.last_build", "unknown")
	expect(c, "tokens.production", "warns", "PRODUCTION") // made, never used
	expect(c, "manual.domain", "to do")
	expect(c, "forms.captcha", "not applicable") // no forms yet
	if n := s.must(200, s.do(s.admin, "GET", "/api/x/launch/badge", nil)).Body["count"]; n.(float64) < 5 {
		t.Fatalf("badge: %v", n)
	}

	// Set up: the site's address, email (tested), a deploy hook, a build
	// with the production token.
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/general", map[string]any{"setting": map[string]any{"data": map[string]any{
		"title": "Acme", "site_base_url": "https://acme.test", "email": "hello@acme.test", "cors_origins": "https://acme-preview.workers.dev"}}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/email", map[string]any{"setting": map[string]any{"data": map[string]any{
		"smtp_host": "smtp.acme.test", "from_name": "Acme", "from_email": "hello@acme.test"}}}))
	c = state()
	expect(c, "email.smtp", "warns", "never tested")
	s.must(200, s.do(s.admin, "POST", "/api/settings/email/test", map[string]any{"to": "ted@acme.test"}))
	s.must(200, s.do(s.admin, "PATCH", "/api/deploy", map[string]any{"deploy": map[string]any{"hook_url": "https://api.cloudflare.test/hooks/1"}}))
	s.must(201, s.do(s.prod, "POST", "/api/frontend/builds", map[string]any{"render": "hybrid", "webhook_url": "https://acme.test/_cms/webhook"}))
	c = state()
	expect(c, "general.site_url", "passes", "https://acme.test")
	expect(c, "general.origins", "warns", "acme-preview.workers.dev")
	expect(c, "email.smtp", "passes", "smtp.acme.test")
	expect(c, "deploy.hook", "passes")
	expect(c, "deploy.last_build", "passes", "hybrid")
	expect(c, "deploy.pending", "warns", "approve")
	expect(c, "tokens.production", "passes", "PRODUCTION")

	// A file the content uses that's gone from disk.
	id := jsonID(s.upload("hero.png", pngOf(t, 400, 200)))
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "home", "title": "Home", "status": "published",
		"blocks": []any{map[string]any{"type": "hero", "data": map[string]any{"heading": "Hello", "image_id": id}}}}}))
	expect(state(), "media.files", "passes")
	var key string
	if err := s.app.store.QueryRowContext(t.Context(), `SELECT key FROM active_storage_blobs ORDER BY id DESC LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.app.cfg.DataDir, "storage", key[0:2], key[2:4], key)); err != nil {
		t.Fatal(err)
	}
	expect(state(), "media.files", "fails", id+" (hero.png)")

	// A link that goes through a redirect, and an empty block (the API
	// refuses one; an import from the Rails CMS brought some in).
	s.must(201, s.do(s.admin, "POST", "/api/redirects", map[string]any{"redirect": map[string]any{"source_path": "/old", "destination_url": "/new", "status_code": 301}}))
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "about", "title": "About", "status": "published",
		"blocks": []any{
			map[string]any{"type": "hero", "data": map[string]any{"heading": "About", "ctas": []any{map[string]any{"label": "Go", "url": map[string]any{"kind": "url", "value": "/old"}, "style": "primary"}}}},
		}}}))
	if _, err := s.app.store.ExecContext(t.Context(), `UPDATE pages SET blocks = json_insert(blocks, '$[#]', json('{"type":"hero","data":{"heading":"","ctas":[]}}')) WHERE path = 'about'`); err != nil {
		t.Fatal(err)
	}
	c = state()
	expect(c, "content.empty_blocks", "warns", "about (hero)")
	expect(c, "content.redirect_links", "warns", "about: /old")

	// Forms: a form on Turnstile's test keys, with a hook on a preview.
	form := map[string]any{"slug": "quote", "name": "Quote", "status": "active", "store_submissions": true,
		"fields":        []any{map[string]any{"id": "f1", "type": "email", "name": "email", "label": "Email", "required": true}},
		"admin_enabled": true, "admin_to": []any{"team@acme.test"}, "captcha": "turnstile",
		"turnstile_site_key": "1x00000000000000000000AA", "turnstile_secret": "1x0000000000000000000000000000000AA",
		"hook_url": "https://acme-preview.workers.dev/api/quote-hook", "hook_secret": "s3cret"}
	s.must(200, s.do(s.admin, "POST", "/api/x/forms/form", map[string]any{"form": form}))
	c = state()
	expect(c, "forms.captcha", "fails", "test keys")
	expect(c, "forms.recipients", "passes")
	expect(c, "forms.hook_site", "fails", "acme-preview.workers.dev")
	expect(c, "forms.hook_answers", "unknown", "Never called")
	form["turnstile_site_key"], form["turnstile_secret"], form["hook_url"] = "0x4AAAAAAA-real", "0x4AAAAAAA-real-secret", "https://acme.test/api/quote-hook"
	s.must(200, s.do(s.admin, "POST", "/api/x/forms/form?id=quote", map[string]any{"form": form}))
	c = state()
	expect(c, "forms.captcha", "passes")
	expect(c, "forms.hook_site", "passes", "https://acme.test")

	// The detail screen, with where to put it right.
	r = s.must(200, s.do(s.admin, "GET", "/api/x/launch/check?id=media.files", nil))
	if dig(r.Body, "check", "found", 2, "href") != "#/media" || dig(r.Body, "check", "manual") != false {
		t.Fatalf("detail: %s", r.Raw)
	}

	// A manual item, ticked and unticked; only those can be.
	s.must(404, s.do(s.admin, "POST", "/api/x/launch/manual?id=email.smtp", map[string]any{"done": true}))
	s.must(403, s.do(s.agent, "POST", "/api/x/launch/manual?id=manual.domain", map[string]any{"done": true}))
	s.must(200, s.do(s.admin, "POST", "/api/x/launch/manual?id=manual.domain", map[string]any{"done": true, "note": "DNS moved 9am"}))
	expect(state(), "manual.domain", "done", "Alice", "DNS moved 9am")
	if dig(s.must(200, s.do(s.admin, "GET", "/api/x/launch/check?id=manual.domain", nil)).Body, "check", "done") != true {
		t.Fatal("detail doesn't say it's done")
	}
	s.must(200, s.do(s.admin, "POST", "/api/x/launch/manual?id=manual.domain", map[string]any{"done": false}))
	expect(state(), "manual.domain", "to do")

	// A build reports its own checks and the site's: shown as reported, a
	// manual one tickable; one that doesn't hold up is dropped.
	s.must(201, s.do(s.prod, "POST", "/api/frontend/builds", map[string]any{"render": "hybrid", "webhook_url": "https://acme.test/_cms/webhook",
		"integration": "librepublish", "integration_version": "0.8.0", "checks": []any{
			map[string]any{"key": "build.no_secrets", "title": "No secrets in the output", "state": "passes", "evidence": "640 files read", "launch": true},
			map[string]any{"key": "build.media_local", "title": "Files are served by the site", "state": "fails", "evidence": "3 pages link the CMS", "launch": true, "fix": "javascript:alert(1)"},
			map[string]any{"key": "site.stella", "title": "Stella receives landing leads", "manual": true, "launch": true},
			map[string]any{"key": "nope", "title": "Not a build key", "state": "passes"},
			map[string]any{"key": "build.weird", "title": "A state that isn't one", "state": "great"},
		}}))
	c = state()
	expect(c, "build.no_secrets", "passes", "640 files read", "the build")
	expect(c, "build.media_local", "fails")
	expect(c, "site.stella", "to do")
	if c["nope"] != nil || c["build.weird"] != nil {
		t.Fatal("a bad build check was kept")
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/x/launch/check?id=build.media_local", nil))
	if dig(r.Body, "check", "about", 0, "value") != "The site's last build" || strings.Contains(string(r.Raw), "javascript:") {
		t.Fatalf("build check detail: %s", r.Raw)
	}
	s.must(200, s.do(s.admin, "POST", "/api/x/launch/manual?id=site.stella", map[string]any{"done": true}))
	expect(state(), "site.stella", "done", "Alice")
	s.must(404, s.do(s.admin, "POST", "/api/x/launch/manual?id=build.no_secrets", map[string]any{"done": true}))

	// The live site answers (POST /_cms/checks, as @librepublish/astro 0.8
	// serves it): signed with the purge secret, which of its runtime
	// variables are set; signed with a form's hook secret, whether it's
	// the site's.
	siteSecrets := map[string]string{"webhook": "purge", "form_hook": "s3cret"}
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_cms/checks" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		ts := r.Header.Get("X-CMS-Timestamp")
		for _, which := range []string{"webhook", "form_hook"} {
			mac := hmac.New(sha256.New, []byte(siteSecrets[which]))
			mac.Write([]byte(ts + "." + string(body)))
			if r.Header.Get("X-CMS-Signature-V2") == "sha256="+hex.EncodeToString(mac.Sum(nil)) {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "verified_with": which, "env": map[string]bool{"CMS_FORM_HOOK_SECRET": true, "STELLA_WEBHOOK_URL": false}})
				return
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(live.Close)
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/general", map[string]any{"setting": map[string]any{"data": map[string]any{"cors_origins": live.URL}}}))
	expect(state(), "deploy.live_site", "not applicable") // the last build didn't say where to ask
	s.must(201, s.do(s.prod, "POST", "/api/frontend/builds", map[string]any{"render": "hybrid", "webhook_url": live.URL + "/_cms/webhook", "checks_url": live.URL + "/_cms/checks",
		"checks": []any{map[string]any{"key": "build.runtime_env", "title": "Set on the host: CMS_FORM_HOOK_SECRET, STELLA_WEBHOOK_URL", "manual": true, "launch": true}}}))
	expect(state(), "deploy.live_site", "warns", "no purge secret")
	if _, err := s.app.putSetting(t.Context(), nil, "frontend", map[string]any{"render": "hybrid", "webhook_url": live.URL + "/_cms/webhook", "secret": "purge"}, false); err != nil {
		t.Fatal(err)
	}
	c = state()
	expect(c, "deploy.live_site", "passes", "verified the CMS's signature")
	expect(c, "build.runtime_env", "fails", "STELLA_WEBHOOK_URL")
	form["hook_url"] = live.URL + "/api/quote-hook"
	s.must(200, s.do(s.admin, "POST", "/api/x/forms/form?id=quote", map[string]any{"form": form}))
	expect(state(), "forms.hook_secret", "passes", "Quote")
	form["hook_secret"] = "not-the-sites"
	s.must(200, s.do(s.admin, "POST", "/api/x/forms/form?id=quote", map[string]any{"form": form}))
	expect(state(), "forms.hook_secret", "fails", "CMS_FORM_HOOK_SECRET")
	siteSecrets["webhook"] = "rotated"
	askedSites.Clear() // the answer's kept a minute
	expect(state(), "deploy.live_site", "fails", "refused")

	// The default view is what's open; the verdict is always first.
	r = s.must(200, s.do(s.admin, "GET", "/api/x/launch/checks", nil))
	rows := r.Body["checks"].([]any)
	if dig(rows[0], "key") != "verdict" {
		t.Fatalf("verdict isn't first: %s", r.Raw)
	}
	for _, row := range rows[1:] {
		if st := dig(row, "state"); st == "passes" || st == "done" || st == "not applicable" {
			t.Fatalf("a settled item in the To do view: %v", row)
		}
	}
}
