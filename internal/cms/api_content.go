package cms

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/fields"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// The management API (/api): the admin's, the CLI's and agents'. Its shapes
// are the Rails core's, frozen — the CLI and integrations read them.

func (a *App) routes(mux *http.ServeMux) {
	h := func(pattern string, fn handlerFunc, opts ...func(*route)) { a.handle(mux, pattern, fn, opts...) }

	h("GET /api/manifest", a.getManifest)
	h("GET /api/api_tokens/me", a.getMe)
	h("POST /api/api_tokens/rotate", a.rotateMyToken)

	h("GET /api/pages", a.listPages)
	h("POST /api/pages", a.createPage)
	h("POST /api/pages/bulk_destroy", a.bulkDestroy("page"))
	h("POST /api/pages/bulk_update_status", a.bulkStatus("page"))
	h("GET /api/pages/{path...}", a.showPage)
	h("PATCH /api/pages/{path...}", a.updatePage)
	h("PUT /api/pages/{path...}", a.updatePage)
	h("DELETE /api/pages/{path...}", a.destroyPage)

	h("GET /api/collections", a.listCollections)
	h("POST /api/collections", a.createCollection)
	h("POST /api/collections/bulk_destroy", a.bulkDestroyCollections)
	h("GET /api/collections/{slug}", a.showCollection)
	h("PATCH /api/collections/{slug}", a.updateCollection)
	h("PUT /api/collections/{slug}", a.updateCollection)
	h("DELETE /api/collections/{slug}", a.destroyCollection)
	h("PATCH /api/collections/{slug}/schema", a.updateCollectionSchema)
	h("GET /api/collections/{slug}/entries", a.listEntries)
	h("POST /api/collections/{slug}/entries", a.createEntry)
	h("POST /api/collections/{slug}/entries/bulk_destroy", a.bulkDestroy("entry"))
	h("POST /api/collections/{slug}/entries/bulk_update_status", a.bulkStatus("entry"))
	h("GET /api/collections/{slug}/entries/{entry}", a.showEntry)
	h("PATCH /api/collections/{slug}/entries/{entry}", a.updateEntry)
	h("PUT /api/collections/{slug}/entries/{entry}", a.updateEntry)
	h("DELETE /api/collections/{slug}/entries/{entry}", a.destroyEntry)
	h("PATCH /api/collections/{slug}/entries/{entry}/toggle_field", a.entryField(true))
	h("PATCH /api/collections/{slug}/entries/{entry}/update_field", a.entryField(false))

	h("GET /api/globals", a.listGlobals)
	h("POST /api/globals", a.createGlobal)
	h("POST /api/globals/bulk_destroy", a.bulkDestroyGlobals)
	h("GET /api/globals/{slug}", a.showGlobal)
	h("PATCH /api/globals/{slug}", a.updateGlobal)
	h("PUT /api/globals/{slug}", a.updateGlobal)
	h("DELETE /api/globals/{slug}", a.destroyGlobal)
	h("PATCH /api/globals/{slug}/schema", a.updateGlobalSchema)

	h("GET /api/block_types", a.listBlockTypes)
	h("POST /api/block_types", a.createBlockType)
	h("POST /api/block_types/bulk_destroy", a.bulkDestroyBlockTypes)
	h("POST /api/block_types/seed", a.seedBlockTypes)
	h("GET /api/block_types/{slug}", a.showBlockType)
	h("PATCH /api/block_types/{slug}", a.updateBlockType)
	h("PUT /api/block_types/{slug}", a.updateBlockType)
	h("DELETE /api/block_types/{slug}", a.destroyBlockType)

	h("GET /api/revisions", a.listRevisions)
	h("GET /api/revisions/{id}", a.showRevision)
	h("POST /api/revisions/{id}/approve", a.decideRevision(true))
	h("POST /api/revisions/{id}/reject", a.decideRevision(false))

	a.mediaRoutes(h)
	a.adminRoutes(h)
}

// ---- pages ---------------------------------------------------------------

