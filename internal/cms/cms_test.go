package cms

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/fields"
)

// site is a running install for a test: the app, its server, and tokens
// for an administrator, an agent and the production site.
type site struct {
	t      *testing.T
	app    *App
	server *Server
	srv    *httptest.Server
	admin  string
	agent  string
	prod   string
}

func newSite(t *testing.T) *site {
	t.Helper()
	return newSiteWith(t, Config{NoDefaultPlugins: true})
}

// newSiteWith is newSite with some of the config set (the mailer, the
// default plugins). A BaseURL given is kept; otherwise it's the server's.
func newSiteWith(t *testing.T, cfg Config) *site {
	t.Helper()
	base := cfg.BaseURL
	cfg.DataDir, cfg.SiteKey, cfg.DeliveryRateLimit = t.TempDir(), "acme", -1
	if base == "" {
		cfg.BaseURL = "http://cms.test"
	}
	if cfg.Logger == nil {
		// Shown with the test's own output: on failure, or with -v.
		cfg.Logger = slog.New(slog.NewTextHandler(t.Output(), nil))
	}
	server, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app := server.App()
	s := &site{t: t, app: app, server: server}
	s.srv = httptest.NewServer(server.Handler())
	if base == "" {
		app.cfg.BaseURL, server.cfg.BaseURL = s.srv.URL, s.srv.URL
	}
	t.Cleanup(func() {
		s.srv.Close()
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := t.Context()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := app.CreateUserWithRole(ctx, "Alice", "alice@site.test", "password1234", "Administrator")
	if err != nil {
		t.Fatal(err)
	}
	s.admin, _, _ = app.mintUserToken(ctx, id)
	if s.agent, err = app.CreateServiceTokenForRole(ctx, "AGENT", "Agent"); err != nil {
		t.Fatal(err)
	}
	if s.prod, err = app.CreateServiceTokenForRole(ctx, "PRODUCTION", "Production site"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SeedBlockTypes(ctx, nil); err != nil {
		t.Fatal(err)
	}
	return s
}

type resp struct {
	Status int
	Header http.Header
	Body   map[string]any
	Raw    []byte
}

func (s *site) do(token, method, path string, body any, headers ...string) resp {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		if b, ok := body.([]byte); ok {
			r = bytes.NewReader(b)
		} else {
			j, err := json.Marshal(body)
			if err != nil {
				s.t.Fatal(err)
			}
			r = bytes.NewReader(j)
		}
	}
	req, err := http.NewRequestWithContext(s.t.Context(), method, s.srv.URL+path, r)
	if err != nil {
		s.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	out := resp{Status: res.StatusCode, Header: res.Header, Raw: raw}
	_ = json.Unmarshal(raw, &out.Body) // not every answer is JSON; Raw has it
	return out
}

func (s *site) must(want int, r resp) resp {
	s.t.Helper()
	if r.Status != want {
		s.t.Fatalf("status %d, want %d: %s", r.Status, want, r.Raw)
	}
	return r
}

// get is http.Get, bound to the test's context.
func get(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func keys(v any) []string {
	m, _ := v.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestPagesRoundTripAndErrors(t *testing.T) {
	t.Parallel()
	s := newSite(t)

	// The page shape the CLI reads.
	r := s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{
		"slug": "about", "title": "About us",
		"blocks": []any{map[string]any{"type": "hero", "data": map[string]any{"heading": "Hello"}}},
	}}))
	// lock_version: sent back on a save, it refuses to overwrite a change made since (docs/delivery-api.md).
	want := []string{"blocks", "category", "created_at", "depth", "frontmatter", "id", "locale", "lock_version", "parent_id", "path", "published_at", "seo", "slug", "status", "tags", "title", "updated_at"}
	if got := keys(r.Body["page"]); !slices.Equal(got, want) {
		t.Fatalf("page keys %v", got)
	}
	if dig(r.Body, "page", "status") != "draft" || dig(r.Body, "page", "blocks", 0, "id") == "" {
		t.Fatalf("a new page is a draft and its blocks get ids: %s", r.Raw)
	}

	// A child page's path is its parent's plus its slug.
	parentID := dig(r.Body, "page", "id")
	r = s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "team", "title": "Team", "parent_id": parentID}}))
	if dig(r.Body, "page", "path") != "about/team" {
		t.Fatalf("child path: %s", r.Raw)
	}
	s.must(200, s.do(s.admin, "GET", "/api/pages/about/team", nil))

	// Renaming the parent moves the child.
	s.must(200, s.do(s.admin, "PATCH", "/api/pages/about", map[string]any{"page": map[string]any{"slug": "company"}}))
	s.must(200, s.do(s.admin, "GET", "/api/pages/company/team", nil))

	// Every error is one shape.
	r = s.must(422, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "Bad Slug", "title": "",
		"blocks": []any{map[string]any{"type": "hero", "data": map[string]any{}}}}}))
	if r.Body["error"] != "invalid" || dig(r.Body, "errors", "slug") == nil || dig(r.Body, "errors", "title") == nil || dig(r.Body, "errors", "blocks[0].data.heading", 0) != "is required" {
		t.Fatalf("422 shape: %s", r.Raw)
	}
	r = s.must(400, s.do(s.admin, "POST", "/api/pages", []byte("{not json")))
	if r.Body["error"] != "bad_request" {
		t.Fatalf("400 shape: %s", r.Raw)
	}
	r = s.must(404, s.do(s.admin, "GET", "/api/nothing/here", nil))
	if r.Body["error"] != "not_found" {
		t.Fatalf("404 shape: %s", r.Raw)
	}
	r = s.must(401, s.do("lp_wrong", "GET", "/api/pages", nil))
	if r.Body["error"] != "unauthorized" {
		t.Fatalf("401 shape: %s", r.Raw)
	}

	// The list keeps /api's paging shape.
	r = s.must(200, s.do(s.admin, "GET", "/api/pages?per=1", nil))
	if dig(r.Body, "total") != float64(2) || dig(r.Body, "per") != float64(1) || len(dig(r.Body, "pages").([]any)) != 1 {
		t.Fatalf("list: %s", r.Raw)
	}

	// Trash takes the children; restoring brings them back.
	s.must(204, s.do(s.admin, "DELETE", "/api/pages/company", nil))
	s.must(404, s.do(s.admin, "GET", "/api/pages/company/team", nil))
	r = s.must(200, s.do(s.admin, "GET", "/api/trash", nil))
	if len(r.Body["entries"].([]any)) != 2 {
		t.Fatalf("trash: %s", r.Raw)
	}
	s.must(200, s.do(s.admin, "POST", "/api/trash/page/"+jsonID(parentID)+"/restore", nil))
	s.must(200, s.do(s.admin, "GET", "/api/pages/company/team", nil))
}

