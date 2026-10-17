package cms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/fields"
)

// The schema's digest is what a site pins the types it generated to:
// "sha256:" and the hex SHA-256 of /api/v1/schema's data in canonical JSON.
// Every /api/v1 answer carries it (X-CMS-Schema-Digest), /schema has it in
// meta, and schema.changed tells webhooks, plugins and the deploy when it
// moves.

const (
	schemaDigestHeader    = "X-CMS-Schema-Digest"
	schemaChangedEvent    = "schema.changed"
	defaultSchemaDebounce = 2 * time.Second
)

// canonicalJSON is v as the digests hash it: compact JSON with every
// object's keys sorted (bytewise), strings escaped as encoding/json does by
// default (<, >, & and U+2028/U+2029 as \uXXXX), numbers as first written.
// A site reproduces it to check a digest.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

// digestOf is "sha256:<hex>" of canonical bytes.
func digestOf(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// schemaCache keeps the schema and its digest while what they're built
// from stays the same (structureVersion), and what webhooks last heard.
type schemaCache struct {
	mu        sync.Mutex
	version   string
	data      json.RawMessage // canonical
	digest    string
	announced string
	// Block types' fields, for delivering data (delivery_declared.go).
	types        fields.BlockTypes
	typesVersion string
	timer        *time.Timer
	stopped      bool
}

// structureVersion changes whenever anything the schema is built from
// does: block types, collections, globals, live pages (their fields), and
// Settings › General (which collections are served).
func (a *App) structureVersion(ctx context.Context) (string, error) {
	var v string
	err := a.store.QueryRowContext(ctx, `SELECT
		(SELECT COALESCE(max(updated_at), '') || ':' || count(*) FROM block_types) || '/' ||
		(SELECT COALESCE(max(updated_at), '') || ':' || count(*) FROM collections) || '/' ||
		(SELECT COALESCE(max(updated_at), '') || ':' || COALESCE(max(deleted_at), '') || ':' || COALESCE(sum(deleted_at IS NULL), 0) FROM globals) || '/' ||
		(SELECT COALESCE(max(updated_at), '') || ':' || count(*) FROM pages WHERE status = 'published' AND deleted_at IS NULL) || '/' ||
		COALESCE((SELECT updated_at FROM settings WHERE key = 'general'), '')`).Scan(&v)
	return v, err
}

// schema is /api/v1/schema's data, canonical, and its digest: cached
// until the structure changes.
func (a *App) schema(ctx context.Context) (json.RawMessage, string, error) {
	version, err := a.structureVersion(ctx)
	if err != nil {
		return nil, "", err
	}
	a.schemas.mu.Lock()
	if a.schemas.version == version && a.schemas.data != nil {
		data, digest := a.schemas.data, a.schemas.digest
		a.schemas.mu.Unlock()
		return data, digest, nil
	}
	a.schemas.mu.Unlock()
	built, err := a.buildSchema(ctx)
	if err != nil {
		return nil, "", err
	}
	data, err := canonicalJSON(built)
	if err != nil {
		return nil, "", err
	}
	digest := digestOf(data)
	a.schemas.mu.Lock()
	a.schemas.version, a.schemas.data, a.schemas.digest = version, data, digest
	a.schemas.mu.Unlock()
	return data, digest, nil
}

// schemaDigest is the current digest; "" (logged) when it can't be read.
func (a *App) schemaDigest(ctx context.Context) string {
	_, digest, err := a.schema(ctx)
	if err != nil {
		a.log.Error("schema digest", "err", err)
		return ""
	}
	return digest
}

// buildSchema is the JSON Schema of everything a site renders: each block
// type's data, each served collection's fields, each global, each live
// page with fields of its own, and SEO.
func (a *App) buildSchema(ctx context.Context) (Obj, error) {
	bts, err := a.blockTypesList(ctx, "1=1 ORDER BY slug")
	if err != nil {
		return nil, err
	}
	blocks := map[string]any{}
	for _, b := range bts {
		if b.Slug == "pattern" {
			continue // expanded before delivery: a site never gets one
		}
		s := b.Schema()
		s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
		blocks[b.Slug] = s
	}
	cols, err := a.servedCollections(ctx)
	if err != nil {
		return nil, err
	}
	collections := map[string]any{}
	for _, col := range cols {
		s := fields.JSONSchema(col.Fields())
		s["title"] = col.Name
		collections[col.Slug] = s
	}
	globals := map[string]any{}
	gs, err := a.globals(ctx, "1=1")
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		s := fields.JSONSchema(g.Fields())
		s["title"] = g.Name
		globals[g.Slug] = s
	}
	pages := map[string]any{}
	ps, err := a.pages(ctx, "status = 'published'")
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if fs := fields.Parse(p.Schema); len(fs) > 0 {
			pages[p.Path] = fields.JSONSchema(fs)
		}
	}
	return O("$schema", "https://json-schema.org/draft/2020-12/schema", "blocks", blocks, "collections", collections,
		"globals", globals, "pages", pages, "seo", seoSchema()), nil
}

// schemaTouched notes that the structure may have changed: once writes
// settle (Config.SchemaDebounce), the digest is read again, and when it
// moved, schema.changed goes out.
func (a *App) schemaTouched() {
	delay := a.cfg.SchemaDebounce
	if delay <= 0 {
		delay = defaultSchemaDebounce
	}
	a.schemas.mu.Lock()
	defer a.schemas.mu.Unlock()
	if a.schemas.stopped {
		return
	}
	if a.schemas.timer != nil {
		a.schemas.timer.Stop()
	}
	a.schemas.timer = time.AfterFunc(delay, func() {
		a.spawn(a.ctx, "schema digest", a.announceSchema)
	})
}

// announceSchema publishes schema.changed when the digest isn't the one
// last announced. The first reading is only noted: there's nothing it
// changed from.
func (a *App) announceSchema(ctx context.Context) {
	digest := a.schemaDigest(ctx)
	if digest == "" {
		return
	}
	a.schemas.mu.Lock()
	prev := a.schemas.announced
	a.schemas.announced = digest
	a.schemas.mu.Unlock()
	if prev == "" || prev == digest {
		return
	}
	a.publish(ctx, change{Event: schemaChangedEvent, Data: O("schema_digest", digest), Tags: []string{"schema"}})
}

// stopSchemaTimer cancels an announcement waiting out its debounce.
func (a *App) stopSchemaTimer() {
	a.schemas.mu.Lock()
	defer a.schemas.mu.Unlock()
	a.schemas.stopped = true
	if a.schemas.timer != nil {
		a.schemas.timer.Stop()
	}
}