func (a *App) listPages(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	where, args := []string{"1=1"}, []any{}
	if !c.can("pages:write") {
		where = append(where, "status = 'published'")
	}
	if s := c.Query("status"); s != "" {
		where, args = append(where, "status = ?"), append(args, s)
	}
	if l := c.Query("locale"); l != "" {
		where, args = append(where, "locale = ?"), append(args, l)
	}
	if q := c.Query("q"); q != "" {
		where, args = append(where, "(title LIKE ? OR path LIKE ?)"), append(args, "%"+q+"%", "%"+q+"%")
	}
	page, per := c.paging(25, 100)
	cond := "deleted_at IS NULL AND " + strings.Join(where, " AND ")
	total, err := store.Count(c.R.Context(), a.store, "SELECT count(*) FROM pages WHERE "+cond, args...)
	if err != nil {
		return err
	}
	// ?sort=path lists the tree in order (parents before their children);
	// the default is most recently updated first.
	order := "updated_at DESC, id DESC"
	switch c.Query("sort") {
	case "path":
		order = "path, id"
	case "title":
		order = "title COLLATE NOCASE, id"
	}
	list, err := a.pages(c.R.Context(), cond+fmt.Sprintf(" ORDER BY %s LIMIT %d OFFSET %d", order, per, (page-1)*per), args...)
	if err != nil {
		return err
	}
	out := []Obj{}
	locks, err := a.lockedBy(c.R.Context(), c.Actor, "page")
	if err != nil {
		return err
	}
	for _, p := range list {
		o, err := a.pageSummaryJSON(c.R.Context(), p)
		if err != nil {
			return err
		}
		if name, ok := locks[p.ID]; ok {
			o = append(o, kv{"locked_by", name})
		}
		out = append(out, o)
	}
	paged := ""
	if total > per {
		paged = fmt.Sprintf(" · page %d", page)
	}
	c.Summary = fmt.Sprintf("%d %s%s.", total, plural(total, "page"), paged)
	first := "<path>"
	if len(list) > 0 {
		first = list[0].Path
	}
	c.Crumbs = []Crumb{crumb("Open one, blocks expanded", "cms page "+first), crumb("Only drafts", "cms pages --status draft"), crumb("Publish one", "cms publish "+first)}
	return c.JSON(200, O("pages", out, "page", page, "per", per, "total", total))
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func (a *App) findPage(c *Ctx, path string) (*Content, error) {
	return a.pageByPath(c.R.Context(), path, !c.can("pages:write"))
}

func (a *App) showPage(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	path := c.Param("path")
	// A page's own field definitions; a page whose path ends in /schema is
	// reached the same way, as the Rails core's PATCH is.
	if before, ok := strings.CutSuffix(path, "/schema"); ok {
		if p, err := a.findPage(c, before); err == nil {
			return c.JSON(200, O("page", O("id", p.ID, "path", p.Path, "schema", p.Schema)))
		}
	}
	p, err := a.findPage(c, path)
	if err != nil {
		return err
	}
	return a.renderPage(c, 200, p)
}

func (a *App) renderPage(c *Ctx, status int, p *Content) error {
	ctx := c.R.Context()
	blocks := a.expandBlocks(ctx, p.Blocks.List(), false)
	o, err := a.pageJSON(ctx, p, blocks)
	if err != nil {
		return err
	}
	body := O("page", o)
	if c.truthy("resolve") || c.Query("resolve") == "assets" {
		body = body.Set("assets", a.resolveAssets(ctx, a.assetIDs(ctx, "Page", []int64{p.ID})))
	}
	c.Summary = fmt.Sprintf("%s (%s) — %s.", p.Title, p.Path, p.Status)
	c.Crumbs = []Crumb{
		crumb("See it back", "cms page "+p.Path),
		crumb("Edit it", fmt.Sprintf(`cms patch /pages/%s '{"page":{"title":"…"}}'`, p.Path)),
		crumb("Publish it", "cms publish "+p.Path),
		crumb("What links here, before deleting it", "cms refs page "+p.Path),
	}
	return c.JSON(status, body)
}

func (a *App) createPage(c *Ctx) error {
	if err := c.Need("pages:write"); err != nil {
		return err
	}
	params, err := c.Wrapped("page")
	if err != nil {
		return err
	}
	p := &Content{Kind: "page"}
	res, err := a.saveContent(c.R.Context(), c.Actor, p, params)
	if err != nil {
		return err
	}
	return a.renderPage(c, 201, res.Record)
}

func (a *App) updatePage(c *Ctx) error {
	path := c.Param("path")
	if before, ok := strings.CutSuffix(path, "/schema"); ok {
		return a.updatePageSchema(c, before)
	}
	if err := c.Need("pages:write"); err != nil {
		return err
	}
	p, err := a.findPage(c, path)
	if err != nil {
		return err
	}
	if err := a.checkLock(c, "page", p.ID); err != nil {
		return err
	}
	params, err := c.Wrapped("page")
	if err != nil {
		return err
	}
	res, err := a.saveContent(c.R.Context(), c.Actor, p, params)
	if err != nil {
		return err
	}
	return a.writeAnswer(c, res, func() error { return a.renderPage(c, 200, res.Record) })
}

// writeAnswer is a write's 202 (held for review) or 200 {status: unchanged}
// or its ordinary answer.
func (a *App) writeAnswer(c *Ctx, res *writeResult, ok func() error) error {
	if res.Held != nil {
		c.Summary = fmt.Sprintf("Held for review: revision %d. The live content is unchanged until a person approves it.", res.Held.ID)
		c.Crumbs = []Crumb{crumb("Tell the person who asked", res.Held.RecordType+" change waiting at "+a.reviewURL(res.Held.ID))}
		return c.JSON(http.StatusAccepted, a.heldJSON(res.Held, res.Ignored))
	}
	if res.Unchanged {
		c.Summary = "Nothing changed."
		return c.JSON(200, O("status", "unchanged"))
	}
	return ok()
}

func (a *App) updatePageSchema(c *Ctx, path string) error {
	if err := c.Need("pages:write"); err != nil {
		return err
	}
	p, err := a.findPage(c, path)
	if err != nil {
		return err
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	schema := body["schema"]
	if pg, ok := body["page"].(map[string]any); ok && schema == nil {
		schema = pg["schema"]
	}
	if schema == nil {
		schema = body["fields"]
	}
	inv := apierr.Invalid{}
	normalized := normalizeSchema(schema)
	fields.ValidateDefinitions(normalized.Map()["fields"], true, inv, "schema.fields")
	if err := inv.Err(); err != nil {
		return err
	}
	if _, err := a.store.ExecContext(c.R.Context(), `UPDATE pages SET schema = ?, updated_at = ? WHERE id = ?`, string(normalized), store.Now().DB(), p.ID); err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "page.schema_updated", "Page", p.ID, p.Label(), nil)
	a.schemaTouched()
	p.Schema = normalized
	return c.JSON(200, O("page", O("id", p.ID, "path", p.Path, "schema", p.Schema)))
}

func (a *App) destroyPage(c *Ctx) error {
	if err := c.Need("pages:delete"); err != nil {
		return err
	}
	p, err := a.findPage(c, c.Param("path"))
	if err != nil {
		return err
	}
	return a.destroyContent(c, p)
}

func (a *App) destroyContent(c *Ctx, rec *Content) error {
	if rec.Live() && !c.can(rec.Prefix()+":publish") {
		return apierr.Forbidden(rec.Prefix() + ":publish")
	}
	if err := a.checkLock(c, rec.Kind, rec.ID); err != nil {
		return err
	}
	if c.truthy("permanent") {
		if err := c.Need("trash:write"); err != nil {
			return err
		}
		if err := a.purgeContent(c.R.Context(), c.Actor, rec); err != nil {
			return err
		}
	} else if err := a.trashContent(c.R.Context(), c.Actor, rec); err != nil {
		return err
	}
	return c.NoContent()
}

// bulkDestroy: {"slugs": [...]} (paths for pages) or {"ids": [...]}.
func (a *App) bulkDestroy(kind string) handlerFunc {
	return func(c *Ctx) error {
		prefix := map[string]string{"page": "pages", "entry": "entries"}[kind]
		if err := c.Need(prefix + ":delete"); err != nil {
			return err
		}
		recs, err := a.bulkTargets(c, kind)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if r.Live() && !c.can(prefix+":publish") {
				return apierr.Forbidden(prefix + ":publish")
			}
		}
		deleted := []string{}
		for _, r := range recs {
			if r.DeletedAt.Valid {
				continue
			}
			if err := a.trashContent(c.R.Context(), c.Actor, r); err != nil {
				return err
			}
			deleted = append(deleted, r.Address())
		}
		c.Summary = fmt.Sprintf("%d moved to the trash.", len(deleted))
		return c.JSON(200, O("ok", true, "deleted", len(deleted), "count", len(deleted), "items", deleted))
	}
}

