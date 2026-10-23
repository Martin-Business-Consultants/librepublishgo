package fuse

import (
	"context"
	"crypto/sha1" //nolint:gosec // a cache key, not a signature
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/plugins/fuse/engine"
)

// Distances come from Google's Directions API, the query Fuse sends
// (MileagePricingService.directions), so the miles match Fuse's. Or, with
// no key to hand (a laptop, the tests), from a fixture of recorded answers.
const (
	directionsURL = "https://maps.googleapis.com/maps/api/directions/json"
	googleHost    = "maps.googleapis.com"

	// distanceBudget is all a quote may spend on Google, every route in
	// parallel, inside the host's 10 s per call. A route not answered by
	// then is no answer: the quote falls back to Fuse's own "Starting at"
	// lines, as Fuse does when Google fails.
	distanceBudget = 4 * time.Second

	cacheOK       = 180 * 24 * time.Hour // a ZIP-to-ZIP route barely moves
	cacheNotFound = 7 * 24 * time.Hour   // ZERO_RESULTS, NOT_FOUND
	cacheFailed   = 10 * time.Minute     // anything else Google refused

	defaultMonthlyCap = 20000
	maxCachedRoutes   = 50000
)

// Distance modes (cfg:settings distance_mode).
const (
	modeGoogle  = "google"  // Google's Directions API, with the sealed key
	modeFixture = "fixture" // recorded answers only (cfg:distance_fixture): no network at all
	modeOff     = "off"     // no distances: every quote gets Fuse's no-answer lines
)

// config is the plugin's own settings (cfg:settings), apart from the rate
// book's: how distances are had.
type config struct {
	GoogleKey     string `json:"google_key,omitempty"` // sealed (api.Seal)
	DistanceMode  string `json:"distance_mode,omitempty"`
	KnownZipsOnly bool   `json:"known_zips_only"`
	MonthlyCap    int64  `json:"monthly_cap,omitempty"`
	LastCheck     string `json:"last_check,omitempty"` // "OK 2026-10-23T…": the key's last check
}

const keyConfig = "cfg:settings"
const keyFixture = "cfg:distance_fixture"

func loadConfig(ctx context.Context, h host) config {
	var c config
	getJSON(ctx, h, keyConfig, &c)
	return c
}

// mode is the distance mode in force: the one chosen, else Google when
// there's a key, else the fixture when there's one, else off.
func (c config) mode(ctx context.Context, h host) string {
	switch c.DistanceMode {
	case modeGoogle, modeFixture, modeOff:
		return c.DistanceMode
	}
	if c.GoogleKey != "" {
		return modeGoogle
	}
	if _, ok, _ := h.KVGet(ctx, keyFixture); ok {
		return modeFixture
	}
	return modeOff
}

func (c config) cap() int64 {
	if c.MonthlyCap > 0 {
		return c.MonthlyCap
	}
	return defaultMonthlyCap
}

// cachedRoute is a Directions answer kept in storage (dist:<sha1>).
type cachedRoute struct {
	Status      string  `json:"status"` // OK, ZERO_RESULTS, NOT_FOUND, or what went wrong
	LegsMeters  []int64 `json:"legs_m,omitempty"`
	LegsSeconds []int64 `json:"legs_s,omitempty"`
	At          string  `json:"at"`
}

func (r cachedRoute) fresh(t time.Time) bool {
	at, err := time.Parse(time.RFC3339, r.At)
	if err != nil {
		return false
	}
	ttl := cacheFailed
	switch r.Status {
	case "OK":
		ttl = cacheOK
	case "ZERO_RESULTS", "NOT_FOUND":
		ttl = cacheNotFound
	}
	return t.Sub(at) < ttl
}

func (r cachedRoute) route() *engine.Route {
	if r.Status != "OK" {
		return nil
	}
	return &engine.Route{LegsMeters: r.LegsMeters, LegsSeconds: r.LegsSeconds}
}

func cacheKey(k engine.RouteKey) string {
	sum := sha1.Sum([]byte("directions|" + k.Origin + "|" + k.Destination + "|" + strings.Join(k.Waypoints, "|"))) //nolint:gosec // a cache key
	return "dist:" + hex.EncodeToString(sum[:])
}

// distanceReport is how a quote's distances were had.
type distanceReport struct {
	Status string `json:"status"` // not_needed, ok, cached, fixture, unavailable, off, skipped
	Mode   string `json:"mode"`
	Routes int    `json:"routes"`
	Live   int    `json:"live,omitempty"`
	Cached int    `json:"cached,omitempty"`
	Failed int    `json:"failed,omitempty"`
	MS     int64  `json:"ms"`
}

