package cms

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/visits"
)

// The contract: what a site (librepublish-astro) is built against, recorded
// from a real install into contracts/: each answer's body in
// contracts/<group>/<name>.json, and what was asked and the headers that
// matter in contracts/index.json. Run with -update to rewrite them after a
// deliberate change; without it, any answer that differs from its file
// fails. See contracts/README.md.

var updateContracts = flag.Bool("update", false, "rewrite contracts/ from what the CMS answers")

// contractBase is the CMS's address in the recordings, whatever port the
// test server has.
const contractBase = "https://cms.acme.example"

// exchange is one recorded request and its answer.
type exchange struct {
	Request map[string]any    `json:"request"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"-"`
}

// The headers a recording keeps: the rest (ETag, dates, ids) change from
// run to run or say nothing about the contract.
var contractHeaders = []string{"Content-Type", "Cache-Tag", "Cache-Control", "Location", schemaDigestHeader}

func TestContract(t *testing.T) {
	t.Parallel()
	// The example plugin is in this build, not installed: the plugins API
	// shows an available one, and installing it.
	s := newSiteWith(t, Config{BaseURL: contractBase, Mailer: &captureMailer{}, SchemaDebounce: 20 * time.Millisecond, Plugins: []*plugin.Module{visits.Module()}})
	hooks := recordWebhooks(t, s)
	seedContract(t, s)

	files := map[string]exchange{}
	get := func(name, path string) resp {
		t.Helper()
		r := s.must(200, s.do(s.prod, "GET", path, nil))
		files[name] = record("GET", path, nil, r)
		return r
	}

	schema := get("delivery/schema.json", "/api/v1/schema")
	digest := str(dig(schema.Body, "meta", "schema_digest"))
	if !strings.HasPrefix(digest, "sha256:") || schema.Header.Get(schemaDigestHeader) != digest {
		t.Fatalf("schema digest %q, header %q", digest, schema.Header.Get(schemaDigestHeader))
	}
	// A site checks the digest the way this does: canonical JSON of data.
	if got := digestOf(mustCanonical(t, schema.Body["data"])); got != digest {
		t.Fatalf("the digest isn't of data: %s, recomputed %s", digest, got)
	}
	if dig(schema.Body, "data", "blocks", "pattern") != nil || dig(schema.Body, "meta", "cms_version") != "dev" {
		t.Fatalf("schema: pattern %v, cms_version %v", dig(schema.Body, "data", "blocks", "pattern"), dig(schema.Body, "meta", "cms_version"))
	}
	site := get("delivery/site.json", "/api/v1/site")
	if w := dig(site.Body, "data", "image_widths"); fmt.Sprint(w) != fmt.Sprint([]any{640.0, 1280.0, 1920.0}) || dig(site.Body, "meta", "cms_version") != "dev" {
		t.Fatalf("site: %s", site.Raw)
	}
	content := get("delivery/content-full.json", "/api/v1/content")
	get("delivery/pages.json", "/api/v1/pages")
	get("delivery/page-home.json", "/api/v1/pages/home")
	about := get("delivery/page-about.json", "/api/v1/pages/about")
	get("delivery/entries-posts.json", "/api/v1/collections/posts/entries")
	get("delivery/entries-projects.json", "/api/v1/collections/projects/entries")
	get("delivery/globals.json", "/api/v1/globals")
	get("delivery/global-nav.json", "/api/v1/globals/nav")
	get("delivery/sitemap.json", "/api/v1/sitemap")
	get("delivery/redirects.json", "/api/v1/redirects")
	get("delivery/robots.json", "/api/v1/robots")
	get("delivery/site-health.json", "/api/v1/site_health")

	// What's delivered is what the schema declares: patterns expanded, and
	// keys older data carries left out.
	for name, x := range files {
		raw, _ := json.Marshal(x.Body)
		for _, leftover := range []string{`"type":"pattern"`, "legacy_", `"canonical":`} {
			if strings.Contains(string(raw), leftover) {
				t.Fatalf("%s delivers %s: %s", name, leftover, raw)
			}
		}
	}
	if dig(about.Body, "data", "seo", "canonical_url") != "https://acme.example/about-us" || dig(about.Body, "data", "seo", "json_ld", "@type") != "Organization" {
		t.Fatalf("about's seo: %s", about.Raw)
	}
	if !strings.Contains(string(files["delivery/entries-projects.json"].mustJSON(t)), `"text":"Built with care."`) {
		t.Fatalf("an entry's pattern wasn't expanded: %s", files["delivery/entries-projects.json"].mustJSON(t))
	}

	// What changed since the full read: a page edited, an entry taken down.
	cursor := str(dig(content.Body, "meta", "cursor"))
	s.must(200, s.do(s.admin, "PATCH", "/api/pages/about", map[string]any{"page": map[string]any{"title": "About Acme"}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/collections/posts/entries/second-post", map[string]any{"entry": map[string]any{"status": "draft"}}))
	delta := get("delivery/content-delta.json", "/api/v1/content?since="+cursor)
	if dig(delta.Body, "meta", "full") != false || dig(delta.Body, "data", "removed", "entries", 0, "slug") != "second-post" {
		t.Fatalf("delta: %s", delta.Raw)
	}

	for name, x := range files {
		if x.Headers[schemaDigestHeader] != digest {
			t.Fatalf("%s: %s is %q, not the schema's", name, schemaDigestHeader, x.Headers[schemaDigestHeader])
		}
	}

	// Forms.
	list := s.must(200, s.do("", "GET", "/api/x/forms/list", nil))
	files["forms/form-list.json"] = record("GET", "/api/x/forms/list", nil, list)
	def := s.must(200, s.do("", "GET", "/api/x/forms/definition?form=contact", nil))
	files["forms/form-definition.json"] = record("GET", "/api/x/forms/definition?form=contact", nil, def)
	form, _ := def.Body["form"].(map[string]any)
	rest := maps.Clone(form)
	delete(rest, "digest")
	if got := digestOf(mustCanonical(t, rest)); got != form["digest"] || dig(list.Body, "forms", 0, "digest") != form["digest"] {
		t.Fatalf("form digest %v, recomputed %s, listed %v", form["digest"], got, dig(list.Body, "forms", 0, "digest"))
	}
	ts := strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)
	submit := func(name, slug string, body map[string]any, want int) {
		t.Helper()
		path := "/api/x/forms/submit?form=" + slug
		files[name] = record("POST", path, body, s.must(want, s.do("", "POST", path, body, "Accept", "application/json")))
	}
	submit("forms/form-submit-200.json", "contact", map[string]any{
		"name": "Ada Lovelace", "email": "ada@example.test", "phone": "+44 (20) 7946 0000", "message": "Hello there.", "guests": "4",
		"topic": "Sales", "contact_by": "Email", "interests": []any{"News", "Events"}, "consent": true, "date": "2026-11-01",
		"website": "https://ada.example", "_hp": "", "_ts": ts,
	}, 200)
	submit("forms/form-submit-422.json", "contact", map[string]any{
		"name": "", "email": "not-an-email", "phone": "12", "guests": "99", "topic": "Other", "contact_by": "Fax",
		"interests": []any{"Spam"}, "date": "2026-02-30", "website": "ftp://ada.example", "_hp": "", "_ts": ts,
	}, 422)
	submit("forms/form-submit-410.json", "newsletter", map[string]any{"email": "ada@example.test", "_hp": "", "_ts": ts}, 410)

	// Webhooks: one of each event, schema.changed once it names the schema
	// as it now is.
	maps.Copy(files, hooks.samples(t, digest))
	want := toStrings(dig(readContract(t), "webhook", "schema_data_keys"))
	if got := keys(dig(files["webhooks/schema.changed.json"].Body, "data")); !slices.Equal(got, want) {
		t.Fatalf("schema.changed data keys %v, contract %v", got, want)
	}

	maps.Copy(files, recordPlugins(t, s))

	index := map[string]exchange{}
	for name, x := range files {
		checkContract(t, name, x.Body)
		index[name] = x
	}
	checkContract(t, "index.json", index)
}

// recordPlugins records the plugins API, as an administrator calls it:
// the list (installed and available), one plugin, installing, granting and
// switching on, a plugin's admin screens and menus, and removing.
func recordPlugins(t *testing.T, s *site) map[string]exchange {
	t.Helper()
	files := map[string]exchange{}
	call := func(name, method, path string, body map[string]any, want int) resp {
		t.Helper()
		var b any
		if body != nil {
			b = body
		}
		r := s.must(want, s.do(s.admin, method, path, b))
		files[name] = record(method, path, body, r)
		return r
	}
	list := call("plugins/plugins.json", "GET", "/api/plugins", nil, 200)
	if dig(list.Body, "plugins", 0, "key") != "forms" || dig(list.Body, "available", 0, "key") != "visits" {
		t.Fatalf("plugins: %s", list.Raw)
	}
	call("plugins/plugin-forms.json", "GET", "/api/plugins/forms", nil, 200)
	call("plugins/plugin-not-found.json", "GET", "/api/plugins/nope", nil, 404)
	call("plugins/plugin-forms-admin.json", "GET", "/api/plugins/forms/admin", nil, 200)
	call("plugins/plugin-menus.json", "GET", "/api/plugin_menus", nil, 200)
	installed := call("plugins/plugin-install-201.json", "POST", "/api/plugins", map[string]any{"plugin": map[string]any{"key": "visits"}}, 201)
	if dig(installed.Body, "plugin", "enabled") != false {
		t.Fatalf("installed: %s", installed.Raw)
	}
	call("plugins/plugin-install-404.json", "POST", "/api/plugins", map[string]any{"plugin": map[string]any{"key": "nope"}}, 404)
	call("plugins/plugin-install-400.json", "POST", "/api/plugins", map[string]any{"plugin": map[string]any{}}, 400)
	// Granting more than it asks for grants what it asks for.
	on := call("plugins/plugin-configure.json", "PATCH", "/api/plugins/visits", map[string]any{"plugin": map[string]any{
		"granted": map[string]any{"content:read": true, "storage": true, "mail": true, "http": []any{"evil.example"}}, "enabled": true}}, 200)
	if dig(on.Body, "plugin", "enabled") != true || dig(on.Body, "plugin", "granted", "mail") != false {
		t.Fatalf("configured: %s", on.Raw)
	}
	call("plugins/plugin-remove-204.json", "DELETE", "/api/plugins/visits?purge_data=1", nil, 204)
	return files
}

func (x exchange) mustJSON(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(x.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// record is an exchange, normalized so it reads the same on every run.
func record(method, path string, body map[string]any, r resp) exchange {
	req := map[string]any{"method": method, "path": path}
	if body != nil {
		req["body"] = roundTrip(body)
	}
	x := exchange{Request: req, Status: r.Status, Headers: map[string]string{}}
	for _, h := range contractHeaders {
		if v := r.Header.Get(h); v != "" {
			x.Headers[h] = v
		}
	}
	if len(r.Raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(r.Raw))
		dec.UseNumber()
		if err := dec.Decode(&x.Body); err != nil {
			x.Body = string(r.Raw)
		}
	}
	normalizeContract(&x)
	return x
}

func roundTrip(v any) any {
	b, _ := json.Marshal(v)
	var out any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&out) // json.Marshal's own output
	return out
}

func mustCanonical(t *testing.T, v any) []byte {
	t.Helper()
	b, err := canonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---- normalizing ---------------------------------------------------------

// Keys whose values are when something happened: kept in their format,
// moved to one fixed moment.
var contractTimes = map[string]bool{"published_at": true, "updated_at": true, "created_at": true, "lastmod": true, "delivered_at": true, "submitted_at": true}

var (
	isoTime   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)
	blobInURL = regexp.MustCompile(`/files/[A-Za-z0-9]+/`)
)

func normalizeContract(x *exchange) {
	x.Request = normalizeValue(x.Request, "").(map[string]any)
	if p, ok := x.Request["path"].(string); ok {
		if before, _, ok := strings.Cut(p, "since="); ok {
			x.Request["path"] = before + "since=<cursor>"
		}
	}
	x.Body = normalizeValue(x.Body, "")
}

func normalizeValue(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeValue(e, k)
		}
	case []any:
		for i := range x {
			x[i] = normalizeValue(x[i], key)
		}
	case string:
		switch {
		case x == "":
		case contractTimes[key] && isoTime.MatchString(x):
			m := isoTime.FindStringSubmatch(x)
			return "2026-01-01T00:00:00" + strings.Map(func(r rune) rune {
				if r >= '1' && r <= '9' {
					return '0'
				}
				return r
			}, m[1]) + m[2]
		case key == "cursor" || key == "since":
			return "<cursor>"
		case key == "_ts":
			return "1767225600000"
		default:
			return blobInURL.ReplaceAllString(x, "/files/<blob>/")
		}
	}
	return v
}