func (a *App) bulkStatus(kind string) handlerFunc {
	return func(c *Ctx) error {
		prefix := map[string]string{"page": "pages", "entry": "entries"}[kind]
		if err := c.Need(prefix + ":write"); err != nil {
			return err
		}
		var body map[string]any
		if err := c.Body(&body); err != nil {
			return err
		}
		status := str(body["status"])
		if !slices.Contains(statuses, status) {
			return &apierr.Error{Status: 422, Code: "invalid", Message: "status must be one of draft, published, archived", Errors: apierr.Invalid{"status": {"is not included in the list"}}}
		}
		recs, err := a.bulkTargets(c, kind)
		if err != nil {
			return err
		}
		changed, held := []string{}, []Obj{}
		for _, r := range recs {
			if r.Status == status {
				continue
			}
			res, err := a.saveContent(c.R.Context(), c.Actor, r, map[string]any{"status": status})
			if err != nil {
				return err
			}
			if res.Held != nil {
				held = append(held, a.heldJSON(res.Held, res.Ignored))
				continue
			}
			changed = append(changed, r.Address())
		}
		c.Summary = fmt.Sprintf("%d set to %s.", len(changed), status)
		if len(held) > 0 {
			c.Summary += fmt.Sprintf(" %d held for review.", len(held))
		}
		return c.JSON(200, O("ok", true, "status", status, "updated", len(changed), "count", len(changed), "items", changed, "held", held))
	}
}

