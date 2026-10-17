package fields

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// decode is JSON text as encoding/json gives it to the validators.
func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIsBlockSlug(t *testing.T) {
	t.Parallel()
	for s, want := range map[string]bool{"hero": true, "rich_text2": true, "": false, "2col": false, "Hero": false, "a-b": false} {
		if got := IsBlockSlug(s); got != want {
			t.Errorf("IsBlockSlug(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  any
		want []Field
	}{
		{"nil", nil, []Field{}},
		{"bad JSON", store.JSON(`[{`), []Field{}},
		{"not definitions", "fields", []Field{}},
		{"a list", store.JSON(`[{"name":"title","type":"string","required":true,"label":"Title","help":"h"}]`),
			[]Field{{Name: "title", Label: "Title", Type: "string", Required: true, Help: "h", Of: []Field{}}}},
		{"under fields", store.JSON(`{"fields":[{"name":"n","type":"integer"}]}`),
			[]Field{{Name: "n", Type: "integer", Of: []Field{}}}},
		{"non-objects skipped", store.JSON(`[1, "x", {"name":"a","type":"text"}]`),
			[]Field{{Name: "a", Type: "text", Of: []Field{}}}},
		{"options as values or objects", store.JSON(`[{"name":"s","type":"select","options":["a",{"value":"b","label":"B"},3]}]`),
			[]Field{{Name: "s", Type: "select", Options: []string{"a", "b", "3"}, Of: []Field{}}}},
		{"nested", store.JSON(`[{"name":"r","type":"repeater","of":[{"name":"x","type":"string"}],"allowed_types":["hero"],"of_collection":"tags"}]`),
			[]Field{{Name: "r", Type: "repeater", OfCollection: "tags", AllowedTypes: []string{"hero"}, Of: []Field{{Name: "x", Type: "string", Of: []Field{}}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Parse(tt.raw); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse = %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestValidateDefinitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		defs        string
		allowBlocks bool
		want        map[string][]string
	}{
		{"null is no fields", `null`, false, map[string][]string{}},
		{"valid", `[{"name":"title","type":"string"},{"name":"tags","type":"string_list"}]`, false, map[string][]string{}},
		{"not a list", `{"name":"x"}`, false, map[string][]string{"fields": {"must be a list of field definitions"}}},
		{"not an object", `["x"]`, false, map[string][]string{"fields[0]": {"must be an object"}}},
		{"bad name and type", `[{"name":"Title","type":"rich"}]`, false, map[string][]string{
			"fields[0].name": {"must start with a letter and use only a-z, 0-9 and _"},
			"fields[0].type": {"isn't a field type here: rich"},
		}},
		{"name used twice", `[{"name":"a","type":"string"},{"name":"a","type":"text"}]`, false, map[string][]string{"fields[1].name": {"is used twice: a"}}},
		{"blocks where not allowed", `[{"name":"body","type":"blocks"}]`, false, map[string][]string{"fields[0].type": {"isn't a field type here: blocks"}}},
		{"blocks where allowed", `[{"name":"body","type":"blocks"}]`, true, map[string][]string{}},
		{"select without options", `[{"name":"s","type":"select"}]`, false, map[string][]string{"fields[0].options": {"a select needs options"}}},
		{"nested problems", `[{"name":"g","type":"group","of":[{"name":"","type":"string"}]}]`, false, map[string][]string{
			"fields[0].of[0].name": {"must start with a letter and use only a-z, 0-9 and _"},
		}},
		{"too deep", `[{"name":"a","type":"group","of":[{"name":"b","type":"group","of":[{"name":"c","type":"group","of":[{"name":"d","type":"group","of":[{"name":"e","type":"string"}]}]}]}]}]`, false,
			map[string][]string{"fields[0].of[0].of[0].of[0].of": {"nests deeper than 4 levels"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			into := apierr.Invalid{}
			ValidateDefinitions(decode(t, tt.defs), tt.allowBlocks, into, "fields")
			if !reflect.DeepEqual(map[string][]string(into), tt.want) {
				t.Errorf("problems = %v, want %v", into, tt.want)
			}
		})
	}
}

func TestSafeURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url  string
		want bool
	}{
		{"https://example.com/a?b=c", true},
		{"HTTP://example.com", true},
		{"mailto:a@example.com", true},
		{"tel:+15551234", true},
		{"/about", true},
		{"about#team", true},
		{"", true},
		{"javascript:alert(1)", false},
		{"JaVaScRiPt:alert(1)", false},
		{" javascript:alert(1)", false},
		{"java\tscript:alert(1)", false},
		{"java\nscript:alert(1)", false},
		{"\x00javascript:alert(1)", false},
		{"data:text/html,<script>", false},
		{"vbscript:msgbox", false},
		{"ftp://example.com", false},
	}
	for _, tt := range tests {
		if got := SafeURL(tt.url); got != tt.want {
			t.Errorf("SafeURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

// FuzzSafeURL: whatever the input, a URL SafeURL passes never reaches a
// browser as a script or data scheme once the browser drops what it
// ignores (control characters and spaces).
func FuzzSafeURL(f *testing.F) {
	for _, s := range []string{"https://x.test", "/a", "javascript:x", " Java\tScript:x", "data:,x", "mailto:a@b", "a:b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, u string) {
		if !SafeURL(u) {
			return
		}
		seen := strings.ToLower(strings.Map(func(r rune) rune {
			if r <= ' ' || r == 0x7f {
				return -1
			}
			return r
		}, u))
		for _, bad := range []string{"javascript:", "vbscript:", "data:", "file:"} {
			if strings.HasPrefix(seen, bad) {
				t.Fatalf("SafeURL(%q) passed a %s URL", u, bad)
			}
		}
	})
}

func TestValidateData(t *testing.T) {
	t.Parallel()
	defs := Parse(decode(t, `[
		{"name":"title","type":"string","required":true},
		{"name":"count","type":"integer"},
		{"name":"on","type":"boolean"},
		{"name":"size","type":"select","options":["s","m"]},
		{"name":"site","type":"url"},
		{"name":"at","type":"datetime"},
		{"name":"cta","type":"link"},
		{"name":"refs","type":"record_refs"},
		{"name":"words","type":"string_list"},
		{"name":"meta","type":"group","of":[{"name":"note","type":"string","required":true}]},
		{"name":"rows","type":"repeater","of":[{"name":"n","type":"integer"}]},
		{"name":"body","type":"blocks","allowed_types":["hero"]},
		{"name":"other","type":"something_new"}
	]`))
	types := func(slug string) ([]Field, bool) {
		if slug == "hero" {
			return []Field{{Name: "heading", Type: "string", Required: true}}, true
		}
		return nil, false
	}
	tests := []struct {
		name string
		data string
		want map[string][]string
	}{
		{"valid", `{"title":"Hi","count":3,"on":false,"size":"m","site":"https://x.test","at":"2026-01-02T03:04:05Z",
			"cta":{"kind":"entry","value":"a","collection":"news"},"refs":["a","b"],"words":["", "x"],
			"meta":{"note":"n"},"rows":[{"n":1},{}],"body":[{"type":"hero","data":{"heading":"H"}}],"other":{"any":"thing"}}`,
			map[string][]string{}},
		{"required", `{"title":""}`, map[string][]string{"title": {"is required"}}},
		{"empty optional values are skipped", `{"title":"x","count":null,"words":[],"site":""}`, map[string][]string{}},
		{"scalars of the wrong type", `{"title":1,"count":1.5,"on":"yes","size":"xl","site":"javascript:alert(1)","at":"yesterday"}`, map[string][]string{
			"title": {"must be a string"},
			"count": {"must be an integer"},
			"on":    {"must be true or false"},
			"size":  {"must be one of s, m"},
			"site":  {"must be a web address (http or https), mailto:, tel:, or a path"},
			"at":    {"must be an ISO 8601 datetime"},
		}},
		{"blank url", `{"title":"x","site":"  "}`, map[string][]string{"site": {"must be a URL"}}},
		{"links", `{"title":"x","cta":{"kind":"url","value":"javascript:x"}}`, map[string][]string{"cta": {"must be a web address (http or https), mailto:, tel:, or a path"}}},
		{"link to an entry without a collection", `{"title":"x","cta":{"kind":"entry","value":"a"}}`, map[string][]string{"cta": {"kind=entry requires a collection"}}},
		{"link of an unknown kind", `{"title":"x","cta":{"kind":"file","value":"a"}}`, map[string][]string{"cta": {"must be { kind: url|page|entry, value: string }"}}},
		{"lists", `{"title":"x","refs":["a",""],"words":["a",2]}`, map[string][]string{
			"refs":  {"must be an array of record slugs"},
			"words": {"must be an array of strings"},
		}},
		{"lists that aren't", `{"title":"x","refs":"a","words":"a"}`, map[string][]string{
			"refs":  {"must be an array of record slugs"},
			"words": {"must be an array of strings"},
		}},
		{"nested", `{"title":"x","meta":{},"rows":[{"n":"one"},3]}`, map[string][]string{
			"meta.note": {"is required"},
			"rows[0].n": {"must be an integer"},
			"rows[1]":   {"must be an object"},
		}},
		{"nested of the wrong shape", `{"title":"x","meta":"m","rows":{}}`, map[string][]string{
			"meta": {"must be an object"},
			"rows": {"must be an array"},
		}},
		{"blocks", `{"title":"x","body":[1,{"data":{}},{"type":"quote"},{"type":"hero","data":[]},{"type":"hero","data":{}},{"type":"hero"}]}`, map[string][]string{
			"body[0]":              {"must be a block object"},
			"body[1].type":         {"is required"},
			"body[2].type":         {"'quote' isn't allowed here"},
			"body[3].data":         {"must be an object"},
			"body[4].data.heading": {"is required"},
			"body[5].data.heading": {"is required"},
		}},
		{"blocks that aren't", `{"title":"x","body":{}}`, map[string][]string{"body": {"must be an array"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, _ := decode(t, tt.data).(map[string]any)
			into := apierr.Invalid{}
			ValidateData(defs, data, types, into, "")
			if !reflect.DeepEqual(map[string][]string(into), tt.want) {
				t.Errorf("problems = %v\nwant %v", into, tt.want)
			}
		})
	}
}

func TestValidateBlocksUnknownType(t *testing.T) {
	t.Parallel()
	into := apierr.Invalid{}
	ValidateBlocks([]any{map[string]any{"type": "nope"}}, nil, func(string) ([]Field, bool) { return nil, false }, into, "blocks")
	if got := into["blocks[0].type"]; !slices.Equal(got, []string{"unknown block type 'nope'"}) {
		t.Errorf("problems = %v", into)
	}
}

func TestJSONSchema(t *testing.T) {
	t.Parallel()
	defs := Parse(decode(t, `[
		{"name":"title","type":"string","required":true,"label":"Title","help":"Shown big"},
		{"name":"size","type":"select","options":["s"]},
		{"name":"bio","type":"markdown"},
		{"name":"rows","type":"repeater","of":[{"name":"n","type":"integer","required":true}]}
	]`))
	got := JSONSchema(defs)
	want := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"title"},
		"properties": map[string]any{
			"title": map[string]any{"x-cms-type": "string", "type": "string", "title": "Title", "description": "Shown big"},
			"size":  map[string]any{"x-cms-type": "select", "type": "string", "enum": []string{"s"}},
			"bio":   map[string]any{"x-cms-type": "markdown", "type": "string", "contentMediaType": "text/markdown"},
			"rows": map[string]any{"x-cms-type": "repeater", "type": "array", "items": map[string]any{
				"type": "object", "required": []string{"n"}, "additionalProperties": false,
				"properties": map[string]any{"n": map[string]any{"x-cms-type": "integer", "type": "integer"}},
			}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("JSONSchema =\n%#v\nwant\n%#v", got, want)
	}
}

func TestRefs(t *testing.T) {
	t.Parallel()
	defs := Parse(decode(t, `[
		{"name":"img","type":"asset"},
		{"name":"author","type":"record_ref","of_collection":"people"},
		{"name":"related","type":"record_refs"},
		{"name":"site","type":"url"},
		{"name":"a","type":"link"},{"name":"b","type":"link"},{"name":"c","type":"link"},{"name":"d","type":"link"},
		{"name":"g","type":"group","of":[{"name":"img","type":"asset"}]},
		{"name":"r","type":"repeater","of":[{"name":"img","type":"asset"}]},
		{"name":"body","type":"blocks"}
	]`))
	types := func(slug string) ([]Field, bool) {
		return []Field{{Name: "photo", Type: "asset"}}, slug == "hero"
	}
	data, _ := decode(t, `{
		"img":"1","author":"ada","related":["x","",3],"site":"https://x.test",
		"a":{"kind":"url","value":"https://y.test"},"b":{"kind":"page","value":"about"},
		"c":{"kind":"entry","value":"e","collection":"news"},"d":{"kind":"page","value":""},
		"g":{"img":"2"},"r":[{"img":"3"},"x"],
		"body":[{"type":"hero","data":{"photo":"4"}},{"type":"other","data":{"photo":"5"}},"x"]
	}`).(map[string]any)
	var got []Ref
	Refs(defs, data, types, &got)
	want := []Ref{
		{"asset", "asset", "1"}, {"record", "people", "ada"}, {"record", "record", "x"}, {"link", "url", "https://x.test"},
		{"link", "url", "https://y.test"}, {"page", "page", "about"}, {"record", "news", "e"},
		{"asset", "asset", "2"}, {"asset", "asset", "3"}, {"asset", "asset", "4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Refs =\n%v\nwant\n%v", got, want)
	}
}

func TestDeclared(t *testing.T) {
	t.Parallel()
	defs := Parse([]any{
		map[string]any{"name": "title", "type": "string"},
		map[string]any{"name": "author", "type": "group", "of": []any{map[string]any{"name": "name", "type": "string"}}},
		map[string]any{"name": "rows", "type": "repeater", "of": []any{map[string]any{"name": "n", "type": "integer"}}},
		map[string]any{"name": "body", "type": "blocks"},
	})
	types := func(slug string) ([]Field, bool) {
		if slug == "quote" {
			return Parse([]any{map[string]any{"name": "text", "type": "text"}}), true
		}
		return nil, false
	}
	data := map[string]any{
		"title": "Hi", "removed_field": "old", "author": map[string]any{"name": "Ada", "twitter": "@ada"},
		"rows": []any{map[string]any{"n": 1.0, "legacy": true}},
		"body": []any{
			map[string]any{"id": "b1", "type": "quote", "data": map[string]any{"text": "Yes", "cite": "old"}},
			map[string]any{"id": "b2", "type": "gone", "data": map[string]any{"x": 1.0}},
		},
	}
	got, _ := json.Marshal(Declared(defs, data, types))
	want := `{"author":{"name":"Ada"},"body":[{"data":{"text":"Yes"},"id":"b1","type":"quote"},{"data":{"x":1},"id":"b2","type":"gone"}],"rows":[{"n":1}],"title":"Hi"}`
	if string(got) != want {
		t.Fatalf("declared:\n got %s\nwant %s", got, want)
	}
	// The data itself is untouched.
	if data["removed_field"] != "old" || data["body"].([]any)[0].(map[string]any)["data"].(map[string]any)["cite"] != "old" {
		t.Fatalf("data was changed: %v", data)
	}
}