func jsonID(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestAgentEnvelope(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "home", "title": "Home"}}))
	r := s.must(200, s.do(s.admin, "GET", "/api/pages", nil, "X-Agent-Envelope", "1"))
	if r.Body["status"] != "ok" || r.Body["summary"] != "1 page." || dig(r.Body, "data", "pages", 0, "path") != "home" {
		t.Fatalf("envelope: %s", r.Raw)
	}
	if dig(r.Body, "breadcrumbs", 0, "command") != "librepublish get_page --path home" || dig(r.Body, "breadcrumbs", 0, "tool") != "get_page" ||
		dig(r.Body, "breadcrumbs", 0, "arguments", "path") != "home" {
		t.Fatalf("breadcrumbs name a real page: %s", r.Raw)
	}
	r = s.must(404, s.do(s.admin, "GET", "/api/pages/nope?envelope=1", nil))
	if r.Body["status"] != "error" || dig(r.Body, "data", "error") != "not_found" {
		t.Fatalf("error envelope: %s", r.Raw)
	}
}

func TestPublishGateAndRevisions(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	page := map[string]any{"page": map[string]any{"slug": "pricing", "title": "Pricing"}}

	// The agent writes drafts...
	s.must(201, s.do(s.agent, "POST", "/api/pages", page))
	s.must(200, s.do(s.agent, "PATCH", "/api/pages/pricing", map[string]any{"page": map[string]any{"title": "Plans"}}))
	// ...but can't publish one.
	r := s.must(403, s.do(s.agent, "PATCH", "/api/pages/pricing", map[string]any{"page": map[string]any{"status": "published"}}))
	if r.Body["capability"] != "pages:publish" {
		t.Fatalf("403 names the capability: %s", r.Raw)
	}

	// A person publishes it; the agent's next edit is held for review.
	s.must(200, s.do(s.admin, "PATCH", "/api/pages/pricing", map[string]any{"page": map[string]any{"status": "published"}}))
	r = s.must(202, s.do(s.agent, "PATCH", "/api/pages/pricing", map[string]any{"page": map[string]any{"title": "Plans & pricing", "status": "draft"}}))
	if r.Body["status"] != "pending_review" || !slices.Equal(toStrings(dig(r.Body, "revision", "fields")), []string{"title"}) ||
		!slices.Equal(toStrings(r.Body["ignored_fields"]), []string{"status"}) || !strings.Contains(str(r.Body["review_url"]), "/revisions/") {
		t.Fatalf("202: %s", r.Raw)
	}
	revID := jsonID(dig(r.Body, "revision", "id"))
	// The live page is unchanged; a second proposal is a 409.
	if got := dig(s.must(200, s.do(s.admin, "GET", "/api/pages/pricing", nil)).Body, "page", "title"); got != "Plans" {
		t.Fatalf("live title changed to %v", got)
	}
	s.must(409, s.do(s.agent, "PATCH", "/api/pages/pricing", map[string]any{"page": map[string]any{"title": "Again"}}))
	// A write that changes nothing says so.
	r = s.must(200, s.do(s.admin, "PATCH", "/api/pages/pricing", map[string]any{"page": map[string]any{"title": "Plans"}}))
	if r.Body["status"] != "unchanged" {
		t.Fatalf("unchanged: %s", r.Raw)
	}

	// The agent can't approve its own revision; a person can.
	s.must(403, s.do(s.agent, "POST", "/api/revisions/"+revID+"/approve", nil))
	s.must(200, s.do(s.admin, "POST", "/api/revisions/"+revID+"/approve", nil))
	page2 := s.must(200, s.do(s.admin, "GET", "/api/pages/pricing", nil))
	if dig(page2.Body, "page", "title") != "Plans & pricing" || dig(page2.Body, "page", "status") != "published" {
		t.Fatalf("approved revision applied, status untouched: %s", page2.Raw)
	}

	// Globals have no draft: every change from the agent is a revision.
	s.must(201, s.do(s.admin, "POST", "/api/globals", map[string]any{"global": map[string]any{"slug": "nav", "name": "Navigation", "data": map[string]any{"items": []any{}}}}))
	s.must(202, s.do(s.agent, "PATCH", "/api/globals/nav", map[string]any{"global": map[string]any{"name": "Nav"}}))
}

