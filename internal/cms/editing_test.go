package cms

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var linkToken = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

func mailedToken(t *testing.T, m *captureMailer, to string) string {
	t.Helper()
	for _, s := range m.take() {
		if s.to[0] == to {
			if tok := linkToken.FindStringSubmatch(decodeQP(s.raw)); tok != nil {
				return tok[1]
			}
		}
	}
	t.Fatalf("no link mailed to %s", to)
	return ""
}

func TestPasswordResetAndEmailChange(t *testing.T) {
	t.Parallel()
	mail := &captureMailer{}
	s := newSiteWith(t, Config{NoDefaultPlugins: true, Mailer: mail})
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/email", map[string]any{"setting": map[string]any{"data": map[string]any{"smtp_host": "smtp.test", "from_email": "cms@site.test"}}}))

	// The answer is the same whether or not the address has an account.
	a := s.must(200, s.do("", "POST", "/api/password/forgot", map[string]any{"email": "nobody@site.test"}))
	b := s.must(200, s.do("", "POST", "/api/password/forgot", map[string]any{"email": "ALICE@site.test"}))
	if a.Body["message"] != b.Body["message"] {
		t.Fatalf("forgot answers differ: %s / %s", a.Raw, b.Raw)
	}
	token := mailedToken(t, mail, "alice@site.test")

	s.must(422, s.do("", "POST", "/api/password/reset", map[string]any{"token": token, "password": "short"}))
	r := s.must(200, s.do("", "POST", "/api/password/reset", map[string]any{"token": token, "password": "a-new-password-1"}))
	if r.Body["signed_in"] != true || r.Header.Get("Set-Cookie") == "" {
		t.Fatalf("reset didn't sign in: %s", r.Raw)
	}
	// A link works once.
	s.must(410, s.do("", "POST", "/api/password/reset", map[string]any{"token": token, "password": "another-password-2"}))
	s.must(401, s.do("", "POST", "/api/session", map[string]any{"email": "alice@site.test", "password": "password1234"}))
	s.must(201, s.do("", "POST", "/api/session", map[string]any{"email": "alice@site.test", "password": "a-new-password-1"}))
	// Whoever had the account may have made a token: the reset ended it.
	s.must(401, s.do(s.admin, "GET", "/api/users", nil))
	var aliceID int64
	if err := s.app.store.QueryRowContext(t.Context(), `SELECT id FROM users WHERE email = 'alice@site.test'`).Scan(&aliceID); err != nil {
		t.Fatal(err)
	}
	s.admin, _, _ = s.app.mintUserToken(t.Context(), aliceID)

	// A new address takes the current password, even with a token...
	me := s.must(200, s.do(s.admin, "GET", "/api/users", nil))
	id := int64(dig(me.Body, "users", 0, "id").(float64))
	path := "/api/users/" + strconv.FormatInt(id, 10)
	r = s.must(422, s.do(s.admin, "PATCH", path, map[string]any{"user": map[string]any{"email": "alice@new.test"}}))
	if dig(r.Body, "errors", "current_password", 0) != "can't be blank" {
		t.Fatalf("no current password: %s", r.Raw)
	}
	s.must(422, s.do(s.admin, "PATCH", path, map[string]any{"user": map[string]any{"email": "alice@new.test", "current_password": "password1234"}}))
	if sent := mail.take(); len(sent) != 0 {
		t.Fatalf("mailed without the password: %+v", sent)
	}
	// ...and waits for its link; the old one is told.
	r = s.must(200, s.do(s.admin, "PATCH", path, map[string]any{"user": map[string]any{"email": "alice@new.test", "current_password": "a-new-password-1"}}))
	if dig(r.Body, "user", "email") != "alice@site.test" || dig(r.Body, "user", "pending_email", "email") != "alice@new.test" {
		t.Fatalf("pending: %s", r.Raw)
	}
	sent := mail.take()
	if len(sent) != 2 || sent[0].to[0] != "alice@new.test" || sent[1].to[0] != "alice@site.test" {
		t.Fatalf("email change mails: %+v", sent)
	}
	confirm := linkToken.FindStringSubmatch(decodeQP(sent[0].raw))[1]
	// Cancelled, the link is dead.
	s.must(204, s.do(s.admin, "DELETE", path+"/pending_email", nil))
	s.must(410, s.do("", "POST", "/api/email/confirm", map[string]any{"token": confirm}))
	s.must(200, s.do(s.admin, "PATCH", path, map[string]any{"user": map[string]any{"email": "alice@new.test", "current_password": "a-new-password-1"}}))
	confirm = mailedToken(t, mail, "alice@new.test")
	r = s.must(200, s.do("", "POST", "/api/email/confirm", map[string]any{"token": confirm}))
	if r.Body["email"] != "alice@new.test" {
		t.Fatalf("confirm: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/users", nil))
	if dig(r.Body, "users", 0, "email") != "alice@new.test" || dig(r.Body, "users", 0, "pending_email") != nil {
		t.Fatalf("after confirm: %s", r.Raw)
	}
}

