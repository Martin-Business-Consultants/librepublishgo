package fields

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	nethtml "golang.org/x/net/html"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/jsonv"
)

// Rich text is Markdown, written in the admin's editor and turned into HTML
// by the site; every other text field is plain text. HTML written into either
// is refused (text a save changes; see Texts): in a plain field it's markup the site shows as text
// (or, worse, trusts), and in a Markdown field it's formatting the editor
// can't show or edit, which a site's renderer escapes anyway.

// The messages a field holding HTML is refused with, and a Markdown link
// to somewhere a link mustn't go.
const (
	PlainTextHTMLMessage = "Formatting isn't allowed here: use a Markdown (rich text) field"
	MarkdownHTMLMessage  = "Use the editor's formatting, not HTML tags"
	MarkdownLinkMessage  = "A link or image " + unsafeURLMessage
)

// htmlTagPattern finds what could be a tag: < or </, a name, then either >
// (after an optional /) or whitespace and attributes up to a >. Whether it
// is one depends on the name (htmlElements), so "<name>" in a template line
// or "List<T>" isn't. "a < b", "<3", "5 <10" and autolinks such as
// <https://example.com> or <you@example.com> never match: the name must
// start with a letter and end at whitespace, / or >.
var htmlTagPattern = regexp.MustCompile(`<(/?)([A-Za-z][A-Za-z0-9]*)(?:\s[^<>]*)?/?>`)

// htmlSpecialPattern finds the rest of what HTML parses as markup: a comment,
// a doctype or CDATA section, and a processing instruction.
var htmlSpecialPattern = regexp.MustCompile(`<!--|<!\[CDATA\[|<![A-Za-z]|<\?[A-Za-z]`)

// htmlElements is every element name HTML, SVG and MathML define, current
// and obsolete, lowercase.
var htmlElements = setOf(`a abbr acronym address applet area article aside audio b base basefont bdi bdo bgsound big blink
blockquote body br button canvas caption center cite code col colgroup data datalist dd del details dfn dialog dir div dl dt
em embed fieldset figcaption figure font footer form frame frameset h1 h2 h3 h4 h5 h6 head header hgroup hr html i iframe
image img input ins isindex kbd keygen label legend li link listing main map mark marquee menu menuitem meta meter nav nobr
noembed noframes noscript object ol optgroup option output p param picture plaintext portal pre progress q rb rp rt rtc ruby
s samp script search section select slot small source spacer span strike strong style sub summary sup table tbody td template
textarea tfoot th thead time title tr track tt u ul var video wbr xmp
svg g path circle ellipse line polygon polyline rect text tspan use defs symbol foreignobject animate set
math mi mn mo ms mtext mrow mfrac msqrt mroot msup msub mtable mtr mtd semantics annotation`)

func setOf(words string) map[string]bool {
	out := map[string]bool{}
	for w := range strings.FieldsSeq(words) {
		out[w] = true
	}
	return out
}

// HasHTML is whether plain text s holds an HTML tag, comment or the like.
// What's a tag is read twice: by the pattern above, and as a browser's
// tokenizer reads it, which also takes / between a name and its
// attributes (<img/src=x onerror=…>). Either finding a known element is
// HTML, and so is any tag with an event handler, known or not
// (<x onclick=…> is an element to a browser, with a handler that runs).
func HasHTML(s string) bool {
	if !strings.Contains(s, "<") {
		return false
	}
	if htmlSpecialPattern.MatchString(s) {
		return true
	}
	for _, m := range htmlTagPattern.FindAllStringSubmatch(s, -1) {
		if htmlElements[strings.ToLower(m[2])] {
			return true
		}
	}
	return tokenizedTag(s)
}

// tokenizedTag is whether HTML's tokenizer reads a known element, or a tag
// with an on… attribute, in s.
func tokenizedTag(s string) bool {
	z := nethtml.NewTokenizer(strings.NewReader(s))
	for {
		switch z.Next() {
		case nethtml.ErrorToken:
			return false
		case nethtml.StartTagToken, nethtml.EndTagToken, nethtml.SelfClosingTagToken:
			name, more := z.TagName()
			if htmlElements[string(name)] {
				return true
			}
			// A handler has a value to run; "<String, Online>" has none.
			for more {
				var key, val []byte
				key, val, more = z.TagAttr()
				if len(key) > 2 && strings.HasPrefix(string(key), "on") && len(val) > 0 {
					return true
				}
			}
		}
	}
}

