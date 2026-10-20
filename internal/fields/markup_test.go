package fields

import (
	"html"
	"reflect"
	"strings"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
)

// notTags is text with < in it that isn't HTML, in any text field.
var notTags = []string{
	"", "plain", "a < b", "a<b", "<3", "I <3 it", "5 <10", "x <= y", "a <- b", "<<>>", "<>", "< p>", "<1>", "<-->",
	"List<T>", "Map<String, Integer>", "Dear <name>,", "<first-name>", "<my-widget>",
	"<https://example.com/a?b=c>", "<mailto:you@example.com>", "<you@example.com>", "<tel:+15550100>",
	"&lt;strong&gt;bold&lt;/strong&gt;", "<p unclosed", "1 <p 2", "<strong", "</", "</ p>", "<!", "<! x>", "<?", "<? x",
	"\\(^o^)/ <(^_^)>",
	"Map<String, Online>", "<name only>", "<x onclick>", "a</b", "<3 <3", "x<y && y>z",
}

// tags is HTML, in any text field.
var tags = []string{
	"<p>x</p>", "Built in <strong class=\"text-cream\">1869</strong>.", "<br>", "line<br/>break", "<br />", "<BR>", "</div>",
	"<a href=\"/x\">x</a>", "<img src=x onerror=alert(1)>", "<script>alert(1)</script>", "<em\n>x</em>", "<h2>", "<span\tclass=a>",
	"<svg onload=alert(1)>", "<!-- note -->", "<!-- unclosed", "<!DOCTYPE html>", "<![CDATA[x]]>", "<?php echo 1 ?>",
	"<b and c>", "<a title=\"x>y\">", "<IFRAME SRC=x>", "<math>", "<foreignObject>",
	// A / reads as a space between a tag's name and its attributes, and an
	// element nobody defined still runs its handlers.
	"<img/src=x onerror=alert(1)>", "<svg/onload=alert(1)>", "<IMG/SRC=x/ONERROR=alert(1)>", "<xss onmouseover=alert(1)>",
	"<my-widget onclick=\"go()\">", "x <details/open/ontoggle=alert(1)>", "</p/>",
}

func TestHasHTML(t *testing.T) {
	t.Parallel()
	for _, s := range notTags {
		if HasHTML(s) {
			t.Errorf("HasHTML(%q) = true, want false", s)
		}
	}
	for _, s := range tags {
		if !HasHTML(s) {
			t.Errorf("HasHTML(%q) = false, want true", s)
		}
	}
	// Markdown's code and escapes are plain text's characters.
	for _, s := range []string{"`<b>`", "\\<b>", "```\n<p>\n```"} {
		if !HasHTML(s) {
			t.Errorf("HasHTML(%q) = false, want true", s)
		}
	}
}

func TestMarkdownHasHTML(t *testing.T) {
	t.Parallel()
	for _, s := range notTags {
		if MarkdownHasHTML(s) {
			t.Errorf("MarkdownHasHTML(%q) = true, want false", s)
		}
	}
	for _, s := range tags {
		if !MarkdownHasHTML(s) {
			t.Errorf("MarkdownHasHTML(%q) = false, want true", s)
		}
	}
	tests := []struct {
		name string
		md   string
		want bool
	}{
		{"code span", "Use `<strong>` for bold.", false},
		{"double-backtick code span", "``a ` <p> b``", false},
		{"unmatched backticks are literal", "`` <p> `", true},
		{"tag after a code span", "`x` <p>", true},
		{"escaped <", "Write \\<p> for a paragraph.", false},
		{"escaped backslash, then a tag", "a \\\\<p>", true},
		{"fenced code", "Before\n\n```html\n<p>hi</p>\n<script>x</script>\n```\n\nAfter", false},
		{"tilde fence", "~~~\n<div>\n~~~", false},
		{"fence in a list item", "- a\n\n  ```\n  <div>\n  ```", false},
		{"fence in a quote", "> ```\n> <div>\n> ```", false},
		{"tag after a fence closes", "```\nx\n```\n<p>", true},
		{"a longer fence holds a shorter one", "````\n```\n<p>\n```\n````", false},
		{"unclosed fence runs to the end", "```\n<p>", false},
		{"backticks with a backtick after aren't a fence", "```a` <p>", true},
		{"hard breaks", "one  \ntwo\\\nthree", false},
		{"<br> is HTML", "one<br>two", true},
		{"entities", "&nbsp;\n\n&lt;b&gt; &amp; 1 &lt; 2", false},
		{"autolinks", "See <https://example.com> or <you@example.com>.", false},
		{"link with a < in its address", "[x](https://x.test/a?b=1&c=<2>)", false},
		{"tag in link text", "[<b>x</b>](https://x.test)", true},
		{"comment", "x <!-- y --> z", true},
		{"what the editor emits", "## Head &lt;b&gt;\n\nBuilt in **1869**  \nnext &lt;you@example.com&gt; `<div>code</div>` [link](https://x.test/a)\n\n&nbsp;\n\n" +
			"- a\n  - nested\n\n1. one\n\n- [x] done\n\n> q  \n> *~~q2~~*\n\n```html\n<p>hi</p>\n```\n\n---\n\nend  \n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := MarkdownHasHTML(tt.md); got != tt.want {
				t.Errorf("MarkdownHasHTML(%q) = %v, want %v", tt.md, got, tt.want)
			}
		})
	}
}

