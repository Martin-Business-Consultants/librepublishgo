package plugin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// New is a host keeping plugins in db and reaching the CMS through env;
// modules are the plugins this binary has. A module whose manifest or
// handlers don't hold up panics: it's a bug in the build, not the site.
func New(env Env, db *store.DB, log *slog.Logger, spawn Spawn, modules ...*Module) *Host {
	h := &Host{env: env, db: db, log: log, spawn: spawn, modules: map[string]*Module{}, emits: make(chan struct{}, maxEmits)}
	for _, m := range modules {
		if err := m.validate(); err != nil {
			panic(err)
		}
		if h.modules[m.Manifest.Key] != nil {
			panic("plugin " + m.Manifest.Key + " is registered twice")
		}
		h.modules[m.Manifest.Key] = m
	}
	return h
}

// Host installs, loads and calls plugins.
type Host struct {
	env     Env
	db      *store.DB
	log     *slog.Logger
	spawn   Spawn
	modules map[string]*Module // what this binary has, by key

	loadMu sync.Mutex // one LoadAll at a time
	mu     sync.RWMutex
	// plugins and caps are swapped whole by LoadAll, under mu.
	plugins map[string]*Plugin
	caps    []CapGroup
	mail    mailLimiter
	// emits holds a slot for each plugin hook running (maxEmits at once).
	emits chan struct{}
}

// maxEmits is how many plugin hooks run at once; the rest wait their turn.
const maxEmits = 8

var keyFormat = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)

const callTimeout = 10 * time.Second

// LoadAll reads the installed plugins. One the binary has whose manifest
// changed (a new build) is upgraded in place: it keeps the grants it still
// asks for, and if it asks for more it's off until someone grants the rest
// (a default plugin is granted what it asks for). One the binary doesn't
// have is left off and logged, not fatal: a plugin can't break the site.
func (h *Host) LoadAll(ctx context.Context) error {
	h.loadMu.Lock()
	defer h.loadMu.Unlock()
	rows, err := h.db.QueryContext(ctx, `SELECT id, key, name, version, COALESCE(description, ''), manifest, granted, enabled, created_at, updated_at FROM plugins ORDER BY key`)
	if err != nil {
		return err
	}
	type row struct {
		p        *Plugin
		manifest store.JSON
	}
	all, err := store.ScanAll(rows, func(r *sql.Rows) (row, error) {
		p := &Plugin{}
		var manifest, granted store.JSON
		if err := r.Scan(&p.ID, &p.Key, &p.Name, &p.Version, &p.Description, &manifest, &granted, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return row{}, err
		}
		// An unreadable manifest or grants isn't "asks for nothing, granted
		// nothing": the plugin is left off, and why is logged.
		if err := json.Unmarshal(manifest, &p.Manifest); err != nil {
			h.log.Warn("plugin manifest unreadable; left off", "plugin", p.Key, "err", err)
			p.Enabled = false
		}
		if len(granted) > 0 {
			if err := json.Unmarshal(granted, &p.Granted); err != nil {
				h.log.Warn("plugin grants unreadable; left off", "plugin", p.Key, "err", err)
				p.Enabled = false
			}
		}
		return row{p, manifest}, nil
	})
	if err != nil {
		return err
	}
	plugins := map[string]*Plugin{}
	var groups []CapGroup
	for _, r := range all {
		p := r.p
		p.module = h.modules[p.Key]
		switch {
		case p.module == nil:
			if p.Enabled {
				h.log.Warn("plugin isn't built into this binary; left off", "plugin", p.Key)
			}
			p.Enabled = false
		case !bytes.Equal(r.manifest, store.MustJSON(p.module.Manifest)):
			if err := h.upgrade(ctx, p); err != nil {
				h.log.Warn("plugin upgrade failed; left off", "plugin", p.Key, "err", err)
				p.Enabled = false
			}
		}
		plugins[p.Key] = p
		if p.Enabled && len(p.Manifest.Capabilities) > 0 {
			g := CapGroup{Group: p.Name}
			for _, c := range p.Manifest.Capabilities {
				g.Caps = append(g.Caps, c.Key)
				g.Labels = append(g.Labels, c.Label)
			}
			groups = append(groups, g)
		}
	}
	h.mu.Lock()
	h.plugins, h.caps = plugins, groups
	h.mu.Unlock()
	return nil
}