// ---- the files -----------------------------------------------------------

// checkContract compares v with its file, or writes it with -update.
func checkContract(t *testing.T, name string, v any) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "contracts", filepath.FromSlash(name))
	if *updateContracts {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("contracts/%s: %v (run go test ./internal/cms -run TestContract -update)", name, err)
		return
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("contracts/%s no longer matches what the CMS answers: %s\nA deliberate change? Rewrite it with go test ./internal/cms -run TestContract -update, and update librepublish-astro to match.",
			name, firstDifference(string(want), buf.String()))
	}
}

func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range max(len(w), len(g)) {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Sprintf("line %d\n  file: %s\n  now:  %s", i+1, a, b)
		}
	}
	return "(whitespace)"
}

// ---- webhooks ------------------------------------------------------------

// hookRecorder keeps every delivery a webhook listening for everything got.
type hookRecorder struct {
	s    *site
	mu   sync.Mutex
	got  []map[string]any
	recv *httptest.Server
}

func recordWebhooks(t *testing.T, s *site) *hookRecorder {
	t.Helper()
	h := &hookRecorder{s: s}
	h.recv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if dec.Decode(&m) == nil {
			h.mu.Lock()
			h.got = append(h.got, m)
			h.mu.Unlock()
		}
	}))
	t.Cleanup(h.recv.Close)
	s.must(201, s.do(s.admin, "POST", "/api/webhooks", map[string]any{"webhook": map[string]any{"name": "everything", "url": h.recv.URL}}))
	return h
}

