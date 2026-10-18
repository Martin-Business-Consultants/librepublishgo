package restore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/richtext"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// An import brings the content onto this core's terms, on the staged copy
// before it's swapped in (the admin's Tools › Clean up content and
// ui/scripts/richtext.mjs do the same to a running site):
//
//   - prose fields (text, markdown) become richtext, the editor's own
//     document, their values converted (HTML, Markdown or plain text);
//     ones holding markup on purpose (pasted scripts) become code, and
//     addresses, alt text and the site's description stay plain text;
//   - a repeater whose only field is prose (paragraphs) becomes one
//     richtext field;
//   - image_url (and any <x>_url beside an <x>_id image field) goes, as the
//     image is its image field, unless it holds the only copy of an image;
//   - alt text beside an image goes onto the image, where it lives;
//   - an entry's body (Markdown) holding HTML becomes the Markdown the
//     editor writes;
//   - SEO's old keys (seo_title, seo_description, no_index) become the
//     standard ones.

// ContentReport is what the clean-up changed.
type ContentReport struct {
	Fields    []FieldChange `json:"fields,omitempty"`
	Values    int           `json:"values"`
	Merged    int           `json:"merged"`
	URLCaches int           `json:"url_caches"`
	Bodies    int           `json:"bodies"`
	SEO       int           `json:"seo"`
	Alts      int           `json:"alts"`
	Records   int           `json:"records"`
}

// FieldChange is one field definition the clean-up changed.
type FieldChange struct {
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to"`
}

var (
	imageURL = regexp.MustCompile(`(^|_)image_url$`)
	urlField = regexp.MustCompile(`_url$`)
	altField = regexp.MustCompile(`(^|_)alt$`)
	codeHint = regexp.MustCompile(`(?i)<head>|</?body>|\bscripts?\b|embed code|pasted`)
	plainKey = regexp.MustCompile(`(^|_)address(_|$)|(^|_)alt$`)
	seoHint  = regexp.MustCompile(`(?i)\b(meta|seo)\b`)
)

// kindFor is what a prose field becomes: code for markup pasted on purpose,
// plain text for addresses, alt text and the site's own description, rich
// text for the rest. (cleanup.js suggest() is the same guess.)
func kindFor(path string, f map[string]any) string {
	words := str(f["label"]) + " " + str(f["help"])
	if codeHint.MatchString(words) {
		return "code"
	}
	if plainKey.MatchString(str(f["name"])) || path == "general.description" || seoHint.MatchString(words) {
		return "plain"
	}
	return "richtext"
}

// move is what becomes of one field's value.
type move struct {
	name      string
	drop      bool   // the field goes
	altFor    string // the field goes, its text onto this image field's asset
	mergeInto string // a repeater of prose becomes this richtext field
	sub       string // the repeater's prose field
	markdown  bool   // the old value is Markdown
	convert   bool   // the value becomes a document
	nested    []move
	repeater  bool
}

type cleaner struct {
	rep     *ContentReport
	orphans map[string]bool // owner.field paths whose image URL is the only copy
	alts    map[string]string
}