// upgrade brings an installed plugin's record up to the module this binary
// has.
func (h *Host) upgrade(ctx context.Context, p *Plugin) error {
	m := p.module.Manifest
	granted := intersectGrants(p.Granted, m.Permissions)
	enabled := p.Enabled && grantsCover(granted, m.Permissions)
	if p.module.Default {
		// First party: what it asks for is granted, and it stays as on or
		// off as it was.
		granted, enabled = m.Permissions, p.Enabled
	}
	now := store.Now()
	if _, err := h.db.ExecContext(ctx, `UPDATE plugins SET name=?, version=?, description=?, manifest=?, granted=?, enabled=?, updated_at=? WHERE id=?`,
		m.Name, m.Version, m.Description, string(store.MustJSON(m)), string(store.MustJSON(granted)), enabled, now.DB(), p.ID); err != nil {
		return err
	}
	if p.Version != m.Version {
		h.env.Audit(ctx, nil, "plugin.upgraded", p.ID, p.Key+" "+m.Version, map[string]any{"from": p.Version, "enabled": enabled})
	}
	p.Name, p.Version, p.Description, p.Manifest, p.Granted, p.Enabled, p.UpdatedAt = m.Name, m.Version, m.Description, m, granted, enabled, now
	return nil
}

// CapGroups is the enabled plugins' capabilities.
func (h *Host) CapGroups() []CapGroup {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.caps
}

// List is every installed plugin, by key.
func (h *Host) List() []*Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*Plugin, 0, len(h.plugins))
	for _, p := range h.plugins {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *Plugin) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// Available is the manifests of the plugins this binary has that aren't
// installed, by key.
func (h *Host) Available() []Manifest {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []Manifest{}
	for key, m := range h.modules {
		if h.plugins[key] == nil {
			out = append(out, m.Manifest)
		}
	}
	slices.SortFunc(out, func(a, b Manifest) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// Get is the plugin installed as key, nil when there's none.
func (h *Host) Get(key string) *Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.plugins[key]
}

// enabled is the plugin installed as key when it's on, and the API its
// grants allow; nil when it's off.
func (h *Host) enabled(key string) (*Plugin, *API) {
	p := h.Get(key)
	if p == nil || !p.Enabled || p.module == nil {
		return nil, nil
	}
	return p, &API{h: h, key: p.Key, grants: p.Granted, events: p.Manifest.Events}
}

// guard runs fn within the time limit, a panic in it answering an error
// rather than taking the server down.
func guard(ctx context.Context, what string, fn func(context.Context) error) (err error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s panicked: %v\n%s", what, r, debug.Stack())
		}
	}()
	return fn(ctx)
}

// Serve hands req to the handler an enabled plugin's route names.
func (h *Host) Serve(ctx context.Context, key, export string, req *Request) (*Response, error) {
	p, api := h.enabled(key)
	if p == nil {
		return nil, fmt.Errorf("plugin %s isn't enabled", key)
	}
	handler := p.module.Handlers[export]
	if handler == nil {
		return nil, fmt.Errorf("plugin %s has no handler %s", key, export)
	}
	var res *Response
	err := guard(ctx, "plugin "+key+"/"+export, func(ctx context.Context) error {
		var err error
		res, err = handler(ctx, api, req)
		return err
	})
	if err == nil && res == nil {
		err = fmt.Errorf("plugin %s/%s answered nothing", key, export)
	}
	return res, err
}

// PluginChecks are the launch checklist's items the enabled plugins add
// (Module.Checks), each marked with its plugin as Source; except leaves
// one out (the checklist's own plugin). A plugin whose checks fail or time
// out says so as one unknown item, rather than holding up the rest.
func (h *Host) PluginChecks(ctx context.Context, except string) []Check {
	out := []Check{}
	for _, p := range h.List() {
		if p.Key == except {
			continue
		}
		p, api := h.enabled(p.Key)
		if p == nil || p.module.Checks == nil {
			continue
		}
		var checks []Check
		err := guard(ctx, "plugin "+p.Key+" checks", func(ctx context.Context) error {
			checks = p.module.Checks(ctx, api)
			return nil
		})
		if err != nil {
			h.log.Warn("plugin checks", "plugin", p.Key, "err", err)
			checks = []Check{{Key: p.Key + ".checks", Group: p.Key, Title: p.Manifest.Name + " can check itself", State: "unknown", Evidence: "Its checks failed: " + truncate(err.Error(), 300)}}
		}
		for _, c := range checks {
			c.Source = p.Key
			out = append(out, c)
		}
	}
	return out
}

