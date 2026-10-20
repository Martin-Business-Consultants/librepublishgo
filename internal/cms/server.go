// Package cms is the core of an install: the App, content and structure,
// revisions, assets, settings, and the HTTP API (/api, /api/v1, /files).
package cms

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/deploy"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/jsonv"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/ratelimit"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/update"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/webhook"
)

// Config is what an install is started with; everything else lives in the
// database.
type Config struct {
	DataDir string // the database, uploaded files, the secret key
	Addr    string
	// The CMS's own address, which asset URLs start with (APP_HOST).
	BaseURL string
	// SITE_KEY: the tenant name webhooks carry.
	SiteKey string
	// SECRET_KEY: seals token plaintexts and settings' secrets instead of
	// DataDir/secret.key.
	SecretKey string
	// Version is this build's, "dev" when empty. The API it serves is
	// frozen whatever the version.
	Version string
	// HandOver stops serving and hands over to the release binary at bin
	// (the update button); nil where that can't be done (tests, one-off
	// commands).
	HandOver func(bin string)
	// UpdateCheck looks for a newer release once a day (GitHub's), for the
	// admin's "a new version is available"; UPDATE_CHECK=0 turns it off.
	UpdateCheck bool
	// Requests a minute per token across /api/v1: 0 for the default
	// (1200), below 0 for no limit.
	DeliveryRateLimit int
	// TrustProxy reads the client's address from X-Forwarded-For, as it
	// must behind a proxy and mustn't when facing the internet directly
	// (anyone could claim any address, and dodge the rate limits).
	TrustProxy bool
	Logger     *slog.Logger
	// Mailer sends email; SMTP from Settings › Email when nil.
	Mailer Mailer
	// Admin serves the install's root (/, /assets/, and /cms redirected
	// there) when set.
	Admin http.Handler
	// NoDefaultPlugins leaves out the plugins the binary ships (tests).
	NoDefaultPlugins bool
	// Plugins are more plugins for the host to have, beside the built-in
	// ones (tests).
	Plugins []*plugin.Module
	// SchemaDebounce is how long schema.changed waits for writes to settle
	// (2s when 0).
	SchemaDebounce time.Duration
	// ImportMaxBytes is the largest backup the admin may upload to import
	// (IMPORT_MAX_BYTES; 10 GiB when 0).
	ImportMaxBytes int64
}

const defaultDeliveryRateLimit = 1200

// App is one install: one site, one database, one process.
type App struct {
	cfg     Config
	log     *slog.Logger
	store   *store.DB
	secrets *secret.Key
	hooks   *webhook.Dispatcher
	deploys *deploy.Debouncer
	plugins *plugin.Host

	// Who hears of changes, and what a settings key's data goes through
	// before it's written (events.go).
	listeners     []listener
	beforeSetting map[string]func(data map[string]any)
	// The routes, and the agent tools declared on them (agent.go).
	mux   *http.ServeMux
	tools []*toolSpec
	// Settings › General's CMS address (cms_url), kept here for BaseURL.
	cmsURL atomic.Pointer[string]
	// The newest release, when it's newer than this build (UpdateCheck).
	newer atomic.Pointer[update.Release]
	// The last answer to "what's the newest release" (Settings › Updates).
	releaseMu sync.Mutex
	release   *update.Release
	releaseAt time.Time

	limiter *ratelimit.Limiter // the delivery API's, per token
	// Sign-in: ten tries a minute from one address, and ten at one account
	// from anywhere (a botnet guessing one password). Forgotten passwords:
	// five.
	signIns *ratelimit.Limiter
	forgots *ratelimit.Limiter
	// pluginCalls counts calls to plugins' public routes: 30 a minute from
	// one address to one plugin, so a form can't be flooded.
	pluginCalls *ratelimit.Limiter
	variants    keyedLocks
	startedAt   time.Time
	schemas     schemaCache // the schema's digest (schema_digest.go)
	// server is the Server the App runs behind, which can replace it (an
	// import); nil for an App on its own.
	server *Server

	// ctx lives as long as the app: Close cancels it, and everything
	// started in the background (spawn) derives from it.
	ctx    context.Context //nolint:containedctx // the app's own lifetime, not a request's
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex // guards closed, so nothing joins wg once Close waits
	closed bool
	once   sync.Once
	err    error
}