// samples waits for schema.changed to name digest, then gives one
// delivery of each event: the first of them in a stable order, since they
// arrive in any.
func (h *hookRecorder) samples(t *testing.T, digest string) map[string]exchange {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		h.mu.Lock()
		done := slices.ContainsFunc(h.got, func(m map[string]any) bool {
			return m["event"] == schemaChangedEvent && dig(m, "data", "schema_digest") == digest
		})
		h.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s naming %s", schemaChangedEvent, digest)
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.s.app.hooks.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	byEvent := map[string][]exchange{}
	for _, m := range h.got {
		if m["event"] == schemaChangedEvent && dig(m, "data", "schema_digest") != digest {
			continue // an intermediate one: when it went out depends on timing
		}
		x := exchange{Request: map[string]any{"method": "POST", "path": "<the webhook's URL>"}, Status: 0,
			Headers: map[string]string{"Content-Type": "application/json", "X-CMS-Event": str(m["event"]), "X-CMS-Signature": "sha256=<hex HMAC-SHA256 of the body>"},
			Body:    roundTrip(m)}
		normalizeContract(&x)
		byEvent[str(m["event"])] = append(byEvent[str(m["event"])], x)
	}
	out := map[string]exchange{}
	for event, xs := range byEvent {
		slices.SortFunc(xs, func(a, b exchange) int {
			ja, _ := json.Marshal(a.Body)
			jb, _ := json.Marshal(b.Body)
			return bytes.Compare(ja, jb)
		})
		out["webhooks/"+event+".json"] = xs[0]
	}
	return out
}

