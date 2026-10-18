package cms

import (
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/fields"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// The delivery API, /api/v1: what a site reads to build and serve itself,
// read-only, frozen, and only ever what's live. One answer shape:
//
//	{"data": …, "meta": {…}, "included": {"assets": {id: asset}}}
//
// It's on unless Settings › General says otherwise: this core has no theme,
// so "website" (the Rails default, where a theme drew the site) serves only
// what's chosen, as "website_api" does.

func (a *App) deliveryRoutes(mux *http.ServeMux) {
	h := func(pattern string, fn handlerFunc) {
		a.handle(mux, pattern, func(c *Ctx) error {
			key := c.ip()
			if c.Actor.ServiceID != 0 {
				key = fmt.Sprintf("svc:%d", c.Actor.ServiceID)
			} else if c.Actor.UserID != 0 {
				key = fmt.Sprintf("user:%d", c.Actor.UserID)
			}
			if ok, wait := a.limiter.Allow(key); !ok {
				c.W.Header().Set("Retry-After", strconv.Itoa(wait))
				return &apierr.Error{Status: 429, Code: "rate_limited", Message: "Over the delivery API's rate limit (CMS_DELIVERY_RATE_LIMIT)."}
			}
			c.etag = true
			if digest := a.schemaDigest(c.R.Context()); digest != "" {
				c.W.Header().Set(schemaDigestHeader, digest)
			}
			return fn(c)
		})
	}
	h("GET /api/v1/site", a.v1Site)
	h("GET /api/v1/content", a.v1Content)
	h("GET /api/v1/pages", a.v1Pages)
	h("GET /api/v1/pages/{path...}", a.v1Page)
	h("GET /api/v1/collections", a.v1Collections)
	h("GET /api/v1/collections/{slug}", a.v1Collection)
	h("GET /api/v1/collections/{slug}/entries", a.v1Entries)
	h("GET /api/v1/collections/{slug}/entries/{entry}", a.v1Entry)
	h("GET /api/v1/globals", a.v1Globals)
	h("GET /api/v1/globals/{slug}", a.v1Global)
	h("GET /api/v1/schema", a.v1Schema)
	h("GET /api/v1/sitemap", a.v1Sitemap)
	h("GET /api/v1/robots", a.v1Robots)
	h("GET /api/v1/robots.txt", a.v1RobotsTxt)
	h("GET /api/v1/redirects", a.v1Redirects)
	h("GET /api/v1/redirects.txt", a.v1RedirectsTxt)
	h("GET /api/v1/site_health", a.v1SiteHealth)
	h("OPTIONS /api/v1/pages", a.v1PagesOptions)
	h("OPTIONS /api/v1/pages/{path...}", a.v1PagesOptions)
	h("OPTIONS /api/v1/collections/{slug}/entries", a.v1EntriesOptions)
	h("OPTIONS /api/v1/collections/{slug}/entries/{entry}", a.v1EntriesOptions)
	h("OPTIONS /api/v1/globals/{slug}", a.v1GlobalOptions)
}

// ---- serving -------------------------------------------------------------

func (a *App) servingMode() string {
	switch m := str(a.general(context.Background())["serving"]); m {
	case "website_api", "api_only":
		return m
	case "website":
		return "website_api"
	}
	return "api_only"
}

func (a *App) servesPages() bool {
	return a.servingMode() == "api_only" || boolOf(a.general(context.Background())["api_pages"])
}

func (a *App) servesCollection(col *Collection) bool {
	return a.servingMode() == "api_only" || col.APIEnabled
}

func notServed(msg string) error { return apierr.NotFound(msg) }

func requirePages(c *Ctx) error {
	if !c.App.servesPages() {
		return notServed("Pages aren't served over the API: Settings › General › How the site is served.")
	}
	return nil
}

// ---- the answer ----------------------------------------------------------

func (c *Ctx) deliver(data any, meta Obj, assets map[string]Obj, tags ...string) error {
	if meta == nil {
		meta = O()
	}
	body := O("data", c.sparse(data), "meta", meta)
	if inc := c.included(assets); inc != nil {
		body = body.Set("included", inc)
	}
	if len(tags) > 0 {
		seen := []string{}
		for _, t := range tags {
			if t != "" && !slices.Contains(seen, t) {
				seen = append(seen, t)
			}
		}
		c.W.Header().Set("Cache-Tag", strings.Join(seen, ","))
	}
	return c.JSON(200, body)
}

func (c *Ctx) deliveryPaging(total int) (page, per int, meta Obj) {
	page, per = c.paging(50, 100)
	var next any
	if page*per < total {
		next = page + 1
	}
	c.pagingHeaders(page, per, total)
	return page, per, O("page", page, "per", per, "total", total, "next_page", next)
}

// ---- records as a site renders them --------------------------------------

func (a *App) v1PageJSON(ctx context.Context, p *Content) (Obj, error) {
	cat, tags, err := a.taxonomy(ctx, p)
	if err != nil {
		return nil, err
	}
	tr, err := a.translations(ctx, p)
	if err != nil {
		return nil, err
	}
	return O("id", p.ID, "path", p.Path, "slug", p.Slug, "title", p.Title, "locale", p.Locale, "url", a.publicPath(p),
		"blocks", a.deliveredBlocks(ctx, p.Blocks.List()), "frontmatter", a.deliveredData(ctx, fields.Parse(p.Schema), p.Frontmatter.Map()), "seo", deliveredSeo(p.Seo.Map()),
		"published_at", p.PublishedAt, "updated_at", p.UpdatedAt, "category", cat, "tags", tags, "translations", tr), nil
}

func (a *App) v1EntryJSON(ctx context.Context, e *Content) (Obj, error) {
	cat, tags, err := a.taxonomy(ctx, e)
	if err != nil {
		return nil, err
	}
	tr, err := a.translations(ctx, e)
	if err != nil {
		return nil, err
	}
	defs, err := a.contentFields(ctx, e)
	if err != nil {
		return nil, err
	}
	return O("id", e.ID, "slug", e.Slug, "title", e.Title, "locale", e.Locale, "collection", e.CollectionSlug, "url", a.publicPath(e),
		"frontmatter", a.deliveredData(ctx, defs, e.Frontmatter.Map()), "body_markdown", e.BodyMarkdown, "blocks", a.deliveredBlocks(ctx, e.Blocks.List()),
		"seo", deliveredSeo(e.Seo.Map()),
		"published_at", e.PublishedAt, "updated_at", e.UpdatedAt, "category", cat, "tags", tags, "translations", tr), nil
}

func (a *App) v1GlobalJSON(ctx context.Context, g *Global) Obj {
	data, _ := redact(g.Data.Map()).(map[string]any)
	return O("id", g.ID, "slug", g.Slug, "name", g.Name, "data", a.deliveredData(ctx, g.Fields(), data), "updated_at", g.UpdatedAt)
}

func blockCacheTags(blocks []any) []string {
	var out []string
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		switch str(m["type"]) {
		case "collection_list":
			if data, ok := m["data"].(map[string]any); ok && str(data["collection_slug"]) != "" {
				out = append(out, "collection:"+str(data["collection_slug"]))
			}
		case "contact_info":
			out = append(out, "site")
		case "pattern":
			out = append(out, "patterns")
		}
	}
	return out
}