// NewApp opens an install: the database (migrated), the key, the defaults
// a new one needs. Nothing runs in the background until Start.
func NewApp(cfg Config) (*App, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "storage"), 0o750); err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "cms.sqlite3"))
	if err != nil {
		return nil, err
	}
	secrets, err := secret.Load(cfg.DataDir, cfg.SecretKey)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	limit := cfg.DeliveryRateLimit
	if limit == 0 {
		limit = defaultDeliveryRateLimit
	}
	a := &App{cfg: cfg, log: cfg.Logger, store: db, secrets: secrets, startedAt: time.Now(),
		limiter: ratelimit.New(limit), signIns: ratelimit.New(10), forgots: ratelimit.New(5), pluginCalls: ratelimit.New(30)}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	// The CMS's address, before anything (webhooks' tenant) reads it.
	if g, err := a.setting(context.Background(), "general"); err == nil {
		a.setCMSURL(str(g["cms_url"]))
	} else {
		a.log.Warn("settings", "err", err)
	}
	a.hooks = webhook.New(db, secrets, a.log, a.tenant(), a.spawn)
	a.deploys = deploy.New(deploySettings{a}, a.log, a.spawn)
	// Builds before 2.1.0 kept compiled WebAssembly plugins here: tens of
	// MB nothing reads any more.
	if cfg.DataDir != "" {
		if err := os.RemoveAll(filepath.Join(cfg.DataDir, "cache", "plugins")); err != nil {
			a.log.Warn("plugin cache", "err", err)
		}
	}
	a.plugins = plugin.New(pluginEnv{a}, db, a.log, a.spawn, append(builtinPlugins(), a.cfg.Plugins...)...)
	a.listen()
	if err := a.seed(); err != nil {
		a.cancel()
		return nil, errors.Join(fmt.Errorf("seed: %w", err), db.Close())
	}
	return a, nil
}

// Start loads the plugins (installing the ones the binary ships) and starts
// the scheduler. One-off commands open an App without it.
func (a *App) Start(ctx context.Context) error {
	if err := a.plugins.LoadAll(ctx); err != nil {
		a.log.Warn("plugins", "err", err)
	}
	if !a.cfg.NoDefaultPlugins {
		a.plugins.EnsureDefaults(ctx)
	}
	// What schema.changed is measured from.
	digest := a.schemaDigest(ctx)
	a.schemas.mu.Lock()
	a.schemas.announced = digest
	a.schemas.mu.Unlock()
	if !a.spawn(a.ctx, "scheduler", a.scheduler) {
		return errors.New("the app is closed")
	}
	if a.cfg.UpdateCheck {
		a.spawn(a.ctx, "update check", a.checkForUpdates)
	}
	return nil
}

// checkForUpdates asks for the newest release a minute after starting, then
// daily, and keeps it when it's newer than this build.
func (a *App) checkForUpdates(ctx context.Context) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 24 * time.Hour
		r, err := update.Latest(ctx)
		if err != nil {
			a.log.Debug("update check", "err", err)
			continue
		}
		if update.Newer(r.Version, a.cfg.Version) {
			a.newer.Store(&r)
			a.log.Info("a new version is available", "version", r.Version, "running", a.cfg.Version, "notes", r.URL)
		}
	}
}

// updateNoticeFor is updateNotice for an Administrator; nil for anyone else.
func (a *App) updateNoticeFor(actor *Actor) any {
	if actor == nil || actor.Role == nil || !actor.Role.System {
		return nil
	}
	return a.updateNotice()
}

// updateNotice is the newer release, for an Administrator: what it is and
// how this install takes it (a container's image, or `librepublish update`).
func (a *App) updateNotice() any {
	if failed, at := update.Failed(a.cfg.DataDir); failed != "" && update.Newer(failed, a.cfg.Version) {
		return O("failed", failed, "failed_at", at.UTC().Format(time.RFC3339), "running", a.cfg.Version)
	}
	r := a.newer.Load()
	if r == nil {
		return nil
	}
	how := "binary"
	if update.InContainer() {
		how = "image"
	}
	return O("version", r.Version, "url", r.URL, "how", how, "button", a.cfg.HandOver != nil)
}

