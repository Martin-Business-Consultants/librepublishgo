package forms

import (
	"strings"
	"testing"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

func TestLongDate(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()
	time.Local = time.UTC
	if got := longDate("2026-10-20T16:41:23Z"); got != "October 20, 2026" {
		t.Fatalf("UTC: %q", got)
	}
	// In the server's zone: 1am UTC is still the evening before in New York.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = ny
	if got := longDate("2026-10-21T01:00:00Z"); got != "October 20, 2026" {
		t.Fatalf("New York: %q", got)
	}
	if got := longDate("soon"); got != "soon" {
		t.Fatalf("not a time: %q", got)
	}
}

// The email wears the brand: the logo on top, linked to the site, the
// primary color's band and links, the secondary color's text.
func TestFrame(t *testing.T) {
	out := frame("<p>Hi</p>", plugin.Site{Name: `MI-BOX "FC"`, URL: "https://miboxfc.com", LogoURL: "https://cms.test/files/logo.png?w=640",
		PrimaryColor: "#e4002b", SecondaryColor: "#1f2a44"})
	for _, want := range []string{
		`<a href="https://miboxfc.com"><img src="https://cms.test/files/logo.png?w=640" alt="MI-BOX &#34;FC&#34;"`,
		`a{color:#e4002b}`, `border-top:4px solid #e4002b`, `color:#1f2a44">`, `<p>Hi</p>`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in\n%s", want, out)
		}
	}
	// Without a brand: no logo, the neutral colors.
	if out := frame("<p>Hi</p>", plugin.Site{}); strings.Contains(out, "<img") || !strings.Contains(out, "border-top:4px solid #18181b") {
		t.Fatalf("plain:\n%s", out)
	}
}

// A date field's answer reads in words in an email; the rest as they came.
func TestAnswer(t *testing.T) {
	if got := answer(Field{Type: "date"}, "2026-11-14"); got != "November 14, 2026" {
		t.Fatalf("date: %q", got)
	}
	if got := answer(Field{Type: "date"}, "next week"); got != "next week" {
		t.Fatalf("not a date: %q", got)
	}
	if got := answer(Field{Type: "text"}, "2026-11-14"); got != "2026-11-14" {
		t.Fatalf("text: %q", got)
	}
}
