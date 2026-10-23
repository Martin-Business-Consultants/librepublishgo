// Package fuse is Fuse's pricing as a plugin: a site's rate book (service
// and product types, products, discounts, fees, mileage tiers, zones,
// locations, ZIPs) kept in the plugin's storage with a draft and published
// versions, and priced by a Go port of Fuse's calculator (plugins/fuse/engine)
// that answers what Fuse's quoting API answers.
//
// Pricing only: nothing about a quote is stored. A quote travels as a
// signed token (token.go) that verifies without storage. No leads, no CRM,
// no emails.
//
// Routes (/api/x/fuse/…):
//
//	GET|POST /quote          every service priced for a ZIP in one call; a token on POST {mint: true}
//	GET      /service-area   Fuse's GET /api/service_areas/:zip
//	GET      /catalog        Fuse's config catalog, for build-time prices
//	/admin/…                 the rate editor, import, price tester, versions
//
// See docs/fuse.md.
package fuse

import (
	"context"
	"encoding/json"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

const pluginVersion = "1.0.0"

// call is one call of the plugin: what the host hands it and the request.
type call struct {
	ctx context.Context //nolint:containedctx // a call lives as long as its context
	h   host
	req request
}

type request struct{ *plugin.Request }

func handle(fn func(c *call) *plugin.Response) plugin.Handler {
	return func(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
		return fn(&call{ctx: ctx, h: api, req: request{req}}), nil
	}
}

// actor is who's calling, for "published by".
func (c *call) actor() string {
	if c.req.Request != nil && c.req.Actor != nil && c.req.Actor.Name != "" {
		return c.req.Actor.Name
	}
	return "someone"
}

func reply(status int, v any) *plugin.Response { return plugin.JSON(status, v) }

func replyRaw(status int, headers map[string]string, body string) *plugin.Response {
	return &plugin.Response{Status: status, Headers: headers, Body: []byte(body)}
}

// replyPublic is an answer browsers and builds may cache a minute.
func replyPublic(v any) *plugin.Response {
	return replyRaw(200, map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "public, max-age=60"}, mustJSON(v))
}

func invalid(errs map[string][]string) *plugin.Response {
	return reply(422, map[string]any{"error": "invalid", "message": "Validation failed", "errors": errs})
}

