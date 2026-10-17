package cms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
)

// managerSite is a site with Bob, who holds users:*, roles:*, settings:*
// and what the Production site role reads — but isn't an Administrator.
func managerSite(t *testing.T) (s *site, bob string, bobID, aliceID int64) {
	t.Helper()
	s = newSite(t)
	r := s.must(201, s.do(s.admin, "POST", "/api/roles", map[string]any{"role": map[string]any{"name": "Manager", "permissions": []string{
		"users:read", "users:write", "users:delete", "roles:read", "roles:write", "settings:read", "settings:write",
		"pages:read", "collections:read", "entries:read", "block_types:read", "globals:read", "assets:read", "redirects:read"}}}))
	roleID, _ := intOf(dig(r.Body, "role", "id"))
	ctx := t.Context()
	var err error
	if bobID, err = s.app.addUser(ctx, nil, "Bob", "bob@site.test", "password1234", roleID); err != nil {
		t.Fatal(err)
	}
	if bob, _, err = s.app.mintUserToken(ctx, bobID); err != nil {
		t.Fatal(err)
	}
	if err := s.app.store.QueryRowContext(t.Context(), `SELECT id FROM users WHERE email = 'alice@site.test'`).Scan(&aliceID); err != nil {
		t.Fatal(err)
	}
	return s, bob, bobID, aliceID
}

func TestNoOneActsAboveTheirRole(t *testing.T) {
	t.Parallel()
	s, bob, bobID, aliceID := managerSite(t)
	alice := "/api/users/" + strconv.FormatInt(aliceID, 10)

	// Users: an Administrator is out of a Manager's reach.
	s.must(403, s.do(bob, "PATCH", alice, map[string]any{"user": map[string]any{"password": "taken-over-1234"}}))
	s.must(403, s.do(bob, "PATCH", alice, map[string]any{"user": map[string]any{"email": "bob+evil@site.test"}}))
	s.must(403, s.do(bob, "DELETE", alice, nil))
	s.must(201, s.do("", "POST", "/api/session", map[string]any{"email": "alice@site.test", "password": "password1234"}))
	// Someone on a role Bob covers is in it.
	r := s.must(201, s.do(bob, "POST", "/api/users", map[string]any{"user": map[string]any{"name": "Carol", "email": "carol@site.test", "password": "password1234", "role_id": roleIDByName(t, s, "Production site")}}))
	carol := "/api/users/" + jsonID(dig(r.Body, "user", "id"))
	s.must(200, s.do(bob, "PATCH", carol, map[string]any{"user": map[string]any{"name": "Carol C."}}))
	// ...but not promoted past him.
	s.must(403, s.do(bob, "PATCH", carol, map[string]any{"user": map[string]any{"role_id": roleIDByName(t, s, "Editor")}}))
	s.must(403, s.do(bob, "POST", "/api/users", map[string]any{"user": map[string]any{"name": "Dan", "email": "dan@site.test", "password": "password1234", "role_id": roleIDByName(t, s, "Editor")}}))

	// Roles: only what he holds, and never his own.
	s.must(403, s.do(bob, "POST", "/api/roles", map[string]any{"role": map[string]any{"name": "Publisher", "permissions": []string{"pages:read", "pages:publish"}}}))
	s.must(201, s.do(bob, "POST", "/api/roles", map[string]any{"role": map[string]any{"name": "Reader", "permissions": []string{"pages:read"}}}))
	own, err := s.app.userRole(t.Context(), bobID)
	if err != nil {
		t.Fatal(err)
	}
	s.must(409, s.do(bob, "PATCH", "/api/roles/"+strconv.FormatInt(own.ID, 10), map[string]any{"role": map[string]any{"permissions": []string{"pages:publish"}}}))
	s.must(403, s.do(bob, "PATCH", "/api/roles/"+roleIDByName(t, s, "Editor"), map[string]any{"role": map[string]any{"name": "Mine now"}}))

	// Service tokens: holding a token's secret is acting as it.
	r = s.must(201, s.do(s.admin, "POST", "/api/service_tokens", map[string]any{"name": "ROOT", "role": "Administrator"}))
	root := jsonID(dig(r.Body, "service_token", "id"))
	s.must(403, s.do(bob, "POST", "/api/service_tokens/"+root+"/reveal", nil))
	s.must(403, s.do(bob, "POST", "/api/service_tokens/"+root+"/rotate", nil))
	s.must(403, s.do(bob, "POST", "/api/service_tokens", map[string]any{"name": "EDIT", "role": "Editor"}))
	r = s.must(201, s.do(bob, "POST", "/api/service_tokens", map[string]any{"name": "SITE", "role": "Production site"}))
	site := jsonID(dig(r.Body, "service_token", "id"))
	s.must(200, s.do(bob, "POST", "/api/service_tokens/"+site+"/reveal", nil))
	s.must(200, s.do(bob, "POST", "/api/service_tokens/"+site+"/rotate", nil))
	s.must(200, s.do(s.admin, "POST", "/api/service_tokens/"+root+"/reveal", nil))
}

