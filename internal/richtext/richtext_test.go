package richtext

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// js is v as compact JSON, < and > as they are.
func js(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(b.String())
}

func TestFromHTML(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"Old Mill's about intro, classes and all",
			`Built in <strong class="text-cream">1869</strong>, a <em>mill</em>.`,
			`{"content":[{"content":[{"text":"Built in ","type":"text"},{"marks":[{"type":"bold"}],"text":"1869","type":"text"},{"text":", a ","type":"text"},{"marks":[{"type":"italic"}],"text":"mill","type":"text"},{"text":".","type":"text"}],"type":"paragraph"}],"type":"doc"}`},
		{"paragraphs, whitespace, a break",
			"<p>\n  One\n  line</p>\n<p>Two<br>lines</p>",
			`{"content":[{"content":[{"text":"One line","type":"text"}],"type":"paragraph"},{"content":[{"text":"Two","type":"text"},{"type":"hardBreak"},{"text":"lines","type":"text"}],"type":"paragraph"}],"type":"doc"}`},
		{"headings, lists, a quote, a rule",
			`<h3>Today</h3><ul><li>beer</li><li><p>food</p><ol start="3"><li>three</li></ol></li></ul><blockquote>quoted</blockquote><hr>`,
			`{"content":[{"attrs":{"level":3},"content":[{"text":"Today","type":"text"}],"type":"heading"},{"content":[{"content":[{"content":[{"text":"beer","type":"text"}],"type":"paragraph"}],"type":"listItem"},{"content":[{"content":[{"text":"food","type":"text"}],"type":"paragraph"},{"attrs":{"start":3},"content":[{"content":[{"content":[{"text":"three","type":"text"}],"type":"paragraph"}],"type":"listItem"}],"type":"orderedList"}],"type":"listItem"}],"type":"bulletList"},{"content":[{"content":[{"text":"quoted","type":"text"}],"type":"paragraph"}],"type":"blockquote"},{"type":"horizontalRule"}],"type":"doc"}`},
		{"links: safe ones kept, others their text",
			`<a href="/menu/">menu</a> <a href="javascript:alert(1)">bad</a>`,
			`{"content":[{"content":[{"marks":[{"attrs":{"href":"/menu/"},"type":"link"}],"text":"menu","type":"text"},{"text":" bad","type":"text"}],"type":"paragraph"}],"type":"doc"}`},
		{"what the editor has no node for keeps its words",
			`<div><span style="color:red">red</span> <img alt="a mill" src="x.jpg"><script>alert(1)</script></div><table><tr><td>cell</td></tr></table>`,
			`{"content":[{"content":[{"text":"red a mill","type":"text"}],"type":"paragraph"},{"content":[{"text":"cell","type":"text"}],"type":"paragraph"}],"type":"doc"}`},
		{"code",
			`<pre><code class="language-js">a &lt; b</code></pre><p><code>x</code></p>`,
			`{"content":[{"attrs":{"language":"js"},"content":[{"text":"a < b","type":"text"}],"type":"codeBlock"},{"content":[{"marks":[{"type":"code"}],"text":"x","type":"text"}],"type":"paragraph"}],"type":"doc"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := js(t, FromHTML(tc.in)); got != tc.want {
				t.Fatalf("FromHTML:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestFromText(t *testing.T) {
	t.Parallel()
	if FromText("  ", false) != nil || FromText("<p></p>", false) != nil {
		t.Fatal("nothing should be nil")
	}
	if got := js(t, FromText("one\ntwo\n\nthree", false)); got != `{"content":[{"content":[{"text":"one","type":"text"},{"type":"hardBreak"},{"text":"two","type":"text"}],"type":"paragraph"},{"content":[{"text":"three","type":"text"}],"type":"paragraph"}],"type":"doc"}` {
		t.Fatalf("plain: %s", got)
	}
	if got := js(t, FromText("A **bold** [link](https://x.example)\n\n- [x] done", true)); got != `{"content":[{"content":[{"text":"A ","type":"text"},{"marks":[{"type":"bold"}],"text":"bold","type":"text"},{"text":" ","type":"text"},{"marks":[{"attrs":{"href":"https://x.example"},"type":"link"}],"text":"link","type":"text"}],"type":"paragraph"},{"content":[{"attrs":{"checked":true},"content":[{"content":[{"text":"done","type":"text"}],"type":"paragraph"}],"type":"taskItem"}],"type":"taskList"}],"type":"doc"}` {
		t.Fatalf("markdown: %s", got)
	}
	// Plain text that looks like Markdown stays as it is.
	if got := js(t, FromText("5 * 3 = 15", false)); got != `{"content":[{"content":[{"text":"5 * 3 = 15","type":"text"}],"type":"paragraph"}],"type":"doc"}` {
		t.Fatalf("plain with a star: %s", got)
	}
}

func TestToMarkdown(t *testing.T) {
	t.Parallel()
	doc := FromHTML(`<p>a half dozen of our <strong>smoked</strong> Meatballs, 5 * 3<br>next</p><ul><li>one</li><li>two</li></ul><h2># not a heading</h2>`)
	want := "a half dozen of our **smoked** Meatballs, 5 \\* 3  \nnext\n\n- one\n- two\n\n## \\# not a heading\n"
	if got := ToMarkdown(doc); got != want {
		t.Fatalf("ToMarkdown:\n got %q\nwant %q", got, want)
	}
	// And back: the Markdown reads as the same document.
	if a, b := js(t, doc), js(t, FromMarkdown(want)); a != b {
		t.Fatalf("round trip:\n%s\n%s", a, b)
	}
}