func notFound(msg string) *plugin.Response {
	return reply(404, map[string]string{"error": "not_found", "message": msg})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Module is the plugin, for the CMS to build its host with.
func Module() *plugin.Module {
	read, write, pub, secrets := "fuse:read", "fuse:write", "fuse:publish", "fuse:secrets"
	r := func(method, path, export, capability string) plugin.Route {
		return plugin.Route{Method: method, Path: path, Export: export, Capability: capability}
	}
	public := func(method, path, export string) plugin.Route {
		return plugin.Route{Method: method, Path: path, Export: export, Public: true}
	}
	importRoute := r("POST", "/admin/import", "import", write)
	importRoute.MaxBody = 10 << 20
	checkImport := r("POST", "/admin/import/check", "import_check", write)
	checkImport.MaxBody = 10 << 20
	zipsBulk := r("POST", "/admin/zips_bulk", "zips_bulk_save", write)
	zipsBulk.MaxBody = 10 << 20

	routes := []plugin.Route{
		public("GET", "/quote", "quote"),
		public("POST", "/quote", "quote"),
		public("GET", "/service-area", "service_area"),
		public("GET", "/catalog", "catalog"),

		r("GET", "/admin/overview", "overview", read),
		r("POST", "/admin/publish", "publish", pub),
		r("POST", "/admin/discard", "discard", write),
		r("POST", "/admin/rollback", "rollback", pub),
		r("GET", "/admin/versions", "versions", read),
		r("POST", "/admin/token/rotate", "rotate_token", secrets),
		r("GET", "/admin/settings", "settings", read),
		r("POST", "/admin/settings", "settings_save", write),
		r("POST", "/admin/settings/check_key", "check_key", secrets),
		r("GET", "/admin/mileage", "mileage", read),
		r("POST", "/admin/mileage", "mileage_save", write),
		r("GET", "/admin/zips", "zips", read),
		r("GET", "/admin/zips_bulk", "zips_bulk", read),
		zipsBulk,
		r("POST", "/admin/zip/delete", "zip_delete", write),
		r("GET", "/admin/import", "import_form", read),
		importRoute,
		checkImport,
		r("GET", "/admin/test", "test_form", read),
		r("POST", "/admin/test", "test_save", read),
		r("POST", "/admin/test/price", "test_price", read),
		r("POST", "/admin/test/token", "test_token", read),
	}
	handlers := map[string]plugin.Handler{
		"quote":          handle((*call).quote),
		"service_area":   handle((*call).serviceArea),
		"catalog":        handle((*call).catalog),
		"overview":       handle((*call).overview),
		"publish":        handle((*call).publish),
		"discard":        handle((*call).discard),
		"rollback":       handle((*call).rollback),
		"versions":       handle((*call).versions),
		"rotate_token":   handle((*call).rotateToken),
		"settings":       handle((*call).settings),
		"settings_save":  handle((*call).saveSettings),
		"check_key":      handle((*call).checkKey),
		"mileage":        handle((*call).mileage),
		"mileage_save":   handle((*call).saveMileage),
		"zips":           handle((*call).zips),
		"zips_bulk":      handle((*call).zipsBulkForm),
		"zips_bulk_save": handle((*call).saveZipsBulk),
		"zip_delete":     handle((*call).deleteZip),
		"import_form":    handle((*call).importForm),
		"import":         handle((*call).importBundle),
		"import_check":   handle((*call).checkBundle),
		"test_form":      handle((*call).testForm),
		"test_save":      handle((*call).saveTest),
		"test_price":     handle((*call).testPrice),
		"test_token":     handle((*call).testToken),
	}
	// The rate editor's tables: a list, a record, its save and its delete each.
	for _, k := range kinds {
		routes = append(routes,
			r("GET", "/admin/"+k.plural, "list_"+k.plural, read),
			r("GET", "/admin/"+k.singular, "show_"+k.singular, read),
			r("POST", "/admin/"+k.singular, "save_"+k.singular, write),
			r("POST", "/admin/"+k.singular+"/delete", "delete_"+k.singular, write),
		)
		handlers["list_"+k.plural] = handle(func(c *call) *plugin.Response { return c.listKind(k) })
		handlers["show_"+k.singular] = handle(func(c *call) *plugin.Response { return c.showKind(k) })
		handlers["save_"+k.singular] = handle(func(c *call) *plugin.Response { return c.saveKind(k) })
		handlers["delete_"+k.singular] = handle(func(c *call) *plugin.Response { return c.deleteKind(k) })
	}

	return &plugin.Module{
		Manifest: plugin.Manifest{
			Key:         "fuse",
			Name:        "Fuse pricing",
			Version:     pluginVersion,
			Description: "Prices quotes as Fuse does, from a rate book kept here: products, fees, discounts, zones, ZIPs and mileage (Google distances). Nothing about a quote is stored.",
			Permissions: plugin.Grants{Storage: true, MediaWrite: true, HTTP: []string{googleHost}},
			Events:      []string{"fuse.rates_published"},
			Capabilities: []plugin.Capability{
				{Key: read, Label: "See the rate book and test prices"},
				{Key: write, Label: "Edit and import the draft rate book"},
				{Key: pub, Label: "Publish and roll back rates"},
				{Key: secrets, Label: "Set the Google key and rotate the quote-token key"},
			},
			Routes: routes,
			Admin: json.RawMessage(mustJSON(map[string]any{
				"menu":    map[string]any{"label": "Pricing", "icon": "tag", "group": "Tools"},
				"screens": screens(),
			})),
			SiteConfig: json.RawMessage(mustJSON(map[string]any{
				"quote": "/api/x/fuse/quote", "service_area": "/api/x/fuse/service-area?zip=<zip>", "catalog": "/api/x/fuse/catalog",
			})),
		},
		Handlers: handlers,
		Checks:   checks,
	}
}