func (a *App) bulkTargets(c *Ctx, kind string) ([]*Content, error) {
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return nil, err
	}
	keys := asList(body["slugs"])
	if keys == nil {
		keys = asList(body["paths"])
	}
	ids := asList(body["ids"])
	if len(keys) == 0 && len(ids) == 0 {
		return nil, apierr.BadRequest("Name what to change: {\"slugs\": [...]} or {\"ids\": [...]}.")
	}
	var out []*Content
	ctx := c.R.Context()
	var col *Collection
	if kind == "entry" {
		var err error
		if col, err = a.collectionBySlug(ctx, c.Param("slug")); err != nil {
			return nil, err
		}
	}
	for _, k := range keys {
		var rec *Content
		var err error
		if kind == "page" {
			rec, err = a.pageByPath(ctx, str(k), false)
		} else {
			rec, err = a.entryBySlug(ctx, col.ID, str(k), false)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	for _, v := range ids {
		id, _ := intOf(v)
		rec, err := a.contentByID(ctx, kind, id, false)
		if err != nil {
			return nil, apierr.NotFound(fmt.Sprintf("Couldn't find %s %d.", kind, id))
		}
		if col != nil && rec.CollectionID != col.ID {
			return nil, apierr.NotFound(fmt.Sprintf("Entry %d isn't in %s.", id, col.Slug))
		}
		out = append(out, rec)
	}
	return out, nil
}

// ---- entries -------------------------------------------------------------

func (a *App) listEntries(c *Ctx) error {
	if err := c.Need("entries:read"); err != nil {
		return err
	}
	ctx := c.R.Context()
	col, err := a.collectionBySlug(ctx, c.Param("slug"))
	if err != nil {
		return err
	}
	where, args := []string{"e.collection_id = ?"}, []any{col.ID}
	if !c.can("entries:write") {
		where = append(where, "e.status = 'published'")
	}
	if s := c.Query("status"); s != "" {
		where, args = append(where, "e.status = ?"), append(args, s)
	}
	if l := c.Query("locale"); l != "" {
		where, args = append(where, "e.locale = ?"), append(args, l)
	}
	if q := c.Query("q"); q != "" {
		where, args = append(where, "(e.title LIKE ? OR e.slug LIKE ?)"), append(args, "%"+q+"%", "%"+q+"%")
	}
	page, per := c.paging(25, 100)
	cond := "e.deleted_at IS NULL AND " + strings.Join(where, " AND ")
	total, err := store.Count(ctx, a.store, "SELECT count(*) FROM collection_entries e WHERE "+cond, args...)
	if err != nil {
		return err
	}
	list, err := a.entries(ctx, cond+fmt.Sprintf(" ORDER BY e.updated_at DESC, e.id DESC LIMIT %d OFFSET %d", per, (page-1)*per), args...)
	if err != nil {
		return err
	}
	out := []Obj{}
	ids := []int64{}
	locks, err := a.lockedBy(ctx, c.Actor, "entry")
	if err != nil {
		return err
	}
	for _, e := range list {
		o, err := a.entrySummaryJSON(ctx, e)
		if err != nil {
			return err
		}
		if name, ok := locks[e.ID]; ok {
			o = append(o, kv{"locked_by", name})
		}
		out = append(out, o)
		ids = append(ids, e.ID)
	}
	body := O("collection", col.Slug, "entries", out, "page", page, "per", per, "total", total)
	if c.Query("resolve") != "" {
		body = body.Set("assets", a.resolveAssets(ctx, a.assetIDs(ctx, "CollectionEntry", ids)))
	}
	c.Summary = fmt.Sprintf("%d %s in %s.", total, plural(total, "entry"), col.Slug)
	first := "<slug>"
	if len(list) > 0 {
		first = list[0].Slug
	}
	c.Crumbs = []Crumb{crumb("Open one", fmt.Sprintf("cms entry %s %s", col.Slug, first)), crumb("Only published", fmt.Sprintf("cms entries %s --status published", col.Slug))}
	return c.JSON(200, body)
}

func (a *App) findEntry(c *Ctx) (*Collection, *Content, error) {
	col, err := a.collectionBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return nil, nil, err
	}
	e, err := a.entryBySlug(c.R.Context(), col.ID, c.Param("entry"), !c.can("entries:write"))
	return col, e, err
}