// MarkdownHasHTML is whether Markdown s holds raw HTML outside code: what's
// in a code span or a fenced code block is shown as written, and \< is a
// literal <, so neither counts. Autolinks (<https://…>, <you@example.com>)
// aren't HTML either.
func MarkdownHasHTML(s string) bool {
	if !strings.Contains(s, "<") {
		return false
	}
	return HasHTML(maskInlineCode(maskFences(s)))
}

var (
	// fenceOpen is a line opening a fenced code block, after any blockquote
	// markers, indentation and list marker.
	fenceOpen   = regexp.MustCompile("^[\\s>]*(?:(?:[-+*]|\\d{1,9}[.)])\\s+)?(`{3,}|~{3,})")
	fenceMarker = regexp.MustCompile("^[\\s>]*(`{3,}|~{3,})\\s*$")
)

// maskFences blanks the lines inside fenced code blocks, fences included.
func maskFences(s string) string {
	if !strings.Contains(s, "```") && !strings.Contains(s, "~~~") {
		return s
	}
	lines := strings.Split(s, "\n")
	fence := ""
	for i, line := range lines {
		if fence == "" {
			if m := fenceOpen.FindStringSubmatch(line); m != nil {
				// A backtick fence's info string can't hold a backtick; that
				// line is inline code instead.
				if m[1][0] == '`' && strings.Contains(line[len(m[0]):], "`") {
					continue
				}
				fence = m[1]
				lines[i] = ""
			}
			continue
		}
		if m := fenceMarker.FindStringSubmatch(line); m != nil && m[1][0] == fence[0] && len(m[1]) >= len(fence) {
			fence = ""
		}
		lines[i] = ""
	}
	return strings.Join(lines, "\n")
}

// maskInlineCode blanks code spans and backslash escapes: a run of n
// backticks up to the next run of exactly n is code; one without a match is
// literal backticks.
func maskInlineCode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]):
			b.WriteString("  ")
			i += 2
		case c == '`':
			n := backticks(s, i)
			if end := closingRun(s, i+n, n); end >= 0 {
				b.WriteString(strings.Repeat(" ", end-i))
				i = end
				continue
			}
			b.WriteString(s[i : i+n])
			i += n
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// backticks is the length of the run of backticks at s[i:].
func backticks(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// closingRun is the index just past the first run of exactly n backticks at
// or after from, or -1.
func closingRun(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		m := backticks(s, i)
		if m == n {
			return i + m
		}
		i += m
	}
	return -1
}

