package plugin

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	extism "github.com/extism/go-sdk"
	"github.com/tetratelabs/wazero"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// New is a host keeping plugins in db and reaching the CMS through env;
// defaults holds the .wasm files the binary ships.
func New(env Env, db *store.DB, log *slog.Logger, spawn Spawn, defaults fs.FS) *Host {
	return &Host{env: env, db: db, log: log, spawn: spawn, defaults: defaults}
}

// Host loads, runs and installs plugins.
type Host struct {
	env      Env
	db       *store.DB
	log      *slog.Logger
	spawn    Spawn
	defaults fs.FS // the plugins the binary ships, *.wasm

	loadMu sync.Mutex // one LoadAll at a time
	mu     sync.RWMutex
	// plugins and caps are swapped whole by LoadAll, under mu.
	plugins map[string]*Plugin
	caps    []CapGroup
	mail    mailLimiter
}

var keyFormat = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)

const callTimeout = 10 * time.Second

// LoadAll compiles every enabled plugin; one that fails is left off and
// logged, not fatal: a plugin can't break the site. The modules it
// replaces are closed once the calls running on them finish.
func (h *Host) LoadAll(ctx context.Context) error {
	h.loadMu.Lock()
	defer h.loadMu.Unlock()
	rows, err := h.db.QueryContext(ctx, `SELECT id, key, name, version, COALESCE(description, ''), manifest, granted, enabled, created_at, updated_at, wasm FROM plugins ORDER BY key`)
	if err != nil {
		return err
	}
	type loaded struct {
		p    *Plugin
		wasm []byte
	}
	all, err := store.ScanAll(rows, func(r *sql.Rows) (loaded, error) {
		p := &Plugin{}
		var granted store.JSON
		var wasm []byte
		if err := r.Scan(&p.ID, &p.Key, &p.Name, &p.Version, &p.Description, &p.RawManifest, &granted, &p.Enabled, &p.CreatedAt, &p.UpdatedAt, &wasm); err != nil {
			return loaded{}, err
		}
		// An unreadable manifest or grants isn't "asks for nothing, granted
		// nothing": the plugin is left off, and why is logged.
		if err := json.Unmarshal(p.RawManifest, &p.Manifest); err != nil {
			h.log.Warn("plugin manifest unreadable; left off", "plugin", p.Key, "err", err)
			p.Enabled = false
		}
		if len(granted) > 0 {
			if err := json.Unmarshal(granted, &p.Granted); err != nil {
				h.log.Warn("plugin grants unreadable; left off", "plugin", p.Key, "err", err)
				p.Enabled = false
			}
		}
		return loaded{p, wasm}, nil
	})
	if err != nil {
		return err
	}
	plugins := map[string]*Plugin{}
	var groups []CapGroup
	for _, l := range all {
		if l.p.Enabled {
			compiled, err := h.compile(ctx, l.p.Key, l.wasm, l.p.Granted)
			if err != nil {
				h.log.Warn("plugin failed to load; left off", "plugin", l.p.Key, "err", err)
				l.p.Enabled = false
			}
			l.p.compiled = compiled
		}
		plugins[l.p.Key] = l.p
		if l.p.Enabled && len(l.p.Manifest.Capabilities) > 0 {
			g := CapGroup{Group: l.p.Name}
			for _, c := range l.p.Manifest.Capabilities {
				g.Caps = append(g.Caps, c.Key)
				g.Labels = append(g.Labels, c.Label)
			}
			groups = append(groups, g)
		}
	}
	h.mu.Lock()
	old := h.plugins
	h.plugins, h.caps = plugins, groups
	h.mu.Unlock()
	h.retire(old)
	return nil
}

// retire closes replaced modules once their calls finish, in the
// background so a reload doesn't wait on a slow plugin.
func (h *Host) retire(old map[string]*Plugin) {
	if len(old) == 0 {
		return
	}
	closeAll := func(ctx context.Context) {
		if err := closePlugins(context.WithoutCancel(ctx), old); err != nil {
			h.log.Warn("plugins", "err", err)
		}
	}
	if !h.spawn(context.Background(), "plugin reload", closeAll) {
		closeAll(context.Background())
	}
}

func closePlugins(ctx context.Context, plugins map[string]*Plugin) error {
	var errs []error
	for _, p := range plugins {
		if p.compiled != nil {
			p.calls.Wait()
			errs = append(errs, p.compiled.Close(ctx))
		}
	}
	return errors.Join(errs...)
}

// Close closes every module, once the calls running on them finish.
func (h *Host) Close() error {
	h.loadMu.Lock()
	defer h.loadMu.Unlock()
	h.mu.Lock()
	old := h.plugins
	h.plugins, h.caps = nil, nil
	h.mu.Unlock()
	return closePlugins(context.Background(), old)
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

// Get is the plugin installed as key, nil when there's none.
func (h *Host) Get(key string) *Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.plugins[key]
}