func toStrings(v any) []string {
	var out []string
	for _, x := range asList(v) {
		out = append(out, str(x))
	}
	return out
}

func TestCollectionsEntriesAndDelivery(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.must(201, s.do(s.admin, "POST", "/api/collections", map[string]any{"collection": map[string]any{
		"slug": "posts", "name": "Posts",
		"schema": map[string]any{"fields": []any{
			map[string]any{"name": "summary", "type": "text", "required": true},
			map[string]any{"name": "featured", "type": "boolean"},
		}},
	}}))
	r := s.must(422, s.do(s.admin, "POST", "/api/collections/posts/entries", map[string]any{"entry": map[string]any{"slug": "hello", "title": "Hello"}}))
	if dig(r.Body, "errors", "frontmatter.summary", 0) != "is required" {
		t.Fatalf("frontmatter validated against the schema: %s", r.Raw)
	}
	// HTML is refused in plain text and in Markdown, the body's included.
	r = s.must(422, s.do(s.admin, "POST", "/api/collections/posts/entries", map[string]any{"entry": map[string]any{"slug": "hello", "title": "Hello",
		"frontmatter": map[string]any{"summary": "<p>Hi</p>"}, "body_markdown": "Built in <strong>1869</strong>"}}))
	if dig(r.Body, "errors", "frontmatter.summary", 0) != fields.PlainTextHTMLMessage || dig(r.Body, "errors", "body_markdown", 0) != fields.MarkdownHTMLMessage {
		t.Fatalf("HTML in text fields: %s", r.Raw)
	}
	s.must(201, s.do(s.admin, "POST", "/api/collections/posts/entries", map[string]any{"entry": map[string]any{
		"slug": "hello", "title": "Hello", "status": "published", "frontmatter": map[string]any{"summary": "Hi"}, "body_markdown": "# Hello",
	}}))
	s.must(201, s.do(s.admin, "POST", "/api/collections/posts/entries", map[string]any{"entry": map[string]any{
		"slug": "draft-one", "title": "Draft", "frontmatter": map[string]any{"summary": "WIP"},
	}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/collections/posts/entries/hello/toggle_field", map[string]any{"field": "featured"}))

	// A page listing them, live.
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "blog", "title": "Blog", "status": "published",
		"blocks": []any{map[string]any{"type": "collection_list", "data": map[string]any{"collection_slug": "posts", "filter_status": "any", "sort_by": "title", "sort_dir": "asc", "layout": "grid"}}}}}))

	// The production site reads only what's live.
	s.must(404, s.do(s.prod, "GET", "/api/collections/posts/entries/draft-one", nil))
	r = s.must(200, s.do(s.prod, "GET", "/api/collections/posts/entries", nil))
	if dig(r.Body, "total") != float64(1) {
		t.Fatalf("site token sees drafts: %s", r.Raw)
	}
	s.must(403, s.do(s.prod, "PATCH", "/api/pages/blog", map[string]any{"page": map[string]any{"title": "x"}}))

	// /api/v1: one shape, live only, blocks expanded.
	r = s.must(200, s.do(s.prod, "GET", "/api/v1/pages/blog", nil))
	list := dig(r.Body, "data", "blocks", 0, "resolved", "entries").([]any)
	if len(list) != 1 || dig(list[0], "slug") != "hello" || dig(list[0], "url") != "/posts/hello" {
		t.Fatalf("collection_list resolves live entries only, even set to any: %s", r.Raw)
	}
	if !strings.Contains(r.Header.Get("Cache-Tag"), "collection:posts") || r.Header.Get("ETag") == "" {
		t.Fatalf("headers: %v", r.Header)
	}
	s.must(304, s.do(s.prod, "GET", "/api/v1/pages/blog", nil, "If-None-Match", r.Header.Get("ETag")))

	r = s.must(200, s.do(s.prod, "GET", "/api/v1/collections/posts/entries", nil))
	if dig(r.Body, "meta", "total") != float64(1) || dig(r.Body, "meta", "next_page") != nil {
		t.Fatalf("v1 paging: %s", r.Raw)
	}

	// /content, then only what changed.
	r = s.must(200, s.do(s.prod, "GET", "/api/v1/content", nil))
	cursor := str(dig(r.Body, "meta", "cursor"))
	if dig(r.Body, "meta", "full") != true || len(dig(r.Body, "data", "entries").([]any)) != 1 {
		t.Fatalf("content: %s", r.Raw)
	}
	s.must(200, s.do(s.admin, "PATCH", "/api/collections/posts/entries/hello", map[string]any{"entry": map[string]any{"status": "draft"}}))
	r = s.must(200, s.do(s.prod, "GET", "/api/v1/content?since="+cursor, nil))
	if dig(r.Body, "meta", "full") != false || len(dig(r.Body, "data", "removed", "entries").([]any)) != 1 {
		t.Fatalf("content since: %s", r.Raw)
	}

	// Serving: website + API serves only what's chosen.
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/general", map[string]any{"setting": map[string]any{"data": map[string]any{"serving": "website_api"}}}))
	r = s.must(404, s.do(s.prod, "GET", "/api/v1/pages", nil))
	if !strings.Contains(str(r.Body["message"]), "How the site is served") {
		t.Fatalf("not served says why: %s", r.Raw)
	}
	s.must(404, s.do(s.prod, "GET", "/api/v1/collections/posts", nil))
	s.must(200, s.do(s.admin, "PATCH", "/api/collections/posts", map[string]any{"collection": map[string]any{"api_enabled": true}}))
	s.must(200, s.do(s.prod, "GET", "/api/v1/collections/posts", nil))
}