// ---- block expansion -----------------------------------------------------

var contactKeys = []string{"title", "phone", "email", "address_line1", "city", "state", "zip"}
var entrySortable = []string{"published_at", "updated_at", "created_at", "title"}

// expandBlocks resolves what blocks pull in: a collection_list's entries, a
// contact_info's details, a synced pattern's blocks. live: only published
// entries, whatever the block says (the delivery API's).
func (a *App) expandBlocks(ctx context.Context, blocks []any, live bool) []any {
	blocks = a.withPatterns(ctx, blocks)
	out := make([]any, 0, len(blocks))
	for _, item := range blocks {
		b, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		data, _ := b["data"].(map[string]any)
		if data == nil {
			data = map[string]any{}
		}
		switch str(b["type"]) {
		case "collection_list":
			b["resolved"] = a.collectionList(ctx, data, live)
		case "contact_info":
			g := a.general(ctx)
			contact := map[string]any{}
			for _, k := range contactKeys {
				if v, ok := g[k]; ok {
					contact[k] = v
				}
			}
			b["resolved"] = map[string]any{"contact": contact}
		}
		out = append(out, b)
	}
	return out
}

// withPatterns puts each synced pattern's blocks where it's used, in a
// blocks field's lists too, and in patterns used by patterns (three deep:
// a pattern can't include itself forever).
func (a *App) withPatterns(ctx context.Context, blocks []any) []any {
	return a.patternsIn(ctx, blocks, 3)
}

func (a *App) patternsIn(ctx context.Context, blocks []any, depth int) []any {
	out := []any{}
	for _, item := range blocks {
		b, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		data, _ := b["data"].(map[string]any)
		if str(b["type"]) != "pattern" {
			for k, v := range data {
				if l, isList := v.([]any); isList && isBlockList(l) {
					data[k] = a.patternsIn(ctx, l, depth)
				}
			}
			out = append(out, b)
			continue
		}
		var pb store.JSON
		if depth > 0 && a.store.QueryRowContext(ctx, `SELECT blocks FROM block_patterns WHERE slug = ?`, str(data["pattern"])).Scan(&pb) == nil {
			out = append(out, a.patternsIn(ctx, pb.List(), depth-1)...)
		}
	}
	return out
}

