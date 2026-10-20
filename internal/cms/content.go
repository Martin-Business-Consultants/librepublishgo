package cms

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/fields"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// Content is a page or a collection entry: the two share status, schedule,
// blocks, frontmatter, SEO, taxonomy and translations, and differ in where
// they live (a page in the tree, an entry in a collection).
type Content struct {
	Kind string // "page" or "entry"

	ID     int64
	Slug   string
	Title  string
	Status string
	Locale string

	// Pages
	Path     string
	ParentID store.NullInt
	Depth    int
	Schema   store.JSON

	// Entries
	CollectionID   int64
	CollectionSlug string
	BodyMarkdown   string

	Blocks             store.JSON
	Frontmatter        store.JSON
	Seo                store.JSON
	CategoryEntryID    store.NullInt
	TranslationGroupID store.NullInt
	LockVersion        int

	PublishAt, PublishedAt, UnpublishAt, DeletedAt, CreatedAt, UpdatedAt store.Time

	tagIDs []int64 // set by a write that names tag_ids
}

var statuses = []string{"draft", "published", "archived"}

var contentSlugFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]*$`)

func (c *Content) table() string {
	if c.Kind == "page" {
		return "pages"
	}
	return "collection_entries"
}

// TaggableType is the Rails class name, as taggings and references store it.
func (c *Content) TaggableType() string {
	if c.Kind == "page" {
		return "Page"
	}
	return "CollectionEntry"
}

// Prefix is the capability resource: pages:… or entries:….
func (c *Content) Prefix() string {
	if c.Kind == "page" {
		return "pages"
	}
	return "entries"
}

// Live is whether the record is published and not in the trash.
func (c *Content) Live() bool { return c.Status == "published" && !c.DeletedAt.Valid }

// Label names the record for people: a page's title and path, an entry's
// title.
func (c *Content) Label() string {
	if c.Kind == "page" {
		return c.Title + " (/" + c.Path + ")"
	}
	return c.Title + " (" + c.CollectionSlug + "/" + c.Slug + ")"
}

// Address is how the CLI names it: a page's path, an entry's collection/slug.
func (c *Content) Address() string {
	if c.Kind == "page" {
		return c.Path
	}
	return c.CollectionSlug + "/" + c.Slug
}

const pageCols = `id, slug, path, parent_id, depth, title, status, locale, blocks, frontmatter, seo, schema,
	category_entry_id, translation_group_id, lock_version, publish_at, published_at, unpublish_at, deleted_at, created_at, updated_at`

const entryCols = `e.id, e.slug, e.title, e.status, e.locale, e.blocks, e.frontmatter, e.seo, e.body_markdown, e.collection_id, c.slug,
	e.category_entry_id, e.translation_group_id, e.lock_version, e.publish_at, e.published_at, e.unpublish_at, e.deleted_at, e.created_at, e.updated_at`

func scanPage(rows interface{ Scan(...any) error }) (*Content, error) {
	p := &Content{Kind: "page"}
	err := rows.Scan(&p.ID, &p.Slug, &p.Path, &p.ParentID, &p.Depth, &p.Title, &p.Status, &p.Locale, &p.Blocks, &p.Frontmatter, &p.Seo, &p.Schema,
		&p.CategoryEntryID, &p.TranslationGroupID, &p.LockVersion, &p.PublishAt, &p.PublishedAt, &p.UnpublishAt, &p.DeletedAt, &p.CreatedAt, &p.UpdatedAt)
	p.normalize()
	return p, err
}

func scanEntry(rows interface{ Scan(...any) error }) (*Content, error) {
	e := &Content{Kind: "entry"}
	err := rows.Scan(&e.ID, &e.Slug, &e.Title, &e.Status, &e.Locale, &e.Blocks, &e.Frontmatter, &e.Seo, &e.BodyMarkdown, &e.CollectionID, &e.CollectionSlug,
		&e.CategoryEntryID, &e.TranslationGroupID, &e.LockVersion, &e.PublishAt, &e.PublishedAt, &e.UnpublishAt, &e.DeletedAt, &e.CreatedAt, &e.UpdatedAt)
	e.normalize()
	return e, err
}

func (c *Content) normalize() {
	c.Blocks = c.Blocks.Or("[]")
	c.Frontmatter = c.Frontmatter.Or("{}")
	c.Seo = c.Seo.Or("{}")
	c.Schema = c.Schema.Or(`{"fields":[]}`)
}

// pages reads pages matching a WHERE clause (trashed ones excluded unless
// the clause says otherwise).
func (a *App) pages(ctx context.Context, where string, args ...any) ([]*Content, error) {
	if where == "" {
		where = "1=1"
	}
	if !strings.Contains(where, "deleted_at") {
		where = scoped("deleted_at IS NULL", where)
	}
	rows, err := a.store.QueryContext(ctx, "SELECT "+pageCols+" FROM pages WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Content{}
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (a *App) entries(ctx context.Context, where string, args ...any) ([]*Content, error) {
	if where == "" {
		where = "1=1"
	}
	if !strings.Contains(where, "deleted_at") {
		where = scoped("e.deleted_at IS NULL", where)
	}
	rows, err := a.store.QueryContext(ctx, "SELECT "+entryCols+" FROM collection_entries e JOIN collections c ON c.id = e.collection_id WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Content{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// scoped puts cond in front of a WHERE clause that may end in ORDER BY or
// LIMIT.
func scoped(cond, where string) string {
	cut := len(where)
	for _, kw := range []string{" ORDER BY ", " LIMIT "} {
		if i := strings.Index(where, kw); i >= 0 && i < cut {
			cut = i
		}
	}
	return cond + " AND (" + where[:cut] + ")" + where[cut:]
}

func one(list []*Content, err error) (*Content, error) {
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, store.ErrNotFound
	}
	return list[0], nil
}

func (a *App) pageByPath(ctx context.Context, path string, liveOnly bool) (*Content, error) {
	where := "path = ?"
	if liveOnly {
		where += " AND status = 'published'"
	}
	p, err := one(a.pages(ctx, where+" ORDER BY id LIMIT 1", strings.Trim(path, "/")))
	if errors.Is(err, store.ErrNotFound) {
		return nil, apierr.NotFound("Couldn't find page " + path + ".")
	}
	return p, err
}

func (a *App) entryBySlug(ctx context.Context, collectionID int64, slug string, liveOnly bool) (*Content, error) {
	where := "e.collection_id = ? AND e.slug = ?"
	if liveOnly {
		where += " AND e.status = 'published'"
	}
	e, err := one(a.entries(ctx, where+" ORDER BY e.id LIMIT 1", collectionID, slug))
	if errors.Is(err, store.ErrNotFound) {
		return nil, apierr.NotFound("Couldn't find entry " + slug + ".")
	}
	return e, err
}

func (a *App) contentByID(ctx context.Context, kind string, id int64, withTrashed bool) (*Content, error) {
	trash := ""
	if withTrashed {
		trash = " AND (deleted_at IS NULL OR deleted_at IS NOT NULL)"
	}
	if kind == "page" {
		return one(a.pages(ctx, "id = ?"+trash, id))
	}
	if withTrashed {
		trash = " AND (e.deleted_at IS NULL OR e.deleted_at IS NOT NULL)"
	}
	return one(a.entries(ctx, "e.id = ?"+trash, id))
}

// ---- taxonomy & translations ---------------------------------------------

func (a *App) taxonomy(ctx context.Context, c *Content) (category any, tags []Obj, err error) {
	tags = []Obj{}
	if c.CategoryEntryID.Valid {
		var id int64
		var slug, title string
		err := a.store.QueryRowContext(ctx, `SELECT id, slug, title FROM collection_entries WHERE id = ? AND deleted_at IS NULL`, c.CategoryEntryID.Int64).Scan(&id, &slug, &title)
		if err == nil {
			category = O("id", id, "slug", slug, "title", title)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
	}
	rows, err := a.store.QueryContext(ctx, `SELECT e.id, e.slug, e.title FROM taggings t JOIN collection_entries e ON e.id = t.tag_entry_id
		WHERE t.taggable_type = ? AND t.taggable_id = ? AND e.deleted_at IS NULL ORDER BY t.position, t.id`, c.TaggableType(), c.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var slug, title string
		if err := rows.Scan(&id, &slug, &title); err != nil {
			return nil, nil, err
		}
		tags = append(tags, O("id", id, "slug", slug, "title", title))
	}
	return category, tags, rows.Err()
}

// translations are the record's other languages that are live.
func (a *App) translations(ctx context.Context, c *Content) ([]Obj, error) {
	out := []Obj{}
	if !c.TranslationGroupID.Valid {
		return out, nil
	}
	var siblings []*Content
	var err error
	if c.Kind == "page" {
		siblings, err = a.pages(ctx, "translation_group_id = ? AND id != ? AND status = 'published'", c.TranslationGroupID.Int64, c.ID)
	} else {
		siblings, err = a.entries(ctx, "e.translation_group_id = ? AND e.id != ? AND e.status = 'published'", c.TranslationGroupID.Int64, c.ID)
	}
	if err != nil {
		return nil, err
	}
	for _, s := range siblings {
		out = append(out, O("locale", s.Locale, "path", a.publicPath(s)))
	}
	return out, nil
}

// ---- addresses -----------------------------------------------------------

// publicPath is where the record lives on the site: an SEO canonical when it
// has one, else its path under its locale's prefix ("home" is "/").
func (a *App) publicPath(c *Content) string {
	seo := c.Seo.Map()
	if canon := strings.TrimSpace(str(seo["canonical_url"])); canon != "" {
		return canon
	}
	if canon := strings.TrimSpace(str(seo["canonical"])); canon != "" {
		return canon
	}
	path := "/" + c.Path
	if c.Kind == "entry" {
		path = "/" + c.CollectionSlug + "/" + c.Slug
	}
	if path == "/home" {
		path = "/"
	}
	def := a.defaultLocale()
	if c.Locale == "" || c.Locale == def {
		return path
	}
	prefix := "/" + c.Locale
	if path == prefix || strings.HasPrefix(path, prefix+"/") {
		return path
	}
	if path == "/" {
		return prefix
	}
	return prefix + path
}

func (a *App) publicURL(c *Content) string {
	path := a.publicPath(c)
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	base := strings.TrimRight(strings.TrimSpace(str(a.general(a.ctx)["site_base_url"])), "/")
	if base == "" {
		return path
	}
	return base + path
}

// ---- answers -------------------------------------------------------------

// pageJSON is a whole page as /api has always answered it.
func (a *App) pageJSON(ctx context.Context, p *Content, blocks any) (Obj, error) {
	cat, tags, err := a.taxonomy(ctx, p)
	if err != nil {
		return nil, err
	}
	if blocks == nil {
		blocks = p.Blocks
	}
	return O("id", p.ID, "slug", p.Slug, "path", p.Path, "parent_id", p.ParentID, "depth", p.Depth, "title", p.Title,
		"status", p.Status, "locale", p.Locale, "blocks", blocks, "frontmatter", p.Frontmatter, "seo", p.Seo,
		"published_at", p.PublishedAt, "created_at", p.CreatedAt, "updated_at", p.UpdatedAt, "category", cat, "tags", tags,
		"lock_version", p.LockVersion), nil
}

func (a *App) pageSummaryJSON(ctx context.Context, p *Content) (Obj, error) {
	cat, tags, err := a.taxonomy(ctx, p)
	if err != nil {
		return nil, err
	}
	return O("id", p.ID, "slug", p.Slug, "path", p.Path, "parent_id", p.ParentID, "depth", p.Depth, "title", p.Title,
		"status", p.Status, "locale", p.Locale, "published_at", p.PublishedAt, "created_at", p.CreatedAt, "updated_at", p.UpdatedAt,
		"category", cat, "tags", tags), nil
}

func (a *App) entryJSON(ctx context.Context, e *Content) (Obj, error) {
	cat, tags, err := a.taxonomy(ctx, e)
	if err != nil {
		return nil, err
	}
	return O("id", e.ID, "slug", e.Slug, "title", e.Title, "status", e.Status, "locale", e.Locale, "frontmatter", e.Frontmatter,
		"seo", e.Seo, "body_markdown", e.BodyMarkdown, "blocks", e.Blocks, "published_at", e.PublishedAt, "created_at", e.CreatedAt,
		"updated_at", e.UpdatedAt, "collection_slug", e.CollectionSlug, "category", cat, "tags", tags, "lock_version", e.LockVersion), nil
}

func (a *App) entrySummaryJSON(ctx context.Context, e *Content) (Obj, error) {
	cat, tags, err := a.taxonomy(ctx, e)
	if err != nil {
		return nil, err
	}
	return O("id", e.ID, "slug", e.Slug, "title", e.Title, "status", e.Status, "locale", e.Locale, "published_at", e.PublishedAt,
		"created_at", e.CreatedAt, "updated_at", e.UpdatedAt, "category", cat, "tags", tags), nil
}

// ---- writing -------------------------------------------------------------

// contentKeys is what a write may set, as the Rails core permitted.
var pageKeys = []string{"slug", "title", "status", "locale", "published_at", "publish_at", "unpublish_at", "category_entry_id", "parent_id", "tag_ids", "blocks", "frontmatter", "seo", "translation_of"}
var entryKeys = []string{"slug", "title", "status", "locale", "body_markdown", "published_at", "publish_at", "unpublish_at", "category_entry_id", "tag_ids", "blocks", "frontmatter", "seo", "translation_of"}

func (c *Content) permitted() []string {
	if c.Kind == "page" {
		return pageKeys
	}
	return entryKeys
}

// assign applies the permitted keys of params to c.
func (c *Content) assign(params map[string]any) error {
	for _, key := range c.permitted() {
		v, ok := params[key]
		if !ok {
			continue
		}
		switch key {
		case "slug":
			c.Slug = strings.TrimSpace(str(v))
		case "title":
			c.Title = strings.TrimSpace(str(v))
		case "status":
			c.Status = str(v)
		case "locale":
			c.Locale = str(v)
		case "body_markdown":
			c.BodyMarkdown = str(v)
		case "published_at", "publish_at", "unpublish_at":
			var t store.Time
			if s := str(v); s != "" {
				var err error
				if t, err = store.ParseTime(s); err != nil {
					return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{key: {"isn't a date and time"}}}
				}
			}
			switch key {
			case "published_at":
				c.PublishedAt = t
			case "publish_at":
				c.PublishAt = t
			default:
				c.UnpublishAt = t
			}
		case "category_entry_id", "parent_id":
			var n store.NullInt
			if id, ok := intOf(v); ok {
				n = store.IntOf(id)
			}
			if key == "parent_id" {
				c.ParentID = n
			} else {
				c.CategoryEntryID = n
			}
		case "tag_ids":
			c.tagIDs = []int64{}
			for _, t := range asList(v) {
				if id, ok := intOf(t); ok {
					c.tagIDs = append(c.tagIDs, id)
				}
			}
		case "blocks":
			if v == nil {
				v = []any{}
			}
			c.Blocks = store.MustJSON(normalizeBlocks(v))
		case "frontmatter", "seo":
			if v == nil {
				v = map[string]any{}
			}
			if key == "frontmatter" {
				c.Frontmatter = store.MustJSON(v)
			} else {
				c.Seo = store.MustJSON(v)
			}
		}
	}
	return nil
}

// normalizeBlocks gives each block an id, as the editor does, and keeps
// only a block's own keys.
func normalizeBlocks(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		b, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		block := map[string]any{"id": b["id"], "type": b["type"], "data": b["data"]}
		if str(b["id"]) == "" {
			block["id"] = secret.Token(8)
		}
		if b["version"] != nil {
			block["version"] = b["version"]
		}
		if block["data"] == nil {
			block["data"] = map[string]any{}
		}
		out = append(out, block)
	}
	return out
}

// blockTypeResolver reads every block type's fields once. Failing to is an
// error, not "no block types": that would pass blocks unvalidated and drop
// their references.
func (a *App) blockTypeResolver(ctx context.Context) (fields.BlockTypes, error) {
	cache := map[string][]fields.Field{}
	rows, err := a.store.QueryContext(ctx, `SELECT slug, fields FROM block_types`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var defs store.JSON
		if err := rows.Scan(&slug, &defs); err != nil {
			return nil, err
		}
		cache[slug] = fields.Parse(defs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return func(slug string) ([]fields.Field, bool) {
		f, ok := cache[slug]
		return f, ok
	}, nil
}

// contentFields is what a record's frontmatter is written against: its
// page's own schema, or its collection's.
func (a *App) contentFields(ctx context.Context, c *Content) ([]fields.Field, error) {
	if c.Kind == "page" {
		return fields.Parse(c.Schema), nil
	}
	var schema store.JSON
	if err := a.store.QueryRowContext(ctx, `SELECT schema FROM collections WHERE id = ?`, c.CollectionID).Scan(&schema); err != nil {
		return nil, store.NotFound(err)
	}
	return fields.Parse(schema), nil
}

// validateContent checks a record about to be saved; prior is what's stored
// (nil for a new one).
func (a *App) validateContent(ctx context.Context, c, prior *Content) error {
	inv := apierr.Invalid{}
	if c.Slug == "" {
		inv.Add("slug", "can't be blank")
	} else if !contentSlugFormat.MatchString(c.Slug) {
		inv.Add("slug", "is invalid: lowercase letters, numbers and hyphens, starting with a letter or number")
	}
	if c.Title == "" {
		inv.Add("title", "can't be blank")
	}
	if !slices.Contains(statuses, c.Status) {
		inv.Add("status", "is not included in the list")
	}
	if c.Locale == "" {
		inv.Add("locale", "can't be blank")
	}
	if c.PublishAt.Valid && c.UnpublishAt.Valid && !c.UnpublishAt.After(c.PublishAt.Time) {
		inv.Add("unpublish_at", "must be after publish_at")
	}
	// A change of status or schedule alone (the scheduler's publish and
	// unpublish among them) leaves what the record says as it was: it isn't
	// held to rules its content may predate.
	if prior == nil || !statusOnly(prior, c) {
		if err := a.validateBody(ctx, c, prior, inv); err != nil {
			return err
		}
	}
	if err := a.validatePlace(ctx, c, inv); err != nil {
		return err
	}
	if c.CategoryEntryID.Valid {
		if !a.inPool(ctx, c, "categories_collection_id", []int64{c.CategoryEntryID.Int64}) {
			inv.Add("category_entry_id", "isn't one of the categories")
		}
	}
	if len(c.tagIDs) > 0 && !a.inPool(ctx, c, "tags_collection_id", c.tagIDs) {
		inv.Add("tag_ids", "aren't all tags")
	}
	return inv.Err()
}

// validateBody checks what the record says: its blocks, its frontmatter
// against its schema, its Markdown body and SEO. Markup is refused only in
// text the save changes (fields.Texts): HTML stored before the rule, or
// imported, doesn't stop a save that leaves it as it was.
func (a *App) validateBody(ctx context.Context, c, prior *Content, inv apierr.Invalid) error {
	types, err := a.blockTypeResolver(ctx)
	if err != nil {
		return err
	}
	defs, err := a.contentFields(ctx, c)
	if err != nil {
		return err
	}
	var before fields.Texts
	if prior != nil {
		before = fields.Texts{}
		var oldBlocks []any
		var oldFm map[string]any
		if json.Unmarshal(prior.Blocks, &oldBlocks) == nil {
			before.AddBlocks(oldBlocks, types, "blocks")
		}
		if json.Unmarshal(prior.Frontmatter, &oldFm) == nil {
			before.AddData(defs, oldFm, types, "frontmatter")
		}
	}
	var blocks []any
	if err := json.Unmarshal(c.Blocks, &blocks); err != nil {
		inv.Add("blocks", "must be a list of blocks")
	} else {
		fields.ValidateBlocks(blocks, nil, types, inv, "blocks")
		fields.ValidateMarkupBlocks(blocks, types, before, inv, "blocks")
	}
	var fm map[string]any
	if err := json.Unmarshal(c.Frontmatter, &fm); err != nil {
		inv.Add("frontmatter", "must be an object")
	} else {
		fields.ValidateData(defs, fm, types, inv, "frontmatter")
		fields.ValidateMarkupData(defs, fm, types, before, inv, "frontmatter")
	}
	if (prior == nil || c.BodyMarkdown != prior.BodyMarkdown) && fields.MarkdownHasHTML(c.BodyMarkdown) {
		inv.Add("body_markdown", fields.MarkdownHTMLMessage)
	}
	if !json.Valid(c.Seo) || !strings.HasPrefix(strings.TrimSpace(string(c.Seo)), "{") {
		inv.Add("seo", "must be an object")
	}
	return nil
}

// statusOnly is whether rec differs from prior in its status and schedule
// alone.
func statusOnly(prior, rec *Content) bool {
	p := *prior
	p.Status, p.PublishAt, p.UnpublishAt, p.PublishedAt = rec.Status, rec.PublishAt, rec.UnpublishAt, rec.PublishedAt
	return contentEqual(&p, rec) && rec.tagIDs == nil
}

// validatePlace checks where the record sits: a page's parent, and that its
// slug is free under that parent (an entry's, in its collection and locale).
func (a *App) validatePlace(ctx context.Context, c *Content, inv apierr.Invalid) error {
	taken, args := `SELECT id FROM collection_entries WHERE collection_id = ? AND slug = ? AND locale = ? AND id != ? AND deleted_at IS NULL LIMIT 1`,
		[]any{c.CollectionID, c.Slug, c.Locale, c.ID}
	if c.Kind == "page" {
		if c.ParentID.Valid {
			if err := a.validateParent(ctx, c, inv); err != nil {
				return err
			}
		}
		taken, args = `SELECT id FROM pages WHERE slug = ? AND parent_id IS ? AND id != ? AND deleted_at IS NULL LIMIT 1`,
			[]any{c.Slug, c.ParentID.DB(), c.ID}
	}
	var clash int64
	err := a.store.QueryRowContext(ctx, taken, args...).Scan(&clash)
	if err == nil {
		inv.Add("slug", "has already been taken")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// validateParent checks a page's parent: a live page, and neither the page
// itself nor one of its descendants.
func (a *App) validateParent(ctx context.Context, c *Content, inv apierr.Invalid) error {
	switch {
	case c.ParentID.Int64 == c.ID:
		inv.Add("parent_id", "can't be the page itself")
	case c.ID != 0 && a.isUnder(ctx, c.ParentID.Int64, c.ID):
		inv.Add("parent_id", "can't be one of the page's own descendants")
	}
	exists, err := store.Count(ctx, a.store, `SELECT count(*) FROM pages WHERE id = ? AND deleted_at IS NULL`, c.ParentID.Int64)
	if err != nil {
		return err
	}
	if exists == 0 {
		inv.Add("parent_id", "isn't a page")
	}
	return nil
}

// isUnder is whether page node is page id or one of its descendants:
// walking up from node meets id (within 64 levels).
func (a *App) isUnder(ctx context.Context, node, id int64) bool {
	for range 64 {
		if node == 0 {
			return false
		}
		if node == id {
			return true
		}
		var parent store.NullInt
		if a.store.QueryRowContext(ctx, `SELECT parent_id FROM pages WHERE id = ?`, node).Scan(&parent) != nil {
			return false
		}
		node = parent.Int64
	}
	return false
}

// inPool: the ids are entries of the record's taxonomy pool (an entry's
// collection's categories or tags collection; for pages, any entries).
func (a *App) inPool(ctx context.Context, c *Content, poolColumn string, ids []int64) bool {
	args := []any{}
	for _, id := range ids {
		args = append(args, id)
	}
	query := `SELECT count(*) FROM collection_entries WHERE deleted_at IS NULL AND id IN (` + store.Placeholders(len(ids)) + `)`
	if c.Kind == "entry" {
		var pool store.NullInt
		// A pool that can't be read is no pool: the ids aren't in it.
		if err := a.store.QueryRowContext(ctx, `SELECT `+poolColumn+` FROM collections WHERE id = ?`, c.CollectionID).Scan(&pool); err != nil || !pool.Valid {
			return false
		}
		query += ` AND collection_id = ?`
		args = append(args, pool.Int64)
	}
	n, err := store.Count(ctx, a.store, query, args...)
	return err == nil && n == len(ids)
}

// writeResult is what a write did: saved (with the record), or held for a
// person (a revision).
type writeResult struct {
	Record    *Content
	Held      *Revision
	Ignored   []string
	Unchanged bool
	Created   bool
}

// saveContent creates or updates a page or entry from a write's params, as
// the actor. It enforces the publish gate: a write to something live, or one
// that publishes or schedules, needs <prefix>:publish. Without it a write to
// a live record is held as a revision for review, and one that would
// publish a draft is refused.
func (a *App) saveContent(ctx context.Context, actor *Actor, rec *Content, params map[string]any) (*writeResult, error) {
	creating := rec.ID == 0
	prior := *rec
	if creating {
		rec.Status = "draft"
		rec.Locale = a.defaultLocale()
		rec.normalize()
	}
	if err := rec.assign(params); err != nil {
		return nil, err
	}
	if rec.Kind == "page" {
		if err := a.computePath(ctx, rec); err != nil {
			return nil, err
		}
	}
	var stored *Content
	if !creating {
		stored = &prior
	}
	if err := a.validateContent(ctx, rec, stored); err != nil {
		return nil, err
	}

	wasLive := !creating && prior.Status == "published"
	if (wasLive || publishes(&prior, rec)) && !actor.Can(rec.Prefix()+":publish") {
		if !wasLive {
			return nil, apierr.Forbidden(rec.Prefix() + ":publish")
		}
		return a.holdWrite(ctx, actor, &prior, params)
	}

	if !creating {
		if lv, ok := intOf(params["lock_version"]); ok && int(lv) != prior.LockVersion {
			return nil, staleWrite(lv, int64(prior.LockVersion))
		}
		if contentEqual(&prior, rec) && rec.tagIDs == nil {
			return &writeResult{Record: rec, Unchanged: true}, nil
		}
	}
	now := store.Now()
	rec.stampPublished(&prior, now)
	if tr, ok := intOf(params["translation_of"]); ok && creating {
		if err := a.joinTranslation(ctx, rec, tr); err != nil {
			return nil, err
		}
	}

	err := a.store.Tx(ctx, func(tx *sql.Tx) error {
		write := updateContent
		if creating {
			write = insertContent
		}
		if err := write(ctx, tx, rec, &prior, now); err != nil {
			return err
		}
		if rec.tagIDs != nil {
			if err := writeTaggings(ctx, tx, rec); err != nil {
				return err
			}
		}
		if creating || bodyChanged(&prior, rec) {
			if err := writeVersion(ctx, tx, rec, actor); err != nil {
				return err
			}
		}
		return a.writeReferences(ctx, tx, rec)
	})
	if err != nil {
		return nil, err
	}

	a.announce(ctx, actor, rec, &prior, creating)
	return &writeResult{Record: rec, Created: creating}, nil
}

// publishes is whether going from prior to rec publishes the record, now
// or (a new publish_at) later.
func publishes(prior, rec *Content) bool {
	if rec.Status == "published" && prior.Status != "published" {
		return true
	}
	return rec.PublishAt.Valid && (!prior.PublishAt.Valid || !rec.PublishAt.Equal(prior.PublishAt.Time))
}

// stampPublished notes when a record that's live first went live, and
// drops a schedule that publishing it now overtook.
func (c *Content) stampPublished(prior *Content, now store.Time) {
	if c.Status != "published" {
		return
	}
	if !c.PublishedAt.Valid {
		c.PublishedAt = now
	}
	if prior.Status != "published" {
		c.PublishAt = store.Time{}
	}
}

// bodyChanged is whether what a version keeps (blocks, frontmatter, the
// markdown body) differs between prior and rec.
func bodyChanged(prior, rec *Content) bool {
	return string(prior.Blocks) != string(rec.Blocks) || string(prior.Frontmatter) != string(rec.Frontmatter) || prior.BodyMarkdown != rec.BodyMarkdown
}

// insertContent writes a new record and sets its id.
func insertContent(ctx context.Context, tx *sql.Tx, rec, _ *Content, now store.Time) error {
	rec.CreatedAt, rec.UpdatedAt = now, now
	var res sql.Result
	var err error
	if rec.Kind == "page" {
		res, err = tx.ExecContext(ctx, `INSERT INTO pages (slug, path, parent_id, depth, title, status, locale, blocks, frontmatter, seo, schema,
			category_entry_id, translation_group_id, publish_at, published_at, unpublish_at, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			rec.Slug, rec.Path, rec.ParentID.DB(), rec.Depth, rec.Title, rec.Status, rec.Locale, string(rec.Blocks), string(rec.Frontmatter),
			string(rec.Seo), string(rec.Schema), rec.CategoryEntryID.DB(), rec.TranslationGroupID.DB(), rec.PublishAt.DB(), rec.PublishedAt.DB(),
			rec.UnpublishAt.DB(), now.DB(), now.DB())
	} else {
		res, err = tx.ExecContext(ctx, `INSERT INTO collection_entries (collection_id, slug, title, status, locale, body_markdown, blocks, frontmatter, seo,
			category_entry_id, translation_group_id, publish_at, published_at, unpublish_at, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			rec.CollectionID, rec.Slug, rec.Title, rec.Status, rec.Locale, rec.BodyMarkdown, string(rec.Blocks), string(rec.Frontmatter),
			string(rec.Seo), rec.CategoryEntryID.DB(), rec.TranslationGroupID.DB(), rec.PublishAt.DB(), rec.PublishedAt.DB(),
			rec.UnpublishAt.DB(), now.DB(), now.DB())
	}
	if err != nil {
		return err
	}
	rec.ID, err = res.LastInsertId()
	return err
}

// updateContent writes rec over prior, and moves a page's descendants
// when its path changed.
func updateContent(ctx context.Context, tx *sql.Tx, rec, prior *Content, now store.Time) error {
	rec.UpdatedAt = now
	rec.LockVersion = prior.LockVersion + 1
	if rec.Kind != "page" {
		res, err := tx.ExecContext(ctx, `UPDATE collection_entries SET slug=?, title=?, status=?, locale=?, body_markdown=?, blocks=?, frontmatter=?, seo=?,
			category_entry_id=?, translation_group_id=?, publish_at=?, published_at=?, unpublish_at=?, lock_version=?, updated_at=? WHERE id=? AND lock_version=?`,
			rec.Slug, rec.Title, rec.Status, rec.Locale, rec.BodyMarkdown, string(rec.Blocks), string(rec.Frontmatter), string(rec.Seo),
			rec.CategoryEntryID.DB(), rec.TranslationGroupID.DB(), rec.PublishAt.DB(), rec.PublishedAt.DB(), rec.UnpublishAt.DB(),
			rec.LockVersion, now.DB(), rec.ID, prior.LockVersion)
		return wroteOver(res, err, prior.LockVersion)
	}
	res, err := tx.ExecContext(ctx, `UPDATE pages SET slug=?, path=?, parent_id=?, depth=?, title=?, status=?, locale=?, blocks=?, frontmatter=?, seo=?,
		category_entry_id=?, translation_group_id=?, publish_at=?, published_at=?, unpublish_at=?, lock_version=?, updated_at=? WHERE id=? AND lock_version=?`,
		rec.Slug, rec.Path, rec.ParentID.DB(), rec.Depth, rec.Title, rec.Status, rec.Locale, string(rec.Blocks), string(rec.Frontmatter),
		string(rec.Seo), rec.CategoryEntryID.DB(), rec.TranslationGroupID.DB(), rec.PublishAt.DB(), rec.PublishedAt.DB(), rec.UnpublishAt.DB(),
		rec.LockVersion, now.DB(), rec.ID, prior.LockVersion)
	err = wroteOver(res, err, prior.LockVersion)
	if err == nil && rec.Path != prior.Path {
		err = resyncDescendants(ctx, tx, rec)
	}
	return err
}

func contentEqual(a, b *Content) bool {
	return a.Slug == b.Slug && a.Title == b.Title && a.Status == b.Status && a.Locale == b.Locale && a.Path == b.Path &&
		a.ParentID == b.ParentID && a.BodyMarkdown == b.BodyMarkdown && jsonEqual(a.Blocks, b.Blocks) &&
		jsonEqual(a.Frontmatter, b.Frontmatter) && jsonEqual(a.Seo, b.Seo) && a.CategoryEntryID == b.CategoryEntryID &&
		a.PublishAt == b.PublishAt && a.PublishedAt == b.PublishedAt && a.UnpublishAt == b.UnpublishAt
}

func jsonEqual(x, y store.JSON) bool {
	var a, b any
	if json.Unmarshal(x, &a) != nil || json.Unmarshal(y, &b) != nil {
		return bytes.Equal(x, y)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func (a *App) computePath(ctx context.Context, p *Content) error {
	if p.Slug == "" {
		return nil
	}
	p.Path, p.Depth = p.Slug, 0
	if p.ParentID.Valid {
		var parentPath string
		var depth int
		err := a.store.QueryRowContext(ctx, `SELECT path, depth FROM pages WHERE id = ?`, p.ParentID.Int64).Scan(&parentPath, &depth)
		if err == nil {
			p.Path, p.Depth = parentPath+"/"+p.Slug, depth+1
		}
	}
	return nil
}

// resyncDescendants rewrites the paths under a page that moved or was
// renamed.
func resyncDescendants(ctx context.Context, tx *sql.Tx, p *Content) error {
	type node struct {
		id    int64
		path  string
		depth int
	}
	queue := []node{{p.ID, p.Path, p.Depth}}
	for visited := 0; len(queue) > 0 && visited < 10000; visited++ {
		n := queue[0]
		queue = queue[1:]
		rows, err := tx.QueryContext(ctx, `SELECT id, slug FROM pages WHERE parent_id = ?`, n.id)
		if err != nil {
			return err
		}
		children, err := store.ScanAll(rows, func(r *sql.Rows) (node, error) {
			var id int64
			var slug string
			err := r.Scan(&id, &slug)
			return node{id, n.path + "/" + slug, n.depth + 1}, err
		})
		if err != nil {
			return err
		}
		for _, ch := range children {
			if _, err := tx.ExecContext(ctx, `UPDATE pages SET path = ?, depth = ?, updated_at = ? WHERE id = ?`, ch.path, ch.depth, store.Now().DB(), ch.id); err != nil {
				return err
			}
			queue = append(queue, ch)
		}
	}
	return nil
}

func writeTaggings(ctx context.Context, tx *sql.Tx, c *Content) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM taggings WHERE taggable_type = ? AND taggable_id = ?`, c.TaggableType(), c.ID); err != nil {
		return err
	}
	now := store.Now().DB()
	for i, id := range c.tagIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO taggings (tag_entry_id, taggable_id, taggable_type, position, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
			id, c.ID, c.TaggableType(), i, now, now); err != nil {
			return err
		}
	}
	return nil
}