// fetchRoutes answers every route in keys, within budget: from the cache,
// the fixture or Google (each missing route at once). The answers go back
// by RouteKey.String(); a route with no answer is absent.
func fetchRoutes(ctx context.Context, h host, cfg config, keys []engine.RouteKey, budget time.Duration) (map[string]*engine.Route, distanceReport) {
	start := time.Now()
	mode := cfg.mode(ctx, h)
	rep := distanceReport{Mode: mode, Routes: len(keys)}
	out := map[string]*engine.Route{}
	if len(keys) == 0 {
		rep.Status = "not_needed"
		return out, rep
	}
	if mode == modeOff {
		rep.Status, rep.Failed = "off", len(keys)
		return out, rep
	}
	if mode == modeFixture {
		var fixture map[string]engine.FixtureRoute
		if _, err := getBlob(ctx, h, keyFixture, &fixture); err != nil {
			h.Log("reading the distance fixture: " + err.Error())
		}
		for _, k := range keys {
			if r, ok := fixture[k.String()]; ok && r.Status == "OK" {
				out[k.String()] = &engine.Route{LegsMeters: r.LegsMeters, LegsSeconds: r.LegsSeconds}
			} else {
				rep.Failed++
			}
		}
		rep.Status = "fixture"
		if rep.Failed > 0 {
			rep.Status = "unavailable"
		}
		rep.MS = time.Since(start).Milliseconds()
		return out, rep
	}

	t := now()
	var missing []engine.RouteKey
	for _, k := range keys {
		var c cachedRoute
		if getJSON(ctx, h, cacheKey(k), &c) && c.fresh(t) {
			rep.Cached++
			if r := c.route(); r != nil {
				out[k.String()] = r
			} else {
				rep.Failed++
			}
			continue
		}
		missing = append(missing, k)
	}
	if len(missing) > 0 {
		key := h.Reveal(cfg.GoogleKey)
		month := "stats:google:" + t.UTC().Format("200601")
		used, _, _ := h.KVGet(ctx, month)
		var n int64
		fmt.Sscan(used, &n)
		switch {
		case key == "":
			rep.Failed += len(missing)
		case n+int64(len(missing)) > cfg.cap():
			h.Log(fmt.Sprintf("Google's monthly cap (%d calls) is reached; distances wait for next month or a higher cap", cfg.cap()))
			rep.Failed += len(missing)
		default:
			bctx, cancel := context.WithTimeout(ctx, budget)
			var mu sync.Mutex
			var wg sync.WaitGroup
			for _, k := range missing {
				wg.Go(func() {
					c, err := directions(bctx, h.HTTP(), key, k)
					_, _ = h.KVAdd(ctx, month, 1)
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						// A timeout or a network failure isn't Google's answer:
						// not kept, so the next quote asks again.
						h.Log("Google Directions: " + redactKey(err.Error(), key))
						rep.Failed++
						return
					}
					rep.Live++
					c.At = t.UTC().Format(time.RFC3339)
					storeRoute(ctx, h, k, c)
					if r := c.route(); r != nil {
						out[k.String()] = r
					} else {
						rep.Failed++
					}
				})
			}
			wg.Wait()
			cancel()
		}
	}
	switch {
	case rep.Failed > 0:
		rep.Status = "unavailable"
	case rep.Live > 0:
		rep.Status = "ok"
	default:
		rep.Status = "cached"
	}
	rep.MS = time.Since(start).Milliseconds()
	return out, rep
}

func storeRoute(ctx context.Context, h host, k engine.RouteKey, c cachedRoute) {
	if n, _ := countPrefix(ctx, h, "dist:"); n >= maxCachedRoutes {
		return
	}
	_ = setJSON(ctx, h, cacheKey(k), c)
}

// countPrefix counts keys a page at a time, stopping at maxCachedRoutes.
func countPrefix(ctx context.Context, h host, prefix string) (int, error) {
	n, after := 0, ""
	for {
		keys, next, err := h.KVList(ctx, prefix, after, 1000)
		if err != nil {
			return n, err
		}
		n += len(keys)
		if next == "" || n >= maxCachedRoutes {
			return n, nil
		}
		after = next
	}
}

// directions is one Directions call, read as Fuse reads it: status OK with
// a route, each leg's distance and duration values, more than 0 meters in
// all. A refusal is an answer (cached briefly); a transport error isn't.
func directions(ctx context.Context, client *http.Client, key string, k engine.RouteKey) (cachedRoute, error) {
	q := url.Values{"origin": {k.Origin}, "destination": {k.Destination}, "key": {key}}
	if len(k.Waypoints) > 0 {
		q.Set("waypoints", strings.Join(k.Waypoints, "|"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, directionsURL+"?"+q.Encode(), nil)
	if err != nil {
		return cachedRoute{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return cachedRoute{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return cachedRoute{}, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return cachedRoute{Status: fmt.Sprintf("HTTP_%d", res.StatusCode)}, nil
	}
	var data struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"error_message"`
		Routes       []struct {
			Legs []struct {
				Distance struct {
					Value float64 `json:"value"`
				} `json:"distance"`
				Duration struct {
					Value float64 `json:"value"`
				} `json:"duration"`
			} `json:"legs"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return cachedRoute{Status: "UNREADABLE"}, nil
	}
	if data.Status != "OK" || len(data.Routes) == 0 {
		st := data.Status
		if st == "" {
			st = "NO_ROUTE"
		}
		return cachedRoute{Status: st}, nil
	}
	c := cachedRoute{Status: "OK"}
	var total int64
	for _, l := range data.Routes[0].Legs {
		m, s := int64(l.Distance.Value), int64(l.Duration.Value) // .to_i
		c.LegsMeters = append(c.LegsMeters, m)
		c.LegsSeconds = append(c.LegsSeconds, s)
		total += m
	}
	if total <= 0 {
		return cachedRoute{Status: "NO_DISTANCE"}, nil
	}
	return c, nil
}

// checkKey makes one Directions call from the business ZIP to itself.
func checkKey(ctx context.Context, h host, key, businessZip string) string {
	if key == "" {
		return "There's no key to check."
	}
	if businessZip == "" {
		businessZip = "10001"
	}
	cctx, cancel := context.WithTimeout(ctx, distanceBudget)
	defer cancel()
	o := businessZip + ", USA"
	c, err := directions(cctx, h.HTTP(), key, engine.RouteKey{Origin: o, Destination: o, Waypoints: []string{businessZip}})
	if err != nil {
		return "Google couldn't be reached: " + redactKey(err.Error(), key)
	}
	if c.Status != "OK" {
		return "Google answered " + c.Status + ": the key may not have the Directions API enabled, or may be restricted to other addresses."
	}
	return "OK"
}

func redactKey(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "***")
		s = strings.ReplaceAll(s, url.QueryEscape(key), "***")
	}
	return s
}
