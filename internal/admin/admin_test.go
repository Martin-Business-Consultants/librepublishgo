package admin

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The admin is the install's root: / is the app, /assets/ its files, and
// /cms (where it was) redirects to /.
func TestHandler(t *testing.T) {
	h := Handler()
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		return w
	}
	for _, p := range []string{"/", "/index.html"} {
		if w := get(p); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<div id=") || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	// The app's assets are at /assets/, as the built index.html asks for them.
	if w := get("/"); !strings.Contains(w.Body.String(), `src="/assets/`) {
		t.Fatalf("index asks for its assets elsewhere: %s", w.Body.String())
	}
	// Scripts are only the built files: the page has none inline, so the
	// policy allows none, and an injected one doesn't run.
	if w := get("/"); strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self' 'unsafe-inline'") ||
		regexp.MustCompile(`<script(\s[^>]*)?>\s*[^<\s]`).MatchString(w.Body.String()) {
		t.Fatalf("inline scripts: %s\n%s", w.Header().Get("Content-Security-Policy"), w.Body.String())
	}
	assets, _ := fs.Glob(dist, "dist/assets/*.js")
	if len(assets) == 0 {
		t.Fatal("no built assets")
	}
	w := get(strings.TrimPrefix(assets[0], "dist"))
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset %s: %d", assets[0], w.Code)
	}
	for _, p := range []string{"/cms", "/cms/", "/cms/assets/x.js", "/cms/index.html"} {
		if w := get(p); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/" {
			t.Fatalf("%s: %d → %q", p, w.Code, w.Header().Get("Location"))
		}
	}
	for _, p := range []string{"/assets/", "/assets", "/nope", "/assets/missing.js"} {
		if w := get(p); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}
