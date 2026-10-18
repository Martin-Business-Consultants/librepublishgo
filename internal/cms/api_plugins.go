package cms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

func (a *App) pluginRoutes(h func(string, handlerFunc, ...func(*route))) {
	h("GET /api/plugins", a.listPlugins, tool("list_plugins", "List the installed plugins."))
	h("POST /api/plugins", a.installPlugin)
	h("GET /api/plugins/{key}", a.showPlugin)
	h("GET /api/plugins/{key}/admin", a.pluginAdmin)
	h("GET /api/plugin_menus", a.pluginMenus)
	h("PATCH /api/plugins/{key}", a.configurePlugin)
	h("DELETE /api/plugins/{key}", a.removePlugin)
	// A plugin's own routes, as its manifest declares them.
	h("/api/x/{key}/{rest...}", a.pluginRoute, open)
}

func (a *App) listPlugins(c *Ctx) error {
	if err := c.Need("plugins:read"); err != nil {
		return err
	}
	out := []Obj{}
	for _, p := range a.plugins.List() {
		out = append(out, pluginJSON(p))
	}
	return c.JSON(200, O("plugins", out))
}

func (a *App) showPlugin(c *Ctx) error {
	if err := c.Need("plugins:read"); err != nil {
		return err
	}
	p := a.plugins.Get(c.Param("key"))
	if p == nil {
		return apierr.NotFound("No plugin " + c.Param("key") + ".")
	}
	return c.JSON(200, O("plugin", pluginJSON(p)))
}

// screensFor is a plugin's admin screens the actor may open: those whose
// source route it may call.
func screensFor(p *plugin.Plugin, actor *Actor) (admin map[string]any, screens []map[string]any) {
	if err := json.Unmarshal(p.Manifest.Admin, &admin); err != nil {
		return nil, nil
	}
	all, _ := admin["screens"].([]any)
	for _, raw := range all {
		s, _ := raw.(map[string]any)
		src, _, _ := strings.Cut(str(s["source"]), "?")
		for i := range p.Manifest.Routes {
			r := &p.Manifest.Routes[i]
			if r.Path == src && (r.Method == "" || strings.EqualFold(r.Method, "GET")) && (r.Public || actor.Can(r.Need("GET"))) {
				screens = append(screens, s)
				break
			}
		}
	}
	return admin, screens
}

// pluginAdmin is what the admin draws a plugin's screens from, for anyone
// who may open one of them.
func (a *App) pluginAdmin(c *Ctx) error {
	p := a.plugins.Get(c.Param("key"))
	if p == nil || !p.Enabled {
		return apierr.NotFound("No plugin " + c.Param("key") + ".")
	}
	admin, screens := screensFor(p, c.Actor)
	if len(screens) == 0 {
		return c.Need("plugins:read")
	}
	writes := []string{}
	for _, r := range p.Manifest.Routes {
		if !strings.EqualFold(r.Method, "GET") && (r.Public || c.Actor.Can(r.Need(r.Method))) {
			writes = append(writes, r.Path)
		}
	}
	return c.JSON(200, O("plugin", O("key", p.Key, "name", p.Name, "version", p.Version, "menu", admin["menu"], "screens", screens, "writes", writes)))
}

// pluginMenus is each enabled plugin's place in the admin menu, with the
// screens the actor may open.
func (a *App) pluginMenus(c *Ctx) error {
	if c.Actor == nil {
		return c.Need("pages:read")
	}
	out := []Obj{}
	for _, p := range a.plugins.List() {
		if !p.Enabled {
			continue
		}
		admin, screens := screensFor(p, c.Actor)
		menu, _ := admin["menu"].(map[string]any)
		if menu == nil || len(screens) == 0 {
			continue
		}
		items := []Obj{}
		for _, s := range screens {
			if label := str(s["menu"]); label != "" {
				items = append(items, O("screen", s["id"], "label", label))
			}
		}
		out = append(out, O("key", p.Key, "label", menu["label"], "icon", menu["icon"], "group", menu["group"], "badge", menu["badge"], "items", items))
	}
	return c.JSON(200, O("menus", out))
}

// maxPluginWasm is the largest plugin module an install takes.
const maxPluginWasm = 64 << 20

func (a *App) installPlugin(c *Ctx) error {
	if err := c.Need("plugins:write"); err != nil {
		return err
	}
	// The module, plus room for a multipart form's boundaries.
	c.R.Body = http.MaxBytesReader(c.W, c.R.Body, maxPluginWasm+1<<20)
	tooLarge := func(err error) bool {
		_, ok := errors.AsType[*http.MaxBytesError](err)
		return ok
	}
	var src io.Reader = c.R.Body
	if strings.HasPrefix(c.R.Header.Get("Content-Type"), "multipart/") {
		f, _, err := c.R.FormFile("file")
		if tooLarge(err) {
			return apierr.TooLarge(maxPluginWasm)
		} else if err != nil {
			return apierr.BadRequest("Upload the .wasm file as \"file\".")
		}
		defer f.Close()
		src = f
	}
	wasm, err := io.ReadAll(io.LimitReader(src, maxPluginWasm+1))
	if tooLarge(err) || len(wasm) > maxPluginWasm {
		return apierr.TooLarge(maxPluginWasm)
	} else if err != nil {
		return err
	}
	if len(wasm) < 8 || string(wasm[:4]) != "\x00asm" {
		return apierr.BadRequest("That isn't a .wasm file.")
	}
	p, err := a.plugins.Install(c.R.Context(), c.Actor, wasm)
	if err != nil {
		return err
	}
	c.Summary = fmt.Sprintf("Installed %s %s, off until its permissions are granted.", p.Name, p.Version)
	return c.JSON(201, O("plugin", pluginJSON(p)))
}

