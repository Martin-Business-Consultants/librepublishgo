// Package richtext makes the admin editor's own document (Tiptap JSON, a
// ProseMirror doc, what a richtext field stores) from HTML, Markdown or
// plain text, and Markdown from a document: what an import uses to bring old
// content onto rich text. Its nodes and marks are the editor's (StarterKit
// with links, and task lists) and no others, so internal/fields accepts
// every document it makes and the editor opens it as it is. What the editor
// has no node for keeps its text: a table's cells become paragraphs, an
// image its alt text, a <span> or <font> just its words.
package richtext

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Node is a document node, as JSON has it.
type Node = map[string]any

// IsDoc is whether v is a document.
func IsDoc(v any) bool {
	m, ok := v.(map[string]any)
	return ok && m["type"] == "doc"
}

// Empty is whether doc has nothing in it but empty paragraphs.
func Empty(doc Node) bool {
	for _, n := range children(doc) {
		if n["type"] != "paragraph" || len(children(n)) > 0 {
			return false
		}
	}
	return true
}

// tag is an HTML tag: what tells HTML from text.
var tag = regexp.MustCompile(`<\/?[A-Za-z][A-Za-z0-9]*(?:\s[^<>]*)?\/?>`)

// HasHTML is whether s holds HTML tags.
func HasHTML(s string) bool { return tag.MatchString(s) }

// FromText is s as a document, whatever it is: HTML when it has tags,
// Markdown when markdown says it is, plain text otherwise. nil for nothing.
func FromText(s string, markdown bool) Node {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var doc Node
	switch {
	case HasHTML(s):
		doc = FromHTML(s)
	case markdown:
		doc = FromMarkdown(s)
	default:
		doc = FromPlain(s)
	}
	if Empty(doc) {
		return nil
	}
	return doc
}

// FromPlain is plain text as paragraphs: a blank line between them, a line
// break within one.
func FromPlain(s string) Node {
	s = strings.TrimSpace(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s))
	var paras []any
	for _, para := range regexp.MustCompile(`\n\s*\n`).Split(s, -1) {
		var parts []any
		for i, line := range strings.Split(para, "\n") {
			if i > 0 {
				parts = append(parts, Node{"type": "hardBreak"})
			}
			if line != "" {
				parts = append(parts, Node{"type": "text", "text": line})
			}
		}
		paras = append(paras, paragraph(parts))
	}
	return Node{"type": "doc", "content": paras}
}

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.Strikethrough, extension.TaskList),
	// HTML in the Markdown is read as HTML (FromHTML keeps what the
	// editor can show), not dropped.
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

// FromMarkdown is Markdown (CommonMark, with strikethrough and task lists,
// as the editor writes it) as a document.
func FromMarkdown(s string) Node {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(s), &buf); err != nil {
		return FromPlain(s)
	}
	return FromHTML(buf.String())
}

// FromHTML is HTML as a document.
func FromHTML(s string) Node {
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(s), body)
	if err != nil {
		return FromPlain(s)
	}
	b := &builder{}
	for _, n := range nodes {
		b.block(n)
	}
	b.flush()
	return Node{"type": "doc", "content": b.out}
}

// builder gathers block nodes, and the inline run waiting to become a
// paragraph.
type builder struct {
	out    []any
	inline []any
}

func (b *builder) flush() {
	if run := trimRun(b.inline); len(run) > 0 {
		b.out = append(b.out, paragraph(run))
	}
	b.inline = nil
}

func (b *builder) add(n Node) {
	b.flush()
	b.out = append(b.out, n)
}

// blocks is n's children as block nodes.
func blocks(n *html.Node) []any {
	b := &builder{}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.block(c)
	}
	b.flush()
	return b.out
}

func (b *builder) block(n *html.Node) {
	if n.Type == html.TextNode || n.Type == html.ElementNode && !isBlock(n.DataAtom) {
		b.inline = append(b.inline, inline(n, nil)...)
		return
	}
	if n.Type != html.ElementNode {
		return
	}
	switch n.DataAtom {
	case atom.P:
		b.flush()
		if run := trimRun(inlines(n, nil)); len(run) > 0 {
			b.add(paragraph(run))
		}
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		if run := trimRun(inlines(n, nil)); len(run) > 0 {
			level, _ := strconv.Atoi(n.Data[1:])
			b.add(Node{"type": "heading", "attrs": Node{"level": level}, "content": run})
		}
	case atom.Ul, atom.Ol:
		if l := list(n); l != nil {
			b.add(l)
		}
	case atom.Blockquote:
		if inner := blocks(n); len(inner) > 0 {
			b.add(Node{"type": "blockquote", "content": inner})
		}
	case atom.Pre:
		b.add(codeBlock(n))
	case atom.Hr:
		b.add(Node{"type": "horizontalRule"})
	case atom.Script, atom.Style, atom.Template, atom.Head, atom.Title, atom.Noscript:
		// Not content.
	default:
		// A wrapper (div, section, table, li outside a list, …): its
		// contents, as blocks of their own.
		b.flush()
		b.out = append(b.out, blocks(n)...)
	}
}

