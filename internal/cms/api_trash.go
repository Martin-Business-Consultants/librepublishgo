package cms

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

var trashKinds = []string{"page", "entry", "global", "asset"}

func (a *App) listTrash(c *Ctx) error {
	if err := c.Need("trash:read"); err != nil {
		return err
	}
	ctx := c.R.Context()
	type item struct {
		at  store.Time
		obj Obj
	}
	var items []item
	counts := O()
	if pages, err := a.pages(ctx, "deleted_at IS NOT NULL"); err == nil {
		counts = append(counts, kv{"page", len(pages)})
		for _, p := range pages {
			items = append(items, item{p.DeletedAt, O("kind", "page", "id", p.ID, "label", p.Label(), "deleted_at", p.DeletedAt.ISO(), "created_at", p.CreatedAt.ISO(), "meta", O("path", p.Path, "status", p.Status))})
		}
	}
	if entries, err := a.entries(ctx, "e.deleted_at IS NOT NULL"); err == nil {
		counts = append(counts, kv{"entry", len(entries)})
		for _, e := range entries {
			items = append(items, item{e.DeletedAt, O("kind", "entry", "id", e.ID, "label", e.Label(), "deleted_at", e.DeletedAt.ISO(), "created_at", e.CreatedAt.ISO(), "meta", O("collection", e.CollectionSlug, "slug", e.Slug, "status", e.Status))})
		}
	}
	if globals, err := a.globals(ctx, "deleted_at IS NOT NULL"); err == nil {
		counts = append(counts, kv{"global", len(globals)})
		for _, g := range globals {
			items = append(items, item{g.DeletedAt, O("kind", "global", "id", g.ID, "label", g.Name, "deleted_at", g.DeletedAt.ISO(), "created_at", g.CreatedAt.ISO(), "meta", O("slug", g.Slug))})
		}
	}
	if assets, err := a.assets(ctx, "a.deleted_at IS NOT NULL"); err == nil {
		counts = append(counts, kv{"asset", len(assets)})
		for _, as := range assets {
			items = append(items, item{as.DeletedAt, O("kind", "asset", "id", as.ID, "label", as.Filename, "deleted_at", as.DeletedAt.ISO(), "created_at", as.CreatedAt.ISO(), "meta", O("folder", as.Folder))})
		}
	}
	slices.SortFunc(items, func(x, y item) int { return y.at.Compare(x.at.Time) }) // newest first
	out := []Obj{}
	for _, it := range items {
		out = append(out, it.obj)
	}
	c.Summary = fmt.Sprintf("%d in the trash.", len(out))
	if len(out) > 0 {
		c.Crumbs = []Crumb{crumb("Restore one", fmt.Sprintf("cms restore %s %v", out[0].Get("kind"), out[0].Get("id")))}
	}
	return c.JSON(200, O("entries", out, "counts", counts, "kinds", trashKinds))
}

func (a *App) restoreTrash(c *Ctx) error {
	if err := c.Need("trash:write"); err != nil {
		return err
	}
	ctx := c.R.Context()
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	switch kind := c.Param("kind"); kind {
	case "page", "entry":
		rec, err := a.contentByID(ctx, kind, id, true)
		if err != nil || !rec.DeletedAt.Valid {
			return apierr.NotFound("Nothing in the trash by that id.")
		}
		if err := a.restoreContent(ctx, c.Actor, rec); err != nil {
			return err
		}
	case "global":
		g, err := a.globals(ctx, "id = ? AND deleted_at IS NOT NULL", id)
		if err != nil || len(g) == 0 {
			return apierr.NotFound("Nothing in the trash by that id.")
		}
		clash, err := store.Count(ctx, a.store, `SELECT count(*) FROM globals WHERE slug = ? AND deleted_at IS NULL`, g[0].Slug)
		if err != nil {
			return err
		}
		if clash > 0 {
			return apierr.Conflict("Another global has the slug " + g[0].Slug + " now.")
		}
		if _, err := a.store.ExecContext(ctx, `UPDATE globals SET deleted_at = NULL WHERE id = ?`, id); err != nil {
			return err
		}
		a.audit(ctx, c.Actor, "global.restored", "Global", id, g[0].Name, nil)
	case "asset":
		if _, err := a.store.ExecContext(ctx, `UPDATE assets SET deleted_at = NULL WHERE id = ?`, id); err != nil {
			return err
		}
		a.audit(ctx, c.Actor, "asset.restored", "Asset", id, "", nil)
	default:
		return apierr.NotFound("Trash has no kind " + kind + ": one of " + strings.Join(trashKinds, ", ") + ".")
	}
	return c.JSON(200, O("ok", true, "kind", c.Param("kind"), "id", id))
}

func (a *App) purgeTrash(c *Ctx) error {
	if err := c.Need("trash:write"); err != nil {
		return err
	}
	ctx := c.R.Context()
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	switch kind := c.Param("kind"); kind {
	case "page", "entry":
		rec, err := a.contentByID(ctx, kind, id, true)
		if err != nil || !rec.DeletedAt.Valid {
			return apierr.NotFound("Nothing in the trash by that id.")
		}
		if err := a.purgeContent(ctx, c.Actor, rec); err != nil {
			return err
		}
	case "global":
		// All or nothing: a live global's id purges nothing of it.
		err := a.store.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM global_versions WHERE global_id = ?`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM content_references WHERE owner_type = 'Global' AND owner_id = ?`, id); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `DELETE FROM globals WHERE id = ? AND deleted_at IS NOT NULL`, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return apierr.NotFound("Nothing in the trash by that id.")
			}
			return nil
		})
		if err != nil {
			return err
		}
		a.audit(ctx, c.Actor, "global.purged", "Global", id, "", nil)
	case "asset":
		as, err := a.assetByID(ctx, id, true)
		if err != nil || !as.DeletedAt.Valid {
			return apierr.NotFound("Nothing in the trash by that id.")
		}
		if err := a.purgeAsset(ctx, c.Actor, as); err != nil {
			return err
		}
	default:
		return apierr.NotFound("Trash has no kind " + kind + ".")
	}
	return c.NoContent()
}