func TestWebhooksKeepTheContract(t *testing.T) {
	t.Parallel()
	contract := readContract(t)
	s := newSite(t)

	var mu sync.Mutex
	var got []map[string]any
	var bodies [][]byte
	var sigs []string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		mu.Lock()
		got, bodies, sigs = append(got, m), append(bodies, b), append(sigs, r.Header.Get("X-CMS-Signature"))
		mu.Unlock()
	}))
	defer recv.Close()

	r := s.must(201, s.do(s.admin, "POST", "/api/webhooks", map[string]any{"webhook": map[string]any{"name": "ads", "url": recv.URL, "events": []any{"page.published", "entry.published"}}}))
	secret := str(dig(r.Body, "webhook", "secret"))

	s.must(201, s.do(s.admin, "POST", "/api/settings", map[string]any{"setting": map[string]any{"key": "x", "data": map[string]any{}}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/general", map[string]any{"setting": map[string]any{"data": map[string]any{"site_base_url": "https://acme.test"}}}))
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "about", "title": "About", "status": "published"}}))
	s.must(201, s.do(s.admin, "POST", "/api/collections", map[string]any{"collection": map[string]any{"slug": "posts", "name": "Posts"}}))
	s.must(201, s.do(s.admin, "POST", "/api/collections/posts/entries", map[string]any{"entry": map[string]any{"slug": "hi", "title": "Hi", "status": "published"}}))
	s.app.hooks.Wait()

	if len(got) != 2 {
		t.Fatalf("deliveries: %d", len(got))
	}
	envelope := toStrings(dig(contract, "webhook", "envelope_keys"))
	slices.Sort(envelope)
	for i, d := range got {
		if k := keys(d); !slices.Equal(k, envelope) {
			t.Fatalf("envelope keys %v, contract %v", k, envelope)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(bodies[i])
		if sigs[i] != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Fatalf("signature doesn't verify")
		}
		if d["tenant"] != "acme" {
			t.Fatalf("tenant: %v", d["tenant"])
		}
		kind, _, _ := strings.Cut(str(d["event"]), ".")
		want := toStrings(dig(contract, "webhook", kind+"_data_keys"))
		slices.Sort(want)
		if k := keys(d["data"]); !slices.Equal(k, want) {
			t.Fatalf("%s data keys %v, contract %v", kind, k, want)
		}
	}
	byEvent := map[string]map[string]any{}
	for _, d := range got {
		byEvent[str(d["event"])] = d
	}
	if dig(byEvent["page.published"], "data", "url") != "https://acme.test/about" || dig(byEvent["entry.published"], "data", "url") != "https://acme.test/posts/hi" {
		t.Fatalf("urls: %v", byEvent)
	}
}

