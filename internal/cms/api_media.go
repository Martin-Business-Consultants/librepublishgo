package cms

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func (a *App) mediaRoutes(h func(string, handlerFunc, ...func(*route))) {
	h("GET /api/assets", a.listAssets, tool("list_assets", "List the media library.", query("q", "string", "Words in the name or alt text."), query("folder", "string", "Only this folder."), query("type", "string", "image, video, document."), query("page", "integer", "Page of results (from 1)."), query("per", "integer", "Results per page.")))
	h("POST /api/assets", a.createAsset, tool("upload_asset", "Add a file to the media library.", file("file", "The file: a path (command line), or {filename, content_base64} (MCP)."), formField("alt", "Alt text, for an image."), formField("folder", "The folder to put it in.")))
	h("GET /api/assets/{id}", a.showAsset, tool("get_asset", "One file in the media library: its URLs, size, alt text, where it's used."))
	h("PATCH /api/assets/{id}", a.updateAsset, tool("update_asset", "Change a file's name, alt text, caption or folder.", body("asset", "name, alt, caption, description, folder.", true)))
	h("PUT /api/assets/{id}", a.updateAsset)
	h("DELETE /api/assets/{id}", a.destroyAsset, tool("delete_asset", "Move a file to the trash (refused while content uses it)."))
	h("PATCH /api/asset_folders", a.renameFolder)
	h("DELETE /api/asset_folders", a.deleteFolder)
}

func (a *App) listAssets(c *Ctx) error {
	if err := c.Need("assets:read"); err != nil {
		return err
	}
	ctx := c.R.Context()
	where, args := []string{"1=1"}, []any{}
	if f := c.Query("folder"); f != "" {
		where, args = append(where, "a.folder = ?"), append(args, normalizeFolder(f))
	}
	if q := c.Query("q"); q != "" {
		where, args = append(where, "(a.name LIKE ? OR b.filename LIKE ? OR a.alt LIKE ?)"), append(args, "%"+q+"%", "%"+q+"%", "%"+q+"%")
	}
	if t := c.Query("type"); t != "" {
		where, args = append(where, "b.content_type LIKE ?"), append(args, t+"%")
	}
	if c.truthy("unused") {
		// Nothing points at it: no page, entry or global (trashed ones
		// count — restoring them would need the file), and no setting.
		where = append(where, `NOT EXISTS (SELECT 1 FROM content_references r WHERE r.ref_type = 'asset' AND r.ref_id = CAST(a.id AS TEXT))`)
		ids, err := a.settingAssetIDs(c)
		if err != nil {
			return err
		}
		for _, id := range ids {
			where, args = append(where, "a.id != ?"), append(args, id)
		}
	}
	page, per := c.paging(50, 200)
	cond := "a.deleted_at IS NULL AND " + strings.Join(where, " AND ")
	total, err := store.Count(ctx, a.store, "SELECT count(*)"+assetFrom+"WHERE "+cond, args...)
	if err != nil {
		return err
	}
	list, err := a.assets(ctx, cond+fmt.Sprintf(" ORDER BY a.created_at DESC, a.id DESC LIMIT %d OFFSET %d", per, (page-1)*per), args...)
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, as := range list {
		out = append(out, a.assetJSON(ctx, as))
	}
	pages := (total + per - 1) / per
	c.Summary = fmt.Sprintf("%d %s.", total, plural(total, "file"))
	if len(list) > 0 {
		c.Crumbs = []Crumb{step("What uses one", "get_asset", "id", list[0].ID)}
	}
	return c.JSON(200, O("assets", out, "pagination", O("page", page, "per", per, "total", total, "total_pages", pages)))
}

// settingAssetIDs: the branding logo and favicon count as uses.
func (a *App) settingAssetIDs(c *Ctx) ([]int64, error) {
	var out []int64
	for _, key := range []string{"branding", "general"} {
		s, err := a.setting(c.R.Context(), key)
		if err != nil {
			return nil, err
		}
		for _, k := range []string{"logo_asset_id", "favicon_asset_id", "logo_id", "favicon_id"} {
			if id, ok := intOf(s[k]); ok {
				out = append(out, id)
			}
		}
	}
	return out, nil
}