// isBlockList is whether l is a list of blocks ({type, data} objects).
func isBlockList(l []any) bool {
	for _, item := range l {
		m, ok := item.(map[string]any)
		if !ok || str(m["type"]) == "" || !has(m, "data") {
			return false
		}
	}
	return len(l) > 0
}

func (a *App) collectionList(ctx context.Context, data map[string]any, live bool) map[string]any {
	slug := str(data["collection_slug"])
	col, err := a.collectionBySlug(ctx, slug)
	if err != nil {
		return map[string]any{"error": "collection not found", "slug": slug, "entries": []any{}, "total": 0}
	}
	where, args := "e.collection_id = ?", []any{col.ID}
	if live || str(data["filter_status"]) != "any" {
		where += " AND e.status = 'published'"
	}
	if tags := str(data["filter_tags"]); tags != "" && col.TagsCollectionID.Valid {
		slugs := []any{}
		for s := range strings.SplitSeq(tags, ",") {
			if s = strings.TrimSpace(s); s != "" {
				slugs = append(slugs, s)
			}
		}
		if len(slugs) > 0 {
			where += ` AND (SELECT count(DISTINCT t.tag_entry_id) FROM taggings t JOIN collection_entries te ON te.id = t.tag_entry_id
				WHERE t.taggable_type = 'CollectionEntry' AND t.taggable_id = e.id AND te.collection_id = ? AND te.slug IN (` + store.Placeholders(len(slugs)) + `)) = ?`
			args = append(append(append(args, col.TagsCollectionID.Int64), slugs...), len(slugs))
		}
	}
	sortBy := str(data["sort_by"])
	if !slices.Contains(entrySortable, sortBy) {
		sortBy = "published_at"
	}
	dir := "DESC"
	if str(data["sort_dir"]) == "asc" {
		dir = "ASC"
	}
	total, err := store.Count(ctx, a.store, "SELECT count(*) FROM collection_entries e WHERE e.deleted_at IS NULL AND "+where, args...)
	if err != nil {
		return map[string]any{"error": err.Error(), "entries": []any{}, "total": 0}
	}
	q := where + fmt.Sprintf(" ORDER BY e.%s %s, e.id %s", sortBy, dir, dir)
	if limit, ok := intOf(data["limit"]); ok && limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	list, err := a.entries(ctx, q, args...)
	if err != nil {
		return map[string]any{"error": err.Error(), "entries": []any{}, "total": 0}
	}
	entries := []Obj{}
	for _, e := range list {
		cat, tags, _ := a.taxonomy(ctx, e)
		var fm, seo any = e.Frontmatter, e.Seo
		if live {
			fm, seo = a.deliveredData(ctx, col.Fields(), e.Frontmatter.Map()), deliveredSeo(e.Seo.Map())
		}
		entries = append(entries, O("id", e.ID, "slug", e.Slug, "title", e.Title, "status", e.Status, "locale", e.Locale,
			"collection", e.CollectionSlug, "url", a.publicPath(e), "frontmatter", fm, "seo", seo,
			"published_at", e.PublishedAt, "updated_at", e.UpdatedAt, "category", cat, "tags", tags))
	}
	out := map[string]any{"collection": map[string]any{"slug": col.Slug, "name": col.Name}, "entries": entries, "total": total}
	if g := str(data["group_by"]); g != "" {
		out["groups"] = a.groupEntries(ctx, col, entries, g)
	}
	return out
}

// groupEntries splits a block's entries by key: by the collection's
// categories or tags (in title order), or else by a frontmatter field's
// values (in the order they first appear).
func (a *App) groupEntries(ctx context.Context, col *Collection, entries []Obj, key string) []any {
	switch key {
	case "category":
		return a.groupByPool(ctx, col.CategoriesCollectionID, entries, func(e Obj, slug string) bool {
			cat, _ := e.Get("category").(Obj)
			return cat != nil && cat.Get("slug") == slug
		})
	case "tags":
		return a.groupByPool(ctx, col.TagsCollectionID, entries, func(e Obj, slug string) bool {
			tags, _ := e.Get("tags").([]Obj)
			return slices.ContainsFunc(tags, func(t Obj) bool { return t.Get("slug") == slug })
		})
	}
	groups := []any{}
	order := []string{}
	byValue := map[string][]Obj{}
	for _, e := range entries {
		fm, _ := e.Get("frontmatter").(store.JSON)
		v := str(fm.Map()[key])
		if _, ok := byValue[v]; !ok {
			order = append(order, v)
		}
		byValue[v] = append(byValue[v], e)
	}
	for _, v := range order {
		groups = append(groups, map[string]any{"key": v, "label": v, "entries": byValue[v]})
	}
	return groups
}