func readContract(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "webhook_contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAssetsAndReferences(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	img := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	for x := range 2000 {
		img.Set(x, x%1000, color.RGBA{200, 10, 10, 255})
	}
	var png1 bytes.Buffer
	png.Encode(&png1, img)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "hero.png")
	fw.Write(png1.Bytes())
	mw.WriteField("alt", "A red line")
	mw.Close()
	r := s.must(201, s.do(s.admin, "POST", "/api/assets", body.Bytes(), "Content-Type", mw.FormDataContentType()))
	asset := r.Body["asset"]
	if dig(asset, "width") != float64(2000) || dig(asset, "alt") != "A red line" || dig(asset, "variants", "w640") == nil {
		t.Fatalf("asset: %s", r.Raw)
	}
	id := jsonID(dig(asset, "id"))

	// The file and a variant serve.
	res, err := get(t, str(dig(asset, "variants", "w640")))
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("variant: %v %v", err, res)
	}
	cfg, _, err := image.DecodeConfig(res.Body)
	res.Body.Close()
	if err != nil || cfg.Width != 640 {
		t.Fatalf("variant width %d %v", cfg.Width, err)
	}

	// Unused until a page points at it; then the page lists it as used.
	r = s.must(200, s.do(s.admin, "GET", "/api/assets?unused=1", nil))
	if dig(r.Body, "pagination", "total") != float64(1) {
		t.Fatalf("unused: %s", r.Raw)
	}
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "home", "title": "Home", "status": "published",
		"blocks": []any{map[string]any{"type": "hero", "data": map[string]any{"heading": "Hi", "image_id": id}}}}}))
	r = s.must(200, s.do(s.admin, "GET", "/api/assets?unused=1", nil))
	if dig(r.Body, "pagination", "total") != float64(0) {
		t.Fatalf("still unused: %s", r.Raw)
	}
	r = s.must(200, s.do(s.admin, "GET", "/api/assets/"+id, nil))
	if dig(r.Body, "asset", "used_by", 0, "path") != "home" || dig(r.Body, "asset", "used_by", 0, "where") != "block 1" {
		t.Fatalf("used_by: %s", r.Raw)
	}
	// The delivery API includes it.
	r = s.must(200, s.do(s.prod, "GET", "/api/v1/pages/home", nil))
	if dig(r.Body, "included", "assets", id, "url") == nil {
		t.Fatalf("included assets: %s", r.Raw)
	}
}