func (a *App) showAsset(c *Ctx) error {
	if err := c.Need("assets:read"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	as, err := a.assetByID(c.R.Context(), id, false)
	if err != nil {
		return err
	}
	used, err := a.usedBy(c, "asset", strconv.FormatInt(id, 10))
	if err != nil {
		return err
	}
	used = append(used, a.brandingUses(c.R.Context(), id)...)
	return c.JSON(200, O("asset", append(a.assetJSON(c.R.Context(), as), kv{"used_by", used})))
}

// usedBy names each record that points at ref, and where in it.
func (a *App) usedBy(c *Ctx, refType, refID string) ([]Obj, error) {
	rows, err := a.store.QueryContext(c.R.Context(), `SELECT owner_type, owner_id, kind, COALESCE(position, -1) FROM content_references WHERE ref_type = ? AND ref_id = ? ORDER BY owner_type, owner_id, position`, refType, refID)
	if err != nil {
		return nil, err
	}
	type use struct {
		t        string
		id       int64
		kind     string
		position int
	}
	uses, err := store.ScanAll(rows, func(r *sql.Rows) (use, error) {
		var u use
		err := r.Scan(&u.t, &u.id, &u.kind, &u.position)
		return u, err
	})
	if err != nil {
		return nil, err
	}
	out := []Obj{}
	for _, u := range uses {
		where := "frontmatter"
		if u.position >= 0 {
			where = fmt.Sprintf("block %d", u.position+1)
		}
		if u.t == "Global" {
			where = "data"
		}
		o := O("type", u.t, "id", u.id, "kind", u.kind, "where", where)
		switch u.t {
		case "Page":
			if p, err := a.contentByID(c.R.Context(), "page", u.id, true); err == nil {
				o = append(o, kv{"label", p.Title}, kv{"path", p.Path}, kv{"status", p.Status}, kv{"trashed", p.DeletedAt.Valid})
			}
		case "CollectionEntry":
			if e, err := a.contentByID(c.R.Context(), "entry", u.id, true); err == nil {
				o = append(o, kv{"label", e.Title}, kv{"path", e.CollectionSlug + "/" + e.Slug}, kv{"status", e.Status}, kv{"trashed", e.DeletedAt.Valid})
			}
		case "Global":
			if g, err := a.globalByID(c.R.Context(), u.id); err == nil {
				o = append(o, kv{"label", g.Name}, kv{"path", g.Slug})
			}
		}
		out = append(out, o)
	}
	return out, nil
}

func (a *App) createAsset(c *Ctx) error {
	if err := c.Need("assets:write"); err != nil {
		return err
	}
	// The file, plus room for the form's other fields and its boundaries.
	c.R.Body = http.MaxBytesReader(c.W, c.R.Body, maxUpload+1<<20)
	if err := c.R.ParseMultipartForm(32 << 20); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return apierr.TooLarge(maxUpload)
		}
		return apierr.BadRequest("Upload the file as multipart/form-data, field \"file\" (or asset[file]).")
	}
	file, header, err := c.R.FormFile("file")
	if err != nil {
		file, header, err = c.R.FormFile("asset[file]")
	}
	if err != nil {
		return apierr.BadRequest("param is missing or the value is empty: file")
	}
	defer file.Close()
	fields := map[string]string{}
	for _, k := range []string{"name", "alt", "caption", "description", "folder"} {
		v := c.R.FormValue(k)
		if v == "" {
			v = c.R.FormValue("asset[" + k + "]")
		}
		fields[k] = v
	}
	as, err := a.uploadAsset(c.R.Context(), c.Actor, header.Filename, header.Header.Get("Content-Type"), file, fields)
	if err != nil {
		return err
	}
	c.Summary = fmt.Sprintf("Uploaded %s as asset %d.", as.Filename, as.ID)
	return c.JSON(201, O("asset", a.assetJSON(c.R.Context(), as)))
}

func (a *App) updateAsset(c *Ctx) error {
	if err := c.Need("assets:write"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	as, err := a.assetByID(c.R.Context(), id, false)
	if err != nil {
		return err
	}
	p, err := c.Wrapped("asset")
	if err != nil {
		return err
	}
	if err := a.saveAsset(c.R.Context(), c.Actor, as, p); err != nil {
		return err
	}
	return c.JSON(200, O("asset", a.assetJSON(c.R.Context(), as)))
}

func (a *App) destroyAsset(c *Ctx) error {
	if err := c.Need("assets:delete"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	as, err := a.assetByID(c.R.Context(), id, false)
	if err != nil {
		return err
	}
	if c.truthy("permanent") {
		if err := c.Need("trash:write"); err != nil {
			return err
		}
		if err := a.purgeAsset(c.R.Context(), c.Actor, as); err != nil {
			return err
		}
		return c.NoContent()
	}
	if _, err := a.store.ExecContext(c.R.Context(), `UPDATE assets SET deleted_at = ? WHERE id = ?`, store.Now().DB(), id); err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "asset.trashed", "Asset", id, as.Filename, nil)
	return c.NoContent()
}

// Folders are paths on assets: renaming one moves everything under it.
func (a *App) renameFolder(c *Ctx) error {
	if err := c.Need("assets:write"); err != nil {
		return err
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	from := normalizeFolder(str(firstOf(body, "path", "from")))
	to := normalizeFolder(str(firstOf(body, "new_path", "to", "name")))
	if from == "/" {
		return apierr.BadRequest("The root folder can't be renamed.")
	}
	res, err := a.store.ExecContext(c.R.Context(), `UPDATE assets SET folder = ? || substr(folder, ?), updated_at = ? WHERE folder = ? OR folder LIKE ?`,
		to, len(from)+1, store.Now().DB(), from, from+"/%")
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	return c.JSON(200, O("ok", true, "path", to, "moved", n))
}

func (a *App) deleteFolder(c *Ctx) error {
	if err := c.Need("assets:delete"); err != nil {
		return err
	}
	folder := normalizeFolder(c.Query("path"))
	n, err := store.Count(c.R.Context(), a.store.DB, `SELECT count(*) FROM assets WHERE deleted_at IS NULL AND (folder = ? OR folder LIKE ?)`, folder, folder+"/%")
	if err != nil {
		return err
	}
	if n > 0 {
		return apierr.Conflict(fmt.Sprintf("%s still holds %d files: move or delete them first.", folder, n))
	}
	return c.JSON(200, O("ok", true))
}