// Close stops what runs in the background, waits for it, then closes the
// database.
func (a *App) Close() error {
	a.once.Do(func() {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		a.cancel()
		a.deploys.Stop()
		a.stopSchemaTimer()
		a.wg.Wait()
		a.err = a.store.Close()
	})
	return a.err
}

// tenant is the site's name in webhooks and the device login: SITE_KEY,
// or the first label of the CMS's host.
func (a *App) tenant() string {
	if a.cfg.SiteKey != "" {
		return a.cfg.SiteKey
	}
	host := strings.TrimPrefix(strings.TrimPrefix(a.BaseURL(), "https://"), "http://")
	return strings.Split(host, ".")[0]
}

// BaseURL is the CMS's own address, which file and admin URLs start with:
// APP_URL (--url) when it's set, else Settings › General's CMS address, else
// http://localhost and the port it listens on (fine on a laptop, wrong
// anywhere else: Site health says so).
func (a *App) BaseURL() string {
	if a.cfg.BaseURL != "" {
		return a.cfg.BaseURL
	}
	if u := a.cmsURL.Load(); u != nil && *u != "" {
		return *u
	}
	return a.localURL()
}

// localURL is the address with nothing set: localhost and the port.
func (a *App) localURL() string {
	host := a.cfg.Addr
	if host == "" || strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	return "http://" + host
}

// addressKnown is whether the CMS's address was given (APP_URL or the
// setting), not guessed.
func (a *App) addressKnown() bool { return a.BaseURL() != a.localURL() }

// spawn runs fn in the background with ctx's values but the app's lifetime:
// it isn't cancelled when a request ends, only when the app closes, and
// Close waits for it. A panic is logged, not fatal. False once the app is
// closed (fn doesn't run).
func (a *App) spawn(ctx context.Context, name string, fn func(context.Context)) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	a.wg.Go(func() {
		ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		defer context.AfterFunc(a.ctx, cancel)()
		defer a.recovered(name)
		fn(ctx)
	})
	return true
}

// recovered logs a panic in background work, where nothing else would.
func (a *App) recovered(name string) {
	if rec := recover(); rec != nil {
		a.log.Error("panic in the background", "task", name, "panic", rec, "stack", string(debug.Stack()))
	}
}

// ---- request context -----------------------------------------------------

// Ctx is one request: who's asking, and how to answer.
type Ctx struct {
	App   *App
	W     http.ResponseWriter
	R     *http.Request
	Actor *Actor

	// The agent envelope's summary and next steps, when a handler has them.
	Summary string
	Crumbs  []Crumb
	// Delivery answers carry an ETag and honor If-None-Match.
	etag     bool
	etagFrom string
	// What a delivery answer's records are, for ?include= (delivery_shape.go).
	owner *deliveryOwner
}

// Crumb is a next step an agent's answer suggests: what it does, and the
// CLI command for it.
// Crumb is a next step an answer suggests: a tool and its arguments, filled
// in from what was just returned (what an MCP client calls), and the same
// as a command line (what a person types).
type Crumb struct {
	Description string         `json:"description"`
	Command     string         `json:"command"`
	Tool        string         `json:"tool,omitempty"`
	Arguments   map[string]any `json:"arguments,omitempty"`
}

// crumb is a next step that isn't a tool call (a URL to pass on).
func crumb(desc, cmd string) Crumb { return Crumb{Description: desc, Command: cmd} }

// step is a next step that's a tool call: tool, then its arguments as
// name, value pairs.
func step(desc, tool string, args ...any) Crumb {
	c := Crumb{Description: desc, Tool: tool}
	line := []string{"librepublish", tool}
	if len(args) > 0 {
		c.Arguments = map[string]any{}
	}
	for i := 0; i+1 < len(args); i += 2 {
		name := fmt.Sprint(args[i])
		c.Arguments[name] = args[i+1]
		v := args[i+1]
		var s string
		switch x := v.(type) {
		case string:
			s = x
		default:
			raw, _ := json.Marshal(x)
			s = string(raw)
		}
		if strings.ContainsAny(s, " '\"{}[]$`&|;<>*?") {
			s = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
		line = append(line, "--"+strings.ReplaceAll(name, "_", "-"), s)
	}
	c.Command = strings.Join(line, " ")
	return c
}

type handlerFunc func(*Ctx) error

// isNotFound is a lookup's "there's no such thing", whichever way it said so.
func isNotFound(err error) bool {
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return true
	}
	e, ok := errors.AsType[*apierr.Error](err)
	return ok && e.Status == http.StatusNotFound
}