func writeVersion(ctx context.Context, tx *sql.Tx, c *Content, actor *Actor) error {
	var author any
	if actor != nil && actor.UserID != 0 {
		author = actor.UserID
	}
	var err error
	if c.Kind == "page" {
		_, err = tx.ExecContext(ctx, `INSERT INTO page_versions (page_id, author_id, blocks, created_at) VALUES (?,?,?,?)`, c.ID, author, string(c.Blocks), store.Now().DB())
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO collection_entry_versions (collection_entry_id, author_id, blocks, body_markdown, frontmatter, created_at) VALUES (?,?,?,?,?,?)`,
			c.ID, author, string(c.Blocks), c.BodyMarkdown, string(c.Frontmatter), store.Now().DB())
	}
	return err
}

// joinTranslation puts a new record in the translation group of the record
// it translates (making the group if that one had none).
func (a *App) joinTranslation(ctx context.Context, rec *Content, ofID int64) error {
	of, err := a.contentByID(ctx, rec.Kind, ofID, false)
	if err != nil {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"translation_of": {"isn't a " + rec.Kind}}}
	}
	if of.Locale == rec.Locale {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"locale": {"must differ from the record it translates"}}}
	}
	if !of.TranslationGroupID.Valid {
		res, err := a.store.ExecContext(ctx, `INSERT INTO translation_groups (kind, created_at, updated_at) VALUES (?,?,?)`, of.TaggableType(), store.Now().DB(), store.Now().DB())
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		of.TranslationGroupID = store.IntOf(id)
		if _, err := a.store.ExecContext(ctx, `UPDATE `+of.table()+` SET translation_group_id = ? WHERE id = ?`, id, of.ID); err != nil {
			return err
		}
	}
	rec.TranslationGroupID = of.TranslationGroupID
	return nil
}

// ---- trash ---------------------------------------------------------------

// trashContent moves a record to the trash; a page takes its children.
func (a *App) trashContent(ctx context.Context, actor *Actor, c *Content) error {
	now := store.Now()
	var trashed []*Content
	err := a.store.Tx(ctx, func(tx *sql.Tx) error {
		ids := []int64{c.ID}
		if c.Kind == "page" {
			for i := 0; i < len(ids) && i < 10000; i++ {
				children, err := store.Int64s(ctx, tx, `SELECT id FROM pages WHERE parent_id = ? AND deleted_at IS NULL`, ids[i])
				if err != nil {
					return err
				}
				ids = append(ids, children...)
			}
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE `+c.table()+` SET deleted_at = ?, updated_at = ? WHERE id = ?`, now.DB(), now.DB(), id); err != nil { //nolint:gosec // table() is "pages" or "collection_entries"
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	trashed = append(trashed, c)
	for _, t := range trashed {
		a.audit(ctx, actor, t.Kind+".trashed", t.TaggableType(), t.ID, t.Label(), nil)
		if t.Live() {
			a.contentEvent(ctx, t, "deleted")
		}
	}
	return nil
}

func (a *App) purgeContent(ctx context.Context, actor *Actor, c *Content) error {
	err := a.store.Tx(ctx, func(tx *sql.Tx) error {
		stmts := []string{
			`DELETE FROM taggings WHERE taggable_type = ? AND taggable_id = ?`,
			`DELETE FROM content_references WHERE owner_type = ? AND owner_id = ?`,
		}
		for _, s := range stmts {
			if _, err := tx.ExecContext(ctx, s, c.TaggableType(), c.ID); err != nil {
				return err
			}
		}
		if c.Kind == "page" {
			if _, err := tx.ExecContext(ctx, `UPDATE pages SET parent_id = NULL WHERE parent_id = ?`, c.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM page_versions WHERE page_id = ?`, c.ID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `DELETE FROM collection_entry_versions WHERE collection_entry_id = ?`, c.ID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM `+c.table()+` WHERE id = ?`, c.ID) //nolint:gosec // table() is "pages" or "collection_entries"
		return err
	})
	if err == nil {
		a.audit(ctx, actor, c.Kind+".purged", c.TaggableType(), c.ID, c.Label(), nil)
		if c.Live() {
			a.contentEvent(ctx, c, "deleted")
		}
	}
	return err
}

func (a *App) restoreContent(ctx context.Context, actor *Actor, c *Content) error {
	if c.Kind == "page" && c.ParentID.Valid {
		parentGone, err := store.Count(ctx, a.store.DB, `SELECT count(*) FROM pages WHERE id = ? AND deleted_at IS NOT NULL`, c.ParentID.Int64)
		if err != nil {
			return err
		}
		if parentGone > 0 {
			return apierr.Conflict("Its parent page is in the trash: restore that first.")
		}
	}
	var clash int
	var err error
	if c.Kind == "page" {
		clash, err = store.Count(ctx, a.store.DB, `SELECT count(*) FROM pages WHERE path = ? AND deleted_at IS NULL AND id != ?`, c.Path, c.ID)
	} else {
		clash, err = store.Count(ctx, a.store.DB, `SELECT count(*) FROM collection_entries WHERE collection_id = ? AND slug = ? AND locale = ? AND deleted_at IS NULL AND id != ?`, c.CollectionID, c.Slug, c.Locale, c.ID)
	}
	if err != nil {
		return err
	}
	if clash > 0 {
		return apierr.Conflict("Something else has its address now (" + c.Address() + ").")
	}
	// A page comes back with the children that went with it.
	deletedAt := c.DeletedAt
	err = a.store.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE `+c.table()+` SET deleted_at = NULL, updated_at = ? WHERE id = ?`, store.Now().DB(), c.ID); err != nil { //nolint:gosec // table() is "pages" or "collection_entries"
			return err
		}
		if c.Kind == "page" && deletedAt.Valid {
			_, err := tx.ExecContext(ctx, `UPDATE pages SET deleted_at = NULL WHERE deleted_at = ? AND path LIKE ?`, deletedAt.DB(), c.Path+"/%")
			return err
		}
		return nil
	})
	if err == nil {
		c.DeletedAt = store.Time{}
		a.audit(ctx, actor, c.Kind+".restored", c.TaggableType(), c.ID, c.Label(), nil)
		if c.Live() {
			a.contentEvent(ctx, c, "published")
		}
	}
	return err
}

// ---- events --------------------------------------------------------------

// announce tells everyone who listens what a save did: the audit log,
// webhooks, the deploy, plugins.
func (a *App) announce(ctx context.Context, actor *Actor, rec, prior *Content, created bool) {
	var action, event string
	switch {
	case created:
		action = "created"
		if rec.Status == "published" {
			event = "published"
		}
	case rec.Status == "published" && prior.Status != "published":
		action, event = "published", "published"
	case rec.Status != "published" && prior.Status == "published":
		action, event = "unpublished", "unpublished"
	default:
		action = "updated"
		if rec.Status == "published" {
			event = "updated"
		}
	}
	a.audit(ctx, actor, rec.Kind+"."+action, rec.TaggableType(), rec.ID, rec.Label(), map[string]any{"path": rec.Address(), "status": rec.Status})
	if event != "" {
		a.contentEvent(ctx, rec, event)
	}
}

// contentEvent is a change the public site sees: webhooks get it, the site
// is rebuilt, plugins hear of it.
func (a *App) contentEvent(ctx context.Context, c *Content, what string) {
	a.publish(ctx, change{Event: c.Kind + "." + what, Data: a.webhookPayload(c), Tags: c.cacheTags()})
}

func (c *Content) cacheTags() []string {
	if c.Kind == "page" {
		return []string{"page:" + c.Path, "pages", "sitemap"}
	}
	return []string{"entry:" + c.CollectionSlug + "/" + c.Slug, "collection:" + c.CollectionSlug, "sitemap"}
}

// webhookPayload is a record's data in a content webhook, the keys
// contracts/webhook_contract.json holds the CMS to.
func (a *App) webhookPayload(c *Content) Obj {
	if c.Kind == "page" {
		return O("id", c.ID, "slug", c.Slug, "path", c.Path, "url", a.publicURL(c), "title", c.Title, "status", c.Status,
			"locale", c.Locale, "published_at", c.PublishedAt.ISO(), "updated_at", c.UpdatedAt.ISO())
	}
	return O("id", c.ID, "slug", c.Slug, "collection_slug", c.CollectionSlug, "url", a.publicURL(c), "title", c.Title,
		"status", c.Status, "locale", c.Locale, "published_at", c.PublishedAt.ISO(), "updated_at", c.UpdatedAt.ISO())
}

// ---- references ----------------------------------------------------------

// writeReferences records what a record points at, for GET /api/references and the
// unused-media list, in the Rails core's content_references rows: a block's
// references at its index, the frontmatter's and SEO's at -1.
func (a *App) writeReferences(ctx context.Context, tx *sql.Tx, c *Content) error {
	types, err := a.blockTypeResolver(ctx)
	if err != nil {
		return err
	}
	defs, err := a.contentFields(ctx, c)
	if err != nil {
		return err
	}
	var rows []refRow
	for i, b := range c.Blocks.List() {
		var refs []fields.Ref
		fields.BlockRefs(b, types, &refs)
		rows = appendRefs(rows, refs, i)
	}
	var refs []fields.Ref
	fields.Refs(defs, c.Frontmatter.Map(), types, &refs)
	if img, ok := c.Seo.Map()["og_image_id"].(string); ok && img != "" {
		refs = append(refs, fields.Ref{Kind: "asset", RefType: "asset", RefID: img})
	}
	rows = appendRefs(rows, refs, -1)
	return replaceReferences(ctx, tx, c.TaggableType(), c.ID, rows)
}

type refRow struct {
	fields.Ref
	Position int
}

func appendRefs(rows []refRow, refs []fields.Ref, position int) []refRow {
	for _, r := range refs {
		rows = append(rows, refRow{r, position})
	}
	return rows
}

func replaceReferences(ctx context.Context, tx *sql.Tx, ownerType string, ownerID int64, rows []refRow) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_references WHERE owner_type = ? AND owner_id = ?`, ownerType, ownerID); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO content_references (owner_type, owner_id, ref_type, ref_id, kind, position, created_at) VALUES (?,?,?,?,?,?,?)`,
			ownerType, ownerID, r.RefType, r.RefID, r.Kind, r.Position, store.Now().DB()); err != nil {
			return err
		}
	}
	return nil
}

// assetIDs is every asset the records point at.
func (a *App) assetIDs(ctx context.Context, ownerType string, ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	args := []any{ownerType}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := a.store.QueryContext(ctx, `SELECT DISTINCT ref_id FROM content_references WHERE ref_type = 'asset' AND owner_type = ? AND owner_id IN (`+store.Placeholders(len(ids))+`)`, args...)
	if err != nil {
		a.log.Error("asset ids", "err", err)
		return nil
	}
	refs, err := store.ScanAll(rows, func(r *sql.Rows) (string, error) {
		var s string
		err := r.Scan(&s)
		return s, err
	})
	if err != nil {
		// Resolved assets are a convenience beside the records: logged, and
		// the records still answer.
		a.log.Error("asset ids", "err", err)
		return nil
	}
	var out []int64
	for _, s := range refs {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// staleWrite is the 409 for a write that names a lock_version other than the
// record's: someone saved it since the writer read it.
func staleWrite(sent, now int64) error {
	return apierr.Conflict(fmt.Sprintf("Someone else saved this since you read it (lock_version %d, now %d). Read it again and redo the change.", sent, now))
}

// wroteOver checks that an UPDATE … WHERE lock_version = <read> changed the
// row: none changed means another save got there between the read and this
// write, which is a conflict rather than a silent overwrite.
func wroteOver(res sql.Result, err error, read int) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return apierr.Conflict(fmt.Sprintf("Someone else saved this while it was being saved (lock_version %d is gone). Read it again and redo the change.", read))
	}
	return nil
}
