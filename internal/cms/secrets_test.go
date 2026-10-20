package cms

import (
	"strings"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
)

// storedRaw is a settings key's data as the database holds it.
func storedRaw(t *testing.T, s *site, key string) string {
	t.Helper()
	var raw string
	if err := s.app.store.QueryRowContext(t.Context(), `SELECT data FROM settings WHERE key = ?`, key).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSecretNames(t *testing.T) {
	t.Parallel()
	for _, k := range []string{"smtp_password", "token", "api_key", "X-Api-Key", "Api-Key", "apikey", "Authorization", "X-Auth", "X-Auth-Token", "auth", "Cookie", "Set-Cookie", "private-key", "client_secret", "passwd"} {
		if !secretKey(k) {
			t.Errorf("%s isn't taken for a secret", k)
		}
	}
	for _, k := range []string{"author", "authors", "cookie_banner", "title", "hook_url", "X-Plain", "Content-Type"} {
		if secretKey(k) {
			t.Errorf("%s is taken for a secret", k)
		}
	}
	if got := mask("abc"); got != "***" {
		t.Errorf("a short secret shows as %q", got)
	}
	if got := mask("hunter22"); got != "***er22" {
		t.Errorf("mask = %q", got)
	}
}

// A search, or a proposed change, shows a global's secrets as the global
// does: masked, and not findable by their value.
func TestSearchAndRevisionsHideGlobalSecrets(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.must(201, s.do(s.admin, "POST", "/api/globals", map[string]any{"global": map[string]any{"slug": "keys", "name": "Keys",
		"data": map[string]any{"title": "Hello there", "api_key": "sk_live_abcdef123456"}}}))
	r := s.must(200, s.do(s.prod, "GET", "/api/search?q=sk_live_abc", nil))
	if len(r.Body["results"].([]any)) != 0 {
		t.Fatalf("searching a secret's value: %s", r.Raw)
	}
	r = s.must(200, s.do(s.prod, "GET", "/api/search?q=api_key", nil))
	if strings.Contains(string(r.Raw), "sk_live") || dig(r.Body, "results", 0, "path") != "keys" {
		t.Fatalf("searching a secret's name: %s", r.Raw)
	}
	r = s.must(200, s.do(s.prod, "GET", "/api/search?q=hello", nil))
	if dig(r.Body, "results", 0, "path") != "keys" {
		t.Fatalf("searching the rest: %s", r.Raw)
	}

	s.must(202, s.do(s.agent, "PATCH", "/api/globals/keys", map[string]any{"global": map[string]any{"data": map[string]any{"title": "Hi", "api_key": "sk_live_proposed9876"}}}))
	r = s.must(200, s.do(s.admin, "GET", "/api/revisions", nil))
	if strings.Contains(string(r.Raw), "sk_live_proposed") || dig(r.Body, "revisions", 0, "changes", "data", "api_key") != "***9876" {
		t.Fatalf("revision: %s", r.Raw)
	}
	// Approved, it applies what was proposed, not the mask.
	id := jsonID(dig(r.Body, "revisions", 0, "id"))
	s.must(200, s.do(s.admin, "POST", "/api/revisions/"+id+"/approve", nil))
	g, err := s.app.globalBySlug(t.Context(), "keys")
	if err != nil {
		t.Fatal(err)
	}
	if g.Data.Map()["api_key"] != "sk_live_proposed9876" {
		t.Fatalf("approved: %s", g.Data)
	}
}

func TestSettingsSecretsSealedAtRest(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	ctx := t.Context()
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/github", map[string]any{"setting": map[string]any{"data": map[string]any{
		"frontend_github_repo": "acme/site", "token": "ghp_abcdefgh1234", "nested": map[string]any{"client_secret": "deep-secret-55"}}}}))
	if raw := storedRaw(t, s, "github"); strings.Contains(raw, "ghp_") || strings.Contains(raw, "deep-secret") || !strings.Contains(raw, "acme/site") {
		t.Fatalf("at rest: %s", raw)
	}
	g, err := s.app.setting(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	if g["token"] != "ghp_abcdefgh1234" || dig(g, "nested", "client_secret") != "deep-secret-55" {
		t.Fatalf("read back: %v", g)
	}
	r := s.must(200, s.do(s.admin, "GET", "/api/settings/github", nil))
	if dig(r.Body, "setting", "data", "token") != "***1234" {
		t.Fatalf("shown: %s", r.Raw)
	}
	// Written back as shown, it's kept.
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/github", map[string]any{"setting": map[string]any{"data": map[string]any{"token": "***1234"}}}))
	if g, _ := s.app.setting(ctx, "github"); g["token"] != "ghp_abcdefgh1234" {
		t.Fatalf("masked write-back: %v", g)
	}
	// A short secret shows none of itself.
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/misc", map[string]any{"setting": map[string]any{"data": map[string]any{"password": "abc"}}}))
	r = s.must(200, s.do(s.admin, "GET", "/api/settings/misc", nil))
	if dig(r.Body, "setting", "data", "password") != "***" {
		t.Fatalf("short secret: %s", r.Raw)
	}

	// A secret kept before sealing still reads, and is sealed on start.
	if _, err := s.app.store.ExecContext(ctx, `INSERT INTO settings (key, data, created_at, updated_at) VALUES ('legacy', '{"api_key":"plain-old-key","name":"x"}', '2020-01-01 00:00:00', '2020-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.app.setting(ctx, "legacy"); l["api_key"] != "plain-old-key" {
		t.Fatalf("legacy read: %v", l)
	}
	if err := s.app.sealStoredSecrets(ctx); err != nil {
		t.Fatal(err)
	}
	if raw := storedRaw(t, s, "legacy"); strings.Contains(raw, "plain-old-key") || !strings.Contains(raw, secret.SealedPrefix) {
		t.Fatalf("legacy at rest: %s", raw)
	}
	if l, _ := s.app.setting(ctx, "legacy"); l["api_key"] != "plain-old-key" || l["name"] != "x" {
		t.Fatalf("legacy after sealing: %v", l)
	}
	// One another key sealed reads as nothing, never as its ciphertext.
	other, err := secret.Load(t.TempDir(), "another install")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.app.store.ExecContext(ctx, `UPDATE settings SET data = ? WHERE key = 'legacy'`, `{"api_key":"`+other.Seal("x")+`"}`); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.app.setting(ctx, "legacy"); l["api_key"] != "" {
		t.Fatalf("another key's: %v", l)
	}
}

func TestWebhookHeadersMasked(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	r := s.must(201, s.do(s.admin, "POST", "/api/webhooks", map[string]any{"webhook": map[string]any{"name": "Build", "url": "https://hooks.example.com/x",
		"events": []any{"page.published"}, "headers": map[string]any{"X-Api-Key": "k_123456789", "X-Tenant": "acme-tenant-7"}}}))
	id := jsonID(dig(r.Body, "webhook", "id"))
	for _, path := range []string{"/api/webhooks/" + id, "/api/webhooks"} {
		r = s.must(200, s.do(s.admin, "GET", path, nil))
		if strings.Contains(string(r.Raw), "k_123456789") || strings.Contains(string(r.Raw), "acme-tenant-7") {
			t.Fatalf("%s shows a header: %s", path, r.Raw)
		}
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/webhooks/"+id, nil))
	if dig(r.Body, "webhook", "headers", "X-Api-Key") != "***6789" || dig(r.Body, "webhook", "headers", "X-Tenant") != "***nt-7" {
		t.Fatalf("headers: %s", r.Raw)
	}
	// Written back as shown, they're kept; a new value replaces one.
	s.must(200, s.do(s.admin, "PATCH", "/api/webhooks/"+id, map[string]any{"webhook": map[string]any{
		"headers": map[string]any{"X-Api-Key": "***6789", "X-Tenant": "other"}}}))
	var raw string
	if err := s.app.store.QueryRowContext(t.Context(), `SELECT headers FROM webhooks WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "k_123456789") || !strings.Contains(raw, `"other"`) {
		t.Fatalf("kept: %s", raw)
	}
}