func (c *Ctx) fail(err error) {
	var e *apierr.Error
	switch {
	case errors.As(err, &e):
	case errors.Is(err, store.ErrNotFound):
		e = apierr.NotFound("Couldn't find that.")
	default:
		c.App.log.Error("request", "path", c.R.URL.Path, "request_id", requestIDFrom(c.R.Context()), "err", err)
		e = &apierr.Error{Status: 500, Code: "internal_error", Message: "Something went wrong on our side."}
	}
	body := map[string]any{"error": e.Code}
	if e.Message != "" {
		body["message"] = e.Message
	}
	if e.Errors != nil {
		body["errors"] = e.Errors
	}
	if e.Capability != "" {
		body["capability"] = e.Capability
	}
	maps.Copy(body, e.Extra)
	_ = c.JSON(e.Status, body) // the client went away; nothing to do

}

func (c *Ctx) envelope() bool {
	if c.R.Header.Get("X-Agent-Envelope") == "1" {
		return true
	}
	switch strings.ToLower(c.R.URL.Query().Get("envelope")) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// JSON answers with v, in the agent envelope when the caller asked for it.
func (c *Ctx) JSON(status int, v any) error {
	if c.envelope() {
		ok := status < 400
		summary := c.Summary
		if summary == "" {
			summary = defaultSummary(c.R, status, v)
		}
		crumbs := c.Crumbs
		if crumbs == nil || !ok {
			crumbs = []Crumb{}
		}
		v = map[string]any{"status": map[bool]string{true: "ok", false: "error"}[ok], "summary": summary, "data": v, "breadcrumbs": crumbs}
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h := c.W.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	if c.etag && status == 200 {
		src := c.etagFrom
		if src == "" {
			src = string(body)
		}
		sum := sha256.Sum256([]byte(src + "|" + c.R.Header.Get("X-Agent-Envelope")))
		tag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
		h.Set("ETag", tag)
		if match := c.R.Header.Get("If-None-Match"); match != "" && strings.Contains(match, tag) {
			c.W.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	c.W.WriteHeader(status)
	_, err = c.W.Write(body)
	return err
}

func defaultSummary(r *http.Request, status int, v any) string {
	if status >= 400 {
		if m, ok := v.(map[string]any); ok {
			if msg, ok := m["message"].(string); ok {
				return msg
			}
			return fmt.Sprint(m["error"])
		}
		return http.StatusText(status)
	}
	return r.Method + " " + r.URL.Path + " — " + http.StatusText(status) + "."
}

// NoContent answers 204.
func (c *Ctx) NoContent() error {
	c.W.WriteHeader(http.StatusNoContent)
	return nil
}

// maxBody is the most a JSON body may be.
const maxBody = 32 << 20

// Body decodes the request's JSON into v; a body that isn't JSON is a 400,
// one over 32 MB a 413.
func (c *Ctx) Body(v any) error { return c.body(v, false) }

// OptionalBody is Body for a request that may come with none (v is left
// as it is); one that does come must still be JSON.
func (c *Ctx) OptionalBody(v any) error { return c.body(v, true) }

func (c *Ctx) body(v any, optional bool) error {
	data, err := io.ReadAll(http.MaxBytesReader(c.W, c.R.Body, maxBody))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return apierr.TooLarge(maxBody)
		}
		return apierr.BadRequest("Couldn't read the request body.")
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		if optional {
			return nil
		}
		return apierr.BadRequest("The request needs a JSON body.")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return apierr.BadRequest("The body isn't JSON: " + err.Error())
	}
	return nil
}

// Wrapped decodes {"<key>": {…}} into a map, 400 when the key is missing,
// as Rails' params.require did.
func (c *Ctx) Wrapped(key string) (map[string]any, error) {
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return nil, err
	}
	inner, ok := body[key].(map[string]any)
	if !ok {
		return nil, apierr.BadRequest("param is missing or the value is empty: " + key)
	}
	return inner, nil
}

// Query is the query string's key.
func (c *Ctx) Query(key string) string { return c.R.URL.Query().Get(key) }

// Param is the route's {key}.
func (c *Ctx) Param(key string) string { return c.R.PathValue(key) }

// IntParam is the route's {key} as an id; a 404 when it isn't one.
func (c *Ctx) IntParam(key string) (int64, error) {
	n, err := strconv.ParseInt(c.R.PathValue(key), 10, 64)
	if err != nil {
		return 0, apierr.NotFound("Couldn't find " + key + " " + c.R.PathValue(key) + ".")
	}
	return n, nil
}

func (c *Ctx) truthy(key string) bool {
	switch strings.ToLower(c.Query(key)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// paging reads ?page= and ?per= with the given default and cap.
func (c *Ctx) paging(def, most int) (page, per int) {
	page, _ = strconv.Atoi(c.Query("page"))
	per, _ = strconv.Atoi(c.Query("per"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		page = 100000
	}
	if per < 1 {
		per = def
	}
	per = min(per, most)
	return
}

// Need refuses unless the caller's role grants capability.
func (c *Ctx) Need(capability string) error {
	if c.Actor == nil {
		return &apierr.Error{Status: 401, Code: "unauthorized", Message: "Sign in, or send Authorization: Bearer <token>."}
	}
	if !c.Actor.Can(capability) {
		return apierr.Forbidden(capability)
	}
	return nil
}

func (c *Ctx) can(capability string) bool { return c.Actor != nil && c.Actor.Can(capability) }

// ip is the client's address. Behind a proxy it's the rightmost
// X-Forwarded-For entry, the one our proxy appended: everything left of it
// is whatever the client chose to send.
func (c *Ctx) ip() string {
	if c.App.cfg.TrustProxy {
		if fwd := c.R.Header.Values("X-Forwarded-For"); len(fwd) > 0 {
			last := fwd[len(fwd)-1]
			if ip := strings.TrimSpace(last[strings.LastIndex(last, ",")+1:]); ip != "" {
				return ip
			}
		}
	}
	host := c.R.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// ---- routing -------------------------------------------------------------

type route struct {
	auth bool      // false: open to anyone (device login, public posts)
	tool *toolSpec // the agent tool this route is (agent.go)
}

func (a *App) handle(mux *http.ServeMux, pattern string, h handlerFunc, opts ...func(*route)) {
	rt := route{auth: true}
	for _, o := range opts {
		o(&rt)
	}
	if rt.tool != nil {
		a.register(pattern, rt.tool)
	}
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		c := &Ctx{App: a, W: w, R: r}
		defer func() {
			if rec := recover(); rec != nil {
				// A handler cutting the connection on purpose (a broken
				// download) is net/http's to handle.
				if isAbort(rec) {
					panic(rec)
				}
				a.log.Error("panic", "path", r.URL.Path, "request_id", requestIDFrom(r.Context()), "panic", rec, "stack", string(debug.Stack()))
				c.fail(fmt.Errorf("panic: %v", rec))
			}
		}()
		actor, err := a.authenticate(c)
		if err != nil && rt.auth {
			c.fail(err)
			return
		}
		c.Actor = actor
		if rt.auth && actor == nil {
			c.fail(&apierr.Error{Status: 401, Code: "unauthorized", Message: "Sign in, or send Authorization: Bearer <token>."})
			return
		}
		if err := h(c); err != nil {
			c.fail(err)
		}
	})
}

func open(r *route) { r.auth = false }

// Handler is the whole install: the API, the admin, the files.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	a.mux, a.tools = mux, nil
	a.routes(mux)
	a.deriveTools()
	mux.HandleFunc("/mcp", a.mcp)
	a.deliveryRoutes(mux)

	mux.HandleFunc("GET /files/{key}/{rest...}", a.serveFile)
	mux.HandleFunc("GET /up", func(w http.ResponseWriter, r *http.Request) {
		if err := a.store.PingContext(r.Context()); err != nil {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	// The CMS is headless, so its own address is the admin: / is the app,
	// /assets/ its files, and /cms (where it was) redirects to /. Without
	// an admin, / is blank. Any other /api path is the API's 404, in its
	// shape.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			c := &Ctx{App: a, W: w, R: r}
			c.fail(apierr.NotFound("No route matches " + r.Method + " " + r.URL.Path + "."))
			return
		}
		if a.cfg.Admin != nil {
			a.cfg.Admin.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<!doctype html><title></title>"))
			return
		}
		http.NotFound(w, r)
	})
	return a.logRequests(a.recoverPanics(a.cors(securityHeaders(mux))))
}

// ---- request logging -----------------------------------------------------

type requestIDKey struct{}

// requestIDFrom is the request's id (X-Request-ID), "" outside one.
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestID keeps a caller's (a proxy's) X-Request-ID when it looks like
// one, and makes one otherwise.
func requestID(r *http.Request) string {
	id := r.Header.Get("X-Request-ID")
	if len(id) == 0 || len(id) > 128 {
		return hex.EncodeToString(secret.RandomBytes(12))
	}
	for _, ch := range id {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && !strings.ContainsRune("-_.:", ch) {
			return hex.EncodeToString(secret.RandomBytes(12))
		}
	}
	return id
}

// statusWriter notes what was answered, for the log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

// WriteHeader notes the first status written.
func (w *statusWriter) WriteHeader(s int) {
	if w.status == 0 {
		w.status = s
	}
	w.ResponseWriter.WriteHeader(s)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the connection (flushing,
// deadlines).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests gives every request an id and logs one line when it's
// answered (/up, polled by health checks, isn't).
func (a *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := requestID(r)
		w.Header().Set("X-Request-ID", id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if r.URL.Path == "/up" {
				return
			}
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			c := &Ctx{App: a, W: w, R: r}
			a.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", status, "bytes", sw.bytes,
				"duration_ms", time.Since(start).Milliseconds(), "ip", c.ip(), "request_id", id)
		}()
		next.ServeHTTP(sw, r)
	})
}