func TestEditLocksAutosavesAndPreferences(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	ctx := t.Context()
	bobID, err := s.app.CreateUserWithRole(ctx, "Bob", "bob@site.test", "password1234", "Editor")
	if err != nil {
		t.Fatal(err)
	}
	bob, _, _ := s.app.mintUserToken(ctx, bobID)
	r := s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "about", "title": "About"}}))
	id := dig(r.Body, "page", "id")

	// Alice opens it; Bob is told she has it, and his saves are refused.
	r = s.must(200, s.do(s.admin, "POST", "/api/locks", map[string]any{"type": "page", "id": id}))
	if dig(r.Body, "lock", "mine") != true {
		t.Fatalf("lock: %s", r.Raw)
	}
	r = s.must(409, s.do(bob, "POST", "/api/locks", map[string]any{"type": "page", "id": id}))
	if r.Body["error"] != "locked" || dig(r.Body, "lock", "user_name") != "Alice" {
		t.Fatalf("bob's lock: %s", r.Raw)
	}
	r = s.must(200, s.do(bob, "GET", "/api/pages?per=10", nil))
	if dig(r.Body, "pages", 0, "locked_by") != "Alice" {
		t.Fatalf("list lock: %s", r.Raw)
	}
	s.must(409, s.do(bob, "PATCH", "/api/pages/about", map[string]any{"page": map[string]any{"title": "Bob's"}}))
	s.must(409, s.do(s.agent, "PATCH", "/api/pages/about", map[string]any{"page": map[string]any{"title": "Agent's"}}))
	s.must(200, s.do(bob, "PATCH", "/api/pages/about?override_lock=1", map[string]any{"page": map[string]any{"title": "Bob's"}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/pages/about", map[string]any{"page": map[string]any{"title": "Alice's"}}))

	// Bob takes over: Alice's next heartbeat says she lost it.
	s.must(200, s.do(bob, "POST", "/api/locks", map[string]any{"type": "page", "id": id, "take_over": true}))
	r = s.must(409, s.do(s.admin, "POST", "/api/locks", map[string]any{"type": "page", "id": id, "held": true}))
	if r.Body["error"] != "lock_lost" || dig(r.Body, "lock", "user_name") != "Bob" {
		t.Fatalf("lost: %s", r.Raw)
	}
	s.must(204, s.do(bob, "DELETE", "/api/locks/page/"+strconv.FormatInt(int64(id.(float64)), 10), nil))
	s.must(200, s.do(s.admin, "PATCH", "/api/pages/about", map[string]any{"page": map[string]any{"title": "Free again"}}))

	// Autosaves are each person's own.
	s.must(200, s.do(s.admin, "PUT", "/api/autosaves?type=page&key=about", map[string]any{"data": map[string]any{"title": "Typed, not saved"}}))
	r = s.must(200, s.do(s.admin, "GET", "/api/autosaves?type=page&key=about", nil))
	if dig(r.Body, "autosave", "data", "title") != "Typed, not saved" {
		t.Fatalf("autosave: %s", r.Raw)
	}
	r = s.must(200, s.do(bob, "GET", "/api/autosaves?type=page&key=about", nil))
	if r.Body["autosave"] != nil {
		t.Fatalf("bob sees alice's autosave: %s", r.Raw)
	}
	s.must(204, s.do(s.admin, "DELETE", "/api/autosaves?type=page&key=about", nil))
	r = s.must(200, s.do(s.admin, "GET", "/api/autosaves?type=page&key=about", nil))
	if r.Body["autosave"] != nil {
		t.Fatalf("autosave kept: %s", r.Raw)
	}

	// Preferences merge, and null removes.
	s.must(200, s.do(s.admin, "PATCH", "/api/me/preferences", map[string]any{"list:pages": map[string]any{"per": 20}, "publish_checks": true}))
	r = s.must(200, s.do(s.admin, "PATCH", "/api/me/preferences", map[string]any{"publish_checks": nil}))
	if dig(r.Body, "preferences", "list:pages", "per") != float64(20) || dig(r.Body, "preferences", "publish_checks") != nil {
		t.Fatalf("preferences: %s", r.Raw)
	}

	// The site info has no secrets in it.
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/email", map[string]any{"setting": map[string]any{"data": map[string]any{"smtp_host": "smtp.test", "smtp_password": "hunter22", "from_email": "cms@site.test"}}}))
	r = s.must(200, s.do(s.admin, "GET", "/api/site_health/info", nil))
	text := str(r.Body["text"])
	if !strings.Contains(text, "### Libre Publish ###") || !strings.Contains(text, "pages: 1") || strings.Contains(text, "hunter22") || !strings.Contains(text, "smtp.test:587") {
		t.Fatalf("site info:\n%s", text)
	}
}

func TestDeliveryShaping(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.must(201, s.do(s.admin, "POST", "/api/collections", map[string]any{"collection": map[string]any{"slug": "authors", "name": "Authors"}}))
	s.must(201, s.do(s.admin, "POST", "/api/collections", map[string]any{"collection": map[string]any{"slug": "posts", "name": "Posts",
		"schema": map[string]any{"fields": []any{map[string]any{"name": "author", "type": "record_ref", "of_collection": "authors"}}}}}))
	s.must(201, s.do(s.admin, "POST", "/api/collections/authors/entries", map[string]any{"entry": map[string]any{"slug": "ada", "title": "Ada", "status": "published"}}))
	for _, slug := range []string{"one", "two", "three"} {
		s.must(201, s.do(s.admin, "POST", "/api/collections/posts/entries", map[string]any{"entry": map[string]any{"slug": slug, "title": strings.ToUpper(slug), "status": "published",
			"seo": map[string]any{"meta_title": "T " + slug, "meta_description": "D"}, "frontmatter": map[string]any{"author": "ada"}}}))
	}

	// Paging headers.
	r := s.must(200, s.do(s.prod, "GET", "/api/v1/collections/posts/entries?per=2", nil))
	if r.Header.Get("X-Total-Count") != "3" || r.Header.Get("X-Total-Pages") != "2" || !strings.Contains(r.Header.Get("Link"), `rel="next"`) || strings.Contains(r.Header.Get("Link"), `rel="prev"`) {
		t.Fatalf("headers: %v", r.Header)
	}
	// Sparse fields, with dot paths.
	r = s.must(200, s.do(s.prod, "GET", "/api/v1/collections/posts/entries/one?fields=title,seo.meta_title", nil))
	data, _ := r.Body["data"].(map[string]any)
	if len(data) != 3 || data["title"] != "ONE" || dig(data, "seo", "meta_title") != "T one" || dig(data, "seo", "meta_description") != nil || data["id"] == nil {
		t.Fatalf("fields: %s", r.Raw)
	}
	// What it points at, included.
	r = s.must(200, s.do(s.prod, "GET", "/api/v1/collections/posts/entries/one?include=entries", nil))
	if dig(r.Body, "included", "entries", "authors/ada", "title") != "Ada" || dig(r.Body, "included", "assets") != nil {
		t.Fatalf("include: %s", r.Raw)
	}
	r = s.must(200, s.do(s.prod, "GET", "/api/v1/collections/posts/entries/one", nil))
	if dig(r.Body, "included", "entries") != nil {
		t.Fatalf("entries included by default: %s", r.Raw)
	}
	// OPTIONS: the resource's schema.
	r = s.must(200, s.do(s.prod, "OPTIONS", "/api/v1/collections/posts/entries", nil))
	if r.Header.Get("Allow") != "GET, OPTIONS" || dig(r.Body, "schema", "properties", "data", "items", "properties", "frontmatter", "properties", "author") == nil {
		t.Fatalf("options: %s", r.Raw)
	}
}
