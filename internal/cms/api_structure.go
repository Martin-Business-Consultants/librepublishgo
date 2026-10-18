package cms

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// The block types the CMS ships, exported from the Rails core's
// BlockType::Defaults so both seed the same pack.
//
//go:embed starter_blocks.json
var starterBlocks []byte

// ---- collections ---------------------------------------------------------

func (a *App) listCollections(c *Ctx) error {
	if err := c.Need("collections:read"); err != nil {
		return err
	}
	list, err := a.collections(c.R.Context(), "1=1 ORDER BY slug")
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, col := range list {
		out = append(out, a.collectionJSON(c.R.Context(), col))
	}
	c.Summary = fmt.Sprintf("%d %s.", len(out), plural(len(out), "collection"))
	if len(list) > 0 {
		c.Crumbs = []Crumb{step("Its entries", "list_entries", "slug", list[0].Slug)}
	}
	return c.JSON(200, O("collections", out))
}

func (a *App) showCollection(c *Ctx) error {
	if err := c.Need("collections:read"); err != nil {
		return err
	}
	ctx := c.R.Context()
	col, err := a.collectionBySlug(ctx, c.Param("slug"))
	if err != nil {
		return err
	}
	where := "e.collection_id = ?"
	if !c.can("entries:write") {
		where += " AND e.status = 'published'"
	}
	entries, err := a.entries(ctx, where+" ORDER BY e.updated_at DESC LIMIT 50", col.ID)
	if err != nil {
		return err
	}
	list := []Obj{}
	if c.can("entries:read") {
		for _, e := range entries {
			o, err := a.entrySummaryJSON(ctx, e)
			if err != nil {
				return err
			}
			list = append(list, o)
		}
	}
	return c.JSON(200, O("collection", append(a.collectionJSON(ctx, col), kv{"entries", list})))
}

func (a *App) createCollection(c *Ctx) error {
	if err := c.Need("collections:write"); err != nil {
		return err
	}
	p, err := c.Wrapped("collection")
	if err != nil {
		return err
	}
	col := &Collection{}
	if err := a.saveCollection(c.R.Context(), c.Actor, col, p); err != nil {
		return err
	}
	c.Crumbs = []Crumb{step("Add an entry", "create_entry", "slug", col.Slug, "entry", O("slug", "…", "title", "…"))}
	return c.JSON(201, O("collection", a.collectionJSON(c.R.Context(), col)))
}

func (a *App) updateCollection(c *Ctx) error {
	if err := c.Need("collections:write"); err != nil {
		return err
	}
	col, err := a.collectionBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	p, err := c.Wrapped("collection")
	if err != nil {
		return err
	}
	if err := a.saveCollection(c.R.Context(), c.Actor, col, p); err != nil {
		return err
	}
	return c.JSON(200, O("collection", a.collectionJSON(c.R.Context(), col)))
}

// schemaBody reads a schema write: {"fields": […]}, {"schema": …}, or a
// bare list (what update_collection_schema sends).
func schemaBody(c *Ctx, wrapper string) (any, error) {
	var body any
	if err := c.Body(&body); err != nil {
		return nil, err
	}
	if list, ok := body.([]any); ok {
		return list, nil
	}
	m, _ := body.(map[string]any)
	if w, ok := m[wrapper].(map[string]any); ok {
		m = w
	}
	if s, ok := m["schema"]; ok {
		return s, nil
	}
	if f, ok := m["fields"]; ok {
		return f, nil
	}
	return nil, apierr.BadRequest("Send the fields: {\"fields\": [...]}.")
}

func (a *App) updateCollectionSchema(c *Ctx) error {
	if err := c.Need("collections:write"); err != nil {
		return err
	}
	col, err := a.collectionBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	schema, err := schemaBody(c, "collection")
	if err != nil {
		return err
	}
	if err := a.saveCollection(c.R.Context(), c.Actor, col, map[string]any{"schema": schema}); err != nil {
		return err
	}
	return c.JSON(200, O("collection", a.collectionJSON(c.R.Context(), col)))
}

func (a *App) destroyCollection(c *Ctx) error {
	if err := c.Need("collections:delete"); err != nil {
		return err
	}
	col, err := a.collectionBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	if err := a.deleteCollection(c.R.Context(), c.Actor, col); err != nil {
		return err
	}
	return c.NoContent()
}