// recoverPanics answers a panic anywhere (the files, the admin, /up) with
// a 500 and logs where it came from. A handler cutting the connection on
// purpose (http.ErrAbortHandler) is net/http's to handle.
// isAbort is whether a recovered panic is http.ErrAbortHandler: a handler
// cutting the connection on purpose.
func isAbort(rec any) bool {
	err, ok := rec.(error)
	return ok && errors.Is(err, http.ErrAbortHandler)
}

func (a *App) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if isAbort(rec) {
				panic(rec)
			}
			a.log.Error("panic", "path", r.URL.Path, "request_id", requestIDFrom(r.Context()), "panic", rec, "stack", string(debug.Stack()))
			if sw, ok := w.(*statusWriter); !ok || sw.status == 0 {
				http.Error(w, "Something went wrong on our side.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// The admin (/ and its assets) is never framed; files and the API
		// may be embedded by the site.
		if !strings.HasPrefix(r.URL.Path, "/files/") && !strings.HasPrefix(r.URL.Path, "/api") {
			h.Set("X-Frame-Options", "DENY")
		}
		next.ServeHTTP(w, r)
	})
}

// CORS for what a site's browser calls: the delivery API, public posts and
// plugins' routes (a form's submit),
// from the origins in Settings › General (and the public site's own).
func (a *App) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (strings.HasPrefix(r.URL.Path, "/api/v1/") || strings.HasPrefix(r.URL.Path, "/api/public/") || strings.HasPrefix(r.URL.Path, "/api/x/") || r.URL.Path == "/api/quote_requests") && a.originAllowed(r.Context(), origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-None-Match")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Expose-Headers", "ETag, Cache-Tag, X-Total-Count, X-Total-Pages, Link, "+schemaDigestHeader)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) originAllowed(ctx context.Context, origin string) bool {
	for _, o := range a.siteOrigins(ctx) {
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// siteOrigins are the site's own origins: the public site URL's and those
// in Settings › General › Allowed origins. CORS allows them, and a form's
// plain post may be sent back to them.
func (a *App) siteOrigins(ctx context.Context) []string {
	g := a.general(ctx)
	out := []string{}
	for _, o := range append([]string{str(g["site_base_url"])}, strings.FieldsFunc(str(g["cors_origins"]), func(r rune) bool { return r == ',' || r == '\n' || r == ' ' })...) {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// ---- small helpers -------------------------------------------------------

// str, boolOf and intOf are jsonv's readers, short because nearly every
// handler reads its body with them.
func str(v any) string { return jsonv.String(v) }

func boolOf(v any) bool { return jsonv.Bool(v) }

func intOf(v any) (int64, bool) { return jsonv.Int(v) }

func has(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			return v
		}
	}
	return nil
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
