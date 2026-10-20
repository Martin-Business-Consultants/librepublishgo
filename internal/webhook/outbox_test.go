package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func TestMain(m *testing.M) {
	// The receivers are httptest servers, on 127.0.0.1; retries come
	// quickly.
	outbound.AllowPrivate(true)
	Backoff = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	os.Exit(m.Run())
}

// install is a database with one webhook to url, and how to start a
// dispatcher on it (a run of the app) and stop it (its close).
type install struct {
	db   *store.DB
	key  *secret.Key
	hook int64
}

func newInstall(t *testing.T, url string) *install {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "cms.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := secret.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	now := store.Now().DB()
	res, err := db.ExecContext(t.Context(), `INSERT INTO webhooks (name, url, secret, created_at, updated_at) VALUES ('w', ?, ?, ?, ?)`, url, key.Seal("s3cret"), now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return &install{db: db, key: key, hook: id}
}

// run is a dispatcher whose background work ends with stop.
func (in *install) run(t *testing.T) (d *Dispatcher, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	d = New(in.db, in.key, slog.New(slog.DiscardHandler), "acme", func(_ context.Context, _ string, fn func(context.Context)) bool {
		wg.Go(func() { fn(ctx) })
		return true
	})
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); wg.Wait() }) }
	t.Cleanup(stop)
	return d, stop
}

func (in *install) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	n, err := store.Count(t.Context(), in.db, q, args...)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// waitFor polls until ok, for up to 5s.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type got struct {
	header http.Header
	body   []byte
}

// A receiver that fails gets the delivery again, the same delivery (its
// id), signed afresh; each attempt is recorded, and the webhook is shown
// as fine once one gets through.
func TestRetriesAfterTheReceiverFails(t *testing.T) {
	var mu sync.Mutex
	var gots []got
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		gots = append(gots, got{r.Header.Clone(), b})
		if len(gots) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer recv.Close()
	in := newInstall(t, recv.URL)
	d, _ := in.run(t)
	d.Deliver(t.Context(), "page.published", map[string]any{"id": 1, "title": "About"})

	waitFor(t, "the outbox to empty", func() bool { return in.count(t, `SELECT count(*) FROM webhook_outbox`) == 0 })
	mu.Lock()
	defer mu.Unlock()
	if len(gots) != 3 {
		t.Fatalf("attempts: %d", len(gots))
	}
	id := gots[0].header.Get(outbound.DeliveryHeader)
	for _, g := range gots {
		if g.header.Get(outbound.DeliveryHeader) != id || id == "" {
			t.Fatalf("delivery ids %q, %q", id, g.header.Get(outbound.DeliveryHeader))
		}
		m := hmac.New(sha256.New, []byte("s3cret"))
		m.Write([]byte(g.header.Get(outbound.TimestampHeader) + "."))
		m.Write(g.body)
		if g.header.Get(outbound.SignatureV2Header) != "sha256="+hex.EncodeToString(m.Sum(nil)) {
			t.Fatalf("v2 signature doesn't verify")
		}
	}
	if n := in.count(t, `SELECT count(*) FROM webhook_deliveries WHERE success = 0`); n != 2 {
		t.Fatalf("failed attempts recorded: %d", n)
	}
	if n := in.count(t, `SELECT count(*) FROM webhook_deliveries WHERE success = 1`); n != 1 {
		t.Fatalf("delivered recorded: %d", n)
	}
	if n := in.count(t, `SELECT failure_count FROM webhooks`); n != 0 {
		t.Fatalf("failure_count %d", n)
	}
}

// A refusal isn't tried again; one that keeps failing is given up on after
// its last attempt. Each failure counts against the webhook.
func TestGivesUp(t *testing.T) {
	status := http.StatusBadRequest
	var mu sync.Mutex
	hits := 0
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		w.WriteHeader(status)
	}))
	defer recv.Close()
	in := newInstall(t, recv.URL)
	d, _ := in.run(t)
	d.Deliver(t.Context(), "page.published", map[string]any{"id": 1})
	waitFor(t, "the outbox to empty", func() bool { return in.count(t, `SELECT count(*) FROM webhook_outbox`) == 0 })
	if mu.Lock(); hits != 1 {
		t.Fatalf("a 400 was sent %d times", hits)
	}
	hits, status = 0, http.StatusInternalServerError
	mu.Unlock()
	d.Deliver(t.Context(), "page.published", map[string]any{"id": 2})
	waitFor(t, "the outbox to empty", func() bool { return in.count(t, `SELECT count(*) FROM webhook_outbox`) == 0 })
	if mu.Lock(); hits != len(Backoff)+1 {
		t.Fatalf("a 500 was sent %d times", hits)
	}
	mu.Unlock()
	if n := in.count(t, `SELECT failure_count FROM webhooks`); n != 1+len(Backoff)+1 {
		t.Fatalf("failure_count %d", n)
	}
}

// What's queued when the app stops, mid-send included, goes out when it
// starts again, and a send the stop cut off isn't counted as a failure.
func TestResumesAfterARestart(t *testing.T) {
	var mu sync.Mutex
	hang, hits := true, 0
	arrived := make(chan struct{}, 1)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // so the server notices the sender hang up
		mu.Lock()
		h := hang
		hits++
		mu.Unlock()
		if h {
			arrived <- struct{}{}
			<-r.Context().Done() // until the sender gives up on it
			return
		}
	}))
	defer recv.Close()
	in := newInstall(t, recv.URL)

	d, stop := in.run(t)
	d.Deliver(t.Context(), "page.published", map[string]any{"id": 1})
	<-arrived
	stop()
	if n := in.count(t, `SELECT count(*) FROM webhook_outbox WHERE attempts = 0 AND next_attempt_at <= ?`, store.Now().DB()); n != 1 {
		t.Fatalf("cut off, the delivery wasn't put back: %d", n)
	}
	if n := in.count(t, `SELECT count(*) FROM webhook_deliveries`); n != 0 {
		t.Fatalf("a cut-off send was recorded: %d", n)
	}

	// Queued while nothing sends (a one-off command, say).
	idle := New(in.db, in.key, slog.New(slog.DiscardHandler), "acme", func(context.Context, string, func(context.Context)) bool { return false })
	idle.Deliver(t.Context(), "page.updated", map[string]any{"id": 1})

	mu.Lock()
	hang = false
	mu.Unlock()
	d, _ = in.run(t)
	d.Start(t.Context())
	waitFor(t, "the outbox to empty", func() bool { return in.count(t, `SELECT count(*) FROM webhook_outbox`) == 0 })
	if n := in.count(t, `SELECT count(*) FROM webhook_deliveries WHERE success = 1`); n != 2 {
		t.Fatalf("delivered after the restart: %d", n)
	}
	if n := in.count(t, `SELECT failure_count FROM webhooks`); n != 0 {
		t.Fatalf("failure_count %d", n)
	}
}
