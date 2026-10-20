package cms

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/fields"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// ---- collections ---------------------------------------------------------

// Collection is a kind of entry (posts, products): its fields, and how
// its entries are categorized, tagged and built.
type Collection struct {
	ID                     int64
	Slug, Name, Icon       string
	APIEnabled             bool
	EnableBlocks           bool
	Schema, BuildConfig    store.JSON
	CategoriesCollectionID store.NullInt
	TagsCollectionID       store.NullInt
	CreatedAt, UpdatedAt   store.Time
}

var collectionSlugFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]*$`)

const collectionCols = `id, slug, name, COALESCE(icon, ''), api_enabled, enable_blocks, schema, build_config, categories_collection_id, tags_collection_id, created_at, updated_at`

func (a *App) collections(ctx context.Context, where string, args ...any) ([]*Collection, error) {
	if where == "" {
		where = "1=1"
	}
	rows, err := a.store.QueryContext(ctx, "SELECT "+collectionCols+" FROM collections WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Collection{}
	for rows.Next() {
		c := &Collection{}
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Icon, &c.APIEnabled, &c.EnableBlocks, &c.Schema, &c.BuildConfig,
			&c.CategoriesCollectionID, &c.TagsCollectionID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Schema = c.Schema.Or(`{"fields":[]}`)
		c.BuildConfig = c.BuildConfig.Or("{}")
		out = append(out, c)
	}
	return out, rows.Err()
}

func (a *App) collectionBySlug(ctx context.Context, slug string) (*Collection, error) {
	list, err := a.collections(ctx, "slug = ?", slug)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apierr.NotFound("Couldn't find collection " + slug + ".")
	}
	return list[0], nil
}

// Fields is the entries' frontmatter fields.
func (c *Collection) Fields() []fields.Field { return fields.Parse(c.Schema) }

// FieldsJSON is the field definitions as written; [] for none.
func (c *Collection) FieldsJSON() any {
	m := c.Schema.Map()
	if f, ok := m["fields"]; ok && f != nil {
		return f
	}
	return []any{}
}

func (a *App) collectionJSON(ctx context.Context, c *Collection) Obj {
	// A count that can't be read shows as 0: it's for display.
	count, _ := store.Count(ctx, a.store, `SELECT count(*) FROM collection_entries WHERE collection_id = ? AND deleted_at IS NULL`, c.ID)
	return O("id", c.ID, "slug", c.Slug, "name", c.Name, "enable_blocks", c.EnableBlocks, "schema", c.Schema, "build_config", c.BuildConfig,
		"categories_collection_id", c.CategoriesCollectionID, "tags_collection_id", c.TagsCollectionID, "created_at", c.CreatedAt,
		"updated_at", c.UpdatedAt, "api_enabled", c.APIEnabled, "frontmatter_json_schema", fields.JSONSchema(c.Fields()), "entry_count", count)
}

// saveCollection creates or updates one from {slug, name, enable_blocks,
// api_enabled, schema, build_config, categories_collection_id,
// tags_collection_id}.
func (a *App) saveCollection(ctx context.Context, actor *Actor, c *Collection, p map[string]any) error {
	creating := c.ID == 0
	if creating {
		c.Schema, c.BuildConfig = store.JSON(`{"fields":[]}`), store.JSON("{}")
	}
	if has(p, "slug") {
		c.Slug = strings.TrimSpace(str(p["slug"]))
	}
	if has(p, "name") {
		c.Name = strings.TrimSpace(str(p["name"]))
	}
	if has(p, "icon") {
		c.Icon = str(p["icon"])
	}
	if has(p, "enable_blocks") {
		c.EnableBlocks = boolOf(p["enable_blocks"])
	}
	if has(p, "api_enabled") {
		c.APIEnabled = boolOf(p["api_enabled"])
	}
	if has(p, "build_config") {
		c.BuildConfig = store.MustJSON(p["build_config"]).Or("{}")
	}
	for _, k := range []string{"categories_collection_id", "tags_collection_id"} {
		if has(p, k) {
			var n store.NullInt
			if id, ok := intOf(p[k]); ok {
				n = store.IntOf(id)
			}
			if k == "categories_collection_id" {
				c.CategoriesCollectionID = n
			} else {
				c.TagsCollectionID = n
			}
		}
	}
	inv := apierr.Invalid{}
	if has(p, "schema") {
		c.Schema = normalizeSchema(p["schema"])
		fields.ValidateDefinitions(c.Schema.Map()["fields"], false, inv, "schema.fields")
	}
	if c.Slug == "" || !collectionSlugFormat.MatchString(c.Slug) {
		inv.Add("slug", "is invalid: lowercase letters, numbers, - and _")
	}
	if c.Name == "" {
		inv.Add("name", "can't be blank")
	}
	clash, err := store.Count(ctx, a.store.DB, `SELECT count(*) FROM collections WHERE slug = ? AND id != ?`, c.Slug, c.ID)
	if err != nil {
		return err
	}
	if clash > 0 {
		inv.Add("slug", "has already been taken")
	}
	if err := inv.Err(); err != nil {
		return err
	}
	now := store.Now()
	if creating {
		res, err := a.store.ExecContext(ctx, `INSERT INTO collections (slug, name, icon, api_enabled, enable_blocks, schema, build_config, categories_collection_id, tags_collection_id, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, c.Slug, c.Name, c.Icon, c.APIEnabled, c.EnableBlocks, string(c.Schema), string(c.BuildConfig),
			c.CategoriesCollectionID.DB(), c.TagsCollectionID.DB(), now.DB(), now.DB())
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		c.CreatedAt = now
	} else {
		if _, err := a.store.ExecContext(ctx, `UPDATE collections SET slug=?, name=?, icon=?, api_enabled=?, enable_blocks=?, schema=?, build_config=?,
			categories_collection_id=?, tags_collection_id=?, updated_at=? WHERE id=?`, c.Slug, c.Name, c.Icon, c.APIEnabled, c.EnableBlocks,
			string(c.Schema), string(c.BuildConfig), c.CategoriesCollectionID.DB(), c.TagsCollectionID.DB(), now.DB(), c.ID); err != nil {
			return err
		}
	}
	c.UpdatedAt = now
	a.audit(ctx, actor, map[bool]string{true: "collection.created", false: "collection.updated"}[creating], "Collection", c.ID, c.Name, nil)
	a.publish(ctx, change{Tags: []string{"collection:" + c.Slug}})
	return nil
}

