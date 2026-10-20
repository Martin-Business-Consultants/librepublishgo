// Package plugin hosts plugins: Go packages compiled into the binary, each a
// Module that does only what it declared and a person granted. Adding one
// is adding its package to the list the CMS builds the host with
// (internal/cms/plugins.go); installing it, granting it and switching it on
// happen at runtime, with no restart.
//
// What a plugin reaches of the CMS is the *API it's handed on each call,
// holding the host's fixed set of capabilities:
//
//	content:read   API.Content — live pages, entries and globals
//	storage        API.KVGet / KVSet / KVDelete / KVList / KVCount — its own namespace
//	mail           API.Mail — email through Settings › Email
//	media:write    API.PutMedia — a file into the media library
//	http           API.HTTP — outbound requests, only to the hosts it declared
//	log, site      API.Log, API.Site — always
//
// A capability that wasn't granted answers an error instead of acting. A
// plugin is called through what its manifest declares: hooks (events such
// as page.published, to its OnEvent), routes (served under /api/x/<key>/…,
// to the handler each names) and admin screens (described as data: forms
// the admin draws, no code in the admin). A call that panics answers an
// error, and each call's context ends after the time limit. See
// docs/plugins.md.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// Module is a plugin, as its package builds it.
type Module struct {
	Manifest Manifest
	// Handlers serve its routes, by the name each route gives as Export.
	Handlers map[string]Handler
	// OnEvent hears the events its manifest hooks; nil when it hooks none.
	OnEvent func(ctx context.Context, api *API, event string, data json.RawMessage) error
	// Default plugins are installed on first run, granted what they ask for
	// and switched on.
	Default bool
}

// Handler answers one of a plugin's routes.
type Handler func(ctx context.Context, api *API, req *Request) (*Response, error)

// Request is a call to a plugin's route.
type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Headers map[string]string // Content-Type, Accept, User-Agent, Referer, Origin, X-Requested-With
	Body    []byte            // up to the route's MaxBody
	IP      string
	Actor   *Caller // nil on a public route called by no one signed in
}

// Header is the request's header name, in any case.
func (r *Request) Header(name string) string {
	for k, v := range r.Headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// Caller is who's calling a route.
type Caller struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"` // User or ServiceToken
	Capabilities []string `json:"capabilities"`
}

// Response is a route's answer. Set-Cookie is dropped, and it's served with
// Content-Security-Policy: sandbox.
type Response struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// JSON is a response with v as its JSON body.
func JSON(status int, v any) *Response {
	b, err := json.Marshal(v)
	if err != nil {
		return &Response{Status: http.StatusInternalServerError, Headers: map[string]string{"Content-Type": "application/json; charset=utf-8"}, Body: []byte(`{"error":"internal"}`)}
	}
	return &Response{Status: status, Headers: map[string]string{"Content-Type": "application/json; charset=utf-8"}, Body: b}
}

// Manifest is what a plugin says it is and needs.
type Manifest struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Permissions Grants   `json:"permissions"`
	Hooks       []string `json:"hooks"`
	// Events are the webhook events the plugin sends (API.Emit), so
	// Settings › Webhooks can offer them.
	Events     []string        `json:"events,omitempty"`
	Routes     []Route         `json:"routes"`
	Admin      json.RawMessage `json:"admin,omitempty"`
	SiteConfig json.RawMessage `json:"site_config,omitempty"`
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
	ContentRead bool `json:"content:read"`
	Storage     bool `json:"storage"`
	Mail        bool `json:"mail"`
	MediaWrite  bool `json:"media:write"`
	// HTTP is the hosts the plugin may call; SiteHosts among them stands
	// for the site's own (its public URL's and its allowed origins').
	HTTP []string `json:"http"`
}

// SiteHosts is the Grants.HTTP entry that grants the hosts of the site's
// own origins, whatever they are when the plugin calls.
const SiteHosts = "@site"

// MarshalJSON writes no hosts as [], never null.
func (g Grants) MarshalJSON() ([]byte, error) {
	type plain Grants
	if g.HTTP == nil {
		g.HTTP = []string{}
	}
	return json.Marshal(plain(g))
}

// Route is one a plugin serves under /api/x/<key>/.
type Route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Export string `json:"export"` // the handler, in Module.Handlers
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