func isASCIIPunct(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

// mdParser reads Markdown as the site's renderers do (CommonMark, with
// the editor's extensions).
var mdParser = goldmark.New(goldmark.WithExtensions(extension.Strikethrough, extension.TaskList)).Parser()

// MarkdownUnsafeLink is whether Markdown s has a link, image or autolink
// whose address SafeURL refuses (javascript: and kin), entities and all:
// a renderer decodes &#58; in an address before a browser reads it.
func MarkdownUnsafeLink(s string) bool {
	if !strings.Contains(s, ":") && !strings.Contains(s, "&") {
		return false
	}
	src := []byte(s)
	doc := mdParser.Parse(text.NewReader(src))
	unsafe := false
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var dest []byte
		switch x := n.(type) {
		case *ast.Link:
			dest = x.Destination
		case *ast.Image:
			dest = x.Destination
		case *ast.AutoLink:
			dest = x.URL(src)
		default:
			return ast.WalkContinue, nil
		}
		if !SafeURL(html.UnescapeString(string(dest))) {
			unsafe = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return unsafe
}

// markupProblem is what's wrong with string v as a value of a text field of
// type typ: HTML where it isn't allowed, a Markdown link to javascript:,
// or "".
func markupProblem(typ, v string) string {
	switch typ {
	case "string", "text":
		if HasHTML(v) {
			return PlainTextHTMLMessage
		}
	case "markdown":
		if MarkdownHasHTML(v) {
			return MarkdownHTMLMessage
		}
		if MarkdownUnsafeLink(v) {
			return MarkdownLinkMessage
		}
	}
	return ""
}

// ---- only what a save changes ----------------------------------------------
//
// The no-markup rule is a rule for what's written now, not a judgement on
// what's stored: content imported or saved before it may hold HTML, and a
// save that leaves that text as it was (a new title, a status change, a
// scheduled publish) mustn't fail over it. So it's checked per field, against
// the text the record held before: a value the same field already held is
// left alone, one that's new is checked. "The same field" is its path with
// list positions dropped (and a block's type kept), so moving a block or a
// repeater item doesn't make its text new.

// Texts is the text a record held: for each field (by its path with list
// positions dropped), the string, text and markdown values it had.
type Texts map[string]map[string]bool

// AddData notes the text data holds for fields, under prefix.
func (t Texts) AddData(defs []Field, data map[string]any, types BlockTypes, prefix string) {
	walkData(defs, data, types, prefix, prefix, func(_ Field, _, shape, s string) { t.add(shape, s) })
}

// AddBlocks notes the text a body of blocks holds, under prefix.
func (t Texts) AddBlocks(list []any, types BlockTypes, prefix string) {
	walkBlocks(list, types, prefix, prefix, func(_ Field, _, shape, s string) { t.add(shape, s) })
}

func (t Texts) add(shape, s string) {
	if t[shape] == nil {
		t[shape] = map[string]bool{}
	}
	t[shape][s] = true
}

// held is whether the field at shape already held s.
func (t Texts) held(shape, s string) bool { return t[shape][s] }

// ValidateMarkupData refuses HTML in data's string, text and markdown values,
// at any depth, that before doesn't hold for the same field (nil before:
// a new record, every value is checked).
func ValidateMarkupData(defs []Field, data map[string]any, types BlockTypes, before Texts, into apierr.Invalid, prefix string) {
	walkData(defs, data, types, prefix, prefix, markupCheck(before, into))
}

// ValidateMarkupBlocks is ValidateMarkupData for a body of blocks.
func ValidateMarkupBlocks(list []any, types BlockTypes, before Texts, into apierr.Invalid, prefix string) {
	walkBlocks(list, types, prefix, prefix, markupCheck(before, into))
}

func markupCheck(before Texts, into apierr.Invalid) func(Field, string, string, string) {
	return func(f Field, at, shape, s string) {
		if before.held(shape, s) {
			return
		}
		if problem := markupProblem(f.Type, s); problem != "" {
			into.Add(at, problem)
		}
	}
}

// walkData calls fn with each string, text or markdown value in data, its
// path, and its path with list positions dropped.
func walkData(defs []Field, data map[string]any, types BlockTypes, at, shape string, fn func(f Field, at, shape, s string)) {
	for _, f := range defs {
		v, ok := data[f.Name]
		if !ok {
			continue
		}
		fAt, fShape := joinPath(at, f.Name), joinPath(shape, f.Name)
		switch f.Type {
		case "string", "text", "markdown":
			if s, isStr := v.(string); isStr {
				fn(f, fAt, fShape, s)
			}
		case "group":
			if m, isMap := v.(map[string]any); isMap {
				walkData(f.Of, m, types, fAt, fShape, fn)
			}
		case "repeater":
			l, _ := v.([]any)
			for i, item := range l {
				if m, isMap := item.(map[string]any); isMap {
					walkData(f.Of, m, types, fmt.Sprintf("%s[%d]", fAt, i), fShape+"[]", fn)
				}
			}
		case "blocks":
			l, _ := v.([]any)
			walkBlocks(l, types, fAt, fShape, fn)
		}
	}
}

func walkBlocks(list []any, types BlockTypes, at, shape string, fn func(f Field, at, shape, s string)) {
	for i, item := range list {
		b, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ := jsonv.String(b["type"])
		defs, known := types(typ)
		data, isMap := b["data"].(map[string]any)
		if !known || !isMap {
			continue
		}
		walkData(defs, data, types, fmt.Sprintf("%s[%d].data", at, i), shape+"["+typ+"].data", fn)
	}
}
