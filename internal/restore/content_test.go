package restore

import (
	"encoding/json"
	"strings"
	"testing"
)

// The clean-up's plan for an Old Mill-shaped block type, and what it makes
// of a block's data: paragraphs of HTML into one rich text body, the image
// URL cache and the alt field gone (the alt onto the image), an address
// kept plain, a pasted script a code field.
func TestCleanPlan(t *testing.T) {
	t.Parallel()
	var defs []any
	if err := json.Unmarshal([]byte(`[
		{"name":"image_id","type":"asset"},
		{"name":"image_url","type":"string"},
		{"name":"image_alt","type":"string"},
		{"name":"address","type":"text"},
		{"name":"head","type":"text","help":"Pasted into <head>."},
		{"name":"paragraphs","type":"repeater","of":[{"name":"html","type":"text"}]}
	]`), &defs); err != nil {
		t.Fatal(err)
	}
	c := &cleaner{rep: &ContentReport{}, orphans: map[string]bool{}, alts: map[string]string{}}
	out, moves := c.plan(defs, "about_intro")
	if got := js(out); got != `[{"name":"image_id","type":"asset"},{"name":"address","type":"text"},{"help":"Pasted into \u003chead\u003e.","name":"head","type":"code"},{"label":"Text","name":"body","type":"richtext"}]` {
		t.Fatalf("fields: %s", got)
	}
	data := map[string]any{
		"image_id": "68", "image_url": "https://old.example/rails/active_storage/x.webp", "image_alt": "The mill",
		"address": "1 Mill St\nPlainwell", "head": "<script>x()</script>",
		"paragraphs": []any{map[string]any{"html": `Built in <strong class="text-cream">1869</strong>.`}, map[string]any{"html": "Now a brewpub."}},
	}
	if !c.apply(data, moves) {
		t.Fatal("nothing changed")
	}
	got := js(data)
	for _, want := range []string{
		`"body":{"content":[{"content":[{"text":"Built in ","type":"text"},{"marks":[{"type":"bold"}],"text":"1869","type":"text"},{"text":".","type":"text"}],"type":"paragraph"},{"content":[{"text":"Now a brewpub.","type":"text"}],"type":"paragraph"}],"type":"doc"}`,
		`"address":"1 Mill St\nPlainwell"`, `"head":"\u003cscript\u003ex()\u003c/script\u003e"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("data %s\nlacks %s", got, want)
		}
	}
	for _, gone := range []string{"image_url", "image_alt", "paragraphs"} {
		if _, ok := data[gone]; ok {
			t.Fatalf("%s is still there: %s", gone, got)
		}
	}
	if c.alts["68"] != "The mill" {
		t.Fatalf("alt: %v", c.alts)
	}
	// Again: nothing left to do.
	out2, moves2 := c.plan(out, "about_intro")
	if js(out2) != js(out) || c.apply(data, moves2) {
		t.Fatal("a second clean-up changed something")
	}
}