// normalizeSchema accepts [fields] or {"fields": [fields]} and stores the
// latter.
func normalizeSchema(v any) store.JSON {
	switch x := v.(type) {
	case []any:
		return store.MustJSON(map[string]any{"fields": x})
	case map[string]any:
		if x["fields"] == nil {
			x["fields"] = []any{}
		}
		return store.MustJSON(x)
	}
	return store.JSON(`{"fields":[]}`)
}

func (a *App) deleteCollection(ctx context.Context, actor *Actor, c *Collection) error {
	live, err := store.Count(ctx, a.store, `SELECT count(*) FROM collection_entries WHERE collection_id = ?`, c.ID)
	if err != nil {
		return err
	}
	if live > 0 {
		return apierr.Conflict("It still has entries (trashed ones count): delete or purge those first.")
	}
	if _, err := a.store.ExecContext(ctx, `DELETE FROM collections WHERE id = ?`, c.ID); err != nil {
		return err
	}
	a.audit(ctx, actor, "collection.deleted", "Collection", c.ID, c.Name, nil)
	return nil
}

// ---- globals -------------------------------------------------------------

// Global is one site-wide set of data (the header, the footer) with its
// own fields.
type Global struct {
	ID                              int64
	Slug, Name, Description, Icon   string
	Data, Schema                    store.JSON
	Version, LockVersion            int
	DeletedAt, CreatedAt, UpdatedAt store.Time
}

const globalCols = `id, slug, name, COALESCE(description, ''), COALESCE(icon, ''), data, schema, version, lock_version, deleted_at, created_at, updated_at`

