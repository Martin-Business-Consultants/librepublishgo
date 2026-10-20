package plugin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
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

// siteEnv is an Env whose site has the given origins, and which notes what's
// delivered.
type siteEnv struct {
	origins   []string
	delivered []string
}

func (e *siteEnv) Setting(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (e *siteEnv) ReplaceSetting(context.Context, string, map[string]any) error {
	return nil
}
func (e *siteEnv) Site(context.Context) (Site, error) {
	return Site{URL: "https://acme.test", Origins: e.origins}, nil
}
func (e *siteEnv) SendMail(context.Context, mail.Message) error { return nil }
func (e *siteEnv) PutMedia(context.Context, Media) (map[string]any, error) {
	return nil, nil
}
func (e *siteEnv) Content(context.Context, ContentQuery) (any, error) { return nil, nil }
func (e *siteEnv) Audit(context.Context, Actor, string, int64, string, map[string]any) {
}
func (e *siteEnv) Deliver(_ context.Context, event string, _ any) {
	e.delivered = append(e.delivered, event)
}
func (e *siteEnv) Seal(plain string) string { return plain }
func (e *siteEnv) Reveal(v string) string   { return v }

// SiteHosts grants the site's own origins as they are when it calls: one
// added later is reached, one that isn't the site's isn't.
func TestHTTPSiteHosts(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	env := &siteEnv{}
	api := &API{h: &Host{env: env}, grants: Grants{HTTP: []string{SiteHosts}}}
	get := func() error {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
		res, err := api.HTTP().Do(req)
		if err == nil {
			res.Body.Close()
		}
		return err
	}
	if err := get(); err == nil || !strings.Contains(err.Error(), "wasn't granted") {
		t.Fatalf("not the site's: %v", err)
	}
	env.origins = []string{srv.URL}
	if err := get(); err != nil {
		t.Fatalf("the site's: %v", err)
	}
}

// A plugin sends only the events its manifest declares.
func TestEmitDeclaredEvents(t *testing.T) {
	t.Parallel()
	env := &siteEnv{}
	api := &API{h: &Host{env: env}, events: []string{"thing.done"}}
	if err := api.Emit(t.Context(), "page.published", nil); err == nil {
		t.Fatal("an undeclared event was sent")
	}
	if err := api.Emit(t.Context(), "thing.done", nil); err != nil || len(env.delivered) != 1 {
		t.Fatalf("declared: %v %v", err, env.delivered)
	}
}