// groupByPool is a group for each live entry of the pool collection that
// some of entries are in, in title order.
func (a *App) groupByPool(ctx context.Context, pool store.NullInt, entries []Obj, in func(e Obj, slug string) bool) []any {
	groups := []any{}
	if !pool.Valid {
		return groups
	}
	refs, _ := a.entries(ctx, "e.collection_id = ? AND e.status = 'published' ORDER BY e.title", pool.Int64)
	for _, ref := range refs {
		var group []Obj
		for _, e := range entries {
			if in(e, ref.Slug) {
				group = append(group, e)
			}
		}
		if len(group) > 0 {
			groups = append(groups, map[string]any{"key": ref.Slug, "label": ref.Title, "entries": group})
		}
	}
	return groups
}

// ---- endpoints -----------------------------------------------------------

func (a *App) siteLocales(ctx context.Context) []string {
	locales := []string{a.defaultLocale()}
	found, err := store.Strings(ctx, a.store.DB, `SELECT DISTINCT locale FROM pages WHERE status = 'published' AND deleted_at IS NULL
		UNION SELECT DISTINCT locale FROM collection_entries WHERE status = 'published' AND deleted_at IS NULL`)
	if err != nil {
		// The default locale alone still answers; the failure is logged.
		a.log.Error("site locales", "err", err)
	}
	for _, l := range found {
		if l != "" && !slices.Contains(locales, l) {
			locales = append(locales, l)
		}
	}
	return locales
}

func (a *App) v1Site(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	g := a.general(c.R.Context())
	contact := map[string]any{}
	for _, k := range contactKeys {
		if v, ok := g[k]; ok {
			contact[k] = v
		}
	}
	plugins, config := []string{}, map[string]any{}
	for _, p := range a.plugins.List() {
		if p.Enabled {
			plugins = append(plugins, p.Key)
			if len(p.Manifest.SiteConfig) > 0 {
				config[p.Key] = p.Manifest.SiteConfig
			}
		}
	}
	return c.deliver(O("name", str(g["title"]), "url", str(g["site_base_url"]), "default_locale", a.defaultLocale(),
		"locales", a.siteLocales(c.R.Context()), "contact", contact, "plugins", plugins, "plugin_config", config, "api_version", "v1",
		"image_widths", slices.Clone(deliveryWidths), "branding", a.brandingJSON(c.R.Context(), false)),
		O("cms_url", a.BaseURL(), "cms_version", a.cfg.Version), nil, "site")
}

func (a *App) v1Pages(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	if err := requirePages(c); err != nil {
		return err
	}
	ctx := c.R.Context()
	where, args := "status = 'published'", []any{}
	if l := c.Query("locale"); l != "" {
		where, args = where+" AND locale = ?", append(args, l)
	}
	total, err := store.Count(ctx, a.store, "SELECT count(*) FROM pages WHERE deleted_at IS NULL AND "+where, args...)
	if err != nil {
		return err
	}
	page, per, meta := c.deliveryPaging(total)
	list, err := a.pages(ctx, where+fmt.Sprintf(" ORDER BY id LIMIT %d OFFSET %d", per, (page-1)*per), args...)
	if err != nil {
		return err
	}
	data, ids, tags := []Obj{}, []int64{}, []string{"pages"}
	for _, p := range list {
		o, err := a.v1PageJSON(ctx, p)
		if err != nil {
			return err
		}
		data = append(data, o)
		ids = append(ids, p.ID)
		tags = append(tags, "page:"+p.Path)
		tags = append(tags, blockCacheTags(p.Blocks.List())...)
	}
	c.owns("Page", ids...)
	return c.deliver(data, meta, a.resolveAssets(ctx, a.assetIDs(ctx, "Page", ids)), tags...)
}

func (a *App) v1Page(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	if err := requirePages(c); err != nil {
		return err
	}
	ctx := c.R.Context()
	p, err := a.pageByPath(ctx, c.Param("path"), true)
	if err != nil {
		return err
	}
	o, err := a.v1PageJSON(ctx, p)
	if err != nil {
		return err
	}
	c.owns("Page", p.ID)
	return c.deliver(o, nil, a.resolveAssets(ctx, a.assetIDs(ctx, "Page", []int64{p.ID})), append([]string{"page:" + p.Path}, blockCacheTags(p.Blocks.List())...)...)
}

func (a *App) servedCollections(ctx context.Context) ([]*Collection, error) {
	all, err := a.collections(ctx, "1=1 ORDER BY slug")
	if err != nil {
		return nil, err
	}
	out := []*Collection{}
	for _, col := range all {
		if a.servesCollection(col) {
			out = append(out, col)
		}
	}
	return out, nil
}

func (a *App) v1CollectionJSON(col *Collection) Obj {
	return O("id", col.ID, "slug", col.Slug, "name", col.Name, "enable_blocks", col.EnableBlocks, "fields", col.FieldsJSON())
}