func (c *cleaner) plan(defs []any, at string) ([]any, []move) {
	out := make([]any, 0, len(defs))
	var moves []move
	names := map[string]bool{}
	for _, d := range defs {
		if f, ok := d.(map[string]any); ok {
			names[str(f["name"])] = true
		}
	}
	for _, d := range defs {
		f, ok := d.(map[string]any)
		if !ok {
			out = append(out, d)
			continue
		}
		name, typ := str(f["name"]), str(f["type"])
		path := at + "." + name
		of, ofKey := nestedDefs(f)
		if urlField.MatchString(name) && (typ == "string" || typ == "url") && !c.orphans[path] {
			if imageURL.MatchString(name) || hasAsset(defs, strings.TrimSuffix(name, "_url")+"_id") {
				moves = append(moves, move{name: name, drop: true})
				c.change(path, typ, "removed (the image is its image field)")
				continue
			}
		}
		if altField.MatchString(name) && (typ == "string" || typ == "text") {
			if img := imageFor(defs, name); img != "" {
				moves = append(moves, move{name: name, altFor: img})
				c.change(path, typ, "removed (alt text lives on the image)")
				continue
			}
		}
		if typ == "text" || typ == "markdown" {
			switch kindFor(path, f) {
			case "code":
				out = append(out, with(f, "type", "code"))
				c.change(path, typ, "code")
			case "richtext":
				out = append(out, with(f, "type", "richtext"))
				moves = append(moves, move{name: name, convert: true, markdown: typ == "markdown"})
				c.change(path, typ, "richtext")
			default:
				out = append(out, f)
			}
			continue
		}
		if typ == "repeater" && len(of) == 1 {
			if merged, m, ok := c.mergeRepeater(f, of[0], at, names); ok {
				out = append(out, merged)
				moves = append(moves, m)
				continue
			}
		}
		if typ == "richtext" {
			out = append(out, f)
			moves = append(moves, move{name: name, convert: true, markdown: true})
			continue
		}
		if (typ == "repeater" || typ == "group") && len(of) > 0 {
			inner, innerMoves := c.plan(of, path)
			out = append(out, with(f, ofKey, inner))
			if len(innerMoves) > 0 {
				moves = append(moves, move{name: name, nested: innerMoves, repeater: typ == "repeater"})
			}
			continue
		}
		out = append(out, f)
	}
	return out, moves
}

// mergeRepeater is a repeater whose only field is prose (paragraphs of
// text) as one richtext field, and the move that joins its items' values.
func (c *cleaner) mergeRepeater(f map[string]any, only any, at string, names map[string]bool) (map[string]any, move, bool) {
	sub, ok := only.(map[string]any)
	if !ok {
		return nil, move{}, false
	}
	name, path, subType := str(f["name"]), at+"."+str(f["name"]), str(sub["type"])
	if subType != "text" && subType != "markdown" || kindFor(path+"."+str(sub["name"]), sub) != "richtext" {
		return nil, move{}, false
	}
	into := "body"
	if names["body"] {
		into = name
	}
	merged := map[string]any{"name": into, "label": "Text", "type": "richtext"}
	for _, k := range []string{"required", "help"} {
		if v, ok := f[k]; ok {
			merged[k] = v
		}
	}
	c.rep.Merged++
	c.change(path+"[]."+str(sub["name"]), "repeater of "+subType, "one richtext field, "+at+"."+into)
	return merged, move{name: name, mergeInto: into, sub: str(sub["name"]), markdown: subType == "markdown"}, true
}

func (c *cleaner) change(path, from, to string) {
	c.rep.Fields = append(c.rep.Fields, FieldChange{Path: path, From: from, To: to})
}

// apply makes moves on data; whether anything changed.
func (c *cleaner) apply(data map[string]any, moves []move) bool {
	if data == nil {
		return false
	}
	changed := c.dropCaches(data, moves)
	for _, m := range moves {
		v, ok := data[m.name]
		if !ok {
			continue
		}
		switch {
		case m.drop:
			delete(data, m.name)
			c.rep.URLCaches++
			changed = true
		case m.altFor != "":
			if alt, id := strings.TrimSpace(str(v)), str(data[m.altFor]); alt != "" && id != "" {
				if _, seen := c.alts[id]; !seen {
					c.alts[id] = alt
				}
			}
			delete(data, m.name)
			changed = true
		case m.mergeInto != "":
			items, _ := v.([]any)
			var content []any
			for _, it := range items {
				item, _ := it.(map[string]any)
				if doc := toDoc(item[m.sub], m.markdown); doc != nil {
					inner, _ := doc["content"].([]any)
					content = append(content, inner...)
				}
			}
			delete(data, m.name)
			if len(content) > 0 {
				data[m.mergeInto] = richtext.Node{"type": "doc", "content": content}
			}
			c.rep.Values++
			changed = true
		case m.nested != nil:
			if m.repeater {
				items, _ := v.([]any)
				for _, it := range items {
					if item, ok := it.(map[string]any); ok && c.apply(item, m.nested) {
						changed = true
					}
				}
			} else if group, ok := v.(map[string]any); ok && c.apply(group, m.nested) {
				changed = true
			}
		case m.convert && v != nil && !richtext.IsDoc(v):
			if doc := toDoc(v, m.markdown); doc != nil {
				data[m.name] = doc
			} else {
				delete(data, m.name)
			}
			c.rep.Values++
			changed = true
		}
	}
	return changed
}