// Plugin is one installed plugin.
type Plugin struct {
	ID          int64
	Key         string
	Name        string
	Version     string
	Description string
	Manifest    Manifest
	Granted     Grants
	Enabled     bool
	CreatedAt   store.Time
	UpdatedAt   store.Time

	module *Module // nil when the binary doesn't have it
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
	// Site is API.Site's answer: the site's name, address, locale, contact.
	Site(ctx context.Context) (Site, error)
	SendMail(ctx context.Context, m mail.Message) error
	// PutMedia adds a file to the media library and answers it as the
	// API shows assets.
	PutMedia(ctx context.Context, f Media) (map[string]any, error)
	// Content answers a query for live content, shaped as /api/v1 shapes it.
	Content(ctx context.Context, q ContentQuery) (any, error)
	// Audit records what someone did to a plugin.
	Audit(ctx context.Context, actor Actor, action string, id int64, label string, meta map[string]any)
	// Deliver sends a plugin's event to the webhooks that take it, and to
	// the plugins that hook it.
	Deliver(ctx context.Context, event string, data any)
}

// Site is the site a plugin runs on.
type Site struct {
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	CMSURL       string   `json:"cms_url"`
	Locale       string   `json:"locale"`
	ContactEmail string   `json:"contact_email"`
	Origins      []string `json:"origins"` // the site's own, as CORS allows them
	// The brand (Settings › Brand), for what a plugin sends out: the logo's
	// absolute URL (a raster rendition when there is one, for email) and
	// the colors, each "" when it isn't set.
	LogoURL        string `json:"logo_url,omitempty"`
	PrimaryColor   string `json:"primary_color,omitempty"`
	SecondaryColor string `json:"secondary_color,omitempty"`
}

// ContentQuery asks for one of: a page by path, every live page, a
// collection's live entries (or one, by slug), or a global.
type ContentQuery struct {
	Page       string
	Pages      bool
	Collection string
	Entry      string
	Global     string
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

// validate checks a module: its manifest, and a handler for each route.
func (m *Module) validate() error {
	if err := m.Manifest.validate(); err != nil {
		return err
	}
	for _, r := range m.Manifest.Routes {
		if m.Handlers[r.Export] == nil {
			return fmt.Errorf("plugin %s: route %s %s names %q, which has no handler", m.Manifest.Key, r.Method, r.Path, r.Export)
		}
	}
	if len(m.Manifest.Hooks) > 0 && m.OnEvent == nil {
		return fmt.Errorf("plugin %s hooks events but has no OnEvent", m.Manifest.Key)
	}
	return nil
}

// validate checks what a manifest says: its key, name and version, its
// routes and capabilities, and the hosts it asks to reach.
func (m *Manifest) validate() error {
	var errs []string
	if !keyFormat.MatchString(m.Key) {
		errs = append(errs, "key must be lowercase letters, digits and _, starting with a letter")
	}
	if m.Name == "" {
		errs = append(errs, "name can't be blank")
	}
	if m.Version == "" {
		errs = append(errs, "version can't be blank")
	}
	for i, c := range m.Capabilities {
		if !capFormat.MatchString(c.Key) || !strings.HasPrefix(c.Key, m.Key+":") || c.Label == "" {
			errs = append(errs, fmt.Sprintf("capabilities[%d] needs a label and a key \"%s:<name>\"", i, m.Key))
		}
	}
	for i, r := range m.Routes {
		if r.Export == "" || !strings.HasPrefix(r.Path, "/") {
			errs = append(errs, fmt.Sprintf("routes[%d] needs a path starting with / and an export", i))
		}
		if r.MaxBody > maxBody {
			errs = append(errs, fmt.Sprintf("routes[%d].max_body is at most 10 MB", i))
		}
		if r.Capability != "" && r.Capability != "plugins:read" && r.Capability != "plugins:write" && !slices.ContainsFunc(m.Capabilities, func(c Capability) bool { return c.Key == r.Capability }) {
			errs = append(errs, fmt.Sprintf("routes[%d].capability must be one the plugin declares under capabilities", i))
		}
	}
	for _, host := range m.Permissions.HTTP {
		if host == "*" || strings.Contains(host, "/") {
			errs = append(errs, "permissions.http names hosts, not \"*\" or URLs: "+host)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("plugin %s: %s", m.Key, strings.Join(errs, "; "))
	}
	return nil
}

// BuiltIn says whether this binary has the plugin; one it doesn't (a
// plugin from an older build) stays off.
func (p *Plugin) BuiltIn() bool { return p.module != nil }