func (a *App) v1Collections(c *Ctx) error {
	if err := c.Need("collections:read"); err != nil {
		return err
	}
	list, err := a.servedCollections(c.R.Context())
	if err != nil {
		return err
	}
	data, tags := []Obj{}, []string{}
	for _, col := range list {
		data = append(data, a.v1CollectionJSON(col))
		tags = append(tags, "collection:"+col.Slug)
	}
	return c.deliver(data, nil, nil, tags...)
}

func (a *App) v1ServedCollection(c *Ctx) (*Collection, error) {
	col, err := a.collectionBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return nil, err
	}
	if !a.servesCollection(col) {
		return nil, notServed("The " + col.Slug + " collection isn't served over the API: its settings › Available over the API.")
	}
	return col, nil
}

func (a *App) v1Collection(c *Ctx) error {
	if err := c.Need("collections:read"); err != nil {
		return err
	}
	col, err := a.v1ServedCollection(c)
	if err != nil {
		return err
	}
	return c.deliver(a.v1CollectionJSON(col), nil, nil, "collection:"+col.Slug)
}

func (a *App) v1Entries(c *Ctx) error {
	if err := c.Need("entries:read"); err != nil {
		return err
	}
	col, err := a.v1ServedCollection(c)
	if err != nil {
		return err
	}
	ctx := c.R.Context()
	where, args := "e.collection_id = ? AND e.status = 'published'", []any{col.ID}
	if l := c.Query("locale"); l != "" {
		where, args = where+" AND e.locale = ?", append(args, l)
	}
	total, err := store.Count(ctx, a.store, "SELECT count(*) FROM collection_entries e WHERE e.deleted_at IS NULL AND "+where, args...)
	if err != nil {
		return err
	}
	page, per, meta := c.deliveryPaging(total)
	list, err := a.entries(ctx, where+fmt.Sprintf(" ORDER BY e.published_at DESC, e.id DESC LIMIT %d OFFSET %d", per, (page-1)*per), args...)
	if err != nil {
		return err
	}
	data, ids := []Obj{}, []int64{}
	for _, e := range list {
		o, err := a.v1EntryJSON(ctx, e)
		if err != nil {
			return err
		}
		data = append(data, o)
		ids = append(ids, e.ID)
	}
	c.owns("CollectionEntry", ids...)
	return c.deliver(data, meta, a.resolveAssets(ctx, a.assetIDs(ctx, "CollectionEntry", ids)), "collection:"+col.Slug)
}

func (a *App) v1Entry(c *Ctx) error {
	if err := c.Need("entries:read"); err != nil {
		return err
	}
	col, err := a.v1ServedCollection(c)
	if err != nil {
		return err
	}
	ctx := c.R.Context()
	e, err := a.entryBySlug(ctx, col.ID, c.Param("entry"), true)
	if err != nil {
		return err
	}
	o, err := a.v1EntryJSON(ctx, e)
	if err != nil {
		return err
	}
	c.owns("CollectionEntry", e.ID)
	return c.deliver(o, nil, a.resolveAssets(ctx, a.assetIDs(ctx, "CollectionEntry", []int64{e.ID})), "entry:"+col.Slug+"/"+e.Slug)
}

func (a *App) v1Globals(c *Ctx) error {
	if err := c.Need("globals:read"); err != nil {
		return err
	}
	if err := requirePages(c); err != nil {
		return err
	}
	ctx := c.R.Context()
	list, err := a.globals(ctx, "1=1 ORDER BY slug")
	if err != nil {
		return err
	}
	data, ids, tags := []Obj{}, []int64{}, []string{}
	for _, g := range list {
		data = append(data, a.v1GlobalJSON(ctx, g))
		ids = append(ids, g.ID)
		tags = append(tags, "global:"+g.Slug)
	}
	c.owns("Global", ids...)
	return c.deliver(data, nil, a.resolveAssets(ctx, a.assetIDs(ctx, "Global", ids)), tags...)
}

func (a *App) v1Global(c *Ctx) error {
	if err := c.Need("globals:read"); err != nil {
		return err
	}
	if err := requirePages(c); err != nil {
		return err
	}
	ctx := c.R.Context()
	g, err := a.globalBySlug(ctx, c.Param("slug"))
	if err != nil {
		return err
	}
	c.owns("Global", g.ID)
	return c.deliver(a.v1GlobalJSON(ctx, g), nil, a.resolveAssets(ctx, a.assetIDs(ctx, "Global", []int64{g.ID})), "global:"+g.Slug)
}