func isBlock(a atom.Atom) bool {
	switch a {
	case atom.P, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Ul, atom.Ol, atom.Li, atom.Blockquote, atom.Pre, atom.Hr,
		atom.Div, atom.Section, atom.Article, atom.Aside, atom.Header, atom.Footer, atom.Main, atom.Nav, atom.Figure, atom.Figcaption,
		atom.Table, atom.Thead, atom.Tbody, atom.Tfoot, atom.Tr, atom.Td, atom.Th, atom.Dl, atom.Dt, atom.Dd, atom.Address, atom.Details, atom.Summary,
		atom.Script, atom.Style, atom.Template, atom.Head, atom.Title, atom.Noscript:
		return true
	}
	return false
}

// list is a <ul>/<ol> as a bullet, ordered or task list.
func list(n *html.Node) Node {
	var items []any
	task := false
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.DataAtom != atom.Li {
			continue
		}
		checked, isTask := taskBox(c)
		task = task || isTask
		content := blocks(c)
		if len(content) == 0 {
			content = []any{paragraph(nil)}
		}
		item := Node{"type": "listItem", "content": content}
		if isTask {
			item = Node{"type": "taskItem", "attrs": Node{"checked": checked}, "content": content}
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil
	}
	if task {
		for i, it := range items {
			if m := asNode(it); m["type"] == "listItem" {
				items[i] = Node{"type": "taskItem", "attrs": Node{"checked": false}, "content": m["content"]}
			}
		}
		return Node{"type": "taskList", "content": items}
	}
	if n.DataAtom == atom.Ol {
		l := Node{"type": "orderedList", "content": items}
		if start, err := strconv.Atoi(attr(n, "start")); err == nil && start != 1 {
			l["attrs"] = Node{"start": start}
		}
		return l
	}
	return Node{"type": "bulletList", "content": items}
}

// taskBox finds (and takes out) a list item's checkbox: a task list's.
func taskBox(li *html.Node) (checked, found bool) {
	var box *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		for c := n.FirstChild; c != nil && box == nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.DataAtom == atom.Input && strings.EqualFold(attr(c, "type"), "checkbox") {
				box = c
				return
			}
			if c.Type == html.ElementNode && (c.DataAtom == atom.P || c.DataAtom == atom.Label) {
				find(c)
			}
		}
	}
	find(li)
	if box == nil {
		return false, false
	}
	_, checked = attrOK(box, "checked")
	box.Parent.RemoveChild(box)
	return checked, true
}

func codeBlock(n *html.Node) Node {
	lang := ""
	if c := n.FirstChild; c != nil && c.Type == html.ElementNode && c.DataAtom == atom.Code {
		for cls := range strings.FieldsSeq(attr(c, "class")) {
			if l, ok := strings.CutPrefix(cls, "language-"); ok {
				lang = l
			}
		}
	}
	text := strings.TrimSuffix(textOf(n), "\n")
	node := Node{"type": "codeBlock"}
	if lang != "" {
		node["attrs"] = Node{"language": lang}
	}
	if text != "" {
		node["content"] = []any{Node{"type": "text", "text": text}}
	}
	return node
}

// inlines is n's children as inline nodes, under marks.
func inlines(n *html.Node, marks []any) []any {
	var out []any
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, inline(c, marks)...)
	}
	return out
}

