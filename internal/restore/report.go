package restore

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
)

// Report is what an import found and did, as the CLI prints it and the
// admin shows it.
type Report struct {
	ID          string    `json:"id"`
	Source      string    `json:"source"`
	Format      string    `json:"format"`
	FormatLabel string    `json:"format_label"`
	Manifest    *Manifest `json:"manifest,omitempty"`
	StagedAt    time.Time `json:"staged_at"`
	// The schema version (PRAGMA user_version) it had, and was migrated to.
	SchemaFrom int `json:"schema_from"`
	SchemaTo   int `json:"schema_to"`
	// Rows per table, the empty left out; Unused are tables this core
	// doesn't read (the Rails core's, or a plugin's), kept as they are.
	Tables  map[string]int `json:"tables"`
	Unused  []string       `json:"unused_tables,omitempty"`
	Ignored int            `json:"ignored_entries,omitempty"`
	Blobs   BlobReport     `json:"blobs"`
	Forms   FormsReport    `json:"forms"`
	// What the content clean-up changed (content.go).
	Content ContentReport `json:"content"`
	Secrets SecretsReport `json:"secrets"`
	// People with two-factor sign-in on, which the Go core can't check.
	TwoFactorUsers int `json:"two_factor_users,omitempty"`
	// The users the site being replaced has.
	TargetUsers int `json:"target_users"`
	// Rows removed on apply: sessions, edit locks, device logins.
	Cleared     map[string]int `json:"cleared,omitempty"`
	Applied     bool           `json:"applied"`
	AppliedAt   time.Time      `json:"applied_at,omitzero"`
	ReplacedDir string         `json:"replaced_dir,omitempty"`
	Warnings    []string       `json:"warnings"`
}

// BlobReport is the uploaded files: how many the database names, how many
// came with the backup whole, and what's wrong with the rest.
type BlobReport struct {
	Total        int `json:"total"`
	Copied       int `json:"copied"`
	Missing      int `json:"missing"`
	Mismatched   int `json:"mismatched"`
	BadKeys      int `json:"bad_keys,omitempty"`
	Renamed      int `json:"renamed,omitempty"`      // tenant prefix stripped
	Unreferenced int `json:"unreferenced,omitempty"` // in the archive, named by no blob
}

// FormsReport is the Rails Forms plugin's tables, as the Go Forms plugin's
// records.
type FormsReport struct {
	Forms       int      `json:"forms"`
	Submissions int      `json:"submissions"`
	Emails      int      `json:"emails"`
	Skipped     int      `json:"skipped,omitempty"`
	NotCarried  []string `json:"not_carried,omitempty"`
}

// SecretsReport is the values sealed with an install's key (token
// plaintexts, webhook secrets, the SMTP password): Plan is what applying
// would do, Done what it did.
type SecretsReport struct {
	KeyInBackup bool        `json:"key_in_backup"`
	KeyGiven    bool        `json:"key_given"`
	Plan        SealedCount `json:"plan"`
	Done        SealedCount `json:"done"`
	// Rails-encrypted values, by table.column: unreadable here.
	RailsEncrypted map[string]int `json:"rails_encrypted,omitempty"`
}

// SealedCount is sealed values: readable with this install's key as they
// are (Kept), re-sealed from the old key (Resealed), or cleared because
// no key opens them; Columns says where.
type SealedCount struct {
	Total    int            `json:"total"`
	Kept     int            `json:"kept"`
	Resealed int            `json:"resealed"`
	Cleared  int            `json:"cleared"`
	Columns  map[string]int `json:"cleared_columns,omitempty"`
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// WriteText writes the report for a person at a terminal.
func (r *Report) WriteText(w io.Writer) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
	state := "Staged (nothing changed yet)"
	if r.Applied {
		state = "Imported"
	}
	p("%s: %s, %s", state, r.Source, r.FormatLabel)
	if m := r.Manifest; m != nil {
		if m.CMSVersion != "" || m.GeneratedAt != "" {
			p("  made by:      %s", strings.TrimSpace(or(m.CMSVersion, "?")+" at "+or(m.GeneratedAt, "?")))
		}
		if m.Tenant != "" {
			p("  tenant:       %s", m.Tenant)
		}
	}
	p("  schema:       %d -> %d", r.SchemaFrom, r.SchemaTo)
	p("  records:      %s", counts(r.Tables))
	if len(r.Unused) > 0 {
		p("  kept unused:  %s", strings.Join(r.Unused, ", "))
	}
	b := r.Blobs
	p("  files:        %d (%d whole, %d missing, %d not matching their checksum)", b.Total, b.Copied, b.Missing, b.Mismatched)
	if f := r.Forms; f.Forms+f.Submissions > 0 {
		p("  forms:        %d forms, %d submissions, %d emails converted for the Forms plugin", f.Forms, f.Submissions, f.Emails)
	}
	if c := r.Content; len(c.Fields)+c.Values+c.URLCaches+c.Bodies+c.SEO > 0 {
		p("  content:      %d fields changed, %d values onto rich text, %d image URLs removed, %d entry bodies' HTML to Markdown, %d SEO keys renamed, %d alt texts onto images",
			len(c.Fields), c.Values, c.URLCaches, c.Bodies, c.SEO, c.Alts)
		for _, f := range c.Fields {
			p("                %s: %s -> %s", f.Path, f.From, f.To)
		}
	}
	s := r.Secrets.Plan
	if r.Applied {
		s = r.Secrets.Done
	}
	if s.Total > 0 {
		verb := "would be"
		if r.Applied {
			verb = "were"
		}
		p("  secrets:      %d sealed: %d readable here, %d %s re-sealed, %d %s cleared%s", s.Total, s.Kept, s.Resealed, verb, s.Cleared, verb, columns(s.Columns))
	}
	if len(r.Cleared) > 0 {
		p("  signed out:   %s", counts(r.Cleared))
	}
	if r.ReplacedDir != "" {
		p("  replaced:     the previous site is in %s", r.ReplacedDir)
	}
	for _, msg := range r.Warnings {
		p("  ! %s", msg)
	}
}

func counts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(out, ", ")
}

func columns(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	return " (" + counts(m) + ")"
}

// plural is "1 person" or "3 people".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