// v1Content is every live page, entry and global, or — given a previous
// read's cursor — what changed since, with what stopped being live.
func (a *App) v1Content(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	ctx := c.R.Context()
	now := time.Now().UTC()
	since := readCursor(c.Query("since"), now)
	full := since == nil
	changed := func(col string) (string, []any) {
		if full {
			return "1=1", nil
		}
		return col + " > ?", []any{store.TimeOf(*since).DB()}
	}

	pages, entries, globals := []Obj{}, []Obj{}, []Obj{}
	removed := map[string][]any{"pages": {}, "entries": {}, "globals": {}}
	var pageIDs, entryIDs, globalIDs []int64

	if a.servesPages() {
		w, args := changed("updated_at")
		list, err := a.pages(ctx, "status = 'published' AND "+w+" ORDER BY id", args...)
		if err != nil {
			return err
		}
		for _, p := range list {
			o, err := a.v1PageJSON(ctx, p)
			if err != nil {
				return err
			}
			pages = append(pages, o)
			pageIDs = append(pageIDs, p.ID)
		}
		if !full {
			gone, err := a.pages(ctx, "(deleted_at IS NOT NULL OR status != 'published') AND updated_at > ? AND (deleted_at IS NULL OR deleted_at IS NOT NULL)", store.TimeOf(*since).DB())
			if err != nil {
				return err
			}
			for _, p := range gone {
				removed["pages"] = append(removed["pages"], O("id", p.ID, "path", p.Path))
			}
		}
	}
	if c.can("entries:read") {
		var err error
		if entries, entryIDs, removed["entries"], err = a.contentEntries(ctx, since); err != nil {
			return err
		}
	}
	if c.can("globals:read") && a.servesPages() {
		w, args := changed("updated_at")
		list, err := a.globals(ctx, w+" ORDER BY slug", args...)
		if err != nil {
			return err
		}
		for _, g := range list {
			globals = append(globals, a.v1GlobalJSON(ctx, g))
			globalIDs = append(globalIDs, g.ID)
		}
		if !full {
			gone, err := a.globals(ctx, "deleted_at IS NOT NULL AND deleted_at > ?", store.TimeOf(*since).DB())
			if err != nil {
				return err
			}
			for _, g := range gone {
				removed["globals"] = append(removed["globals"], O("id", g.ID, "slug", g.Slug))
			}
		}
	}

	assets := map[string]Obj{}
	maps.Copy(assets, a.resolveAssets(ctx, a.assetIDs(ctx, "Page", pageIDs)))
	maps.Copy(assets, a.resolveAssets(ctx, a.assetIDs(ctx, "CollectionEntry", entryIDs)))
	maps.Copy(assets, a.resolveAssets(ctx, a.assetIDs(ctx, "Global", globalIDs)))
	data := O("pages", pages, "entries", entries, "globals", globals)
	if !full {
		data = data.Set("removed", removed)
	}
	cursor := base64.RawURLEncoding.EncodeToString([]byte(now.Format(time.RFC3339Nano)))
	// The tag stands for the content it was built from, not the bytes: each
	// read carries a fresh cursor.
	c.etagFrom = a.contentVersion(ctx) + "|" + c.Query("since") + "|" + fmt.Sprint(c.can("entries:read"), c.can("globals:read"))
	return c.deliver(data, O("cursor", cursor, "full", full, "since", c.Query("since")), assets, "content")
}

// readCursor is the time a /content cursor stands for; nil (read it all)
// for none, one that doesn't parse, or one over 30 days old.
func readCursor(cur string, now time.Time) *time.Time {
	raw, err := base64.RawURLEncoding.DecodeString(cur)
	if cur == "" || err != nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil || now.Sub(t) >= 30*24*time.Hour {
		return nil
	}
	return &t
}