func TestRedirectsAndTokens(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.must(201, s.do(s.admin, "POST", "/api/redirects", map[string]any{"redirect": map[string]any{"source_path": "/old", "destination_url": "/new"}}))
	s.must(201, s.do(s.admin, "POST", "/api/redirects", map[string]any{"redirect": map[string]any{"source_path": "/blog/*", "destination_url": "/posts/:splat", "status_code": 302}}))
	r := s.must(200, s.do(s.admin, "GET", "/api/redirects/resolve?path=/blog/a/b", nil))
	if r.Body["destination"] != "/posts/a/b" {
		t.Fatalf("splat: %s", r.Raw)
	}
	r = s.must(200, s.do(s.prod, "GET", "/api/v1/redirects.txt", nil))
	if string(r.Raw) != "/old /new 301\n/blog/* /posts/:splat 302\n" {
		t.Fatalf("_redirects: %q", r.Raw)
	}

	// whoami, and a service token: reveal, rotate, revoke.
	r = s.must(200, s.do(s.agent, "GET", "/api/api_tokens/me", nil))
	if dig(r.Body, "service", "role") != "Agent" || slices.Contains(toStrings(r.Body["capabilities"]), "pages:publish") {
		t.Fatalf("me: %s", r.Raw)
	}
	r = s.must(201, s.do(s.admin, "POST", "/api/service_tokens", map[string]any{"name": "BUILD", "role": "Production site"}))
	tid, plain := jsonID(dig(r.Body, "service_token", "id")), str(dig(r.Body, "service_token", "token"))
	if !strings.HasPrefix(plain, "lps_") {
		t.Fatalf("token: %s", r.Raw)
	}
	if got := str(dig(s.must(200, s.do(s.admin, "POST", "/api/service_tokens/"+tid+"/reveal", nil)).Body, "service_token", "token")); got != plain {
		t.Fatalf("reveal gave %q", got)
	}
	rot := str(dig(s.must(200, s.do(s.admin, "POST", "/api/service_tokens/"+tid+"/rotate", nil)).Body, "service_token", "token"))
	s.must(401, s.do(plain, "GET", "/api/v1/site", nil))
	s.must(200, s.do(rot, "GET", "/api/v1/site", nil))
	s.must(200, s.do(s.admin, "POST", "/api/service_tokens/"+tid+"/revoke", nil))
	s.must(401, s.do(rot, "GET", "/api/v1/site", nil))
}

func TestDeviceLogin(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	r := s.must(200, s.do("", "POST", "/api/device/code", map[string]any{"hostname": "laptop"}))
	device, code := str(r.Body["device_code"]), str(r.Body["user_code"])
	r = s.must(200, s.do("", "POST", "/api/device/token", map[string]any{"device_code": device}))
	if r.Body["status"] != "pending" {
		t.Fatalf("pending: %s", r.Raw)
	}
	s.must(200, s.do(s.admin, "POST", "/api/device/approve", map[string]any{"user_code": code}))
	r = s.must(200, s.do("", "POST", "/api/device/token", map[string]any{"device_code": device}))
	if r.Body["status"] != "ok" || r.Body["token"] != s.admin {
		t.Fatalf("token: %s", r.Raw)
	}
	s.must(400, s.do("", "POST", "/api/device/token", map[string]any{"device_code": device}))
}