func (a *App) bulkDestroyCollections(c *Ctx) error {
	if err := c.Need("collections:delete"); err != nil {
		return err
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	n := 0
	for _, s := range asList(body["slugs"]) {
		col, err := a.collectionBySlug(c.R.Context(), str(s))
		if err != nil {
			return err
		}
		if err := a.deleteCollection(c.R.Context(), c.Actor, col); err != nil {
			return err
		}
		n++
	}
	return c.JSON(200, O("ok", true, "deleted", n))
}

// ---- globals -------------------------------------------------------------

func (a *App) listGlobals(c *Ctx) error {
	if err := c.Need("globals:read"); err != nil {
		return err
	}
	ctx := c.R.Context()
	list, err := a.globals(ctx, "1=1 ORDER BY name")
	if err != nil {
		return err
	}
	out := []Obj{}
	ids := []int64{}
	for _, g := range list {
		out = append(out, a.globalJSON(g))
		ids = append(ids, g.ID)
	}
	body := O("globals", out)
	if c.Query("resolve") != "" {
		body = body.Set("assets", a.resolveAssets(ctx, a.assetIDs(ctx, "Global", ids)))
	}
	c.Summary = fmt.Sprintf("%d %s.", len(out), plural(len(out), "global"))
	return c.JSON(200, body)
}

func (a *App) showGlobal(c *Ctx) error {
	if err := c.Need("globals:read"); err != nil {
		return err
	}
	g, err := a.globalBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	return a.renderGlobal(c, 200, g)
}

func (a *App) renderGlobal(c *Ctx, status int, g *Global) error {
	ctx := c.R.Context()
	body := O("global", a.globalJSON(g))
	if c.Query("resolve") != "" {
		body = body.Set("assets", a.resolveAssets(ctx, a.assetIDs(ctx, "Global", []int64{g.ID})))
	}
	c.Summary = g.Name + " (" + g.Slug + ")."
	c.Crumbs = []Crumb{step("Edit it", "update_global", "slug", g.Slug, "global", O("data", O()))}
	return c.JSON(status, body)
}

func (a *App) createGlobal(c *Ctx) error {
	if err := c.Need("globals:write"); err != nil {
		return err
	}
	p, err := c.Wrapped("global")
	if err != nil {
		return err
	}
	g := &Global{}
	if _, err := a.saveGlobal(c.R.Context(), c.Actor, g, p); err != nil {
		return err
	}
	return a.renderGlobal(c, 201, g)
}

func (a *App) updateGlobal(c *Ctx) error {
	if err := c.Need("globals:write"); err != nil {
		return err
	}
	g, err := a.globalBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	if err := a.checkLock(c, "global", g.ID); err != nil {
		return err
	}
	p, err := c.Wrapped("global")
	if err != nil {
		return err
	}
	res, err := a.saveGlobal(c.R.Context(), c.Actor, g, p)
	if err != nil {
		return err
	}
	return a.writeAnswer(c, res, func() error { return a.renderGlobal(c, 200, g) })
}

func (a *App) updateGlobalSchema(c *Ctx) error {
	if err := c.Need("globals:write"); err != nil {
		return err
	}
	g, err := a.globalBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	schema, err := schemaBody(c, "global")
	if err != nil {
		return err
	}
	res, err := a.saveGlobal(c.R.Context(), c.Actor, g, map[string]any{"schema": schema})
	if err != nil {
		return err
	}
	return a.writeAnswer(c, res, func() error { return a.renderGlobal(c, 200, g) })
}

func (a *App) destroyGlobal(c *Ctx) error {
	if err := c.Need("globals:delete"); err != nil {
		return err
	}
	g, err := a.globalBySlug(c.R.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	if err := a.checkLock(c, "global", g.ID); err != nil {
		return err
	}
	if err := a.trashGlobal(c.R.Context(), c.Actor, g); err != nil {
		return err
	}
	return c.NoContent()
}

func (a *App) trashGlobal(ctx context.Context, actor *Actor, g *Global) error {
	if _, err := a.store.ExecContext(ctx, `UPDATE globals SET deleted_at = ? WHERE id = ?`, store.Now().DB(), g.ID); err != nil {
		return err
	}
	a.audit(ctx, actor, "global.trashed", "Global", g.ID, g.Name, nil)
	a.publish(ctx, change{Tags: []string{"global:" + g.Slug}})
	return nil
}

func (a *App) bulkDestroyGlobals(c *Ctx) error {
	if err := c.Need("globals:delete"); err != nil {
		return err
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	n := 0
	for _, s := range asList(body["slugs"]) {
		g, err := a.globalBySlug(c.R.Context(), str(s))
		if err != nil {
			return err
		}
		if err := a.trashGlobal(c.R.Context(), c.Actor, g); err != nil {
			return err
		}
		n++
	}
	return c.JSON(200, O("ok", true, "deleted", n))
}

// ---- block types ---------------------------------------------------------

func (a *App) listBlockTypes(c *Ctx) error {
	if err := c.Need("block_types:read"); err != nil {
		return err
	}
	list, err := a.blockTypesList(c.R.Context(), "1=1 ORDER BY category, label")
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, b := range list {
		out = append(out, b.JSON())
	}
	c.Summary = fmt.Sprintf("%d block %s.", len(out), plural(len(out), "type"))
	if len(out) == 0 {
		c.Crumbs = []Crumb{step("Install the starter pack", "api_request", "method", "POST", "path", "/api/block_types/seed")}
	}
	return c.JSON(200, O("block_types", out))
}

func (a *App) blockType(c *Ctx) (*BlockType, error) {
	list, err := a.blockTypesList(c.R.Context(), "slug = ?", c.Param("slug"))
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apierr.NotFound("Couldn't find block type " + c.Param("slug") + ".")
	}
	return list[0], nil
}

func (a *App) showBlockType(c *Ctx) error {
	if err := c.Need("block_types:read"); err != nil {
		return err
	}
	b, err := a.blockType(c)
	if err != nil {
		return err
	}
	return c.JSON(200, O("block_type", b.JSON()))
}

func (a *App) createBlockType(c *Ctx) error {
	if err := c.Need("block_types:write"); err != nil {
		return err
	}
	p, err := c.Wrapped("block_type")
	if err != nil {
		return err
	}
	b := &BlockType{}
	if err := a.saveBlockType(c.R.Context(), c.Actor, b, p); err != nil {
		return err
	}
	return c.JSON(201, O("block_type", b.JSON()))
}

func (a *App) updateBlockType(c *Ctx) error {
	if err := c.Need("block_types:write"); err != nil {
		return err
	}
	b, err := a.blockType(c)
	if err != nil {
		return err
	}
	p, err := c.Wrapped("block_type")
	if err != nil {
		return err
	}
	if err := a.saveBlockType(c.R.Context(), c.Actor, b, p); err != nil {
		return err
	}
	return c.JSON(200, O("block_type", b.JSON()))
}

// destroyBlockType refuses while content uses it: removing it would leave
// blocks no one can edit.
func (a *App) destroyBlockType(c *Ctx) error {
	if err := c.Need("block_types:delete"); err != nil {
		return err
	}
	b, err := a.blockType(c)
	if err != nil {
		return err
	}
	if err := a.deleteBlockType(c.R.Context(), c.Actor, b); err != nil {
		return err
	}
	return c.NoContent()
}

func (a *App) deleteBlockType(ctx context.Context, actor *Actor, b *BlockType) error {
	needle := `%"type":"` + b.Slug + `"%`
	uses, err := store.Count(ctx, a.store, `SELECT (SELECT count(*) FROM pages WHERE blocks LIKE ?) + (SELECT count(*) FROM collection_entries WHERE blocks LIKE ?)`, needle, needle)
	if err != nil {
		return err
	}
	if uses > 0 {
		return apierr.Conflict(fmt.Sprintf("%d pages or entries use %s blocks: mark it deprecated instead, or remove those blocks first.", uses, b.Slug))
	}
	if _, err := a.store.ExecContext(ctx, `DELETE FROM block_types WHERE id = ?`, b.ID); err != nil {
		return err
	}
	a.audit(ctx, actor, "block_type.deleted", "BlockType", b.ID, b.Label, nil)
	a.schemaTouched()
	return nil
}

func (a *App) bulkDestroyBlockTypes(c *Ctx) error {
	if err := c.Need("block_types:delete"); err != nil {
		return err
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	n := 0
	for _, s := range asList(body["slugs"]) {
		list, err := a.blockTypesList(c.R.Context(), "slug = ?", str(s))
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return apierr.NotFound("Couldn't find block type " + str(s) + ".")
		}
		if err := a.deleteBlockType(c.R.Context(), c.Actor, list[0]); err != nil {
			return err
		}
		n++
	}
	return c.JSON(200, O("ok", true, "deleted", n))
}

// SeedBlockTypes installs the starter pack's missing slugs.
func (a *App) SeedBlockTypes(ctx context.Context, actor *Actor) ([]string, error) {
	var pack []map[string]any
	if err := json.Unmarshal(starterBlocks, &pack); err != nil {
		return nil, err
	}
	added := []string{}
	for _, def := range pack {
		n, err := store.Count(ctx, a.store, `SELECT count(*) FROM block_types WHERE slug = ?`, def["slug"])
		if err != nil {
			return nil, err
		}
		if n > 0 {
			continue
		}
		b := &BlockType{BuiltIn: true}
		if err := a.saveBlockType(ctx, actor, b, def); err != nil {
			return nil, fmt.Errorf("seeding %s: %w", def["slug"], err)
		}
		if _, err := a.store.ExecContext(ctx, `UPDATE block_types SET built_in = 1 WHERE id = ?`, b.ID); err != nil {
			return nil, err
		}
		added = append(added, b.Slug)
	}
	return added, nil
}

func (a *App) seedBlockTypes(c *Ctx) error {
	if err := c.Need("block_types:write"); err != nil {
		return err
	}
	added, err := a.SeedBlockTypes(c.R.Context(), c.Actor)
	if err != nil {
		return err
	}
	c.Summary = fmt.Sprintf("Installed %d block types.", len(added))
	return c.JSON(200, O("ok", true, "created", added, "count", len(added)))
}