func (a *App) configurePlugin(c *Ctx) error {
	if err := c.Need("plugins:write"); err != nil {
		return err
	}
	var body struct {
		Plugin struct {
			Granted *plugin.Grants `json:"granted"`
			Enabled *bool          `json:"enabled"`
		} `json:"plugin"`
	}
	if err := c.Body(&body); err != nil {
		return err
	}
	p, err := a.plugins.Configure(c.R.Context(), c.Actor, c.Param("key"), body.Plugin.Granted, body.Plugin.Enabled)
	if err != nil {
		return err
	}
	return c.JSON(200, O("plugin", pluginJSON(p)))
}

func (a *App) removePlugin(c *Ctx) error {
	if err := c.Need("plugins:write"); err != nil {
		return err
	}
	if a.plugins.Get(c.Param("key")) == nil {
		return apierr.NotFound("No plugin " + c.Param("key") + ".")
	}
	if err := a.plugins.Remove(c.R.Context(), c.Actor, c.Param("key"), c.truthy("purge_data")); err != nil {
		return err
	}
	return c.NoContent()
}

// pluginRoute hands a request to the plugin export its manifest names. The
// plugin gets {method, path, query, headers, body, actor} and answers
// {status, headers, body} (or anything else, sent as JSON).
func (a *App) pluginRoute(c *Ctx) error {
	p := a.plugins.Get(c.Param("key"))
	if p == nil || !p.Enabled {
		return apierr.NotFound("No plugin " + c.Param("key") + ".")
	}
	path := "/" + c.Param("rest")
	var rt *plugin.Route
	for i := range p.Manifest.Routes {
		r := &p.Manifest.Routes[i]
		if r.Path == path && (r.Method == "" || strings.EqualFold(r.Method, c.R.Method)) {
			rt = r
			break
		}
	}
	if rt == nil {
		return apierr.NotFound("Plugin " + p.Key + " has no route " + c.R.Method + " " + path + ".")
	}
	if rt.Public && c.Actor == nil {
		if ok, wait := a.pluginCalls.Allow(p.Key + " " + c.ip()); !ok {
			c.W.Header().Set("Retry-After", strconv.Itoa(wait))
			return &apierr.Error{Status: 429, Code: "rate_limited", Message: "Too many requests; try again in a minute."}
		}
	}
	if !rt.Public {
		if err := c.Need(rt.Need(c.R.Method)); err != nil {
			return err
		}
	}
	limit := rt.MaxBody
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(c.R.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return &apierr.Error{Status: 413, Code: "too_large", Message: fmt.Sprintf("The request is larger than this route takes (%d MB).", limit>>20)}
	}
	headers := map[string]string{}
	for _, k := range []string{"Content-Type", "Accept", "User-Agent", "Referer", "Origin", "X-Requested-With"} {
		if v := c.R.Header.Get(k); v != "" {
			headers[k] = v
		}
	}
	var actor any
	if c.Actor != nil {
		actor = O("name", c.Actor.Name, "type", c.Actor.Type(), "capabilities", a.capabilitiesOf(c.Actor.Role))
	}
	in, _ := json.Marshal(O("method", c.R.Method, "path", path, "query", c.R.URL.Query(), "headers", headers, "body", string(body), "ip", c.ip(), "actor", actor))
	out, err := a.plugins.Call(c.R.Context(), p.Key, rt.Export, in)
	if err != nil {
		a.log.Warn("plugin route", "plugin", p.Key, "err", err)
		return &apierr.Error{Status: 502, Code: "plugin_error", Message: "The plugin failed to answer."}
	}
	var resp struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    *string           `json:"body"`
	}
	if json.Unmarshal(out, &resp) == nil && resp.Status != 0 {
		for k, v := range resp.Headers {
			if !strings.EqualFold(k, "Set-Cookie") {
				c.W.Header().Set(k, v)
			}
		}
		if c.W.Header().Get("Content-Type") == "" {
			c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		c.W.Header().Set("Content-Security-Policy", "sandbox")
		c.W.WriteHeader(resp.Status)
		if resp.Body != nil {
			c.W.Write([]byte(*resp.Body))
		}
		return nil
	}
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.WriteHeader(http.StatusOK)
	_, err = c.W.Write(out)
	return err
}

// pluginJSON is a plugin as the API shows it.
func pluginJSON(p *plugin.Plugin) Obj {
	return O("id", p.ID, "key", p.Key, "name", p.Name, "version", p.Version, "description", p.Description,
		"requested", p.Manifest.Permissions, "granted", p.Granted, "enabled", p.Enabled, "hooks", p.Manifest.Hooks,
		"routes", p.Manifest.Routes, "admin", p.Manifest.Admin, "created_at", p.CreatedAt, "updated_at", p.UpdatedAt)
}
