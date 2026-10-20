package deploy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

type memSettings struct {
	mu   sync.Mutex
	data map[string]map[string]any
}

func (m *memSettings) Setting(_ context.Context, key string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.data[key]; ok {
		return v, nil
	}
	return map[string]any{}, nil
}

func (m *memSettings) ReplaceSetting(_ context.Context, key string, data map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = data
	return nil
}

func TestTriggerByRenderMode(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		render, kind   string
		purges, builds int
	}{
		{"static", "build", 0, 1},
		{"server", "purge", 1, 0},
		{"hybrid", "purge+build", 1, 1},
	} {
		t.Run(tc.render, func(t *testing.T) {
			t.Parallel()
			settings := &memSettings{data: map[string]map[string]any{
				"deploy":   {"hook_url": srv.URL + "/build/" + tc.render},
				"frontend": {"render": tc.render, "webhook_url": srv.URL + "/purge/" + tc.render, "secret": "s"},
			}}
			d := New(settings, slog.New(slog.DiscardHandler), func(context.Context, string, func(context.Context)) bool { return false })
			status, err := d.Trigger(t.Context(), "publish")
			if err != nil || status["kind"] != tc.kind || status["status"] != "success" {
				t.Fatalf("status %v, err %v", status, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if hits["/purge/"+tc.render] != tc.purges || hits["/build/"+tc.render] != tc.builds {
				t.Fatalf("purges %d, builds %d", hits["/purge/"+tc.render], hits["/build/"+tc.render])
			}
		})
	}
}

// builds counts the build hook's calls.
func builds(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		n++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

func goSpawn(wg *sync.WaitGroup) Spawn {
	return func(ctx context.Context, _ string, fn func(context.Context)) bool {
		wg.Go(func() { fn(ctx) })
		return true
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal(what)
}

// A deploy waiting out its debounce when the process stops goes out when
// the next one starts.
func TestPendingDeploySurvivesRestart(t *testing.T) {
	t.Parallel()
	srv, count := builds(t)
	settings := &memSettings{data: map[string]map[string]any{
		"deploy": {"hook_url": srv.URL, "debounce_seconds": 3600},
	}}
	var wg sync.WaitGroup
	d := New(settings, slog.New(slog.DiscardHandler), goSpawn(&wg))
	d.Changed(t.Context(), "page:1")
	d.Stop()
	if p, _ := settings.Setting(t.Context(), pendingKey); p["since"] == nil {
		t.Fatalf("nothing recorded as waiting: %v", p)
	}
	if count() != 0 {
		t.Fatal("deployed before the debounce was out")
	}

	next := New(settings, slog.New(slog.DiscardHandler), goSpawn(&wg))
	next.Resume(t.Context())
	eventually(t, "the waiting deploy never went out", func() bool { return count() == 1 })
	wg.Wait()
	if p, _ := settings.Setting(t.Context(), pendingKey); len(p) != 0 {
		t.Fatalf("still waiting after the deploy: %v", p)
	}
	if st, _ := settings.Setting(t.Context(), "deploy_status"); !slices.Equal(st["tags"].([]string), []string{"page:1"}) {
		t.Fatalf("deployed tags %v", st["tags"])
	}
}

// Changes that keep restarting the debounce still deploy by maxWait after
// the first.
func TestDebounceHasMaximumWait(t *testing.T) {
	t.Parallel()
	srv, count := builds(t)
	settings := &memSettings{data: map[string]map[string]any{
		"deploy": {"hook_url": srv.URL, "debounce_seconds": 3600},
	}}
	var wg sync.WaitGroup
	d := New(settings, slog.New(slog.DiscardHandler), goSpawn(&wg))
	d.Changed(t.Context(), "page:1")
	d.mu.Lock()
	d.since = time.Now().Add(-maxWait + 50*time.Millisecond) // as if it came in long ago
	d.mu.Unlock()
	d.Changed(t.Context(), "page:2")
	eventually(t, "the deploy waited past maxWait", func() bool { return count() == 1 })
	wg.Wait()
	if p, _ := settings.Setting(t.Context(), pendingKey); len(p) != 0 {
		t.Fatalf("still waiting after the deploy: %v", p)
	}
}

// A failed deploy stays waiting, for the next start to try again.
func TestFailedDeployStaysPending(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	settings := &memSettings{data: map[string]map[string]any{
		"deploy": {"hook_url": srv.URL, "debounce_seconds": 3600},
	}}
	d := New(settings, slog.New(slog.DiscardHandler), func(context.Context, string, func(context.Context)) bool { return false })
	d.Changed(t.Context(), "page:1")
	if _, err := d.Trigger(t.Context(), "publish"); err == nil {
		t.Fatal("a 502 deployed")
	}
	if p, _ := settings.Setting(t.Context(), pendingKey); p["since"] == nil {
		t.Fatalf("the failed deploy was forgotten: %v", p)
	}
}
