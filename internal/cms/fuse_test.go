package cms

import (
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
)

// The Fuse pricing plugin (plugins/fuse) through the CMS: installed and
// granted like any plugin, its rate book imported and published by an
// administrator, its quotes public — rate limited for anonymous callers,
// not for a site's service token — and its admin closed to roles without
// its capabilities.
func TestFusePricing(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	r := s.must(200, s.do(s.admin, "GET", "/api/plugins", nil))
	if !slices.ContainsFunc(r.Body["available"].([]any), func(m any) bool { return dig(m, "key") == "fuse" }) {
		t.Fatalf("fuse isn't available: %s", r.Raw)
	}
	s.must(201, s.do(s.admin, "POST", "/api/plugins", map[string]any{"plugin": map[string]any{"key": "fuse"}}))
	r = s.must(200, s.do(s.admin, "PATCH", "/api/plugins/fuse", map[string]any{"plugin": map[string]any{
		"granted": map[string]any{"storage": true, "media:write": true, "http": []any{"maps.googleapis.com", "evil.example"}}, "enabled": true}}))
	if hosts := dig(r.Body, "plugin", "granted", "http").([]any); len(hosts) != 1 || hosts[0] != "maps.googleapis.com" {
		t.Fatalf("grants: %s", r.Raw)
	}

	// Nothing published: quotes are refused, ZIPs biased toward served.
	s.must(409, s.do("", "GET", "/api/x/fuse/quote?zip=78701", nil))
	if r := s.must(200, s.do("", "GET", "/api/x/fuse/service-area?zip=90210", nil)); r.Body["served"] != true {
		t.Fatalf("service area: %s", r.Raw)
	}

	bundle, err := os.ReadFile("../../plugins/fuse/testdata/austin/rates.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../plugins/fuse/testdata/austin/distances.json")
	if err != nil {
		t.Fatal(err)
	}
	importBody := map[string]any{"import": map[string]any{"bundle": string(bundle), "distance_fixture": string(fixture)}}
	// The Agent role neither imports nor publishes; anonymous callers see nothing.
	s.must(403, s.do(s.agent, "POST", "/api/x/fuse/admin/import", importBody))
	s.must(401, s.do("", "GET", "/api/x/fuse/admin/overview", nil))
	s.must(200, s.do(s.admin, "POST", "/api/x/fuse/admin/import", importBody))
	s.must(403, s.do(s.agent, "POST", "/api/x/fuse/admin/publish", nil))
	r = s.must(200, s.do(s.admin, "POST", "/api/x/fuse/admin/publish", nil))
	if !strings.HasPrefix(str(r.Body["message"]), "Published") {
		t.Fatalf("publish: %s", r.Raw)
	}

	// A quote, as a browser on the thank-you page asks for it.
	r = s.must(200, s.do("", "GET", "/api/x/fuse/quote?zip=78701&dest=78626&services=keep-it,move-it,store-it", nil))
	if dig(r.Body, "services", "move-it", "preview", "total") != float64(84554) || dig(r.Body, "distance", "status") != "fixture" {
		t.Fatalf("quote: %s", r.Raw)
	}
	if r.Header.Get("Cache-Control") != "private, max-age=60" || !strings.Contains(r.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("headers: %v", r.Header)
	}

	// The form's server side mints a token with the site's token; the
	// thank-you page verifies it.
	r = s.must(200, s.do(s.prod, "POST", "/api/x/fuse/quote", map[string]any{"zip": "78701", "dest": "78626", "services": "move-it", "selected": "move-it", "mint": true, "name": "Karen"}))
	tok := str(r.Body["token"])
	r = s.must(200, s.do("", "GET", "/api/x/fuse/quote?t="+url.QueryEscape(tok), nil))
	if dig(r.Body, "quoted", "valid") != true || dig(r.Body, "quoted", "payload", "fig", "tot") != float64(84554) {
		t.Fatalf("verify: %s", r.Raw)
	}

	// Anonymous callers are held to 30 calls a minute; the site's token isn't.
	limited := false
	for range 35 {
		if s.do("", "GET", "/api/x/fuse/service-area?zip=78701", nil).Status == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("anonymous calls weren't rate limited")
	}
	for range 35 {
		s.must(200, s.do(s.prod, "GET", "/api/x/fuse/service-area?zip=78701", nil))
	}

	// What a site's build reads.
	r = s.must(200, s.do(s.prod, "GET", "/api/v1/site", nil))
	if !strings.Contains(string(r.Raw), "/api/x/fuse/quote") {
		t.Fatalf("site config: %s", r.Raw)
	}
	// The launch checklist hears from it.
	r = s.must(200, s.do(s.admin, "GET", "/api/x/fuse/admin/overview", nil))
	if dig(r.Body, "overview", "published") != true {
		t.Fatalf("overview: %s", r.Raw)
	}
}
