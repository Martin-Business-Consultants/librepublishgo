// Package plugin hosts plugins: WebAssembly modules (Extism's ABI, so they
// can be written in Go, Rust, TypeScript, Python, …) that can do only what
// they declared and a person granted. Each one is a single .wasm file; installing it is
// uploading it, and loading it needs no restart.
//
// What a plugin can reach is the host's fixed set of capabilities:
//
//	content:read   lp_content(query) — live pages, entries and globals
//	storage        lp_kv_get / lp_kv_set / lp_kv_delete / lp_kv_list — its own namespace
//	mail           lp_mail(message) — email through Settings › Email
//	media:write    lp_media_put(file) — a file into the media library
//	http           outbound requests, only to the hosts it declared
//	log, site      lp_log(message), lp_site() — always
//
// It is called through what its manifest declares: hooks (events such as
// page.published, to its on_event export), routes (served under
// /api/x/<key>/…, to the export each names) and admin screens (described as
// data: forms the admin draws, no code in the admin).
//
// A plugin can't touch the database, the filesystem, other plugins' storage
// or the network beyond its hosts, and a call that runs past its time limit
// is stopped. See docs/plugins.md.
package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
	"time"

	extism "github.com/extism/go-sdk"
	"github.com/tetratelabs/wazero"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// Manifest is what a plugin says it is and needs (its manifest export).
type Manifest struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Permissions Grants          `json:"permissions"`
	Hooks       []string        `json:"hooks"`
	Routes      []Route         `json:"routes"`
	Admin       json.RawMessage `json:"admin,omitempty"`
	SiteConfig  json.RawMessage `json:"site_config,omitempty"`
	// Capabilities it adds to the role editor, each "<key>:<name>", which
	// its routes can require.
	Capabilities []Capability `json:"capabilities"`
}

// Capability is one a plugin adds to the role editor.
type Capability struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Grants is what a plugin asks for, and the subset a person granted.
type Grants struct {
	ContentRead bool     `json:"content:read"`
	Storage     bool     `json:"storage"`
	Mail        bool     `json:"mail"`
	MediaWrite  bool     `json:"media:write"`
	HTTP        []string `json:"http"`
}

// Route is one a plugin serves under /api/x/<key>/.
type Route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Export string `json:"export"`
	// Public routes answer anyone (a form's submissions). The rest need a
	// signed-in caller with Capability: one of the plugin's own, or by
	// default plugins:read for GET and plugins:write for anything else.
	Public     bool   `json:"public"`
	Capability string `json:"capability,omitempty"`
	// MaxBody is the largest request body it takes: 1 MB unless it says
	// more, at most 10 MB (a form with file uploads).
	MaxBody int64 `json:"max_body,omitempty"`
}

const maxBody = 10 << 20

var capFormat = regexp.MustCompile(`^[a-z][a-z0-9_]*:[a-z][a-z0-9_]*$`)

// Plugin is one installed module.
type Plugin struct {
	ID          int64
	Key         string
	Name        string
	Version     string
	Description string
	RawManifest store.JSON // as the plugin declared it
	Manifest    Manifest   // RawManifest, read
	Granted     Grants
	Enabled     bool
	CreatedAt   store.Time
	UpdatedAt   store.Time

	// The compiled module, made on the first call and let go after
	// IdleUnload without one (a compiled module is tens of MB of native
	// code), under mod; active counts the calls running on it.
	mod      sync.Mutex
	compiled *extism.CompiledPlugin
	cache    wazero.CompilationCache // compiled's own, closed with it (cacheDir)
	active   int
	lastUsed time.Time
	// calls is the calls running, which a reload waits out before closing
	// the module.
	calls sync.WaitGroup
}

// Need is the capability a route asks of its caller.
func (r *Route) Need(method string) string {
	if r.Capability != "" {
		return r.Capability
	}
	if method == http.MethodGet || method == http.MethodHead {
		return "plugins:read"
	}
	return "plugins:write"
}

// CapGroup is a plugin's capabilities, as its manifest declares them; the
// enabled plugins' join the catalog.
type CapGroup struct {
	Group  string
	Caps   []string
	Labels []string
}

// Actor is who did something, as the CMS knows them, passed back to
// Env.Audit; nil for the system.
type Actor any

// Env is what plugins reach of the CMS beyond their own storage.
type Env interface {
	Setting(ctx context.Context, key string) (map[string]any, error)
	// ReplaceSetting writes data as key's, replacing what was there.
	ReplaceSetting(ctx context.Context, key string, data map[string]any) error
	// Site is lp_site's answer: the site's name, address, locale, contact.
	Site(ctx context.Context) (map[string]any, error)
	SendMail(ctx context.Context, m mail.Message) error
	// PutMedia adds a file to the media library and answers it as the
	// API shows assets.
	PutMedia(ctx context.Context, f Media) (any, error)
	// Content answers lp_content's JSON query with live content.
	Content(ctx context.Context, query string) (any, error)
	// Audit records what someone did to a plugin.
	Audit(ctx context.Context, actor Actor, action string, id int64, label string, meta map[string]any)
}

// Media is a file a plugin puts in the media library.
type Media struct {
	Filename, ContentType string
	Data                  []byte
	Folder, Alt           string
}

// Spawn runs fn in the background for as long as the app lives; false when
// it's closed (fn doesn't run).
type Spawn func(ctx context.Context, name string, fn func(context.Context)) bool
