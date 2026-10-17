package cms

import (
	"context"
	"maps"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/fields"
)

// What the delivery API sends is what /api/v1/schema declares: frontmatter,
// block data, globals' data and SEO keep only their declared keys, however
// old the data (a Rails import, a field since removed). Patterns are
// expanded everywhere, so a site never meets a "pattern" block.

// deliveredTypes resolves block types to their fields, cached with the
// schema (structureVersion).
func (a *App) deliveredTypes(ctx context.Context) fields.BlockTypes {
	version, err := a.structureVersion(ctx)
	if err == nil {
		a.schemas.mu.Lock()
		types := a.schemas.types
		if a.schemas.typesVersion != version {
			types = nil
		}
		a.schemas.mu.Unlock()
		if types != nil {
			return types
		}
	}
	types, rerr := a.blockTypeResolver(ctx)
	if rerr != nil {
		// Unknown types are left as they are: data isn't lost, only unchecked.
		a.log.Error("block types", "err", rerr)
		return func(string) ([]fields.Field, bool) { return nil, false }
	}
	if err == nil {
		a.schemas.mu.Lock()
		a.schemas.types, a.schemas.typesVersion = types, version
		a.schemas.mu.Unlock()
	}
	return types
}

// deliveredBlocks is blocks as a site renders them: patterns expanded, what
// they pull in resolved, each one's data its type's fields.
func (a *App) deliveredBlocks(ctx context.Context, blocks []any) []any {
	return fields.DeclaredBlocks(a.expandBlocks(ctx, blocks, true), a.deliveredTypes(ctx))
}

// deliveredData is data with only the keys defs declare.
func (a *App) deliveredData(ctx context.Context, defs []fields.Field, data map[string]any) map[string]any {
	return fields.Declared(defs, data, a.deliveredTypes(ctx))
}

// deliveredSeo is a record's SEO with only the keys seoSchema declares; the
// Rails core's "canonical" is read as canonical_url when that's empty.
func deliveredSeo(seo map[string]any) map[string]any {
	in := maps.Clone(seo)
	if in == nil {
		in = map[string]any{}
	}
	if str(in["canonical_url"]) == "" && str(in["canonical"]) != "" {
		in["canonical_url"] = in["canonical"]
	}
	props, _ := seoSchema()["properties"].(map[string]any)
	out := map[string]any{}
	for k, v := range in {
		if _, ok := props[k]; ok {
			out[k] = v
		}
	}
	return out
}
