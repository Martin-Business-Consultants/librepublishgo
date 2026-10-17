package cms

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
)

func (a *App) search(c *Ctx) error {
	q := c.Query("q")
	if c.R.Method == http.MethodPost {
		var body map[string]any
		if err := c.Body(&body); err != nil {
			return err
		}
		q = str(firstOf(body, "query", "q"))
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return apierr.BadRequest("param is missing or the value is empty: query")
	}
	ctx := c.R.Context()
	like := "%" + q + "%"
	results := []Obj{}
	if c.can("pages:read") {
		where := "(title LIKE ? OR path LIKE ? OR blocks LIKE ? OR frontmatter LIKE ?)"
		if !c.can("pages:write") {
			where += " AND status = 'published'"
		}
		pages, err := a.pages(ctx, where+" ORDER BY updated_at DESC LIMIT 25", like, like, like, like)
		if err != nil {
			return err
		}
		for _, p := range pages {
			results = append(results, O("type", "page", "id", p.ID, "title", p.Title, "path", p.Path, "status", p.Status, "snippet", snippet(string(p.Blocks), q)))
		}
	}
	if c.can("entries:read") {
		where := "(e.title LIKE ? OR e.slug LIKE ? OR e.body_markdown LIKE ? OR e.frontmatter LIKE ? OR e.blocks LIKE ?)"
		if !c.can("entries:write") {
			where += " AND e.status = 'published'"
		}
		entries, err := a.entries(ctx, where+" ORDER BY e.updated_at DESC LIMIT 25", like, like, like, like, like)
		if err != nil {
			return err
		}
		for _, e := range entries {
			results = append(results, O("type", "entry", "id", e.ID, "title", e.Title, "path", e.CollectionSlug+"/"+e.Slug, "collection", e.CollectionSlug,
				"status", e.Status, "snippet", snippet(e.BodyMarkdown+" "+string(e.Frontmatter), q)))
		}
	}
	if c.can("globals:read") {
		globals, err := a.globals(ctx, "(name LIKE ? OR slug LIKE ? OR data LIKE ?) LIMIT 10", like, like, like)
		if err != nil {
			return err
		}
		for _, g := range globals {
			results = append(results, O("type", "global", "id", g.ID, "title", g.Name, "path", g.Slug, "snippet", snippet(string(g.Data), q)))
		}
	}
	if c.can("assets:read") {
		assets, err := a.assets(ctx, "(a.name LIKE ? OR a.alt LIKE ? OR b.filename LIKE ?) LIMIT 10", like, like, like)
		if err != nil {
			return err
		}
		for _, as := range assets {
			results = append(results, O("type", "asset", "id", as.ID, "title", as.Filename, "path", as.Folder, "snippet", as.Alt))
		}
	}
	c.Summary = fmt.Sprintf("%d results for %q.", len(results), q)
	return c.JSON(200, O("query", q, "results", results, "total", len(results)))
}

func snippet(text, q string) string {
	i := strings.Index(strings.ToLower(text), strings.ToLower(q))
	if i < 0 {
		return ""
	}
	start, end := max(0, i-60), min(len(text), i+len(q)+60)
	return strings.TrimSpace(text[start:end])
}

// references: GET /api/references?type=page&id=<path or id> — what points at
// a record.
func (a *App) references(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	ctx := c.R.Context()
	typ, id := strings.ToLower(c.Query("type")), c.Query("id")
	if typ == "" || id == "" {
		return apierr.BadRequest("Name the record: ?type=page|entry|asset|global&id=<id or path>.")
	}
	var refType, refID, label string
	switch typ {
	case "page":
		p, err := a.refPage(ctx, id)
		if err != nil {
			return err
		}
		refType, refID, label = "page", p.Path, "/"+p.Path
	case "entry", "collectionentry", "collection_entry":
		col, slug, ok := strings.Cut(id, "/")
		if !ok {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return apierr.BadRequest("An entry is named by collection/slug or its id.")
			}
			e, err := a.contentByID(ctx, "entry", n, false)
			if err != nil {
				return err
			}
			col, slug = e.CollectionSlug, e.Slug
		}
		refType, refID, label = col, slug, col+"/"+slug
	case "asset":
		refType, refID, label = "asset", id, "asset "+id
	case "url", "link":
		refType, refID, label = "url", id, id
	default:
		return apierr.BadRequest("type is one of page, entry, asset, url.")
	}
	used, err := a.usedBy(c, refType, refID)
	if err != nil {
		return err
	}
	page, per := c.paging(50, 200)
	total := len(used)
	from, to := min((page-1)*per, total), min(page*per, total)
	c.Summary = fmt.Sprintf("%d %s point at %s.", total, plural(total, "record"), label)
	return c.JSON(200, O("references", used[from:to], "page", page, "per", per, "total", total, "total_pages", (total+per-1)/per))
}

func (a *App) refPage(ctx context.Context, id string) (*Content, error) {
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		if p, err := a.contentByID(ctx, "page", n, false); err == nil {
			return p, nil
		}
	}
	return a.pageByPath(ctx, id, false)
}

func (a *App) managementSitemap(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	urls, err := a.sitemap(c.R.Context())
	if err != nil {
		return err
	}
	return c.JSON(200, O("urls", urls, "total", len(urls)))
}