func TestAdminSessionNeedsCSRF(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	r := s.must(201, s.do("", "POST", "/api/session", map[string]any{"email": "alice@site.test", "password": "password1234"}))
	csrf := str(r.Body["csrf"])
	var cookie string
	for _, c := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(c, sessionCookie+"=") {
			cookie = strings.Split(c, ";")[0]
		}
	}
	s.must(200, s.do("", "GET", "/api/pages", nil, "Cookie", cookie))
	r = s.must(422, s.do("", "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "x", "title": "X"}}, "Cookie", cookie))
	if r.Body["error"] != "invalid_authenticity_token" {
		t.Fatalf("csrf: %s", r.Raw)
	}
	s.must(201, s.do("", "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "x", "title": "X"}}, "Cookie", cookie, "X-CSRF-Token", csrf))
	s.must(401, s.do("", "POST", "/api/session", map[string]any{"email": "alice@site.test", "password": "wrong"}))
}

func TestScheduledPublishing(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "launch", "title": "Launch", "publish_at": "2020-01-01T00:00:00Z"}}))
	s.app.runSchedule(t.Context())
	r := s.must(200, s.do(s.admin, "GET", "/api/pages/launch", nil))
	if dig(r.Body, "page", "status") != "published" {
		t.Fatalf("scheduled: %s", r.Raw)
	}
}

