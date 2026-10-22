package cms

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/visits"
)

// newSiteWithVisits is a site whose build has the example plugin
// (plugins/visits), not installed.
func newSiteWithVisits(t *testing.T) *site {
	t.Helper()
	return newSiteWith(t, Config{NoDefaultPlugins: true, Plugins: []*plugin.Module{visits.Module()}})
}

func TestPlugin(t *testing.T) {
	t.Parallel()
	s := newSiteWithVisits(t)
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "about", "title": "About", "status": "published"}}))

	// It's available until it's installed; installing leaves it off.
	r := s.must(200, s.do(s.admin, "GET", "/api/plugins", nil))
	if !slices.ContainsFunc(r.Body["available"].([]any), func(m any) bool { return dig(m, "key") == "visits" }) || len(r.Body["plugins"].([]any)) != 0 {
		t.Fatalf("list: %s", r.Raw)
	}
	install := map[string]any{"plugin": map[string]any{"key": "visits"}}
	s.must(403, s.do(s.agent, "POST", "/api/plugins", install))
	s.must(404, s.do(s.admin, "POST", "/api/plugins", map[string]any{"plugin": map[string]any{"key": "nope"}}))
	r = s.must(201, s.do(s.admin, "POST", "/api/plugins", install))
	if a := s.must(200, s.do(s.admin, "GET", "/api/plugins", nil)).Body["available"].([]any); slices.ContainsFunc(a, func(m any) bool { return dig(m, "key") == "visits" }) {
		t.Fatalf("still available after install: %v", a)
	}
	if dig(r.Body, "plugin", "enabled") != false || dig(r.Body, "plugin", "requested", "storage") != true || dig(r.Body, "plugin", "granted", "storage") != false {
		t.Fatalf("install: %s", r.Raw)
	}
	s.must(404, s.do("", "POST", "/api/x/visits/hit", map[string]any{"body": "{}"}))

	// Granting more than it asked for grants only what it asked for.
	r = s.must(200, s.do(s.admin, "PATCH", "/api/plugins/visits", map[string]any{"plugin": map[string]any{
		"granted": map[string]any{"storage": true, "content:read": true, "http": []any{"evil.example"}}, "enabled": true}}))
	if len(dig(r.Body, "plugin", "granted", "http").([]any)) != 0 || dig(r.Body, "plugin", "enabled") != true {
		t.Fatalf("configure: %s", r.Raw)
	}

	// Its public route, its storage, its content reads.
	for i := 1; i <= 2; i++ {
		r = s.must(200, s.do("", "POST", "/api/x/visits/hit", map[string]any{"path": "/about"}))
		if r.Body["views"] != float64(i) {
			t.Fatalf("hit %d: %s", i, r.Raw)
		}
	}
	s.must(404, s.do("", "POST", "/api/x/visits/hit", map[string]any{"path": "/nope"}))
	s.must(401, s.do("", "GET", "/api/x/visits/stats", nil))
	r = s.must(200, s.do(s.admin, "GET", "/api/x/visits/stats", nil))
	if dig(r.Body, "pages", 0, "path") != "about" || dig(r.Body, "pages", 0, "views") != float64(2) {
		t.Fatalf("stats: %s", r.Raw)
	}

	// Hits at once all count: the counter is one add, not a read and a write.
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { s.do("", "POST", "/api/x/visits/hit", map[string]any{"path": "/about"}) })
	}
	wg.Wait()
	r = s.must(200, s.do(s.admin, "GET", "/api/x/visits/stats", nil))
	if dig(r.Body, "pages", 0, "views") != float64(22) {
		t.Fatalf("concurrent hits lost: %s", r.Raw)
	}

	// It hears the events it hooked.
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "news", "title": "News", "status": "published"}}))
	deadline := time.Now().Add(5 * time.Second)
	for {
		var v []byte
		s.app.store.QueryRowContext(t.Context(), `SELECT value FROM plugin_kv WHERE plugin_key = 'visits' AND key = 'published:news'`).Scan(&v)
		if len(v) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the plugin never heard page.published")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Without storage granted, its storage calls are refused, not crashes.
	s.must(200, s.do(s.admin, "PATCH", "/api/plugins/visits", map[string]any{"plugin": map[string]any{"granted": map[string]any{"content:read": true}}}))
	r = s.must(200, s.do("", "POST", "/api/x/visits/hit", map[string]any{"path": "/about"}))
	if r.Body["views"] != float64(1) {
		t.Fatalf("storage still reachable: %s", r.Raw)
	}
}