func (a *App) showEntry(c *Ctx) error {
	if err := c.Need("entries:read"); err != nil {
		return err
	}
	_, e, err := a.findEntry(c)
	if err != nil {
		return err
	}
	return a.renderEntry(c, 200, e)
}

func (a *App) renderEntry(c *Ctx, status int, e *Content) error {
	ctx := c.R.Context()
	o, err := a.entryJSON(ctx, e)
	if err != nil {
		return err
	}
	body := O("entry", o)
	if c.Query("resolve") != "" {
		body = body.Set("assets", a.resolveAssets(ctx, a.assetIDs(ctx, "CollectionEntry", []int64{e.ID})))
	}
	c.Summary = fmt.Sprintf("%s (%s/%s) — %s.", e.Title, e.CollectionSlug, e.Slug, e.Status)
	c.Crumbs = []Crumb{
		crumb("See it back", fmt.Sprintf("cms entry %s %s", e.CollectionSlug, e.Slug)),
		crumb("Publish it", fmt.Sprintf("cms entry-status %s published %s", e.CollectionSlug, e.Slug)),
	}
	return c.JSON(status, body)
}

func (a *App) createEntry(c *Ctx) error {
	if err := c.Need("entries:write"); err != nil {
		return err
	}
	col, err := a.collectionBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	params, err := c.Wrapped("entry")
	if err != nil {
		return err
	}
	e := &Content{Kind: "entry", CollectionID: col.ID, CollectionSlug: col.Slug}
	res, err := a.saveContent(c.R.Context(), c.Actor, e, params)
	if err != nil {
		return err
	}
	return a.renderEntry(c, 201, res.Record)
}