// Content stored before HTML was refused (or imported) still saves: the rule
// is for text a save changes, and status changes and scheduled publishing
// never trip over what they don't touch.
func TestLegacyHTMLDoesntBlockSaves(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	ctx := t.Context()
	s.must(201, s.do(s.admin, "POST", "/api/collections", map[string]any{"collection": map[string]any{
		"slug": "posts", "name": "Posts",
		"schema": map[string]any{"fields": []any{map[string]any{"name": "summary", "type": "text"}}},
	}}))
	for _, slug := range []string{"old", "later"} {
		s.must(201, s.do(s.admin, "POST", "/api/collections/posts/entries", map[string]any{"entry": map[string]any{
			"slug": slug, "title": "Old", "frontmatter": map[string]any{"summary": "Hi"}, "body_markdown": "Hi",
		}}))
	}
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "about", "title": "About",
		"blocks": []any{map[string]any{"type": "text", "data": map[string]any{"body": "Hi"}}}}}))
	// As an import leaves them: HTML everywhere, one due to be published.
	for _, q := range []string{
		`UPDATE collection_entries SET frontmatter = '{"summary":"<p>Built in <strong>1869</strong></p>"}', body_markdown = 'A <em>mill</em>'`,
		`UPDATE collection_entries SET publish_at = '2020-01-01 00:00:00' WHERE slug = 'later'`,
		`UPDATE pages SET blocks = '[{"id":"b1","type":"text","data":{"body":"<div>Old</div>"}},{"id":"b2","type":"text","data":{"body":"Two"}}]' WHERE slug = 'about'`,
	} {
		if _, err := s.app.store.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// A new title, a status change, and the scheduler: all fine.
	s.must(200, s.do(s.admin, "PATCH", "/api/collections/posts/entries/old", map[string]any{"entry": map[string]any{"title": "Older"}}))
	// A field required since: a status change and the scheduler don't
	// answer for content they leave as it was.
	s.must(200, s.do(s.admin, "PATCH", "/api/collections/posts", map[string]any{"collection": map[string]any{
		"schema": map[string]any{"fields": []any{map[string]any{"name": "summary", "type": "text"}, map[string]any{"name": "author", "type": "string", "required": true}}},
	}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/collections/posts/entries/old", map[string]any{"entry": map[string]any{"status": "published"}}))
	s.app.runSchedule(ctx)
	if r := s.must(200, s.do(s.admin, "GET", "/api/collections/posts/entries/later", nil)); dig(r.Body, "entry", "status") != "published" {
		t.Fatalf("scheduled publish of legacy HTML: %s", r.Raw)
	}
	// The page's blocks, reordered with their HTML as it was, save too.
	s.must(200, s.do(s.admin, "PATCH", "/api/pages/about", map[string]any{"page": map[string]any{"title": "About us", "blocks": []any{
		map[string]any{"id": "b2", "type": "text", "data": map[string]any{"body": "Two"}},
		map[string]any{"id": "b1", "type": "text", "data": map[string]any{"body": "<div>Old</div>"}},
	}}}))

	// Writing new HTML into those fields is still refused...
	r := s.must(422, s.do(s.admin, "PATCH", "/api/collections/posts/entries/old", map[string]any{"entry": map[string]any{
		"frontmatter": map[string]any{"summary": "<p>Built in 1870</p>"}, "body_markdown": "A <b>mill</b>",
	}}))
	if dig(r.Body, "errors", "frontmatter.summary", 0) != fields.PlainTextHTMLMessage || dig(r.Body, "errors", "body_markdown", 0) != fields.MarkdownHTMLMessage {
		t.Fatalf("new HTML: %s", r.Raw)
	}
	r = s.must(422, s.do(s.admin, "PATCH", "/api/pages/about", map[string]any{"page": map[string]any{"blocks": []any{
		map[string]any{"id": "b1", "type": "text", "data": map[string]any{"body": "<div>New</div>"}},
	}}}))
	if dig(r.Body, "errors", "blocks[0].data.body", 0) != fields.MarkdownHTMLMessage {
		t.Fatalf("new HTML in a block: %s", r.Raw)
	}
	// ...and cleaning it up to Markdown is accepted.
	s.must(200, s.do(s.admin, "PATCH", "/api/collections/posts/entries/old", map[string]any{"entry": map[string]any{
		"frontmatter": map[string]any{"summary": "Built in 1869", "author": "Ann"}, "body_markdown": "A *mill*",
	}}))
}

// Optimistic locking through the management API: a read gives lock_version,
// a save that sends back a stale one is a 409, the current one saves.
func TestLockVersion(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "about", "title": "About"}}))
	s.must(201, s.do(s.admin, "POST", "/api/collections", map[string]any{"collection": map[string]any{"slug": "posts", "name": "Posts"}}))
	s.must(201, s.do(s.admin, "POST", "/api/collections/posts/entries", map[string]any{"entry": map[string]any{"slug": "hi", "title": "Hi"}}))
	s.must(201, s.do(s.admin, "POST", "/api/globals", map[string]any{"global": map[string]any{"slug": "nav", "name": "Nav", "data": map[string]any{}}}))

	for _, c := range []struct{ path, wrap, field string }{
		{"/api/pages/about", "page", "title"},
		{"/api/collections/posts/entries/hi", "entry", "title"},
		{"/api/globals/nav", "global", "name"},
	} {
		read := s.must(200, s.do(s.admin, "GET", c.path, nil))
		lv, ok := dig(read.Body, c.wrap, "lock_version").(float64)
		if !ok {
			t.Fatalf("%s: no lock_version: %s", c.path, read.Raw)
		}
		// Someone else saves in between.
		saved := s.must(200, s.do(s.admin, "PATCH", c.path, map[string]any{c.wrap: map[string]any{c.field: "Theirs", "lock_version": lv}}))
		if dig(saved.Body, c.wrap, "lock_version") != lv+1 {
			t.Fatalf("%s: the save's answer has the new lock_version: %s", c.path, saved.Raw)
		}
		r := s.must(409, s.do(s.admin, "PATCH", c.path, map[string]any{c.wrap: map[string]any{c.field: "Mine", "lock_version": lv}}))
		if r.Body["error"] != "conflict" || !strings.Contains(str(r.Body["message"]), fmt.Sprintf("lock_version %d, now %d", int(lv), int(lv)+1)) {
			t.Fatalf("%s: stale: %s", c.path, r.Raw)
		}
		if got := s.must(200, s.do(s.admin, "GET", c.path, nil)); dig(got.Body, c.wrap, c.field) != "Theirs" {
			t.Fatalf("%s: the stale save changed nothing: %s", c.path, got.Raw)
		}
		s.must(200, s.do(s.admin, "PATCH", c.path, map[string]any{c.wrap: map[string]any{c.field: "Mine", "lock_version": lv + 1}}))
	}
}

type rowsAffected int64

func (n rowsAffected) LastInsertId() (int64, error) { return 0, nil }
func (n rowsAffected) RowsAffected() (int64, error) { return int64(n), nil }

// A save racing another between its read and its write changes no row
// (UPDATE … AND lock_version = <read>): that's a conflict, not a success.
func TestWroteOver(t *testing.T) {
	t.Parallel()
	if err := wroteOver(rowsAffected(1), nil, 3); err != nil {
		t.Fatal(err)
	}
	if e, ok := errors.AsType[*apierr.Error](wroteOver(rowsAffected(0), nil, 3)); !ok || e.Status != 409 {
		t.Fatalf("no row changed: %v", e)
	}
}
