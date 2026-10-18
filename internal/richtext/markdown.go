package richtext

import (
	"fmt"
	"regexp"
	"strings"
)

// ToMarkdown is doc as the Markdown the admin's editor writes: what an
// entry's body (body_markdown, Markdown by its nature) holds once HTML in it
// is brought onto the editor's terms.
func ToMarkdown(doc Node) string {
	var b strings.Builder
	writeBlocks(&b, children(doc), "")
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeBlocks(b *strings.Builder, nodes []Node, indent string) {
	for i, n := range nodes {
		if i > 0 {
			b.WriteString(indent + "\n")
		}
		writeBlock(b, n, indent)
	}
}

func writeBlock(b *strings.Builder, n Node, indent string) {
	switch n["type"] {
	case "paragraph":
		b.WriteString(indent + inlineMarkdown(children(n), indent) + "\n")
	case "heading":
		level := 2
		if a, ok := n["attrs"].(map[string]any); ok {
			if l, ok := a["level"].(int); ok {
				level = l
			} else if l, ok := a["level"].(float64); ok {
				level = int(l)
			}
		}
		b.WriteString(indent + strings.Repeat("#", max(1, min(6, level))) + " " + inlineMarkdown(children(n), indent) + "\n")
	case "bulletList", "taskList":
		for _, item := range children(n) {
			marker := "- "
			if item["type"] == "taskItem" {
				box := "[ ] "
				if a, _ := item["attrs"].(map[string]any); a["checked"] == true {
					box = "[x] "
				}
				marker += box
			}
			writeItem(b, item, indent, marker)
		}
	case "orderedList":
		start := 1
		if a, ok := n["attrs"].(map[string]any); ok {
			switch s := a["start"].(type) {
			case int:
				start = s
			case float64:
				start = int(s)
			}
		}
		for i, item := range children(n) {
			writeItem(b, item, indent, fmt.Sprintf("%d. ", start+i))
		}
	case "blockquote":
		var inner strings.Builder
		writeBlocks(&inner, children(n), "")
		for line := range strings.SplitSeq(strings.TrimRight(inner.String(), "\n"), "\n") {
			b.WriteString(strings.TrimRight(indent+"> "+line, " ") + "\n")
		}
	case "codeBlock":
		lang := ""
		if a, ok := n["attrs"].(map[string]any); ok {
			lang, _ = a["language"].(string)
		}
		var code strings.Builder
		for _, c := range children(n) {
			code.WriteString(asString(c["text"]))
		}
		text := code.String()
		b.WriteString(indent + "```" + lang + "\n")
		for line := range strings.SplitSeq(text, "\n") {
			b.WriteString(indent + line + "\n")
		}
		b.WriteString(indent + "```\n")
	case "horizontalRule":
		b.WriteString(indent + "---\n")
	}
}

// writeItem is one list item: its marker on the first line, the rest of
// its blocks indented under it.
func writeItem(b *strings.Builder, item Node, indent, marker string) {
	var inner strings.Builder
	sub := indent + strings.Repeat(" ", len(marker))
	writeBlocks(&inner, children(item), sub)
	text := strings.TrimPrefix(inner.String(), sub)
	b.WriteString(indent + marker + text)
}

var (
	escapeInline = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", `\<`, "~", `\~`)
	lineStart    = regexp.MustCompile(`^(\s*)([#>+-]|\d+\.)(\s)`)
)

// inlineMarkdown is a paragraph's text with its marks and links; a line
// break is two trailing spaces, as the editor writes it.
func inlineMarkdown(nodes []Node, indent string) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n["type"] {
		case "hardBreak":
			b.WriteString("  \n" + indent)
		case "text":
			t, _ := n["text"].(string)
			marks, _ := n["marks"].([]any)
			code := false
			for _, m := range marks {
				if asNode(m)["type"] == "code" {
					code = true
				}
			}
			if code {
				t = "`" + t + "`"
			} else {
				t = escapeInline.Replace(t)
			}
			for _, m := range marks {
				mm := asNode(m)
				switch mm["type"] {
				case "bold":
					t = "**" + t + "**"
				case "italic":
					t = "_" + t + "_"
				case "strike":
					t = "~~" + t + "~~"
				case "link":
					a := asNode(mm["attrs"])
					href, _ := a["href"].(string)
					t = "[" + t + "](" + strings.ReplaceAll(href, ")", "%29") + ")"
				}
			}
			b.WriteString(t)
		}
	}
	// A line that would read as a heading, quote or list item isn't one.
	out := b.String()
	if m := lineStart.FindStringSubmatchIndex(out); m != nil {
		out = out[:m[4]] + `\` + out[m[4]:]
	}
	return out
}