// contentEntries is /content's entries: every live one in the served
// collections, or (since a time) those that changed and those that
// stopped being live.
func (a *App) contentEntries(ctx context.Context, since *time.Time) (entries []Obj, ids []int64, removed []any, err error) {
	entries, removed = []Obj{}, []any{}
	cols, err := a.servedCollections(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, col := range cols {
		where, args := "e.collection_id = ? AND e.status = 'published' ORDER BY e.id", []any{col.ID}
		if since != nil {
			where, args = "e.collection_id = ? AND e.status = 'published' AND e.updated_at > ? ORDER BY e.id", append(args, store.TimeOf(*since).DB())
		}
		list, err := a.entries(ctx, where, args...)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, e := range list {
			o, err := a.v1EntryJSON(ctx, e)
			if err != nil {
				return nil, nil, nil, err
			}
			entries = append(entries, o)
			ids = append(ids, e.ID)
		}
		if since == nil {
			continue
		}
		gone, err := a.entries(ctx, "e.collection_id = ? AND (e.deleted_at IS NOT NULL OR e.status != 'published') AND e.updated_at > ? AND (e.deleted_at IS NULL OR e.deleted_at IS NOT NULL)", col.ID, store.TimeOf(*since).DB())
		if err != nil {
			return nil, nil, nil, err
		}
		for _, e := range gone {
			removed = append(removed, O("id", e.ID, "collection", e.CollectionSlug, "slug", e.Slug))
		}
	}
	return entries, ids, removed, nil
}

// contentVersion changes whenever anything /content is built from does.
func (a *App) contentVersion(ctx context.Context) string {
	var v string
	err := a.store.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT max(updated_at) FROM pages), '') || '/' || (SELECT count(*) FROM pages) || '/' ||
		COALESCE((SELECT max(updated_at) FROM collection_entries), '') || '/' || (SELECT count(*) FROM collection_entries) || '/' ||
		COALESCE((SELECT max(updated_at) FROM globals), '') || '/' || COALESCE((SELECT max(deleted_at) FROM globals), '') || '/' ||
		COALESCE((SELECT max(updated_at) FROM collections), '') || '/' || COALESCE((SELECT max(updated_at) FROM assets), '') || '/' ||
		COALESCE((SELECT max(updated_at) FROM settings), '')`).Scan(&v)
	if err != nil {
		// Unknown, so never the same twice: no false 304.
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return v
}

func (a *App) v1Schema(c *Ctx) error {
	if err := c.Need("block_types:read"); err != nil {
		return err
	}
	data, digest, err := a.schema(c.R.Context())
	if err != nil {
		return err
	}
	return c.deliver(data, O("schema_digest", digest, "cms_version", a.cfg.Version), nil, "schema")
}

// sitemap is every URL on the public site, with hreflang alternates.
func (a *App) sitemap(ctx context.Context) ([]Obj, error) {
	var recs []*Content
	if a.servesPages() {
		ps, err := a.pages(ctx, "status = 'published' ORDER BY path")
		if err != nil {
			return nil, err
		}
		recs = append(recs, ps...)
	}
	cols, err := a.servedCollections(ctx)
	if err != nil {
		return nil, err
	}
	for _, col := range cols {
		es, err := a.entries(ctx, "e.collection_id = ? AND e.status = 'published' ORDER BY e.slug", col.ID)
		if err != nil {
			return nil, err
		}
		recs = append(recs, es...)
	}
	out := []Obj{}
	for _, r := range recs {
		seo := r.Seo.Map()
		if boolOf(seo["noindex"]) || boolOf(seo["exclude_from_sitemap"]) {
			continue
		}
		alts := []Obj{}
		tr, _ := a.translations(ctx, r)
		for _, t := range tr {
			alts = append(alts, O("hreflang", t.Get("locale"), "href", t.Get("path")))
		}
		out = append(out, O("loc", a.publicURL(r), "path", a.publicPath(r), "lastmod", r.UpdatedAt.ISO(), "locale", r.Locale,
			"type", r.Kind, "alternates", alts))
	}
	return out, nil
}

func (a *App) v1Sitemap(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	urls, err := a.sitemap(c.R.Context())
	if err != nil {
		return err
	}
	return c.deliver(urls, O("total", len(urls)), nil, "sitemap")
}

func (a *App) robotsText(ctx context.Context) (string, string) {
	s := a.settingOr(ctx, "search")
	if custom := str(s["robots_txt"]); custom != "" {
		return custom, "custom"
	}
	text := "User-agent: *\nAllow: /\n"
	if base := strings.TrimRight(str(a.general(ctx)["site_base_url"]), "/"); base != "" {
		text += "\nSitemap: " + base + "/sitemap.xml\n"
	}
	return text, "generated"
}

func (a *App) v1Robots(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	text, mode := a.robotsText(c.R.Context())
	return c.deliver(O("text", text, "mode", mode), nil, nil, "robots")
}

func (a *App) v1RobotsTxt(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	text, _ := a.robotsText(c.R.Context())
	c.W.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.W.Header().Set("Cache-Tag", "robots")
	_, err := c.W.Write([]byte(text))
	return err
}

func (a *App) v1Redirects(c *Ctx) error {
	if err := c.Need("redirects:read"); err != nil {
		return err
	}
	list, err := a.orderedRedirects(c.R.Context())
	if err != nil {
		return err
	}
	data := []Obj{}
	for _, r := range list {
		data = append(data, O("source", r.Source, "destination", r.Destination, "status", r.Status, "wildcard", r.Wildcard))
	}
	return c.deliver(data, nil, nil, "redirects")
}

// orderedRedirects: exact rules first, then wildcards longest first, as an
// edge applies them.
func (a *App) orderedRedirects(ctx context.Context) ([]*Redirect, error) {
	list, err := a.redirects(ctx, "active = 1")
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(list, func(x, y *Redirect) int {
		switch {
		case x.Wildcard != y.Wildcard:
			if x.Wildcard {
				return 1
			}
			return -1
		case x.Wildcard:
			return cmp.Compare(len(y.Source), len(x.Source))
		}
		return cmp.Compare(x.ID, y.ID)
	})
	return list, nil
}

// v1RedirectsTxt is a Cloudflare _redirects file.
func (a *App) v1RedirectsTxt(c *Ctx) error {
	if err := c.Need("redirects:read"); err != nil {
		return err
	}
	list, err := a.orderedRedirects(c.R.Context())
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, r := range list {
		dest := r.Destination
		if r.Wildcard && !strings.Contains(dest, ":splat") && strings.HasSuffix(dest, "*") {
			dest = strings.TrimSuffix(dest, "*") + ":splat"
		}
		status := r.Status
		if status == 410 {
			status = 404
		}
		fmt.Fprintf(&b, "%s %s %d\n", r.Source, dest, status)
	}
	c.W.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.W.Header().Set("Cache-Tag", "redirects")
	_, err = c.W.Write([]byte(b.String()))
	return err
}

// ---- site health ---------------------------------------------------------

func (a *App) healthChecks(ctx context.Context) []Obj {
	var checks []Obj
	add := func(key, state, message string) {
		checks = append(checks, O("key", key, "state", state, "message", message))
	}
	g := a.general(ctx)
	if str(g["site_base_url"]) == "" {
		add("site_url", "fails", "Settings › General has no public site URL: webhooks and the sitemap can only give relative addresses.")
	} else {
		add("site_url", "passes", "The public site URL is set.")
	}
	if a.addressKnown() {
		add("cms_url", "passes", "The CMS's own address is set: file and image URLs start with it.")
	} else {
		add("cms_url", "fails", "The CMS doesn't know its own address, so file and image URLs start with "+a.BaseURL()+": set Settings › General's CMS address (or APP_URL).")
	}
	pages, _ := a.pages(ctx, "status = 'published'")
	var noDesc, longTitle []string
	for _, p := range pages {
		seo := p.Seo.Map()
		if str(seo["meta_description"]) == "" {
			noDesc = append(noDesc, p.Path)
		}
		if t := str(seo["meta_title"]); len([]rune(t)) > 60 || (t == "" && len([]rune(p.Title)) > 60) {
			longTitle = append(longTitle, p.Path)
		}
	}
	if len(noDesc) > 0 {
		add("meta_descriptions", "fails", fmt.Sprintf("%d live pages have no meta description: %s", len(noDesc), strings.Join(firstN(noDesc, 5), ", ")))
	} else {
		add("meta_descriptions", "passes", "Every live page has a meta description.")
	}
	if len(longTitle) > 0 {
		add("title_length", "yours_to_judge", fmt.Sprintf("%d page titles are over 60 characters: %s", len(longTitle), strings.Join(firstN(longTitle, 5), ", ")))
	} else {
		add("title_length", "passes", "Page titles fit in search results.")
	}
	home, err := store.Count(ctx, a.store, `SELECT count(*) FROM pages WHERE path = 'home' AND status = 'published' AND deleted_at IS NULL`)
	switch {
	case err != nil:
		add("home_page", "fails", "Couldn't check: "+err.Error())
	case home == 0:
		add("home_page", "fails", "There's no live page at \"home\" (the site's /).")
	default:
		add("home_page", "passes", "The home page is live.")
	}
	noAlt, err := store.Count(ctx, a.store, `SELECT count(*) FROM assets a JOIN content_references r ON r.ref_type = 'asset' AND r.ref_id = CAST(a.id AS TEXT)
		JOIN active_storage_attachments att ON att.record_type = 'Asset' AND att.record_id = a.id JOIN active_storage_blobs b ON b.id = att.blob_id
		WHERE a.deleted_at IS NULL AND COALESCE(a.alt, '') = '' AND b.content_type LIKE 'image/%'`)
	switch {
	case err != nil:
		add("image_alt", "fails", "Couldn't check: "+err.Error())
	case noAlt > 0:
		add("image_alt", "fails", fmt.Sprintf("%d images in use have no alt text.", noAlt))
	default:
		add("image_alt", "passes", "Every image in use has alt text.")
	}
	return checks
}

func firstN(l []string, n int) []string {
	if len(l) > n {
		return l[:n]
	}
	return l
}

func (a *App) v1SiteHealth(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	return c.deliver(a.healthChecks(c.R.Context()), nil, nil, "site")
}

func (a *App) siteHealth(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	checks := a.healthChecks(c.R.Context())
	fails := 0
	for _, ch := range checks {
		if ch.Get("state") == "fails" {
			fails++
		}
	}
	c.Summary = fmt.Sprintf("%d checks, %d failing.", len(checks), fails)
	return c.JSON(200, O("checks", checks, "failing", fails))
}