// FuzzMarkdownHasHTML: whatever the text, the editor's encoding of it (< as
// &lt;) is never refused, nor is text without a <; and a tag appended
// outside any code always is.
func FuzzMarkdownHasHTML(f *testing.F) {
	for _, s := range append(append([]string{"```", "`", "\\", "~~~\n"}, notTags...), tags...) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if MarkdownHasHTML(html.EscapeString(s)) || HasHTML(html.EscapeString(s)) {
			t.Fatalf("the escaped text of %q was refused", s)
		}
		if !strings.Contains(s, "<") && (MarkdownHasHTML(s) || HasHTML(s)) {
			t.Fatalf("%q has no < but was refused", s)
		}
		if !HasHTML(s + "<p>") {
			t.Fatalf("%q<p> wasn't refused as plain text", s)
		}
	})
}

func TestMarkdownUnsafeLink(t *testing.T) {
	t.Parallel()
	for _, md := range []string{
		"[x](javascript:alert(1))", "[x](JavaScript:alert(1) \"t\")", "![i](javascript:alert(1))", "<javascript:alert(1)>",
		"[x](javascript&#58;alert(1))", "[x](&#106;avascript:alert(1))", "[x](data:text/html,<script>x</script>)",
		"[x][r]\n\n[r]: vbscript:msgbox(1)", "- a [x](javascript:void(0))",
	} {
		if !MarkdownUnsafeLink(md) {
			t.Errorf("MarkdownUnsafeLink(%q) = false, want true", md)
		}
		if got := markupProblem("markdown", md); got != MarkdownLinkMessage && got != MarkdownHTMLMessage {
			t.Errorf("markupProblem(%q) = %q", md, got)
		}
	}
	for _, md := range []string{
		"", "plain", "[x](https://x.test/a?b=1)", "[x](/about)", "[x](#top)", "[x](mailto:you@example.com)", "[x](tel:+15550100)",
		"<https://example.com>", "<you@example.com>", "![logo](/files/a/logo.png)", "`[x](javascript:alert(1))`",
		"```\n[x](javascript:alert(1))\n```", "Ratio 3:1, at 10:30", "\\[x](javascript:alert(1)) is how not to",
	} {
		if MarkdownUnsafeLink(md) {
			t.Errorf("MarkdownUnsafeLink(%q) = true, want false", md)
		}
	}
}