// Emit hands an event to every enabled plugin that hooks it, in the
// background: a slow or failing plugin never holds up a save. At most
// maxEmits hooks run at once, so a burst of saves can't run thousands.
func (h *Host) Emit(ctx context.Context, event string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		h.log.Warn("plugin hook", "event", event, "err", err)
		return
	}
	for _, p := range h.List() {
		if !p.Enabled || !slices.Contains(p.Manifest.Hooks, event) {
			continue
		}
		h.spawn(ctx, "plugin hook", func(ctx context.Context) {
			select {
			case h.emits <- struct{}{}:
				defer func() { <-h.emits }()
			case <-ctx.Done():
				return
			}
			p, api := h.enabled(p.Key)
			if p == nil {
				return
			}
			err := guard(ctx, "plugin "+p.Key+" on "+event, func(ctx context.Context) error {
				return p.module.OnEvent(ctx, api, event, body)
			})
			if err != nil {
				h.log.Warn("plugin hook", "plugin", p.Key, "event", event, "err", err)
			}
		})
	}
}

// Install installs a plugin this binary has, off until a person grants what
// it asks for and enables it.
func (h *Host) Install(ctx context.Context, actor Actor, key string) (*Plugin, error) {
	m := h.modules[key]
	if m == nil {
		return nil, apierr.NotFound("This build has no plugin " + key + ".")
	}
	if p := h.Get(key); p != nil {
		return p, nil
	}
	now := store.Now().DB()
	_, err := h.db.ExecContext(ctx, `INSERT INTO plugins (key, name, version, description, manifest, granted, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,0,?,?)
		ON CONFLICT(key) DO NOTHING`,
		m.Manifest.Key, m.Manifest.Name, m.Manifest.Version, m.Manifest.Description, string(store.MustJSON(m.Manifest)), string(store.MustJSON(Grants{HTTP: []string{}})), now, now)
	if err != nil {
		return nil, err
	}
	h.env.Audit(ctx, actor, "plugin.installed", 0, key+" "+m.Manifest.Version, map[string]any{"permissions": m.Manifest.Permissions})
	if err := h.LoadAll(ctx); err != nil {
		return nil, err
	}
	return h.Get(key), nil
}

// intersectGrants is what's both granted and asked for.
func intersectGrants(granted, asked Grants) Grants {
	out := Grants{ContentRead: granted.ContentRead && asked.ContentRead, Storage: granted.Storage && asked.Storage,
		Mail: granted.Mail && asked.Mail, MediaWrite: granted.MediaWrite && asked.MediaWrite, SiteRead: granted.SiteRead && asked.SiteRead, HTTP: []string{}}
	for _, host := range granted.HTTP {
		if slices.Contains(asked.HTTP, host) {
			out.HTTP = append(out.HTTP, host)
		}
	}
	return out
}

// grantsCover says whether granted holds all that's asked for.
func grantsCover(granted, asked Grants) bool {
	if asked.ContentRead && !granted.ContentRead || asked.Storage && !granted.Storage || asked.Mail && !granted.Mail || asked.MediaWrite && !granted.MediaWrite || asked.SiteRead && !granted.SiteRead {
		return false
	}
	for _, host := range asked.HTTP {
		if !slices.Contains(granted.HTTP, host) {
			return false
		}
	}
	return true
}

// Configure sets what's granted (never more than was asked) and whether
// it's on.
func (h *Host) Configure(ctx context.Context, actor Actor, key string, grant *Grants, enabled *bool) (*Plugin, error) {
	p := h.Get(key)
	if p == nil {
		return nil, apierr.NotFound("No plugin " + key + ".")
	}
	granted := p.Granted
	if grant != nil {
		granted = intersectGrants(*grant, p.Manifest.Permissions)
	}
	on := p.Enabled
	if enabled != nil {
		on = *enabled
	}
	if on && p.module == nil {
		return p, apierr.Conflict("This build doesn't have the plugin " + key + ", so it can't be switched on.")
	}
	if _, err := h.db.ExecContext(ctx, `UPDATE plugins SET granted = ?, enabled = ?, updated_at = ? WHERE key = ?`, string(store.MustJSON(granted)), on, store.Now().DB(), key); err != nil {
		return nil, err
	}
	h.env.Audit(ctx, actor, "plugin.configured", p.ID, key, map[string]any{"granted": granted, "enabled": on})
	if err := h.LoadAll(ctx); err != nil {
		return nil, err
	}
	return h.Get(key), nil
}

// Remove uninstalls a plugin, and its storage too when purgeData.
func (h *Host) Remove(ctx context.Context, actor Actor, key string, purgeData bool) error {
	if _, err := h.db.ExecContext(ctx, `DELETE FROM plugins WHERE key = ?`, key); err != nil {
		return err
	}
	if purgeData {
		if _, err := h.db.ExecContext(ctx, `DELETE FROM plugin_kv WHERE plugin_key = ?`, key); err != nil {
			return err
		}
	}
	if err := h.forgetDefault(ctx, key); err != nil {
		return err
	}
	h.env.Audit(ctx, actor, "plugin.removed", 0, key, map[string]any{"purge_data": purgeData})
	return h.LoadAll(ctx)
}
