package cms

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRequestID(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	r := s.do("", "GET", "/robots.txt", nil, "X-Request-ID", "edge-42.a")
	if got := r.Header.Get("X-Request-ID"); got != "edge-42.a" {
		t.Fatalf("a sane incoming id is kept: got %q", got)
	}
	r = s.do("", "GET", "/robots.txt", nil, "X-Request-ID", "no spaces <here>")
	if got := r.Header.Get("X-Request-ID"); got == "" || strings.ContainsAny(got, " <>") {
		t.Fatalf("an unsafe incoming id is replaced: got %q", got)
	}
	if a, b := s.do("", "GET", "/up", nil).Header.Get("X-Request-ID"), s.do("", "GET", "/up", nil).Header.Get("X-Request-ID"); a == "" || a == b {
		t.Fatalf("each request gets its own id: %q, %q", a, b)
	}
}

func TestPanicsAreLoggedAndAnswered(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	a := &App{log: slog.New(slog.NewTextHandler(&logs, nil))}
	h := a.logRequests(a.recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("boom") })))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/files/x/y", nil))
	if w.Code != 500 {
		t.Fatalf("status %d, want 500", w.Code)
	}
	if !strings.Contains(logs.String(), "boom") || !strings.Contains(logs.String(), "stack=") || !strings.Contains(logs.String(), "status=500") {
		t.Fatalf("log: %s", logs.String())
	}

	// Cutting the connection on purpose is still net/http's to handle.
	abort := a.recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if rec := recover(); !isAbort(rec) {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	abort.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
}

func TestKeyedLocksForget(t *testing.T) {
	t.Parallel()
	var k keyedLocks
	var wg sync.WaitGroup
	n := 0
	for range 50 {
		wg.Go(func() {
			defer k.lock("x")()
			n++
		})
	}
	wg.Wait()
	if n != 50 || len(k.locks) != 0 {
		t.Fatalf("n %d, %d locks left", n, len(k.locks))
	}
}

// A plugin reloaded while it's answering keeps answering.
func TestPluginReloadDuringCalls(t *testing.T) {
	t.Parallel()
	s := newSiteWithVisits(t)
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "about", "title": "About", "status": "published"}}))
	s.must(201, s.do(s.admin, "POST", "/api/plugins", map[string]any{"plugin": map[string]any{"key": "visits"}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/plugins/visits", map[string]any{"plugin": map[string]any{
		"granted": map[string]any{"storage": true, "content:read": true}, "enabled": true}}))

	var wg sync.WaitGroup
	done := make(chan struct{})
	for range 3 {
		wg.Go(func() {
			for {
				if r := s.do(s.admin, "POST", "/api/x/visits/hit", map[string]any{"path": "/about"}); r.Status != 200 {
					t.Errorf("hit during reload: %d %s", r.Status, r.Raw)
					return
				}
				select {
				case <-done:
					return
				default:
				}
			}
		})
	}
	for range 2 {
		if err := s.app.plugins.LoadAll(t.Context()); err != nil {
			t.Error(err)
		}
	}
	close(done)
	wg.Wait()
}

// The compiled WebAssembly an older build cached is cleared at start.
func TestOldPluginCacheCleared(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache", "plugins")
	if err := os.MkdirAll(cache, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "forms.bin"), []byte("compiled"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{DataDir: dir, SiteKey: "acme", BaseURL: "http://cms.test", NoDefaultPlugins: true, Logger: slog.New(slog.NewTextHandler(t.Output(), nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.App().Close() })
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("the old cache is still there (%v)", err)
	}
}