func (a *App) globals(ctx context.Context, where string, args ...any) ([]*Global, error) {
	if where == "" {
		where = "1=1"
	}
	if !strings.Contains(where, "deleted_at") {
		where = scoped("deleted_at IS NULL", where)
	}
	rows, err := a.store.QueryContext(ctx, "SELECT "+globalCols+" FROM globals WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Global{}
	for rows.Next() {
		g := &Global{}
		if err := rows.Scan(&g.ID, &g.Slug, &g.Name, &g.Description, &g.Icon, &g.Data, &g.Schema, &g.Version, &g.LockVersion, &g.DeletedAt, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		g.Data = g.Data.Or("{}")
		g.Schema = g.Schema.Or(`{"fields":[]}`)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (a *App) globalBySlug(ctx context.Context, slug string) (*Global, error) {
	list, err := a.globals(ctx, "slug = ?", slug)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apierr.NotFound("Couldn't find global " + slug + ".")
	}
	return list[0], nil
}

func (a *App) globalByID(ctx context.Context, id int64) (*Global, error) {
	list, err := a.globals(ctx, "id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, store.ErrNotFound
	}
	return list[0], nil
}

// Fields is the global's data fields.
func (g *Global) Fields() []fields.Field { return fields.Parse(g.Schema) }

// FieldsJSON is the field definitions as written; [] for none.
func (g *Global) FieldsJSON() any {
	if f := g.Schema.Map()["fields"]; f != nil {
		return f
	}
	return []any{}
}

func (a *App) globalJSON(g *Global) Obj {
	return O("id", g.ID, "slug", g.Slug, "name", g.Name, "description", g.Description, "icon", g.Icon, "version", g.Version,
		"data", redact(g.Data.Map()), "created_at", g.CreatedAt, "updated_at", g.UpdatedAt, "lock_version", g.LockVersion, "fields", g.FieldsJSON(),
		"json_schema", fields.JSONSchema(g.Fields()))
}

// assign applies a write's params to g, noting in inv what's malformed.
func (g *Global) assign(p map[string]any, inv apierr.Invalid) {
	if has(p, "slug") {
		g.Slug = strings.TrimSpace(str(p["slug"]))
	}
	if has(p, "name") {
		g.Name = strings.TrimSpace(str(p["name"]))
	}
	if has(p, "description") {
		g.Description = str(p["description"])
	}
	if has(p, "icon") {
		g.Icon = str(p["icon"])
	}
	if v, ok := intOf(p["version"]); ok {
		g.Version = int(v)
	}
	if has(p, "schema") {
		g.Schema = normalizeSchema(p["schema"])
		fields.ValidateDefinitions(g.Schema.Map()["fields"], true, inv, "schema.fields")
	}
	if !has(p, "data") {
		return
	}
	data, ok := p["data"].(map[string]any)
	if !ok {
		inv.Add("data", "must be an object")
		return
	}
	// A masked secret written back means "keep what's there".
	old := g.Data.Map()
	for k, v := range data {
		if s, ok := v.(string); ok && strings.HasPrefix(s, "***") && secretKey(k) {
			data[k] = old[k]
		}
	}
	g.Data = store.MustJSON(data)
}

// globalEqual is whether a write left the global as it was.
func globalEqual(a, b *Global) bool {
	return jsonEqual(a.Data, b.Data) && jsonEqual(a.Schema, b.Schema) && a.Name == b.Name && a.Slug == b.Slug &&
		a.Description == b.Description && a.Icon == b.Icon && a.Version == b.Version
}

// saveGlobal creates or updates a global. Globals have no draft state, so
// changing one that exists needs globals:publish; without it the write is
// held as a revision.
func (a *App) saveGlobal(ctx context.Context, actor *Actor, g *Global, p map[string]any) (*writeResult, error) {
	creating := g.ID == 0
	if !creating && !actor.Can("globals:publish") {
		rev, ignored, err := a.fileRevision(ctx, actor, "Global", g.ID, g.Name, p, []string{"name", "description", "icon", "data"})
		if err != nil {
			return nil, err
		}
		return &writeResult{Held: rev, Ignored: ignored}, nil
	}
	if (g.Slug == "scripts" || str(p["slug"]) == "scripts") && !actor.Can("globals:scripts") {
		return nil, apierr.Forbidden("globals:scripts")
	}
	if creating {
		g.Data, g.Schema, g.Version = store.JSON("{}"), store.JSON(`{"fields":[]}`), 1
	}
	prior := *g
	inv := apierr.Invalid{}
	g.assign(p, inv)
	if g.Slug == "" || !collectionSlugFormat.MatchString(g.Slug) {
		inv.Add("slug", "is invalid: lowercase letters, numbers, - and _")
	}
	if g.Name == "" {
		inv.Add("name", "can't be blank")
	}
	clash, err := store.Count(ctx, a.store.DB, `SELECT count(*) FROM globals WHERE slug = ? AND id != ? AND deleted_at IS NULL`, g.Slug, g.ID)
	if err != nil {
		return nil, err
	}
	if clash > 0 {
		inv.Add("slug", "has already been taken")
	}
	types, err := a.blockTypeResolver(ctx)
	if err != nil {
		return nil, err
	}
	fields.ValidateData(g.Fields(), g.Data.Map(), types, inv, "data")
	// Markup only in text this save changes: HTML a global already held
	// doesn't stop saving the rest.
	var before fields.Texts
	if !creating {
		before = fields.Texts{}
		before.AddData(g.Fields(), prior.Data.Map(), types, "data")
	}
	fields.ValidateMarkupData(g.Fields(), g.Data.Map(), types, before, inv, "data")
	if err := inv.Err(); err != nil {
		return nil, err
	}
	if !creating {
		if lv, ok := intOf(p["lock_version"]); ok && int(lv) != prior.LockVersion {
			return nil, staleWrite(lv, int64(prior.LockVersion))
		}
		if globalEqual(&prior, g) {
			return &writeResult{Unchanged: true}, nil
		}
	}
	now := store.Now()
	var refs []fields.Ref
	fields.Refs(g.Fields(), g.Data.Map(), types, &refs)
	err = a.store.Tx(ctx, func(tx *sql.Tx) error {
		if creating {
			res, err := tx.ExecContext(ctx, `INSERT INTO globals (slug, name, description, icon, data, schema, version, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				g.Slug, g.Name, g.Description, g.Icon, string(g.Data), string(g.Schema), g.Version, now.DB(), now.DB())
			if err != nil {
				return err
			}
			if g.ID, err = res.LastInsertId(); err != nil {
				return err
			}
			g.CreatedAt = now
		} else {
			g.LockVersion++
			res, err := tx.ExecContext(ctx, `UPDATE globals SET slug=?, name=?, description=?, icon=?, data=?, schema=?, version=?, lock_version=?, updated_at=? WHERE id=? AND lock_version=?`,
				g.Slug, g.Name, g.Description, g.Icon, string(g.Data), string(g.Schema), g.Version, g.LockVersion, now.DB(), g.ID, prior.LockVersion)
			if err := wroteOver(res, err, prior.LockVersion); err != nil {
				return err
			}
		}
		var author any
		if actor.UserID != 0 {
			author = actor.UserID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO global_versions (global_id, author_id, data, name, created_at) VALUES (?,?,?,?,?)`, g.ID, author, string(g.Data), g.Name, now.DB()); err != nil {
			return err
		}
		return replaceReferences(ctx, tx, "Global", g.ID, appendRefs(nil, refs, -1))
	})
	if err != nil {
		return nil, err
	}
	g.UpdatedAt = now
	a.audit(ctx, actor, map[bool]string{true: "global.created", false: "global.updated"}[creating], "Global", g.ID, g.Name, nil)
	a.publish(ctx, change{Event: "global.updated", Data: O("id", g.ID, "slug", g.Slug, "name", g.Name, "updated_at", g.UpdatedAt.ISO()),
		Tags: []string{"global:" + g.Slug}})
	return &writeResult{Created: creating}, nil
}

// ---- block types ---------------------------------------------------------

// BlockType is a kind of block pages and entries are built from: its
// fields, and the defaults a new one starts with.
type BlockType struct {
	ID                                       int64
	Slug, Label, Description, Category, Icon string
	Fields, Defaults                         store.JSON
	BuiltIn, Deprecated                      bool
	Version                                  int
	CreatedAt, UpdatedAt                     store.Time
}

const blockTypeCols = `id, slug, label, COALESCE(description, ''), COALESCE(category, ''), COALESCE(icon, ''), fields, defaults, built_in, deprecated, version, created_at, updated_at`

func (a *App) blockTypesList(ctx context.Context, where string, args ...any) ([]*BlockType, error) {
	if where == "" {
		where = "1=1"
	}
	rows, err := a.store.QueryContext(ctx, "SELECT "+blockTypeCols+" FROM block_types WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*BlockType{}
	for rows.Next() {
		b := &BlockType{}
		if err := rows.Scan(&b.ID, &b.Slug, &b.Label, &b.Description, &b.Category, &b.Icon, &b.Fields, &b.Defaults, &b.BuiltIn, &b.Deprecated, &b.Version, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		b.Fields = b.Fields.Or("[]")
		b.Defaults = b.Defaults.Or("{}")
		out = append(out, b)
	}
	return out, rows.Err()
}

// JSON is the block type as the API answers it.
func (b *BlockType) JSON() Obj {
	return O("id", b.ID, "slug", b.Slug, "label", b.Label, "description", b.Description, "category", b.Category, "icon", b.Icon,
		"version", b.Version, "built_in", b.BuiltIn, "deprecated", b.Deprecated, "fields", b.Fields, "defaults", b.Defaults,
		"created_at", b.CreatedAt, "updated_at", b.UpdatedAt, "json_schema", b.Schema())
}

// Schema is the JSON Schema of a block's data.
func (b *BlockType) Schema() map[string]any {
	s := fields.JSONSchema(fields.Parse(b.Fields))
	s["title"] = b.Label
	return s
}

func (a *App) saveBlockType(ctx context.Context, actor *Actor, b *BlockType, p map[string]any) error {
	creating := b.ID == 0
	if creating {
		b.Fields, b.Defaults, b.Version = store.JSON("[]"), store.JSON("{}"), 1
	}
	for k, dst := range map[string]*string{"slug": &b.Slug, "label": &b.Label, "description": &b.Description, "category": &b.Category, "icon": &b.Icon} {
		if has(p, k) {
			*dst = strings.TrimSpace(str(p[k]))
		}
	}
	if has(p, "deprecated") {
		b.Deprecated = boolOf(p["deprecated"])
	}
	if v, ok := intOf(p["version"]); ok {
		b.Version = int(v)
	}
	inv := apierr.Invalid{}
	if has(p, "fields") {
		fields.ValidateDefinitions(p["fields"], true, inv, "fields")
		b.Fields = store.MustJSON(p["fields"]).Or("[]")
	}
	if has(p, "defaults") {
		if _, ok := p["defaults"].(map[string]any); !ok {
			inv.Add("defaults", "must be an object")
		} else {
			b.Defaults = store.MustJSON(p["defaults"])
		}
	}
	if !fields.IsBlockSlug(b.Slug) {
		inv.Add("slug", "is invalid: a lowercase letter, then a-z, 0-9 and _")
	}
	if b.Label == "" {
		inv.Add("label", "can't be blank")
	}
	clash, err := store.Count(ctx, a.store.DB, `SELECT count(*) FROM block_types WHERE slug = ? AND id != ?`, b.Slug, b.ID)
	if err != nil {
		return err
	}
	if clash > 0 {
		inv.Add("slug", "has already been taken")
	}
	if err := inv.Err(); err != nil {
		return err
	}
	now := store.Now()
	if creating {
		res, err := a.store.ExecContext(ctx, `INSERT INTO block_types (slug, label, description, category, icon, fields, defaults, built_in, deprecated, version, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, b.Slug, b.Label, b.Description, b.Category, b.Icon, string(b.Fields), string(b.Defaults), b.BuiltIn, b.Deprecated, b.Version, now.DB(), now.DB())
		if err != nil {
			return err
		}
		b.ID, _ = res.LastInsertId()
		b.CreatedAt = now
	} else if _, err := a.store.ExecContext(ctx, `UPDATE block_types SET slug=?, label=?, description=?, category=?, icon=?, fields=?, defaults=?, deprecated=?, version=?, updated_at=? WHERE id=?`,
		b.Slug, b.Label, b.Description, b.Category, b.Icon, string(b.Fields), string(b.Defaults), b.Deprecated, b.Version, now.DB(), b.ID); err != nil {
		return err
	}
	b.UpdatedAt = now
	a.audit(ctx, actor, map[bool]string{true: "block_type.created", false: "block_type.updated"}[creating], "BlockType", b.ID, b.Label, nil)
	a.schemaTouched()
	return nil
}

// ---- seeding -------------------------------------------------------------

// seed makes what an empty install needs: the built-in roles, and the
// general settings saying the site is served over the API.
func (a *App) seed() error {
	ctx := context.Background()
	// Roles only on an empty install: a Rails database brings its own.
	roles, err := store.Count(ctx, a.store.DB, `SELECT count(*) FROM roles`)
	if err != nil {
		return err
	}
	for _, r := range defaultRoles {
		if roles > 0 {
			break
		}
		perms, _ := json.Marshal(r.Caps)
		if _, err := a.store.ExecContext(ctx, `INSERT INTO roles (name, description, permissions, system, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
			r.Name, r.Description, string(perms), r.System, store.Now().DB(), store.Now().DB()); err != nil {
			return err
		}
	}
	n, err := store.Count(ctx, a.store.DB, `SELECT count(*) FROM settings WHERE key = 'general'`)
	if err != nil {
		return err
	}
	if n == 0 {
		_, err := a.store.ExecContext(ctx, `INSERT INTO settings (key, data, created_at, updated_at) VALUES ('general', ?, ?, ?)`,
			`{"title":"My site","default_locale":"en","serving":"api_only"}`, store.Now().DB(), store.Now().DB())
		a.generalCache.forget()
		return err
	}
	return nil
}