func (a *App) updateEntry(c *Ctx) error {
	if err := c.Need("entries:write"); err != nil {
		return err
	}
	_, e, err := a.findEntry(c)
	if err != nil {
		return err
	}
	if err := a.checkLock(c, "entry", e.ID); err != nil {
		return err
	}
	params, err := c.Wrapped("entry")
	if err != nil {
		return err
	}
	res, err := a.saveContent(c.R.Context(), c.Actor, e, params)
	if err != nil {
		return err
	}
	return a.writeAnswer(c, res, func() error { return a.renderEntry(c, 200, res.Record) })
}

// entryField merges one key into an entry's frontmatter: toggle_field flips
// a boolean ({"field": "featured"}), update_field sets one ({"field", "value"}).
func (a *App) entryField(toggle bool) handlerFunc {
	return func(c *Ctx) error {
		if err := c.Need("entries:write"); err != nil {
			return err
		}
		_, e, err := a.findEntry(c)
		if err != nil {
			return err
		}
		if err := a.checkLock(c, "entry", e.ID); err != nil {
			return err
		}
		var body map[string]any
		if err := c.Body(&body); err != nil {
			return err
		}
		field := str(body["field"])
		if field == "" {
			return apierr.BadRequest("param is missing or the value is empty: field")
		}
		fm := e.Frontmatter.Map()
		if toggle {
			fm[field] = !boolOf(fm[field])
		} else {
			fm[field] = body["value"]
		}
		res, err := a.saveContent(c.R.Context(), c.Actor, e, map[string]any{"frontmatter": fm})
		if err != nil {
			return err
		}
		return a.writeAnswer(c, res, func() error { return a.renderEntry(c, 200, res.Record) })
	}
}

func (a *App) destroyEntry(c *Ctx) error {
	if err := c.Need("entries:delete"); err != nil {
		return err
	}
	_, e, err := a.findEntry(c)
	if err != nil {
		return err
	}
	return a.destroyContent(c, e)
}

// ---- revisions -----------------------------------------------------------

func (a *App) listRevisions(c *Ctx) error {
	// Only revisions to what the caller may read: a proposed change shows
	// the content it would make.
	types := []any{}
	for _, t := range []string{"Page", "CollectionEntry", "Global"} {
		if c.can(revisionCap(t, "read")) {
			types = append(types, t)
		}
	}
	if len(types) == 0 {
		return apierr.Forbidden("pages:read")
	}
	status := c.Query("status")
	if status == "" {
		status = "open"
	}
	args := append([]any{status, status}, types...)
	list, err := a.revisions(c.R.Context(), "(? = 'all' OR status = ?) AND record_type IN ("+store.Placeholders(len(types))+") ORDER BY id DESC LIMIT 200", args...)
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, r := range list {
		out = append(out, r.JSON())
	}
	c.Summary = fmt.Sprintf("%d %s revisions.", len(out), status)
	return c.JSON(200, O("revisions", out))
}

func (a *App) showRevision(c *Ctx) error {
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	list, err := a.revisions(c.R.Context(), "id = ?", id)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return apierr.NotFound("Couldn't find that revision.")
	}
	if err := c.Need(revisionCap(list[0].RecordType, "read")); err != nil {
		return err
	}
	return c.JSON(200, O("revision", list[0].JSON()))
}

func (a *App) decideRevision(approve bool) handlerFunc {
	return func(c *Ctx) error {
		id, err := c.IntParam("id")
		if err != nil {
			return err
		}
		var body map[string]any
		if err := c.OptionalBody(&body); err != nil {
			return err
		}
		rev, err := a.settleRevision(c.R.Context(), c.Actor, id, approve, str(body["note"]))
		if err != nil {
			return err
		}
		return c.JSON(200, O("revision", rev.JSON()))
	}
}