func inline(n *html.Node, marks []any) []any {
	switch n.Type {
	case html.TextNode:
		t := collapse(n.Data)
		if t == "" {
			return nil
		}
		node := Node{"type": "text", "text": t}
		if len(marks) > 0 {
			node["marks"] = append([]any(nil), marks...)
		}
		return []any{node}
	case html.ElementNode:
	default:
		return nil
	}
	switch n.DataAtom {
	case atom.Br:
		return []any{Node{"type": "hardBreak"}}
	case atom.Strong, atom.B:
		return inlines(n, withMark(marks, Node{"type": "bold"}))
	case atom.Em, atom.I, atom.Cite, atom.Dfn:
		return inlines(n, withMark(marks, Node{"type": "italic"}))
	case atom.S, atom.Del, atom.Strike:
		return inlines(n, withMark(marks, Node{"type": "strike"}))
	case atom.Code, atom.Kbd, atom.Samp, atom.Tt:
		return inlines(n, withMark(marks, Node{"type": "code"}))
	case atom.A:
		if href := strings.TrimSpace(attr(n, "href")); href != "" && safeHref(href) {
			return inlines(n, withMark(marks, Node{"type": "link", "attrs": Node{"href": href}}))
		}
		return inlines(n, marks)
	case atom.Img:
		if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
			return []any{textNode(alt, marks)}
		}
		return nil
	case atom.Script, atom.Style, atom.Template, atom.Input, atom.Button, atom.Select, atom.Textarea, atom.Iframe, atom.Object, atom.Svg:
		return nil
	}
	// A span, font, u, abbr, …: its words, keeping the marks around it.
	return inlines(n, marks)
}

func textNode(t string, marks []any) Node {
	n := Node{"type": "text", "text": t}
	if len(marks) > 0 {
		n["marks"] = append([]any(nil), marks...)
	}
	return n
}

func withMark(marks []any, m Node) []any {
	for _, have := range marks {
		if asNode(have)["type"] == m["type"] {
			return marks
		}
	}
	return append(append([]any(nil), marks...), m)
}

var spaces = regexp.MustCompile(`[ \t\n\r\f]+`)

// collapse folds whitespace as HTML shows it: one space for any run.
func collapse(s string) string { return spaces.ReplaceAllString(s, " ") }

// trimRun is an inline run without the whitespace at its ends or around its
// line breaks, its neighbouring texts with the same marks joined.
func trimRun(run []any) []any {
	var out []any
	for _, it := range run {
		n := asNode(it)
		if n["type"] == "text" && len(out) > 0 {
			if prev := asNode(out[len(out)-1]); prev["type"] == "text" && sameMarks(prev["marks"], n["marks"]) {
				prev["text"] = asString(prev["text"]) + asString(n["text"])
				continue
			}
		}
		out = append(out, n)
	}
	for i, it := range out {
		n := asNode(it)
		if n["type"] != "text" {
			continue
		}
		t := asString(n["text"])
		if i == 0 || asNode(out[i-1])["type"] == "hardBreak" {
			t = strings.TrimLeft(t, " ")
		}
		if i == len(out)-1 || asNode(out[i+1])["type"] == "hardBreak" {
			t = strings.TrimRight(t, " ")
		}
		n["text"] = t
	}
	kept := out[:0]
	for _, it := range out {
		if n := asNode(it); n["type"] != "text" || n["text"] != "" {
			kept = append(kept, n)
		}
	}
	// No line break at a paragraph's ends.
	for len(kept) > 0 && asNode(kept[0])["type"] == "hardBreak" {
		kept = kept[1:]
	}
	for len(kept) > 0 && asNode(kept[len(kept)-1])["type"] == "hardBreak" {
		kept = kept[:len(kept)-1]
	}
	return kept
}

func sameMarks(a, b any) bool {
	la, _ := a.([]any)
	lb, _ := b.([]any)
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		ma, mb := asNode(la[i]), asNode(lb[i])
		if ma["type"] != mb["type"] {
			return false
		}
		if ma["type"] == "link" && asNode(ma["attrs"])["href"] != asNode(mb["attrs"])["href"] {
			return false
		}
	}
	return true
}

func paragraph(content []any) Node {
	p := Node{"type": "paragraph"}
	if len(content) > 0 {
		p["content"] = content
	}
	return p
}

// asString is x as a string; "" when it isn't.
func asString(x any) string {
	s, _ := x.(string)
	return s
}

// asNode is x as a node; an empty one when it isn't.
func asNode(x any) Node {
	n, _ := x.(map[string]any)
	return n
}

func children(n Node) []Node {
	list, _ := n["content"].([]any)
	out := make([]Node, 0, len(list))
	for _, c := range list {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Br {
			b.WriteString("\n")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func attr(n *html.Node, key string) string {
	v, _ := attrOK(n, key)
	return v
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

var scheme = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*):`)

// safeHref is internal/fields' SafeURL: http(s), mailto:, tel: or a path.
func safeHref(u string) bool {
	clean := strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return -1
		}
		return r
	}, u)
	m := scheme.FindStringSubmatch(clean)
	if m == nil {
		return true
	}
	switch strings.ToLower(m[1]) {
	case "http", "https", "mailto", "tel":
		return true
	}
	return false
}