func TestDeployHookMaskedForReaders(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	const hook = "https://api.netlify.com/build_hooks/abc123secretxyz9"
	s.must(200, s.do(s.admin, "PATCH", "/api/deploy", map[string]any{"deploy": map[string]any{"hook_url": hook, "enabled": false}}))
	if raw := storedRaw(t, s, "deploy"); strings.Contains(raw, "abc123") {
		t.Fatalf("at rest: %s", raw)
	}
	shown := "https://api.netlify.com/…xyz9"
	r := s.must(200, s.do(s.agent, "GET", "/api/deploy", nil))
	if dig(r.Body, "deploy", "config", "hook_url") != shown || dig(r.Body, "deploy", "configured") != true {
		t.Fatalf("agent's deploy: %s", r.Raw)
	}
	r = s.must(200, s.do(s.agent, "GET", "/api/settings/deploy", nil))
	if dig(r.Body, "setting", "data", "hook_url") != shown {
		t.Fatalf("agent's settings: %s", r.Raw)
	}
	r = s.must(200, s.do(s.agent, "GET", "/api/settings", nil))
	if strings.Contains(string(r.Raw), "abc123") {
		t.Fatalf("agent's settings list: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/deploy", nil))
	if dig(r.Body, "deploy", "config", "hook_url") != hook {
		t.Fatalf("admin's deploy: %s", r.Raw)
	}
	// The masked address written back keeps the hook.
	s.must(200, s.do(s.admin, "PATCH", "/api/deploy", map[string]any{"deploy": map[string]any{"hook_url": shown}}))
	if d, _ := s.app.setting(t.Context(), "deploy"); d["hook_url"] != hook {
		t.Fatalf("masked write-back: %v", d)
	}
}