// dropCaches removes URL caches no schema declares any more, beside their
// image (or empty); whether there were any.
func (c *cleaner) dropCaches(data map[string]any, moves []move) bool {
	changed := false
	for k, v := range data {
		if !urlField.MatchString(k) || slices.ContainsFunc(moves, func(m move) bool { return m.name == k }) {
			continue
		}
		id := strings.TrimSuffix(k, "_url") + "_id"
		if _, hasID := data[id]; (imageURL.MatchString(k) || hasID) && (str(v) == "" || str(data[id]) != "") {
			delete(data, k)
			c.rep.URLCaches++
			changed = true
		}
	}
	return changed
}

func toDoc(v any, markdown bool) richtext.Node {
	if doc, ok := v.(map[string]any); ok && richtext.IsDoc(doc) {
		return doc
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return richtext.FromText(s, markdown)
}

func (c *cleaner) applyBlocks(blocks []any, plans map[string][]move) bool {
	changed := false
	for _, b := range blocks {
		blk, ok := b.(map[string]any)
		if !ok {
			continue
		}
		data, _ := blk["data"].(map[string]any)
		if c.apply(data, plans[str(blk["type"])]) {
			changed = true
		}
		for _, v := range data {
			if inner, ok := v.([]any); ok && looksLikeBlocks(inner) && c.applyBlocks(inner, plans) {
				changed = true
			}
		}
	}
	return changed
}

func looksLikeBlocks(l []any) bool {
	for _, it := range l {
		m, ok := it.(map[string]any)
		if !ok || m["type"] == nil || m["data"] == nil {
			return false
		}
	}
	return len(l) > 0
}

var seoAliases = [][2]string{{"seo_title", "meta_title"}, {"seo_description", "meta_description"}, {"description", "meta_description"}, {"no_index", "noindex"}}

func (c *cleaner) aliasSEO(seo map[string]any) bool {
	if seo == nil {
		return false
	}
	changed := false
	for _, a := range seoAliases {
		v, ok := seo[a[0]]
		if !ok {
			continue
		}
		if a[1] == "noindex" {
			v = v == true || v == float64(1) || v == "1" || v == "true"
		}
		if cur, has := seo[a[1]]; !has || cur == nil || cur == "" {
			seo[a[1]] = v
		}
		delete(seo, a[0])
		changed = true
	}
	if n, ok := seo["noindex"].(float64); ok {
		seo["noindex"] = n == 1
		changed = true
	}
	if changed {
		c.rep.SEO++
	}
	return changed
}

// cleanContent brings the staged database's content onto this core's
// terms; see the top of this file.
func cleanContent(ctx context.Context, db *store.DB, rep *Report) error {
	c := &cleaner{rep: &rep.Content, orphans: map[string]bool{}, alts: map[string]string{}}
	return db.Tx(ctx, func(tx *sql.Tx) error {
		if err := c.findOrphans(ctx, tx, rep); err != nil {
			return err
		}
		blockPlans := map[string][]move{}
		if err := eachRow(ctx, tx, `SELECT id, slug, fields FROM block_types`, func(id int64, slug string, cols []string) error {
			defs := list(cols[0])
			out, moves := c.plan(defs, slug)
			blockPlans[slug] = moves
			if len(moves) == 0 && !changedDefs(defs, out) {
				return nil
			}
			_, err := tx.ExecContext(ctx, `UPDATE block_types SET fields = ? WHERE id = ?`, js(out), id)
			return err
		}); err != nil {
			return fmt.Errorf("block types: %w", err)
		}
		collectionPlans := map[int64][]move{}
		if err := eachRow(ctx, tx, `SELECT id, slug, schema FROM collections`, func(id int64, slug string, cols []string) error {
			schema := object(cols[0])
			defs, _ := schema["fields"].([]any)
			out, moves := c.plan(defs, slug)
			collectionPlans[id] = moves
			if len(moves) == 0 && !changedDefs(defs, out) {
				return nil
			}
			schema["fields"] = out
			_, err := tx.ExecContext(ctx, `UPDATE collections SET schema = ? WHERE id = ?`, js(schema), id)
			return err
		}); err != nil {
			return fmt.Errorf("collections: %w", err)
		}
		if err := eachRow(ctx, tx, `SELECT id, slug, schema, data FROM globals`, func(id int64, slug string, cols []string) error {
			schema, data := object(cols[0]), object(cols[1])
			defs, _ := schema["fields"].([]any)
			out, moves := c.plan(defs, slug)
			changed := c.apply(data, moves)
			if !changed && !changedDefs(defs, out) {
				return nil
			}
			schema["fields"] = out
			c.rep.Records++
			_, err := tx.ExecContext(ctx, `UPDATE globals SET schema = ?, data = ? WHERE id = ?`, js(schema), js(data), id)
			return err
		}); err != nil {
			return fmt.Errorf("globals: %w", err)
		}
		if err := eachRow(ctx, tx, `SELECT id, path, blocks, seo FROM pages`, func(id int64, _ string, cols []string) error {
			blocks, seo := list(cols[0]), object(cols[1])
			b, s := c.applyBlocks(blocks, blockPlans), c.aliasSEO(seo)
			if !b && !s {
				return nil
			}
			c.rep.Records++
			_, err := tx.ExecContext(ctx, `UPDATE pages SET blocks = ?, seo = ? WHERE id = ?`, js(blocks), js(seo), id)
			return err
		}); err != nil {
			return fmt.Errorf("pages: %w", err)
		}
		if err := eachRow(ctx, tx, `SELECT id, CAST(collection_id AS TEXT), frontmatter, blocks, COALESCE(body_markdown, ''), seo FROM collection_entries`, func(id int64, collection string, cols []string) error {
			var cid int64
			fmt.Sscan(collection, &cid) //nolint:errcheck // a row's own collection id
			fm, blocks, body, seo := object(cols[0]), list(cols[1]), cols[2], object(cols[3])
			f, b, s := c.apply(fm, collectionPlans[cid]), c.applyBlocks(blocks, blockPlans), c.aliasSEO(seo)
			bodyChanged := false
			if richtext.HasHTML(body) {
				body = richtext.ToMarkdown(richtext.FromHTML(body))
				c.rep.Bodies++
				bodyChanged = true
			}
			if !f && !b && !s && !bodyChanged {
				return nil
			}
			c.rep.Records++
			_, err := tx.ExecContext(ctx, `UPDATE collection_entries SET frontmatter = ?, blocks = ?, body_markdown = ?, seo = ? WHERE id = ?`, js(fm), js(blocks), body, js(seo), id)
			return err
		}); err != nil {
			return fmt.Errorf("entries: %w", err)
		}
		// Alt text found beside an image goes on the image, where it has none.
		for id, alt := range c.alts {
			res, err := tx.ExecContext(ctx, `UPDATE assets SET alt = ? WHERE id = ? AND COALESCE(TRIM(alt), '') = ''`, alt, id)
			if err != nil {
				return fmt.Errorf("assets: %w", err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				c.rep.Alts++
			}
		}
		return nil
	})
}

// findOrphans finds image URLs that hold the only copy of an image (no image
// in its image field): those fields are kept, and said so.
func (c *cleaner) findOrphans(ctx context.Context, tx *sql.Tx, rep *Report) error {
	var walk func(v any, owner string)
	walk = func(v any, owner string) {
		switch x := v.(type) {
		case []any:
			for _, it := range x {
				walk(it, owner)
			}
		case map[string]any:
			if t := str(x["type"]); t != "" && x["data"] != nil {
				walk(x["data"], t) // a block: its fields are its type's
				return
			}
			for k, val := range x {
				if imageURL.MatchString(k) && strings.TrimSpace(str(val)) != "" && str(x[strings.TrimSuffix(k, "_url")+"_id"]) == "" {
					if !c.orphans[owner+"."+k] {
						rep.warn("%s.%s holds an image URL with no image beside it, so it's kept: pick the image in the media library, then remove it", owner, k)
					}
					c.orphans[owner+"."+k] = true
				}
				walk(val, owner)
			}
		}
	}
	if err := eachRow(ctx, tx, `SELECT id, path, blocks FROM pages`, func(_ int64, _ string, cols []string) error {
		walk(list(cols[0]), "")
		return nil
	}); err != nil {
		return err
	}
	return eachRow(ctx, tx, `SELECT e.id, c.slug, e.frontmatter, e.blocks FROM collection_entries e JOIN collections c ON c.id = e.collection_id`, func(_ int64, slug string, cols []string) error {
		walk(object(cols[0]), slug)
		walk(list(cols[1]), "")
		return nil
	})
}

// eachRow runs fn on each row of q: its id, a name, and the rest as text.
func eachRow(ctx context.Context, tx *sql.Tx, q string, fn func(id int64, name string, cols []string) error) error {
	all, err := readRows(ctx, tx, q)
	if err != nil {
		return err
	}
	for _, r := range all {
		if err := fn(r.id, r.name, r.cols); err != nil {
			return err
		}
	}
	return nil
}

type row struct {
	id   int64
	name string
	cols []string
}

// readRows is q's rows, read through before any is worked on (the work
// writes to the same tables).
func readRows(ctx context.Context, tx *sql.Tx, q string) ([]row, error) {
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	n, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var all []row
	for rows.Next() {
		var r row
		vals := make([]sql.NullString, len(n)-2)
		dest := []any{&r.id, &r.name}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		for _, v := range vals {
			r.cols = append(r.cols, v.String)
		}
		all = append(all, r)
	}
	return all, rows.Err()
}

func nestedDefs(f map[string]any) ([]any, string) {
	if of, ok := f["of"].([]any); ok {
		return of, "of"
	}
	if of, ok := f["fields"].([]any); ok {
		return of, "fields"
	}
	return nil, "of"
}

func hasAsset(defs []any, name string) bool {
	for _, d := range defs {
		if f, ok := d.(map[string]any); ok && str(f["name"]) == name && str(f["type"]) == "asset" {
			return true
		}
	}
	return false
}

// imageFor is the image field an alt field describes: <x>_id beside <x>_alt,
// or the one image field there is.
func imageFor(defs []any, alt string) string {
	if want := strings.TrimSuffix(alt, "alt") + "id"; hasAsset(defs, want) {
		return want
	}
	var only []string
	for _, d := range defs {
		if f, ok := d.(map[string]any); ok && str(f["type"]) == "asset" {
			only = append(only, str(f["name"]))
		}
	}
	if len(only) == 1 {
		return only[0]
	}
	return ""
}

func with(f map[string]any, k string, v any) map[string]any {
	out := maps.Clone(f)
	out[k] = v
	return out
}

func changedDefs(a, b []any) bool { return js(a) != js(b) }

func list(s string) []any {
	var l []any
	_ = json.Unmarshal([]byte(s), &l)
	return l
}

func object(s string) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(s), &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
