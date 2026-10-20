package cms

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// A backup outlasting the server's write timeout still arrives whole: it
// extends its own deadline.
func TestBackupOutlastsWriteTimeout(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	slow := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond) // past the timeout before the handler starts
		s.server.Handler().ServeHTTP(w, r)
	}))
	slow.Config.WriteTimeout = 100 * time.Millisecond
	slow.Start()
	t.Cleanup(slow.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, slow.URL+"/api/tools/backup", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.admin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, err %v", res.StatusCode, err)
	}
	if files := entries(t, raw); files["manifest.json"] == nil {
		t.Fatalf("no manifest in %d bytes", len(raw))
	}
}

// busyErr is the error SQLite gives a write while another connection holds
// the write lock and there's no busy timeout to wait it out.
func busyErr(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "busy.sqlite3")
	holder, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	if _, err := holder.ExecContext(t.Context(), `CREATE TABLE t (id integer)`); err != nil {
		t.Fatal(err)
	}
	conn, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	_, err = other.ExecContext(t.Context(), `INSERT INTO t VALUES (1)`)
	if err == nil {
		t.Fatal("wrote under another connection's write lock")
	}
	return fmt.Errorf("saving: %w", err)
}

// A lookup's sql.ErrNoRows is a 404, and a database busy past its timeout
// a 503 to retry, not a 500.
func TestFailMapsStoreErrors(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	busy := busyErr(t)
	if !store.IsBusy(busy) || store.IsBusy(sql.ErrNoRows) {
		t.Fatalf("IsBusy(%v) wrong", busy)
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("page: %w", sql.ErrNoRows), 404, "not_found"},
		{store.ErrNotFound, 404, "not_found"},
		{busy, 503, "busy"},
		{errors.New("boom"), 500, "internal_error"},
	} {
		rec := httptest.NewRecorder()
		c := &Ctx{App: s.app, W: rec, R: httptest.NewRequestWithContext(t.Context(), "GET", "/api/pages/x", nil)}
		c.fail(tc.err)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Fatalf("%v: %d %s", tc.err, rec.Code, rec.Body)
		}
		if (tc.status == 503) != (rec.Header().Get("Retry-After") != "") {
			t.Fatalf("%v: Retry-After %q", tc.err, rec.Header().Get("Retry-After"))
		}
	}
}

// Redirect hits are counted in memory and written in batches; the listing
// counts them either way, and none is counted twice or lost on Close.
func TestRedirectHitsBatched(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.must(201, s.do(s.admin, "POST", "/api/redirects", map[string]any{"redirect": map[string]any{"source_path": "/old", "destination_url": "/new"}}))
	for range 3 {
		s.must(200, s.do(s.prod, "GET", "/api/redirects/resolve?path=/old", nil))
	}
	stored := func() int {
		n, err := store.Count(t.Context(), s.app.store.DB, `SELECT hit_count FROM redirects WHERE source_path = '/old'`)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	listed := func() any {
		return dig(s.must(200, s.do(s.admin, "GET", "/api/redirects/all", nil)).Body, "redirects", 0, "hit_count")
	}
	if stored() != 0 || listed() != 3.0 {
		t.Fatalf("before the flush: stored %d, listed %v", stored(), listed())
	}
	s.app.flushHits(t.Context())
	if stored() != 3 || listed() != 3.0 {
		t.Fatalf("after the flush: stored %d, listed %v", stored(), listed())
	}
	s.must(200, s.do(s.prod, "GET", "/api/redirects/resolve?path=/old", nil))
	if err := s.app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(filepath.Join(s.app.cfg.DataDir, "cms.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if n, _ := store.Count(t.Context(), reopened, `SELECT hit_count FROM redirects WHERE source_path = '/old'`); n != 4 {
		t.Fatalf("after Close: %d hits", n)
	}
}

// Another process's scheduler saving a due page first isn't a warning.
func TestSchedulerConflictIsQuiet(t *testing.T) {
	t.Parallel()
	var buf syncBuffer
	s := newSiteWith(t, Config{NoDefaultPlugins: true, Logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	page := &Content{Kind: "page", Title: "Launch"}
	s.app.scheduleFailed("scheduled publish", page, apierr.Conflict("lock_version 1 is gone"))
	if strings.Contains(buf.String(), "scheduled publish") {
		t.Fatalf("a conflict warned: %s", buf.String())
	}
	s.app.scheduleFailed("scheduled publish", page, errors.New("disk full"))
	if !strings.Contains(buf.String(), "disk full") {
		t.Fatalf("a failure didn't warn: %s", buf.String())
	}
}

// syncBuffer is a bytes.Buffer safe for a logger and a test to share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Seeding a new database from several places at once makes the roles once.
func TestSeedIsIdempotentUnderConcurrency(t *testing.T) {
	t.Parallel()
	app, err := NewApp(Config{DataDir: t.TempDir(), NoDefaultPlugins: true, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ctx := t.Context()
	if _, err := app.store.ExecContext(ctx, `DELETE FROM roles`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- app.seed() })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.Count(ctx, app.store.DB, `SELECT count(*) FROM roles`)
	if err != nil || n != len(defaultRoles) {
		t.Fatalf("%d roles (err %v), want %d", n, err, len(defaultRoles))
	}
}
