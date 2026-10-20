package cms

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// entries is a .tar.gz's files, by name.
func entries(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name], _ = io.ReadAll(tr)
	}
}

// A Go site's backup, imported into another running site through the
// admin's API, replaces it without a restart: content, files, the forms
// plugin and its submissions, and the secrets (the key came with it).
func TestImportGoBackup(t *testing.T) {
	t.Parallel()
	a := newSiteWith(t, Config{})
	ctx := t.Context()
	a.must(201, a.do(a.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "mill", "title": "The mill in 1890"}}))
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "notes.txt")
	fw.Write([]byte("grain and water"))
	mw.Close()
	asset := a.must(201, a.do(a.admin, "POST", "/api/assets", body.Bytes(), "Content-Type", mw.FormDataContentType())).Body["asset"]
	fileURL, _ := url.Parse(str(dig(asset, "url")))
	a.must(200, a.do(a.admin, "POST", "/api/x/forms/form", map[string]any{"form": map[string]any{"name": "Contact", "status": "active", "store_submissions": true,
		"fields": []any{map[string]any{"type": "text", "name": "name", "label": "Name"}}}}))
	ts := strconv.FormatInt(time.Now().Add(-10*time.Second).UnixMilli(), 10)
	a.must(200, a.do("", "POST", "/api/x/forms/submit?form=contact", map[string]any{"name": "Ada", "_ts": ts}))
	hook := a.must(201, a.do(a.admin, "POST", "/api/webhooks", map[string]any{"webhook": map[string]any{"name": "Build", "url": "https://hooks.example.com/x", "events": []any{"page.published"}}}))
	hookSecret := str(dig(hook.Body, "webhook", "secret"))

	// Only the system role may include the key.
	a.must(403, a.do(a.agent, "GET", "/api/tools/backup?include_secret_key=1", nil))
	backup := a.must(200, a.do(a.admin, "GET", "/api/tools/backup?include_secret_key=1", nil)).Raw
	files := entries(t, backup)
	var manifest map[string]any
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil || manifest["format"] != "librepublish-go" || manifest["blobs"] != float64(1) || manifest["secret_key"] != true {
		t.Fatalf("manifest: %s %v", files["manifest.json"], err)
	}
	if strings.TrimSpace(string(files["secret.key"])) != a.app.secrets.Hex() || files["cms.sqlite3"] == nil {
		t.Fatalf("entries: %v", keys(files))
	}

	b := newSiteWith(t, Config{})
	// Nobody below the system role stages or applies one.
	b.must(403, b.do(b.agent, "POST", "/api/tools/import", backup))
	r := b.must(201, b.do(b.admin, "POST", "/api/tools/import?filename=a.tar.gz", backup, "Content-Type", "application/gzip"))
	id := str(dig(r.Body, "import", "id"))
	if dig(r.Body, "import", "format") != "go" || dig(r.Body, "import", "blobs", "copied") != float64(1) || dig(r.Body, "import", "target_users") != float64(1) ||
		dig(r.Body, "import", "secrets", "key_in_backup") != true || dig(r.Body, "import", "source") != "a.tar.gz" {
		t.Fatalf("staged: %s", r.Raw)
	}
	b.must(200, b.do(b.admin, "GET", "/api/tools/import/"+id, nil))
	// Staged is all: the site is as it was.
	if r := b.must(200, b.do(b.admin, "GET", "/api/pages", nil)); strings.Contains(string(r.Raw), "The mill") {
		t.Fatalf("staging changed the site: %s", r.Raw)
	}
	// It has users, so it takes force.
	b.must(409, b.do(b.admin, "POST", "/api/tools/import/"+id+"/apply", map[string]any{}))
	r = b.must(200, b.do(b.admin, "POST", "/api/tools/import/"+id+"/apply", map[string]any{"force": true}))
	if r.Body["signed_out"] != true || dig(r.Body, "import", "applied") != true || dig(r.Body, "import", "secrets", "done", "cleared") != float64(0) ||
		dig(r.Body, "import", "secrets", "done", "resealed") != float64(4) { // three tokens and the webhook
		t.Fatalf("applied: %s", r.Raw)
	}

	// The same server answers as site A now: A's token works and B's
	// doesn't, A's page, file and form are here.
	b.must(401, b.do(b.admin, "GET", "/api/pages", nil))
	if r := b.must(200, b.do(a.admin, "GET", "/api/pages", nil)); !strings.Contains(string(r.Raw), "The mill in 1890") {
		t.Fatalf("pages: %s", r.Raw)
	}
	res, err := get(t, b.srv.URL+fileURL.Path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(got) != "grain and water" {
		t.Fatalf("file: %d %q", res.StatusCode, got)
	}
	if r := b.must(200, b.do(a.admin, "GET", "/api/x/forms/forms", nil)); !strings.Contains(string(r.Raw), `"submissions":1`) {
		t.Fatalf("forms: %s", r.Raw)
	}
	b.must(200, b.do("", "POST", "/api/x/forms/submit?form=contact", map[string]any{"name": "Grace", "_ts": ts}))
	// The webhook's secret was re-sealed under B's key.
	app := b.server.App()
	var sealed string
	if err := app.store.QueryRowContext(ctx, `SELECT secret FROM webhooks`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if plain, ok := app.secrets.Open(sealed); !ok || plain != hookSecret {
		t.Fatalf("webhook secret: %q %v", plain, ok)
	}
	var n int
	if err := app.store.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'backup.imported' AND actor_label = 'Alice'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit: %d %v", n, err)
	}
	b.must(404, b.do(a.admin, "GET", "/api/tools/import/"+id, nil))
}

// While an import is applied every request waits outside with a 503
// (the health check excepted), and the site is back after.
func TestMaintenanceDuringReplace(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	inside, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		// As the apply request is: counted in flight.
		s.server.inflight.Add(1)
		defer s.server.inflight.Add(-1)
		done <- s.server.Replace(t.Context(), func() (func() error, error) {
			close(inside)
			<-release
			return nil, nil
		})
	}()
	<-inside
	r := s.do(s.admin, "GET", "/api/pages", nil)
	if r.Status != http.StatusServiceUnavailable || r.Body["error"] != "maintenance" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("during: %d %s", r.Status, r.Raw)
	}
	if r := s.do("", "GET", "/", nil); r.Status != http.StatusServiceUnavailable {
		t.Fatalf("admin during: %d", r.Status)
	}
	if r := s.do("", "GET", "/up", nil); r.Status != http.StatusOK {
		t.Fatalf("up during: %d", r.Status)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s.must(200, s.do(s.admin, "GET", "/api/pages", nil))
	if s.server.App() == s.app {
		t.Fatal("the app wasn't replaced")
	}

	// A request still running holds the replacement off; past the wait it
	// gives up, and the site carries on.
	s.server.drain = 50 * time.Millisecond
	s.server.inflight.Add(2)
	err := s.server.Replace(t.Context(), func() (func() error, error) { t.Fatal("ran"); return nil, nil })
	s.server.inflight.Add(-2)
	if !errors.Is(err, errBusy) {
		t.Fatalf("busy: %v", err)
	}
	s.must(200, s.do(s.admin, "GET", "/api/pages", nil))
}
