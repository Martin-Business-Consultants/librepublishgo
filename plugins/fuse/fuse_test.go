package fuse

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/fuse/engine"
)

// fakeHost is the plugin's host in memory: storage that keeps the 1 MB
// cap, sealing that's visibly not plaintext, and an HTTP client whose
// transport the test supplies.
type fakeHost struct {
	mu       sync.Mutex
	site     string
	kv       map[string]string
	logs     []string
	emitted  []string
	media    int
	rt       http.RoundTripper
	maxValue int
}

func newHost(t *testing.T) *fakeHost {
	return &fakeHost{site: "https://cms-" + strings.ReplaceAll(t.Name(), "/", "-") + ".test", kv: map[string]string{}}
}

func (h *fakeHost) Key() string { return "fuse" }
func (h *fakeHost) Log(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, msg)
}
func (h *fakeHost) Site(context.Context) (plugin.Site, error) { return plugin.Site{CMSURL: h.site}, nil }
func (h *fakeHost) Seal(p string) string {
	if p == "" || strings.HasPrefix(p, "sealed:") {
		return p
	}
	return "sealed:" + reverse(p)
}
func (h *fakeHost) Reveal(v string) string {
	if !strings.HasPrefix(v, "sealed:") {
		return v
	}
	return reverse(strings.TrimPrefix(v, "sealed:"))
}
func reverse(s string) string {
	r := []rune(s)
	slices.Reverse(r)
	return string(r)
}
func (h *fakeHost) HTTP() *http.Client {
	rt := h.rt
	if rt == nil {
		rt = roundTrip(func(r *http.Request) (*http.Response, error) { return nil, errors.New("no network in this test") })
	}
	return &http.Client{Transport: rt}
}
func (h *fakeHost) Emit(_ context.Context, event string, _ any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emitted = append(h.emitted, event)
	return nil
}
func (h *fakeHost) PutMedia(_ context.Context, f plugin.Media) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.media++
	return map[string]any{"id": h.media, "url": "https://cms.test/media/" + f.Filename}, nil
}
func (h *fakeHost) KVGet(_ context.Context, key string) (string, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.kv[key]
	return v, ok, nil
}
func (h *fakeHost) KVSet(_ context.Context, key, value string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(value) > 1<<20 {
		return errors.New("a value is at most 1 MB")
	}
	h.maxValue = max(h.maxValue, len(value))
	h.kv[key] = value
	return nil
}
func (h *fakeHost) KVAdd(_ context.Context, key string, d int64) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var n int64
	fmt.Sscan(h.kv[key], &n)
	n += d
	h.kv[key] = fmt.Sprint(n)
	return n, nil
}
func (h *fakeHost) KVDelete(_ context.Context, key string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.kv, key)
	return nil
}
func (h *fakeHost) KVList(_ context.Context, prefix, after string, limit int) ([]string, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var keys []string
	for k := range h.kv {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	if len(keys) > limit {
		return keys[:limit], keys[limit-1], nil
	}
	return keys, "", nil
}
func (h *fakeHost) KVAll(ctx context.Context, prefix string) ([]string, error) {
	var all []string
	after := ""
	for {
		keys, next, _ := h.KVList(ctx, prefix, after, 1000)
		all = append(all, keys...)
		if next == "" {
			return all, nil
		}
		after = next
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var admin = &plugin.Caller{Name: "Ted", Type: "User", Capabilities: []string{"fuse:read", "fuse:write", "fuse:publish", "fuse:secrets"}}

// do calls a handler as the host would.
func do(t *testing.T, h *fakeHost, fn func(*call) *plugin.Response, method, path string, body any, actor *plugin.Caller) (int, map[string]any) {
	t.Helper()
	u, _ := url.Parse(path)
	req := &plugin.Request{Method: method, Path: u.Path, Query: u.Query(), Headers: map[string]string{}, Actor: actor}
	if body != nil {
		req.Body, _ = json.Marshal(body)
	}
	res := fn(&call{ctx: t.Context(), h: h, req: request{req}})
	var out map[string]any
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("%s %s: %s", method, path, res.Body)
	}
	return res.Status, out
}

func must(t *testing.T, want int) func(int, map[string]any) map[string]any {
	return func(got int, body map[string]any) map[string]any {
		t.Helper()
		if got != want {
			b, _ := json.Marshal(body)
			t.Fatalf("status %d, want %d: %s", got, want, b)
		}
		return body
	}
}

func readFile(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/austin/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// published is a host with the Austin export imported, the recorded routes
// as its distance fixture, and published.
func published(t *testing.T) *fakeHost {
	t.Helper()
	h := newHost(t)
	must(t, 200)(do(t, h, (*call).importBundle, "POST", "/admin/import", map[string]any{"import": map[string]any{
		"bundle": readFile(t, "rates.json"), "distance_fixture": readFile(t, "distances.json")}}, admin))
	r := must(t, 200)(do(t, h, (*call).publish, "POST", "/admin/publish", nil, admin))
	if !strings.HasPrefix(r["message"].(string), "Published") || !slices.Contains(h.emitted, "fuse.rates_published") {
		t.Fatalf("publish: %v %v", r, h.emitted)
	}
	return h
}

type goldenPreview struct {
	Variant  string          `json:"variant"`
	Request  map[string]any  `json:"request"`
	Response json.RawMessage `json:"response"`
}

func goldens(t *testing.T) []goldenPreview {
	f, err := os.Open("testdata/austin/goldens.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, _ := gzip.NewReader(f)
	var g struct {
		Previews []goldenPreview `json:"previews"`
	}
	if err := json.NewDecoder(gz).Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g.Previews
}

func sameJSON(a, b []byte) bool {
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	return reflect.DeepEqual(x, y)
}

// quoteFor asks /quote for a golden case: one service, the destination for
// it, the case's quantities.
func quoteFor(t *testing.T, h *fakeHost, req map[string]any, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"zip": req["zip_code"], "dest": req["destination_zip_code"], "product_type": req["product_type"],
		"services": req["service_type"], "dest_for": "all", "selected": req["service_type"]}
	if q, ok := req["quantities"]; ok {
		body["quantities"] = q
	}
	for k, v := range extra {
		body[k] = v
	}
	return must(t, 200)(do(t, h, (*call).quote, "POST", "/quote", body, nil))
}

// Through the whole plugin — import, publish, the ZIPs read a shard at a
// time from storage, the fixture's routes, /quote — every golden preview of
// the recorded settings is Fuse's, field for field.
func TestQuoteMatchesFuse(t *testing.T) {
	t.Parallel()
	h := published(t)
	n := 0
	for _, g := range goldens(t) {
		zip, _ := g.Request["zip_code"].(string)
		st, _ := g.Request["service_type"].(string)
		var want engine.Preview
		_ = json.Unmarshal(g.Response, &want)
		if g.Variant != "round_trip" || zip == "" || st == "" || st == "bogus" || len(want.Products) == 0 {
			continue // /quote prices a service only where it has products
		}
		r := quoteFor(t, h, g.Request, nil)
		got, _ := json.Marshal(r["services"].(map[string]any)[st].(map[string]any)["preview"])
		if !sameJSON(got, g.Response) {
			t.Errorf("%v:\n got  %s\n fuse %s", g.Request, got, g.Response)
		}
		n++
	}
	if n < 100 {
		t.Fatalf("only %d cases compared", n)
	}
}

// The summary is the preview's arithmetic, and agrees with Fuse's total.
func TestQuoteSummary(t *testing.T) {
	t.Parallel()
	h := published(t)
	r := must(t, 200)(do(t, h, (*call).quote, "GET", "/quote?zip=78738&dest=78620&services=keep-it,move-it,store-it&dest_for=move-it", nil, nil))
	svcs := r["services"].(map[string]any)
	for _, st := range []string{"keep-it", "move-it", "store-it"} {
		s := svcs[st].(map[string]any)
		sum, pv := s["summary"].(map[string]any), s["preview"].(map[string]any)
		if sum["due"] != pv["total"] {
			t.Errorf("%s: due %v, Fuse's total %v", st, sum["due"], pv["total"])
		}
	}
	if svcs["move-it"].(map[string]any)["dest"] != "78620" || svcs["keep-it"].(map[string]any)["dest"] != nil {
		t.Errorf("dest_for: %v", svcs)
	}
	if r["service_area"].(map[string]any)["served"] != true || r["labels"].(map[string]any)["total"] != "Due Before Delivery" {
		t.Errorf("answer: %v", r)
	}
	if r["distance"].(map[string]any)["status"] != "fixture" {
		t.Errorf("distance: %v", r["distance"])
	}
	// An unknown ZIP: Fuse's out-of-area text, and the service area says so.
	r = must(t, 200)(do(t, h, (*call).quote, "GET", "/quote?zip=90210&services=keep-it", nil, nil))
	if r["out_of_area_text"] == nil || r["service_area"].(map[string]any)["served"] != false {
		t.Errorf("unknown ZIP: %v", r)
	}
	// Bad input, and no rates.
	must(t, 400)(do(t, h, (*call).quote, "GET", "/quote?zip=7870", nil, nil))
	must(t, 400)(do(t, h, (*call).quote, "GET", "/quote?zip=78701&dest=abc", nil, nil))
	must(t, 409)(do(t, newHost(t), (*call).quote, "GET", "/quote?zip=78701", nil, nil))
}

func TestServiceAreaAndCatalog(t *testing.T) {
	t.Parallel()
	h := published(t)
	for zip, served := range map[string]bool{"78701": true, "78640": true, "90210": false} {
		r := must(t, 200)(do(t, h, (*call).serviceArea, "GET", "/service-area?zip="+zip, nil, nil))
		if r["served"] != served {
			t.Errorf("%s: %v", zip, r)
		}
	}
	must(t, 400)(do(t, h, (*call).serviceArea, "GET", "/service-area?zip=7870", nil, nil))
	// Nothing published: biased toward served.
	r := must(t, 200)(do(t, newHost(t), (*call).serviceArea, "GET", "/service-area?zip=90210", nil, nil))
	if r["served"] != true || r["configured"] != false {
		t.Errorf("unpublished: %v", r)
	}
	r = must(t, 200)(do(t, h, (*call).catalog, "GET", "/catalog?zip=78703", nil, nil))
	p := r["products"].([]any)[0].(map[string]any)
	if p["discounted_price"] != float64(9900) || r["location_phone"] != "5125550100" {
		t.Errorf("catalog: %v", r)
	}
}

// A minted token verifies without anything stored about it, carries no
// contact details, fails once changed, and still verifies after the key is
// rotated.
func TestQuoteToken(t *testing.T) {
	t.Parallel()
	h := published(t)
	r := quoteFor(t, h, map[string]any{"zip_code": "78701", "destination_zip_code": "78626", "service_type": "move-it", "product_type": "portable-storage"},
		map[string]any{"mint": true, "name": "Karen Smith-Jones", "email": "k@example.com"})
	tok := r["token"].(string)
	if !strings.HasPrefix(tok, "v1.") || strings.Contains(tok, "@") {
		t.Fatalf("token %q", tok)
	}
	p, _, _, err := decodeToken(tok)
	if err != nil || p.FN != "Karen" || p.ST != "move-it" || p.Fig.Tot != int64(r["services"].(map[string]any)["move-it"].(map[string]any)["preview"].(map[string]any)["total"].(float64)) || p.VU == "" {
		t.Fatalf("payload %+v %v", p, err)
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "Smith") || strings.Contains(string(raw), "example.com") {
		t.Fatalf("PII in the token: %s", raw)
	}
	v := must(t, 200)(do(t, h, (*call).quote, "GET", "/quote?services=move-it&t="+url.QueryEscape(tok), nil, nil))
	q := v["quoted"].(map[string]any)
	if q["valid"] != true || q["rates_changed"] != false || v["zip"] != "78701" || v["dest"] != "78626" {
		t.Fatalf("verify: %v", v)
	}
	// Changing a figure breaks the signature; the inputs are still priced.
	parts := strings.Split(tok, ".")
	p.Fig.Tot = 1
	forged, _ := json.Marshal(p)
	bad := "v1." + b64(forged) + "." + parts[2]
	v = must(t, 200)(do(t, h, (*call).quote, "GET", "/quote?t="+url.QueryEscape(bad), nil, nil))
	if q := v["quoted"].(map[string]any); q["valid"] != false || q["payload"] != nil || v["zip"] != "78701" {
		t.Fatalf("forged: %v", v)
	}
	// Garbage is not a token; with no zip of its own it's a bad request.
	must(t, 400)(do(t, h, (*call).quote, "GET", "/quote?t=nope", nil, nil))
	// Rotating keeps old links verifying; new ones use the new key.
	must(t, 200)(do(t, h, (*call).rotateToken, "POST", "/admin/token/rotate", nil, admin))
	if _, err := verifyToken(t.Context(), h, tok); err != nil {
		t.Fatalf("old token after rotation: %v", err)
	}
	tok2 := quoteFor(t, h, map[string]any{"zip_code": "78701", "service_type": "keep-it", "product_type": "portable-storage"}, map[string]any{"mint": true})["token"].(string)
	if p2, _, _, _ := decodeToken(tok2); p2.Kid != "k2" {
		t.Fatalf("new kid %q", p2.Kid)
	}
	// The key is sealed in storage.
	if strings.Contains(h.kv[keyTokenKeys], `"k1":"`) && !strings.Contains(h.kv[keyTokenKeys], "sealed:") {
		t.Fatalf("token key stored in the clear: %s", h.kv[keyTokenKeys])
	}
	// Minting needs a priced selection.
	must(t, 422)(do(t, h, (*call).quote, "POST", "/quote", map[string]any{"zip": "78701", "selected": "bogus", "mint": true}, nil))
	// The admin's checker says the same.
	r = must(t, 200)(do(t, h, (*call).testToken, "POST", "/admin/test/token", map[string]any{"test": map[string]any{"token": "https://austin.mulebox.com/quote-thank-you?t=" + tok}}, admin))
	if !strings.Contains(fmt.Sprint(r), "Genuine") {
		t.Fatalf("checker: %v", r)
	}
	r = must(t, 200)(do(t, h, (*call).testToken, "POST", "/admin/test/token", map[string]any{"test": map[string]any{"token": bad}}, admin))
	if !strings.Contains(fmt.Sprint(r), "Not genuine") {
		t.Fatalf("checker on a forgery: %v", r)
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// googleFake answers Directions requests from the recorded routes, and
// notes each request.
type googleFake struct {
	mu     sync.Mutex
	routes map[string]engine.FixtureRoute
	calls  []*url.URL
	delay  time.Duration
}

func (g *googleFake) RoundTrip(r *http.Request) (*http.Response, error) {
	g.mu.Lock()
	g.calls = append(g.calls, r.URL)
	g.mu.Unlock()
	if g.delay > 0 {
		select {
		case <-time.After(g.delay):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	q := r.URL.Query()
	k := engine.RouteKey{Origin: q.Get("origin"), Destination: q.Get("destination")}
	if w := q.Get("waypoints"); w != "" {
		k.Waypoints = strings.Split(w, "|")
	}
	rt, ok := g.routes[k.String()]
	body := `{"status":"ZERO_RESULTS","routes":[]}`
	if ok && rt.Status == "OK" {
		var legs []string
		for i := range rt.LegsMeters {
			legs = append(legs, fmt.Sprintf(`{"distance":{"value":%d,"text":"x"},"duration":{"value":%d,"text":"y"}}`, rt.LegsMeters[i], rt.LegsSeconds[i]))
		}
		body = `{"status":"OK","routes":[{"legs":[` + strings.Join(legs, ",") + `]}]}`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// With a Google key the plugin asks Google what Fuse asks, gets Fuse's
// prices, keeps the answers, never logs the key, and counts the calls.
func TestGoogleDistances(t *testing.T) {
	t.Parallel()
	h := published(t)
	var routes map[string]engine.FixtureRoute
	_ = json.Unmarshal([]byte(readFile(t, "distances.json")), &routes)
	g := &googleFake{routes: routes}
	h.rt = g
	r := must(t, 200)(do(t, h, (*call).saveSettings, "POST", "/admin/settings", map[string]any{"settings": map[string]any{
		"zip": "78747", "mileage_based_pricing": true, "mileage_round_trip": true, "zip_qualification": true,
		"out_of_area_zip_text": "We don't serve that ZIP yet. Call (512) 555-0100 and we'll see what we can do.", "quote_validity_days": 30,
		"total_price_label": "Due Before Delivery", "name": "Mule Box Austin", "phone": "(512) 555-0100",
		"distance_mode": "google", "google_key": "AIzaSECRETKEY123456"}}, admin))
	if r["settings"].(map[string]any)["google_key"] != "***3456" || strings.Contains(h.kv[keyConfig], "AIzaSECRETKEY123456") {
		t.Fatalf("key not masked or not sealed: %v / %s", r, h.kv[keyConfig])
	}
	if has, _, _ := loadDraftMeta(t.Context(), h); has {
		t.Fatal("saving the settings as they were made a draft")
	}
	compared := 0
	for _, gp := range goldens(t) {
		if gp.Variant != "round_trip" || gp.Request["zip_code"] != "78738" && gp.Request["zip_code"] != "78613" {
			continue
		}
		st, _ := gp.Request["service_type"].(string)
		var want engine.Preview
		_ = json.Unmarshal(gp.Response, &want)
		if st == "" || st == "bogus" || len(want.Products) == 0 {
			continue
		}
		got, _ := json.Marshal(quoteFor(t, h, gp.Request, nil)["services"].(map[string]any)[st].(map[string]any)["preview"])
		if !sameJSON(got, gp.Response) {
			t.Errorf("%v:\n got  %s\n fuse %s", gp.Request, got, gp.Response)
		}
		compared++
	}
	if compared == 0 || len(g.calls) == 0 {
		t.Fatalf("compared %d, Google calls %d", compared, len(g.calls))
	}
	for _, u := range g.calls {
		if u.Host != googleHost || u.Path != "/maps/api/directions/json" || u.Query().Get("key") != "AIzaSECRETKEY123456" {
			t.Fatalf("request %s", u)
		}
	}
	// Asked again, every route comes from storage.
	before := len(g.calls)
	r = quoteFor(t, h, map[string]any{"zip_code": "78738", "destination_zip_code": "78620", "service_type": "move-it", "product_type": "portable-storage"}, nil)
	if len(g.calls) != before || r["distance"].(map[string]any)["status"] != "cached" {
		t.Fatalf("not cached: %d calls, %v", len(g.calls)-before, r["distance"])
	}
	month := h.kv["stats:google:"+time.Now().UTC().Format("200601")]
	if month != fmt.Sprint(before) {
		t.Errorf("calls counted %s, made %d", month, before)
	}
	// A ZIP Google can't route: ZERO_RESULTS is kept, so it isn't asked twice.
	quoteFor(t, h, map[string]any{"zip_code": "78799", "service_type": "keep-it", "product_type": "portable-storage"}, nil)
	n := len(g.calls)
	quoteFor(t, h, map[string]any{"zip_code": "78799", "service_type": "keep-it", "product_type": "portable-storage"}, nil)
	if len(g.calls) != n {
		t.Errorf("a ZERO_RESULTS route was asked for again")
	}
	for _, l := range h.logs {
		if strings.Contains(l, "AIzaSECRETKEY123456") {
			t.Fatalf("the key reached the log: %s", l)
		}
	}
	// Only ZIPs in the table, when asked: an unknown ZIP never reaches Google.
	cfg := loadConfig(t.Context(), h)
	cfg.KnownZipsOnly = true
	_ = setJSON(t.Context(), h, keyConfig, cfg)
	n = len(g.calls)
	r = must(t, 200)(do(t, h, (*call).quote, "GET", "/quote?zip=90211&services=keep-it", nil, nil))
	if len(g.calls) != n || r["distance"].(map[string]any)["status"] != "skipped" {
		t.Errorf("known ZIPs only: %d calls, %v", len(g.calls)-n, r["distance"])
	}
}

// Google slower than the budget: the quote answers in time, with Fuse's
// own no-distance prices (its google_down goldens).
func TestGoogleBudget(t *testing.T) {
	t.Parallel()
	h := published(t)
	g := &googleFake{delay: time.Minute}
	h.rt = g
	_ = setJSON(t.Context(), h, keyConfig, config{GoogleKey: h.Seal("AIzaKEY"), DistanceMode: modeGoogle})
	start := time.Now()
	compared := 0
	for _, gp := range goldens(t) {
		if gp.Variant != "google_down" || gp.Request["zip_code"] != "78701" {
			continue
		}
		st := gp.Request["service_type"].(string)
		r := quoteFor(t, h, gp.Request, map[string]any{"budget_ms": 150})
		if r["distance"].(map[string]any)["status"] != "unavailable" {
			t.Fatalf("distance %v", r["distance"])
		}
		got, _ := json.Marshal(r["services"].(map[string]any)[st].(map[string]any)["preview"])
		if !sameJSON(got, gp.Response) {
			t.Errorf("%v:\n got  %s\n fuse %s", gp.Request, got, gp.Response)
		}
		compared++
	}
	if compared == 0 {
		t.Fatal("no google_down cases")
	}
	if took := time.Since(start); took > time.Duration(compared)*time.Second {
		t.Fatalf("%d quotes took %s: the budget didn't hold", compared, took)
	}
	// A timeout isn't Google's answer, so it isn't kept.
	for k := range h.kv {
		if strings.HasPrefix(k, "dist:") {
			t.Fatalf("a timed-out route was cached: %s", k)
		}
	}
	if distanceBudget != 4*time.Second {
		t.Fatalf("the budget is %s", distanceBudget)
	}
}

// A book far past 1 MB publishes in parts no bigger than the cap, and is
// read back a shard at a time.
func TestLargeBook(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	_, book, err := engine.ParseBundle([]byte(readFile(t, "rates.json")))
	if err != nil {
		t.Fatal(err)
	}
	zone, loc := book.ZipCodes[0].ZoneID, book.ZipCodes[0].LocationID
	for i := range 45000 {
		book.ZipCodes = append(book.ZipCodes, engine.ZipCode{ID: int64(100000 + i), PostalCode: fmt.Sprintf("%05d-%04d", 10000+i/3, i%3), ZoneID: zone, LocationID: loc})
	}
	if err := saveDraft(t.Context(), h, book, "test", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := publish(t.Context(), h, "test"); err != nil {
		t.Fatal(err)
	}
	if h.maxValue > blobPart || h.maxValue == 0 {
		t.Fatalf("largest value %d bytes", h.maxValue)
	}
	parts := 0
	for k := range h.kv {
		if strings.HasPrefix(k, keyDraft+"#") {
			parts++
		}
	}
	if parts != 0 {
		t.Fatalf("the draft wasn't cleared on publish: %d parts", parts)
	}
	r := must(t, 200)(do(t, h, (*call).serviceArea, "GET", "/service-area?zip=12000", nil, nil))
	if r["served"] != true {
		t.Fatalf("ZIP+4 under 12000: %v", r)
	}
	r = must(t, 200)(do(t, h, (*call).quote, "GET", "/quote?zip=78701&services=keep-it", nil, nil))
	if r["services"].(map[string]any)["keep-it"].(map[string]any)["available"] != true {
		t.Fatalf("quote: %v", r)
	}
	// The draft too: a whole book in parts, edited and read back.
	d, _, err := loadDraft(t.Context(), h)
	if err != nil || len(d.ZipCodes) != len(book.ZipCodes) {
		t.Fatalf("draft from the published version: %d zips, %v", len(d.ZipCodes), err)
	}
}

// The editor checks links and Fuse's validations; publish refuses a book
// that doesn't hold up; versions are kept to roll back to, the last 10.
func TestEditorPublishRollback(t *testing.T) {
	t.Parallel()
	h := published(t)
	r := must(t, 422)(do(t, h, kindFn((*call).saveKind, kinds[0]), "POST", "/admin/product", map[string]any{"product": map[string]any{
		"name": "X", "price": 100, "product_type": "nope", "service_types": []any{"keep-it", "teleport"}, "discount": "Free Lunch"}}, admin))
	errs := r["errors"].(map[string]any)
	for _, k := range []string{"product_type", "service_types", "discount", "name"} {
		if errs[k] == nil {
			t.Errorf("no error on %s: %v", k, errs)
		}
	}
	r = must(t, 200)(do(t, h, kindFn((*call).saveKind, kinds[0]), "POST", "/admin/product", map[string]any{"product": map[string]any{
		"name": "10' Mule Box", "price": 12900, "product_type": "portable-storage", "service_types": []any{"keep-it"}, "active": true, "monthly": true, "position": 0}}, admin))
	id := r["product"].(map[string]any)["id"]
	// The draft prices it; the published rates don't, until it's published.
	test := map[string]any{"test": map[string]any{"zip": "78701", "service": "keep-it", "product_type": "portable-storage"}}
	r = must(t, 200)(do(t, h, (*call).testPrice, "POST", "/admin/test/price", test, admin))
	items := r["result"].(map[string]any)["items"].([]any)
	if !strings.Contains(fmt.Sprint(items[0]), "10' Mule Box") || strings.Contains(fmt.Sprint(items[1]), "10' Mule Box") {
		t.Fatalf("tester: %v", items)
	}
	// A discount with a bad amount can't be saved; a fee can't go while a ZIP charges it.
	must(t, 422)(do(t, h, kindFn((*call).saveKind, kinds[2]), "POST", "/admin/discount", map[string]any{"discount": map[string]any{"name": "Too much", "discount_type": "percentage", "amount": 150}}, admin))
	var gated float64
	for _, row := range must(t, 200)(do(t, h, kindFn((*call).listKind, kinds[1]), "GET", "/admin/fees", nil, admin))["fees"].([]any) {
		if row.(map[string]any)["name"] == "Gated Community Fee" {
			gated = row.(map[string]any)["id"].(float64)
		}
	}
	must(t, 409)(do(t, h, kindFn((*call).deleteKind, kinds[1]), "POST", fmt.Sprintf("/admin/fee/delete?id=%v", gated), nil, admin))
	first := must(t, 200)(do(t, h, (*call).publish, "POST", "/admin/publish", nil, admin))["active"].(map[string]any)["version"].(string)
	r = must(t, 200)(do(t, h, (*call).quote, "GET", "/quote?zip=78701&services=keep-it", nil, nil))
	if r["services"].(map[string]any)["keep-it"].(map[string]any)["product_id"] != id {
		t.Fatalf("the new first product isn't priced: %v", r["services"])
	}
	// Publishing with nothing changed is refused.
	must(t, 409)(do(t, h, (*call).publish, "POST", "/admin/publish", nil, admin))
	// A draft that doesn't hold up isn't published.
	b, _, _ := loadDraft(t.Context(), h)
	b.ZipCodes = append(b.ZipCodes, engine.ZipCode{ID: 999, PostalCode: "7870", LocationID: b.Locations[0].ID})
	_ = saveDraft(t.Context(), h, b, "test", "")
	must(t, 422)(do(t, h, (*call).publish, "POST", "/admin/publish", nil, admin))
	discardDraft(t.Context(), h)
	// Ten more versions: the oldest go, with their storage.
	for i := range 10 {
		b, _, _ := loadDraft(t.Context(), h)
		b.Settings.TotalPriceLabel = fmt.Sprint("Total ", i)
		_ = saveDraft(t.Context(), h, b, "test", "")
		must(t, 200)(do(t, h, (*call).publish, "POST", "/admin/publish", nil, admin))
	}
	vs := must(t, 200)(do(t, h, (*call).versions, "GET", "/admin/versions", nil, admin))["versions"].([]any)
	if len(vs) != keepVersions {
		t.Fatalf("%d versions kept", len(vs))
	}
	for k := range h.kv {
		if strings.HasPrefix(k, "rates:v:") && !slices.ContainsFunc(vs, func(v any) bool { return strings.HasPrefix(k, "rates:v:"+v.(map[string]any)["version"].(string)+":") }) {
			t.Fatalf("a pruned version's key is left: %s", k)
		}
	}
	must(t, 422)(do(t, h, (*call).rollback, "POST", "/admin/rollback?version="+first, nil, admin))
	back := vs[len(vs)-1].(map[string]any)["version"].(string)
	must(t, 200)(do(t, h, (*call).rollback, "POST", "/admin/rollback?version="+back, nil, admin))
	if r := must(t, 200)(do(t, h, (*call).quote, "GET", "/quote?zip=78701&services=keep-it", nil, nil)); r["rates_version"] != back {
		t.Fatalf("after rollback: %v", r["rates_version"])
	}
}

// The import refuses an export that was changed after Fuse wrote it, and
// puts product images in the media library.
func TestImport(t *testing.T) {
	t.Parallel()
	h := newHost(t)
	raw := readFile(t, "rates.json")
	edited := strings.Replace(raw, `"price":14900`, `"price":1`, 1)
	r := must(t, 422)(do(t, h, (*call).importBundle, "POST", "/admin/import", map[string]any{"import": map[string]any{"bundle": edited}}, admin))
	if !strings.Contains(fmt.Sprint(r["errors"]), "sha256") {
		t.Fatalf("edited export: %v", r)
	}
	must(t, 422)(do(t, h, (*call).importBundle, "POST", "/admin/import", map[string]any{"import": map[string]any{"bundle": "{}"}}, admin))
	r = must(t, 200)(do(t, h, (*call).checkBundle, "POST", "/admin/import/check", map[string]any{"import": map[string]any{"bundle": raw}}, admin))
	if !strings.Contains(fmt.Sprint(r["result"]), "7 in the export: 7 added") {
		t.Fatalf("dry run: %v", r)
	}
	if _, ok := h.kv[keyDraft]; ok {
		t.Fatal("the dry run saved a draft")
	}
	// An export with an image.
	var bundle map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw), &bundle)
	var rates map[string]any
	_ = json.Unmarshal(bundle["rates"], &rates)
	rates["products"].([]any)[0].(map[string]any)["images"] = []any{map[string]any{"filename": "box.png", "content_type": "image/png", "base64": "iVBORw0KGgo="}}
	rb, _ := json.Marshal(rates)
	bundle["rates"] = rb
	bundle["sha256"], _ = json.Marshal(sha(rb))
	withImage, _ := json.Marshal(bundle)
	must(t, 200)(do(t, h, (*call).importBundle, "POST", "/admin/import", map[string]any{"import": map[string]any{"bundle": string(withImage)}}, admin))
	must(t, 200)(do(t, h, (*call).publish, "POST", "/admin/publish", nil, admin))
	r = must(t, 200)(do(t, h, (*call).catalog, "GET", "/catalog", nil, nil))
	if h.media != 1 || r["products"].([]any)[0].(map[string]any)["image_url"] != "https://cms.test/media/box.png" {
		t.Fatalf("image: %d %v", h.media, r["products"])
	}
}

func TestChecks(t *testing.T) {
	t.Parallel()
	if c := checksFor(t.Context(), newHost(t)); c[0].State != "fails" {
		t.Fatalf("unpublished: %+v", c)
	}
	h := published(t)
	got := map[string]plugin.Check{}
	for _, c := range checksFor(t.Context(), h) {
		got[c.Key] = c
	}
	if got["pricing.rates"].State != "passes" || got["pricing.distances"].State != "warns" {
		t.Fatalf("checks: %+v", got)
	}
}

func TestModuleManifest(t *testing.T) {
	t.Parallel()
	m := Module()
	if m.Manifest.Key != "fuse" || !slices.Equal(m.Manifest.Permissions.HTTP, []string{"maps.googleapis.com"}) || m.Default {
		t.Fatalf("manifest %+v", m.Manifest)
	}
	for _, r := range m.Manifest.Routes {
		if m.Handlers[r.Export] == nil {
			t.Errorf("route %s %s has no handler", r.Method, r.Path)
		}
		if strings.HasPrefix(r.Path, "/admin") == r.Public {
			t.Errorf("route %s %s public=%v", r.Method, r.Path, r.Public)
		}
	}
	var screens struct {
		Screens []map[string]any `json:"screens"`
	}
	_ = json.Unmarshal(m.Manifest.Admin, &screens)
	ids := map[string]bool{}
	for _, s := range screens.Screens {
		ids[s["id"].(string)] = true
	}
	for _, want := range []string{"overview", "settings", "products", "fee", "mileage", "zips", "import", "test", "versions"} {
		if !ids[want] {
			t.Errorf("no %s screen", want)
		}
	}
	if !bytes.Contains(m.Manifest.SiteConfig, []byte("/api/x/fuse/quote")) {
		t.Errorf("site config %s", m.Manifest.SiteConfig)
	}
}

func kindFn(fn func(*call, kind) *plugin.Response, k kind) func(*call) *plugin.Response {
	return func(c *call) *plugin.Response { return fn(c, k) }
}