func roleIDByName(t *testing.T, s *site, name string) string {
	t.Helper()
	r, err := s.app.roleByName(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(r.ID, 10)
}

func TestRevisionsNeedReadAndPublish(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "pricing", "title": "Pricing", "status": "published"}}))
	r := s.must(202, s.do(s.agent, "PATCH", "/api/pages/pricing", map[string]any{"page": map[string]any{"title": "Plans"}}))
	rev := "/api/revisions/" + jsonID(dig(r.Body, "revision", "id"))

	// A token that reads entries but not pages doesn't see a page's revision.
	s.must(201, s.do(s.admin, "POST", "/api/roles", map[string]any{"role": map[string]any{"name": "Entry reader", "permissions": []string{"entries:read", "collections:read"}}}))
	entries, err := s.app.CreateServiceTokenForRole(t.Context(), "ENTRIES", "Entry reader")
	if err != nil {
		t.Fatal(err)
	}
	if got := asList(s.must(200, s.do(entries, "GET", "/api/revisions", nil)).Body["revisions"]); len(got) != 0 {
		t.Fatalf("listed %d page revisions to an entries reader", len(got))
	}
	s.must(403, s.do(entries, "GET", rev, nil))
	s.must(200, s.do(s.agent, "GET", rev, nil))

	// Rejecting takes publish, as approving does.
	s.must(403, s.do(s.agent, "POST", rev+"/reject", nil))
	s.must(403, s.do(entries, "POST", rev+"/reject", nil))

	// Two reviewers at once: one decides it.
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Go(func() {
			codes[i] = s.do(s.admin, "POST", rev+"/approve", nil).Status
		})
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 200 {
			ok++
		} else if c != 409 {
			t.Fatalf("decide: %v", codes)
		}
	}
	if ok != 1 {
		t.Fatalf("%d approvals landed, want 1: %v", ok, codes)
	}
}

func TestOneTimeCodesSpendOnce(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	ctx := t.Context()
	var aliceID int64
	if err := s.app.store.QueryRowContext(t.Context(), `SELECT id FROM users WHERE email = 'alice@site.test'`).Scan(&aliceID); err != nil {
		t.Fatal(err)
	}
	token, err := s.app.issueAccountToken(ctx, aliceID, purposeReset, "", resetTTL)
	if err != nil {
		t.Fatal(err)
	}
	if spent := race(8, func() error { _, _, err := s.app.redeemAccountToken(ctx, token, purposeReset); return err }); spent != 1 {
		t.Fatalf("a reset link was redeemed %d times", spent)
	}

	r := s.must(200, s.do("", "POST", "/api/device/code", map[string]any{"hostname": "laptop"}))
	device, code := str(r.Body["device_code"]), str(r.Body["user_code"])
	s.must(200, s.do(s.admin, "POST", "/api/device/approve", map[string]any{"user_code": code}))
	s.must(404, s.do(s.admin, "POST", "/api/device/approve", map[string]any{"user_code": code, "approve": false}))
	got := race(8, func() error {
		if r := s.do("", "POST", "/api/device/token", map[string]any{"device_code": device}); r.Body["status"] != "ok" {
			return apierr.BadRequest(string(r.Raw))
		}
		return nil
	})
	if got != 1 {
		t.Fatalf("a device code handed out %d credentials", got)
	}
}