// compile builds the module with exactly the host functions its grants
// allow; what wasn't granted isn't there to call.
func (h *Host) compile(ctx context.Context, key string, wasm []byte, grants Grants) (*extism.CompiledPlugin, error) {
	manifest := extism.Manifest{
		Wasm:         []extism.Wasm{extism.WasmData{Data: wasm, Name: key}},
		AllowedHosts: grants.HTTP,
		Timeout:      uint64(callTimeout.Milliseconds()), //nolint:gosec // a positive constant
		Memory:       &extism.ManifestMemory{MaxPages: 1024, MaxHttpResponseBytes: 4 << 20, MaxVarBytes: 1 << 20},
	}
	if manifest.AllowedHosts == nil {
		manifest.AllowedHosts = []string{}
	}
	cfg := extism.PluginConfig{EnableWasi: true}
	if CompilationCache != nil {
		cfg.RuntimeConfig = wazero.NewRuntimeConfig().WithCompilationCache(CompilationCache)
	}
	return extism.NewCompiledPlugin(ctx, manifest, cfg, h.hostFunctions(key, grants))
}

// CompilationCache keeps compiled modules, when it's set (before any
// plugin loads), for the next compile of the same bytes. The server leaves it
// nil; tests, which compile the same few plugins over and over, share one.
var CompilationCache wazero.CompilationCache

// instanceConfig gives each call the real clock and real randomness
// (wazero's defaults are fixed, for reproducible runs).
func instanceConfig() extism.PluginInstanceConfig {
	return extism.PluginInstanceConfig{ModuleConfig: wazero.NewModuleConfig().WithSysWalltime().WithSysNanotime().WithRandSource(crand.Reader)}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// Call runs one export of an enabled plugin with input, in a fresh
// instance, within the time limit.
func (h *Host) Call(ctx context.Context, key, export string, input []byte) ([]byte, error) {
	p := h.acquire(key)
	if p == nil {
		return nil, fmt.Errorf("plugin %s isn't enabled", key)
	}
	defer p.calls.Done()
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	inst, err := p.compiled.Instance(ctx, instanceConfig())
	if err != nil {
		return nil, err
	}
	defer inst.Close(ctx)
	if !inst.FunctionExists(export) {
		return nil, fmt.Errorf("plugin %s has no export %s", key, export)
	}
	exit, out, err := inst.CallWithContext(ctx, export, input)
	if err != nil {
		return nil, fmt.Errorf("plugin %s/%s: %w", key, export, err)
	}
	if exit != 0 {
		return nil, fmt.Errorf("plugin %s/%s exited %d: %s", key, export, exit, inst.GetError())
	}
	return out, nil
}

// acquire is an enabled plugin with its call counted (done with
// p.calls.Done), so a reload can't close it mid-call; nil when it's off.
func (h *Host) acquire(key string) *Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p := h.plugins[key]
	if p == nil || !p.Enabled || p.compiled == nil {
		return nil
	}
	p.calls.Add(1)
	return p
}

// Emit hands an event to every enabled plugin that hooks it, in the
// background: a slow or failing plugin never holds up a save.
func (h *Host) Emit(ctx context.Context, event string, data any) {
	for _, p := range h.List() {
		if !p.Enabled || !slices.Contains(p.Manifest.Hooks, event) {
			continue
		}
		h.spawn(ctx, "plugin hook", func(ctx context.Context) {
			body, _ := json.Marshal(map[string]any{"event": event, "data": data})
			if _, err := h.Call(ctx, p.Key, "on_event", body); err != nil {
				h.log.Warn("plugin hook", "plugin", p.Key, "event", event, "err", err)
			}
		})
	}
}