// ---- the site ------------------------------------------------------------

// seedContract makes a small site touching every kind of thing a site
// reads: block types (the starter pack), a collection with most field
// types and one with blocks, live and draft pages and entries with blocks,
// a synced pattern and an asset, a global, redirects, two forms (one
// closed) with every field type — and keys left over from older data that
// the delivery API mustn't pass on.
func seedContract(t *testing.T, s *site) {
	t.Helper()
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/general", map[string]any{"setting": map[string]any{"data": map[string]any{
		"title": "Acme", "site_base_url": "https://acme.example", "phone": "+1 555 0100", "email": "hello@acme.example", "city": "Springfield"}}}))

	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := range 40 {
		img.Set(x, x%20, color.RGBA{20, 120, 200, 255})
	}
	var pngData, body bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "hero.png")
	fw.Write(pngData.Bytes())
	mw.WriteField("alt", "A blue line")
	mw.Close()
	asset := jsonID(dig(s.must(201, s.do(s.admin, "POST", "/api/assets", body.Bytes(), "Content-Type", mw.FormDataContentType())).Body, "asset", "id"))
	// The visual identity, the logo and favicon the same file; top_bar is
	// the admin's and isn't delivered.
	s.must(200, s.do(s.admin, "PATCH", "/api/settings/branding", map[string]any{"setting": map[string]any{"data": map[string]any{
		"logo_id": asset, "favicon_id": asset, "primary_color": "#b45309", "secondary_color": "#1c1917",
		"font": "Playfair Display", "border_radius": "medium", "box_shadow": "small", "top_bar": "dark"}}}))

	s.must(201, s.do(s.admin, "POST", "/api/collections", map[string]any{"collection": map[string]any{
		"slug": "posts", "name": "Posts", "api_enabled": true, "enable_blocks": true,
		"schema": map[string]any{"fields": []any{
			map[string]any{"name": "summary", "label": "Summary", "type": "text", "required": true},
			map[string]any{"name": "rating", "label": "Rating", "type": "integer"},
			map[string]any{"name": "featured", "label": "Featured", "type": "boolean"},
			map[string]any{"name": "kind", "label": "Kind", "type": "select", "options": []any{"news", "guide"}},
			map[string]any{"name": "source", "label": "Source", "type": "url"},
			map[string]any{"name": "event_at", "label": "Event at", "type": "datetime"},
			map[string]any{"name": "cover", "label": "Cover", "type": "asset"},
			map[string]any{"name": "keywords", "label": "Keywords", "type": "string_list"},
			map[string]any{"name": "cta", "label": "Call to action", "type": "link"},
			map[string]any{"name": "author", "label": "Author", "type": "group", "of": []any{
				map[string]any{"name": "name", "label": "Name", "type": "string"},
				map[string]any{"name": "bio", "label": "Bio", "type": "markdown"},
			}},
			map[string]any{"name": "faqs", "label": "FAQs", "type": "repeater", "of": []any{
				map[string]any{"name": "question", "label": "Question", "type": "string", "required": true},
				map[string]any{"name": "answer", "label": "Answer", "type": "text"},
			}},
		}},
	}}))
	entry := func(slug, title, status string, fm map[string]any) {
		s.must(201, s.do(s.admin, "POST", "/api/collections/posts/entries", map[string]any{"entry": map[string]any{
			"slug": slug, "title": title, "status": status, "frontmatter": fm, "body_markdown": "# " + title + "\n\nWords.",
			"seo": map[string]any{"meta_description": title + ", in brief."}}}))
	}
	entry("first-post", "First post", "published", map[string]any{
		"summary": "The first one.", "rating": 5, "featured": true, "kind": "news", "source": "https://source.example/a",
		"event_at": "2026-03-04T05:06:07Z", "cover": asset, "keywords": []any{"launch", "news"},
		"cta":    map[string]any{"kind": "url", "value": "https://acme.example/signup"},
		"author": map[string]any{"name": "Ada", "bio": "Writes *things*."},
		"faqs":   []any{map[string]any{"question": "Why?", "answer": "Because."}},
	})
	entry("second-post", "Second post", "published", map[string]any{"summary": "The second one.", "kind": "guide"})
	entry("draft-post", "Draft post", "draft", map[string]any{"summary": "Not yet."})

	s.must(201, s.do(s.admin, "POST", "/api/collections", map[string]any{"collection": map[string]any{
		"slug": "projects", "name": "Projects", "api_enabled": true, "enable_blocks": true,
		"schema": map[string]any{"fields": []any{
			map[string]any{"name": "client", "label": "Client", "type": "string", "required": true},
			map[string]any{"name": "year", "label": "Year", "type": "integer"},
		}},
	}}))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")
	if _, err := s.app.store.ExecContext(t.Context(), `INSERT INTO block_patterns (slug, name, blocks, synced, created_at, updated_at) VALUES (?,?,?,1,?,?)`,
		"credits", "Credits", `[{"id":"p1","type":"quote","data":{"text":"Built with care.","attribution":"Acme","legacy_note":"x"}}]`, now, now); err != nil {
		t.Fatal(err)
	}
	s.must(201, s.do(s.admin, "POST", "/api/collections/projects/entries", map[string]any{"entry": map[string]any{
		"slug": "alpha", "title": "Alpha", "status": "published", "frontmatter": map[string]any{"client": "Initech", "year": 2025},
		"blocks": []any{
			map[string]any{"id": "b1", "type": "text", "data": map[string]any{"body": "The *brief*."}},
			map[string]any{"id": "b2", "type": "pattern", "data": map[string]any{"pattern": "credits"}},
		},
	}}))

	s.must(201, s.do(s.admin, "POST", "/api/globals", map[string]any{"global": map[string]any{
		"slug": "nav", "name": "Navigation",
		"schema": map[string]any{"fields": []any{
			map[string]any{"name": "tagline", "label": "Tagline", "type": "string", "required": true},
			map[string]any{"name": "links", "label": "Links", "type": "repeater", "of": []any{
				map[string]any{"name": "label", "label": "Label", "type": "string"},
				map[string]any{"name": "url", "label": "Link", "type": "link"},
			}},
		}},
		"data": map[string]any{"tagline": "Made by Acme.", "links": []any{map[string]any{"label": "About", "url": map[string]any{"kind": "page", "value": "about"}}}},
	}}))

	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{
		"slug": "home", "title": "Home", "status": "published",
		"seo": map[string]any{"meta_title": "Acme", "meta_description": "What Acme does."},
		"blocks": []any{
			map[string]any{"id": "b1", "type": "hero", "data": map[string]any{"heading": "Hello", "subheading": "From Acme.", "image_id": asset,
				"ctas": []any{map[string]any{"label": "Read the blog", "url": map[string]any{"kind": "url", "value": "/posts"}, "style": "primary"}}}},
			map[string]any{"id": "b2", "type": "collection_list", "data": map[string]any{"collection_slug": "posts", "heading": "Latest",
				"filter_status": "published", "sort_by": "title", "sort_dir": "asc", "layout": "list"}},
			map[string]any{"id": "b3", "type": "contact_info", "data": map[string]any{"heading": "Find us"}},
			map[string]any{"id": "b4", "type": "pattern", "data": map[string]any{"pattern": "credits"}},
		},
	}}))
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{
		"slug": "about", "title": "About", "status": "published",
		"seo":    map[string]any{"meta_description": "Who we are."},
		"blocks": []any{map[string]any{"id": "b1", "type": "text", "data": map[string]any{"body": "We make **things**."}}},
	}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/pages/about/schema", map[string]any{"schema": map[string]any{"fields": []any{
		map[string]any{"name": "team_size", "label": "Team size", "type": "integer"},
		map[string]any{"name": "founded", "label": "Founded", "type": "datetime"},
	}}}))
	s.must(200, s.do(s.admin, "PATCH", "/api/pages/about", map[string]any{"page": map[string]any{"frontmatter": map[string]any{"team_size": 12, "founded": "2020-01-01T00:00:00Z"}}}))
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "drafty", "title": "Not live"}}))

	// What a Rails import or a removed field leaves behind.
	for _, q := range []string{
		`UPDATE pages SET seo = json_set(seo, '$.legacy_twitter_title', 'x', '$.canonical', 'https://acme.example/about-us', '$.json_ld', json('{"@type":"Organization","name":"Acme"}')),
			frontmatter = json_set(frontmatter, '$.legacy_field', 'x') WHERE path = 'about'`,
		`UPDATE pages SET blocks = json_set(blocks, '$[0].data.legacy_flag', 1), frontmatter = json_set(frontmatter, '$.legacy_field', 'x') WHERE path = 'home'`,
		`UPDATE collection_entries SET frontmatter = json_set(frontmatter, '$.legacy_field', 'x', '$.author.legacy_handle', '@ada'), seo = json_set(seo, '$.legacy_key', 1)`,
		`UPDATE globals SET data = json_set(data, '$.legacy_key', 'x') WHERE slug = 'nav'`,
	} {
		if _, err := s.app.store.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}

	s.must(201, s.do(s.admin, "POST", "/api/redirects", map[string]any{"redirect": map[string]any{"source_path": "/old", "destination_url": "/about"}}))
	s.must(201, s.do(s.admin, "POST", "/api/redirects", map[string]any{"redirect": map[string]any{"source_path": "/blog/*", "destination_url": "/posts/:splat", "status_code": 302}}))

	s.must(200, s.do(s.admin, "POST", "/api/x/forms/form", map[string]any{"form": map[string]any{
		"name": "Contact", "slug": "contact", "status": "active", "store_submissions": true, "honeypot": true, "min_seconds": 3, "captcha": "none",
		"admin_enabled": false, "submit_label": "Send", "success_message": "Thanks!",
		"fields": []any{
			map[string]any{"type": "heading", "text": "Say hello"},
			map[string]any{"type": "text", "name": "name", "label": "Name", "required": true, "max_length": 80, "width": "half"},
			map[string]any{"type": "email", "name": "email", "label": "Email", "required": true, "width": "half"},
			map[string]any{"type": "tel", "name": "phone", "label": "Phone"},
			map[string]any{"type": "textarea", "name": "message", "label": "Message", "rows": 5},
			map[string]any{"type": "number", "name": "guests", "label": "Guests", "min": 1, "max": 12},
			map[string]any{"type": "select", "name": "topic", "label": "Topic", "options": []any{"Sales", "Support"}},
			map[string]any{"type": "radio", "name": "contact_by", "label": "Contact me by", "options": []any{"Email", "Phone"}},
			map[string]any{"type": "checkboxes", "name": "interests", "label": "Interests", "options": []any{"News", "Events"}},
			map[string]any{"type": "checkbox", "name": "consent", "label": "Consent", "text": "You may email me.", "required": true},
			map[string]any{"type": "date", "name": "date", "label": "Preferred date"},
			map[string]any{"type": "url", "name": "website", "label": "Website"},
			map[string]any{"type": "file", "name": "brief", "label": "Brief", "accept": ".pdf, image/*", "max_mb": 2},
			map[string]any{"type": "hidden", "name": "source", "default": "site"},
		},
	}}))
	s.must(200, s.do(s.admin, "POST", "/api/x/forms/form", map[string]any{"form": map[string]any{
		"name": "Newsletter", "slug": "newsletter", "status": "closed", "admin_enabled": false, "honeypot": true,
		"fields": []any{map[string]any{"type": "email", "name": "email", "label": "Email", "required": true}},
	}}))
}