// race runs fn n times at once and counts the successes.
func race(n int, fn func() error) int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range n {
		wg.Go(func() {
			if fn() == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return ok
}

func TestSignInLimitsAndProxyAddress(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.app.cfg.TrustProxy = true

	// The client's address is the one our proxy appended, not one it sent.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 10.0.0.9")
	if got := (&Ctx{App: s.app, R: req}).ip(); got != "10.0.0.9" {
		t.Fatalf("ip %q, want the rightmost entry", got)
	}

	// Guessing at one account from many addresses still stops.
	if _, err := s.app.CreateUserWithRole(t.Context(), "Target", "target@site.test", "password1234", "Author"); err != nil {
		t.Fatal(err)
	}
	last := 0
	for i := range 11 {
		last = s.do("", "POST", "/api/session", map[string]any{"email": "Target@site.test", "password": "guess"}, "X-Forwarded-For", "203.0.113."+strconv.Itoa(i)).Status
	}
	if last != 429 {
		t.Fatalf("11th guess at one account: %d, want 429", last)
	}
	// An unknown address is the same 401 as a wrong password.
	r := s.must(401, s.do("", "POST", "/api/session", map[string]any{"email": "nobody@site.test", "password": "guess"}, "X-Forwarded-For", "198.51.100.1"))
	if r.Body["message"] != "That email and password don't match." {
		t.Fatalf("unknown email: %s", r.Raw)
	}
}

func TestBodiesAndImagesAreBounded(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	big := append([]byte(`{"page":{"slug":"x","title":"`), bytes.Repeat([]byte("a"), maxBody)...)
	big = append(big, `"}}`...)
	s.must(413, s.do(s.admin, "POST", "/api/pages", big))

	ctx := t.Context()
	// A few bytes can claim any size: past the cap the upload is refused...
	_, err := s.app.uploadAsset(ctx, nil, "bomb.png", "image/png", bytes.NewReader(pngHeader(20000, 20000)), nil)
	if e, ok := errors.AsType[*apierr.Error](err); !ok || e.Status != 422 {
		t.Fatalf("a 400-megapixel upload: %v", err)
	}
	// ...and below it, a big one is served as it is rather than decoded.
	as, err := s.app.uploadAsset(ctx, nil, "huge.png", "image/png", bytes.NewReader(pngHeader(8000, 8000)), nil)
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.app.variant(*as, 640)
	if err != nil || path != s.app.blobPath(as.BlobKey) {
		t.Fatalf("variant of a 64-megapixel image: %q %v", path, err)
	}
}

// pngHeader is a PNG that says it's w×h and holds no pixels.
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2
	chunk("IHDR", ihdr)
	chunk("IEND", nil)
	return b.Bytes()
}

func TestSettingsReadFailureDoesntWipe(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	ctx := t.Context()
	if _, err := s.app.store.ExecContext(t.Context(), `UPDATE settings SET data = '{"title":' WHERE key = 'general'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.app.setting(ctx, "general"); err == nil {
		t.Fatal("an unreadable setting read as {}")
	}
	if _, err := s.app.putSetting(ctx, nil, "general", map[string]any{"cors_origins": "https://x.test"}, false); err == nil {
		t.Fatal("a merge into an unreadable setting went ahead")
	}
	var data string
	s.app.store.QueryRowContext(t.Context(), `SELECT data FROM settings WHERE key = 'general'`).Scan(&data)
	if data != `{"title":` {
		t.Fatalf("setting overwritten: %q", data)
	}
	if m, err := s.app.setting(ctx, "never-set"); err != nil || len(m) != 0 {
		t.Fatalf("unset key: %v %v", m, err)
	}
}

func TestIntOfRefusesFractions(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in any
		n  int64
		ok bool
	}{{float64(7), 7, true}, {1.5, 0, false}, {1e300, 0, false}, {"12", 12, true}, {nil, 0, false}} {
		if n, ok := intOf(c.in); n != c.n || ok != c.ok {
			t.Errorf("intOf(%v) = %d, %v", c.in, n, ok)
		}
	}
}