// Install reads a module's manifest (calling its `manifest` export in a
// sandbox with nothing granted) and stores it, off until a person grants
// what it asks for and enables it.
func (h *Host) Install(ctx context.Context, actor Actor, wasm []byte) (*Plugin, error) {
	probe, err := h.compile(ctx, "probe", wasm, Grants{})
	if err != nil {
		return nil, &apierr.Error{Status: 422, Code: "invalid", Message: "That isn't a WebAssembly plugin this CMS can load: " + err.Error()}
	}
	defer probe.Close(ctx)
	inst, err := probe.Instance(ctx, instanceConfig())
	if err != nil {
		return nil, &apierr.Error{Status: 422, Code: "invalid", Message: "The plugin wouldn't start: " + err.Error()}
	}
	defer inst.Close(ctx)
	if !inst.FunctionExists("manifest") {
		return nil, &apierr.Error{Status: 422, Code: "invalid", Message: "The plugin has no `manifest` export saying what it is and what it needs."}
	}
	_, out, err := inst.CallWithContext(ctx, "manifest", nil)
	if err != nil {
		return nil, &apierr.Error{Status: 422, Code: "invalid", Message: "Its manifest export failed: " + err.Error()}
	}
	var m Manifest
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, &apierr.Error{Status: 422, Code: "invalid", Message: "Its manifest isn't JSON: " + err.Error()}
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	now := store.Now().DB()
	// A new version of an installed plugin keeps its grants where they're
	// still asked for, and stays off until re-enabled if it asks for more.
	var existing int64
	var oldGranted store.JSON
	var wasEnabled bool
	err = h.db.QueryRowContext(ctx, `SELECT id, granted, enabled FROM plugins WHERE key = ?`, m.Key).Scan(&existing, &oldGranted, &wasEnabled)
	granted := Grants{}
	enabled := false
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		var old Grants
		if len(oldGranted) > 0 {
			if err := json.Unmarshal(oldGranted, &old); err != nil {
				return nil, fmt.Errorf("plugin %s: reading its grants: %w", m.Key, err)
			}
		}
		granted = intersectGrants(old, m.Permissions)
		enabled = wasEnabled && grantsCover(granted, m.Permissions)
		_, err = h.db.ExecContext(ctx, `UPDATE plugins SET name=?, version=?, description=?, manifest=?, granted=?, enabled=?, wasm=?, updated_at=? WHERE id=?`,
			m.Name, m.Version, m.Description, string(out), string(store.MustJSON(granted)), enabled, wasm, now, existing)
	} else {
		_, err = h.db.ExecContext(ctx, `INSERT INTO plugins (key, name, version, description, manifest, granted, enabled, wasm, created_at, updated_at) VALUES (?,?,?,?,?,?,0,?,?,?)`,
			m.Key, m.Name, m.Version, m.Description, string(out), string(store.MustJSON(granted)), wasm, now, now)
	}
	if err != nil {
		return nil, err
	}
	h.env.Audit(ctx, actor, "plugin.installed", 0, m.Key+" "+m.Version, map[string]any{"permissions": m.Permissions})
	if err := h.LoadAll(ctx); err != nil {
		return nil, err
	}
	return h.Get(m.Key), nil
}

// validate checks what a manifest says: its key, name and version, its
// routes and capabilities, and the hosts it asks to reach.
func (m *Manifest) validate() error {
	inv := apierr.Invalid{}
	if !keyFormat.MatchString(m.Key) {
		inv.Add("key", "must be lowercase letters, digits and _, starting with a letter")
	}
	if m.Name == "" {
		inv.Add("name", "can't be blank")
	}
	if m.Version == "" {
		inv.Add("version", "can't be blank")
	}
	for i, c := range m.Capabilities {
		if !capFormat.MatchString(c.Key) || !strings.HasPrefix(c.Key, m.Key+":") || c.Label == "" {
			inv.Add(fmt.Sprintf("capabilities[%d]", i), "needs a label and a key \""+m.Key+":<name>\"")
		}
	}
	for i, r := range m.Routes {
		if r.Export == "" || !strings.HasPrefix(r.Path, "/") {
			inv.Add(fmt.Sprintf("routes[%d]", i), "needs a path starting with / and an export")
		}
		if r.MaxBody > maxBody {
			inv.Add(fmt.Sprintf("routes[%d].max_body", i), "is at most 10 MB")
		}
		if r.Capability != "" && r.Capability != "plugins:read" && r.Capability != "plugins:write" && !slices.ContainsFunc(m.Capabilities, func(c Capability) bool { return c.Key == r.Capability }) {
			inv.Add(fmt.Sprintf("routes[%d].capability", i), "must be one the plugin declares under capabilities")
		}
	}
	for _, host := range m.Permissions.HTTP {
		if host == "*" || strings.Contains(host, "/") {
			inv.Add("permissions.http", "names hosts, not \"*\" or URLs: "+host)
		}
	}
	return inv.Err()
}

// intersectGrants is what's both granted and asked for.
func intersectGrants(granted, asked Grants) Grants {
	out := Grants{ContentRead: granted.ContentRead && asked.ContentRead, Storage: granted.Storage && asked.Storage,
		Mail: granted.Mail && asked.Mail, MediaWrite: granted.MediaWrite && asked.MediaWrite, HTTP: []string{}}
	for _, host := range granted.HTTP {
		if slices.Contains(asked.HTTP, host) {
			out.HTTP = append(out.HTTP, host)
		}
	}
	return out
}

// grantsCover says whether granted holds all that's asked for.
func grantsCover(granted, asked Grants) bool {
	if asked.ContentRead && !granted.ContentRead || asked.Storage && !granted.Storage || asked.Mail && !granted.Mail || asked.MediaWrite && !granted.MediaWrite {
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
	if _, err := h.db.ExecContext(ctx, `UPDATE plugins SET granted = ?, enabled = ?, updated_at = ? WHERE key = ?`, string(store.MustJSON(granted)), on, store.Now().DB(), key); err != nil {
		return nil, err
	}
	h.env.Audit(ctx, actor, "plugin.configured", p.ID, key, map[string]any{"granted": granted, "enabled": on})
	if err := h.LoadAll(ctx); err != nil {
		return nil, err
	}
	p = h.Get(key)
	if on && !p.Enabled {
		return p, apierr.Conflict("The plugin failed to load and was left off: see the server log.")
	}
	return p, nil
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