func TestValidateDataMarkup(t *testing.T) {
	t.Parallel()
	defs := Parse(decode(t, `[
		{"name":"title","type":"string"},
		{"name":"summary","type":"text"},
		{"name":"body","type":"markdown"},
		{"name":"snippet","type":"code"},
		{"name":"words","type":"string_list"},
		{"name":"paragraphs","type":"repeater","of":[{"name":"html","type":"text"}]},
		{"name":"meta","type":"group","of":[{"name":"bio","type":"markdown"}]},
		{"name":"blocks","type":"blocks"}
	]`))
	types := func(slug string) ([]Field, bool) {
		if slug == "text" {
			return []Field{{Name: "body", Type: "markdown"}}, true
		}
		return nil, false
	}
	tests := []struct {
		name string
		data string
		want map[string][]string
	}{
		{"plain text and Markdown", `{"title":"a < b","summary":"<3","body":"**Bold**, <https://x.test>, ` + "`<p>`" + `",
			"snippet":"<div>code is exempt</div>","words":["<b>"],"paragraphs":[{"html":"Built in 1869"}],"meta":{"bio":"*Hi*"},
			"blocks":[{"type":"text","data":{"body":"Hello  \nthere"}}]}`, map[string][]string{}},
		{"HTML everywhere", `{"title":"<em>Hi</em>","summary":"<p>x</p>","body":"<strong>x</strong>",
			"paragraphs":[{"html":"Built in <strong class=\"text-cream\">1869</strong>"}],"meta":{"bio":"a<br>b"},
			"blocks":[{"type":"text","data":{"body":"<!-- x -->"}}]}`, map[string][]string{
			"title":               {PlainTextHTMLMessage},
			"summary":             {PlainTextHTMLMessage},
			"body":                {MarkdownHTMLMessage},
			"paragraphs[0].html":  {PlainTextHTMLMessage},
			"meta.bio":            {MarkdownHTMLMessage},
			"blocks[0].data.body": {MarkdownHTMLMessage},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, _ := decode(t, tt.data).(map[string]any)
			into := apierr.Invalid{}
			ValidateData(defs, data, types, into, "")
			ValidateMarkupData(defs, data, types, nil, into, "")
			if !reflect.DeepEqual(map[string][]string(into), tt.want) {
				t.Errorf("problems = %v\nwant %v", into, tt.want)
			}
		})
	}
}

func TestValidateMarkupOnlyWhatChanged(t *testing.T) {
	t.Parallel()
	defs := Parse(decode(t, `[
		{"name":"title","type":"string"},
		{"name":"body","type":"markdown"},
		{"name":"paragraphs","type":"repeater","of":[{"name":"html","type":"text"}]}
	]`))
	types := func(slug string) ([]Field, bool) {
		if slug == "text" {
			return []Field{{Name: "body", Type: "markdown"}}, true
		}
		return nil, false
	}
	stored, _ := decode(t, `{"title":"<em>Old</em>","body":"<p>Legacy</p>","paragraphs":[{"html":"a"},{"html":"Built in <b>1869</b>"}]}`).(map[string]any)
	storedBlocks, _ := decode(t, `[{"type":"text","data":{"body":"<div>x</div>"}},{"type":"text","data":{"body":"y"}}]`).([]any)
	before := Texts{}
	before.AddData(defs, stored, types, "frontmatter")
	before.AddBlocks(storedBlocks, types, "blocks")

	tests := []struct {
		name, data, blocks string
		want               map[string][]string
	}{
		{"untouched legacy HTML", `{"title":"<em>Old</em>","body":"<p>Legacy</p>","paragraphs":[{"html":"a"},{"html":"Built in <b>1869</b>"}]}`,
			`[{"type":"text","data":{"body":"<div>x</div>"}}]`, map[string][]string{}},
		{"moved, not changed", `{"paragraphs":[{"html":"Built in <b>1869</b>"},{"html":"a"}]}`,
			`[{"type":"text","data":{"body":"y"}},{"type":"text","data":{"body":"<div>x</div>"}}]`, map[string][]string{}},
		{"cleaned up", `{"title":"Old","body":"Legacy"}`, `[]`, map[string][]string{}},
		{"new HTML", `{"title":"<em>New</em>","body":"<p>Legacy</p>","paragraphs":[{"html":"<i>c</i>"}]}`,
			`[{"type":"text","data":{"body":"y <b>z</b>"}}]`, map[string][]string{
				"frontmatter.title":              {PlainTextHTMLMessage},
				"frontmatter.paragraphs[0].html": {PlainTextHTMLMessage},
				"blocks[0].data.body":            {MarkdownHTMLMessage},
			}},
		{"another field's HTML", `{"title":"<p>Legacy</p>"}`, `[]`, map[string][]string{"frontmatter.title": {PlainTextHTMLMessage}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, _ := decode(t, tt.data).(map[string]any)
			blocks, _ := decode(t, tt.blocks).([]any)
			into := apierr.Invalid{}
			ValidateMarkupData(defs, data, types, before, into, "frontmatter")
			ValidateMarkupBlocks(blocks, types, before, into, "blocks")
			if !reflect.DeepEqual(map[string][]string(into), tt.want) {
				t.Errorf("problems = %v\nwant %v", into, tt.want)
			}
		})
	}
}