func TestMovedHostDropsSecret(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	ctx := t.Context()
	email := func(data map[string]any) {
		t.Helper()
		s.must(200, s.do(s.admin, "PATCH", "/api/settings/email", map[string]any{"setting": map[string]any{"data": data}}))
	}
	email(map[string]any{"smtp_host": "smtp.acme.test", "smtp_password": "hunter22"})
	email(map[string]any{"smtp_host": "smtp.acme.test", "smtp_password": "***er22"})
	if pw := s.app.mailConfig(ctx).Password; pw != "hunter22" {
		t.Fatalf("same host: %q", pw)
	}
	// Somewhere else with the mask: the password doesn't follow.
	email(map[string]any{"smtp_host": "smtp.evil.test", "smtp_password": "***er22"})
	if pw := s.app.mailConfig(ctx).Password; pw != "" {
		t.Fatalf("moved with the mask: %q", pw)
	}
	email(map[string]any{"smtp_host": "smtp.evil.test", "smtp_password": "fresh-pass"})
	if pw := s.app.mailConfig(ctx).Password; pw != "fresh-pass" {
		t.Fatalf("moved with a new one: %q", pw)
	}
	email(map[string]any{"smtp_host": "smtp.acme.test"})
	if pw := s.app.mailConfig(ctx).Password; pw != "" {
		t.Fatalf("moved without one: %q", pw)
	}
	// The purge address and its signing secret, the same.
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/frontend", map[string]any{"setting": map[string]any{"data": map[string]any{"webhook_url": "https://site.test/purge", "secret": "purge-secret"}}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/frontend", map[string]any{"setting": map[string]any{"data": map[string]any{"webhook_url": "https://evil.test/purge"}}}))
	if f, _ := s.app.setting(ctx, "frontend"); f["secret"] != nil {
		t.Fatalf("purge secret moved: %v", f)
	}
}

// settings:write isn't enough to say where mail goes, what reset links
// point at, or what the site is built from: with those, a reset of the
// Administrator's password lands with whoever chose them.
func TestSystemSettingsNeedAdministrator(t *testing.T) {
	t.Parallel()
	s, bob, _, _ := managerSite(t)
	patch := func(token, key string, data map[string]any) resp {
		return s.do(token, "PATCH", "/api/settings/"+key, map[string]any{"setting": map[string]any{"data": data}})
	}
	s.must(403, patch(bob, "email", map[string]any{"smtp_host": "smtp.evil.test"}))
	s.must(403, patch(bob, "github", map[string]any{"frontend_github_repo": "evil/site"}))
	s.must(403, patch(bob, "deploy", map[string]any{"hook_url": "https://evil.test/hook"}))
	s.must(403, s.do(bob, "PATCH", "/api/deploy", map[string]any{"deploy": map[string]any{"hook_url": "https://evil.test/hook"}}))
	s.must(403, s.do(bob, "POST", "/api/settings", map[string]any{"setting": map[string]any{"key": "email", "data": map[string]any{"smtp_host": "smtp.evil.test"}}}))
	s.must(403, s.do(bob, "PUT", "/api/settings/email", map[string]any{"setting": map[string]any{"data": map[string]any{}}}))
	s.must(403, patch(bob, "general", map[string]any{"cms_url": "https://evil.test"}))
	s.must(200, patch(s.admin, "email", map[string]any{"smtp_host": "smtp.acme.test"}))
	s.must(403, s.do(bob, "DELETE", "/api/settings/email", nil))
	if e, _ := s.app.setting(t.Context(), "email"); e["smtp_host"] != "smtp.acme.test" {
		t.Fatalf("email: %v", e)
	}
	// The rest of General is his, and so is a whole form that leaves the
	// address as it is.
	s.must(200, patch(s.admin, "general", map[string]any{"cms_url": "https://cms.acme.test"}))
	s.must(200, patch(bob, "general", map[string]any{"title": "Acme"}))
	s.must(200, patch(bob, "general", map[string]any{"title": "Acme Co", "cms_url": "https://cms.acme.test/"}))
	s.must(403, s.do(bob, "PUT", "/api/settings/general", map[string]any{"setting": map[string]any{"data": map[string]any{"title": "Acme"}}}))
	if g := s.app.general(t.Context()); g["cms_url"] != "https://cms.acme.test" || g["title"] != "Acme Co" {
		t.Fatalf("general: %v", g)
	}
	s.must(200, patch(bob, "brand", map[string]any{"voice": "plain"}))
}

func TestGeneralIsKeptUntilWritten(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	ctx := t.Context()
	g := s.app.general(ctx)
	g["title"] = "changed by a caller"
	if s.app.general(ctx)["title"] == "changed by a caller" {
		t.Fatal("a caller's change reached what's kept")
	}
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/general", map[string]any{"setting": map[string]any{"data": map[string]any{"default_locale": "fr", "serving": "website_api"}}}))
	if s.app.defaultLocale() != "fr" || s.app.servingMode() != "website_api" {
		t.Fatalf("after a write: %v", s.app.general(ctx))
	}
}
