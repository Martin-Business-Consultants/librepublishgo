package plugin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A plugin that panics answers an error; the server carries on.
func TestGuardRecoversPanic(t *testing.T) {
	t.Parallel()
	err := guard(t.Context(), "plugin x/y", func(context.Context) error { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
}

// HTTP reaches the hosts granted and no others, redirects included, and
// reads at most 4 MB.
func TestHTTPOnlyGrantedHosts(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", maxHTTPResponse+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, "http://elsewhere.test/", http.StatusFound)
		case "/big":
			io.WriteString(w, big)
		default:
			io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	get := func(api *API, path string) (string, error) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		res, err := api.HTTP().Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return string(b), err
	}
	if _, err := get(&API{}, "/"); err == nil || !strings.Contains(err.Error(), "wasn't granted") {
		t.Fatalf("ungranted: %v", err)
	}
	api := &API{grants: Grants{HTTP: []string{u.Hostname()}}}
	if body, err := get(api, "/"); err != nil || body != "ok" {
		t.Fatalf("granted: %q %v", body, err)
	}
	if _, err := get(api, "/away"); err == nil || !strings.Contains(err.Error(), "elsewhere.test") {
		t.Fatalf("redirect: %v", err)
	}
	if _, err := get(api, "/big"); err == nil || !strings.Contains(err.Error(), "4 MB") {
		t.Fatalf("big: %v", err)
	}
}

// A capability that wasn't granted is refused before it does anything.
func TestAPIRefusesUngranted(t *testing.T) {
	t.Parallel()
	api := &API{key: "x"}
	if _, _, err := api.KVGet(t.Context(), "k"); err == nil || err.Error() != "storage wasn't granted to this plugin" {
		t.Fatalf("kv: %v", err)
	}
	if _, err := api.Content(t.Context(), ContentQuery{Pages: true}); err == nil {
		t.Fatal("content wasn't refused")
	}
	if _, err := api.PutMedia(t.Context(), Media{Filename: "a", Data: []byte("a")}); err == nil {
		t.Fatal("media wasn't refused")
	}
}

// A module whose routes name no handler is a bug in the build.
func TestModuleValidate(t *testing.T) {
	t.Parallel()
	m := &Module{Manifest: Manifest{Key: "ok", Name: "OK", Version: "1", Routes: []Route{{Method: "GET", Path: "/a", Export: "a"}}}}
	if err := m.validate(); err == nil || !strings.Contains(err.Error(), "no handler") {
		t.Fatalf("err %v", err)
	}
	m.Handlers = map[string]Handler{"a": func(context.Context, *API, *Request) (*Response, error) { return nil, nil }}
	if err := m.validate(); err != nil {
		t.Fatal(err)
	}
}
