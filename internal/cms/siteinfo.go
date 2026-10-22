package cms

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// ---- site info -----------------------------------------------------------

// siteHealthInfo is everything about this install someone helping with it
// would ask first, in sections, and as one text to paste (WordPress's
// "Copy site info to clipboard"). No secrets: addresses and counts, never a
// password, token or key.
func (a *App) siteHealthInfo(c *Ctx) error {
	if err := c.Need("settings:read"); err != nil {
		return err
	}
	ctx := c.R.Context()
	sections := a.siteInfo(ctx)
	var b strings.Builder
	for _, s := range sections {
		fmt.Fprintf(&b, "### %s ###\n\n", s.title)
		for _, row := range s.rows {
			fmt.Fprintf(&b, "%s: %s\n", row[0], row[1])
		}
		b.WriteString("\n")
	}
	out := []Obj{}
	for _, s := range sections {
		rows := []Obj{}
		for _, r := range s.rows {
			rows = append(rows, O("label", r[0], "value", r[1]))
		}
		out = append(out, O("title", s.title, "rows", rows))
	}
	return c.JSON(200, O("sections", out, "text", strings.TrimSpace(b.String())))
}

type infoSection struct {
	title string
	rows  [][2]string
}

func (a *App) siteInfo(ctx context.Context) []infoSection {
	count := func(q string) string {
		n, err := store.Count(ctx, a.store, q)
		if err != nil {
			return "?"
		}
		return strconv.Itoa(n)
	}
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	g := a.general(ctx)
	dbSize, storage, files := fileSize(filepath.Join(a.cfg.DataDir, "cms.sqlite3")), int64(0), 0
	// Best effort: what can't be read isn't counted, so it never errs.
	_ = filepath.WalkDir(filepath.Join(a.cfg.DataDir, "storage"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				storage += info.Size()
				files++
			}
		}
		return nil
	})
	mail := a.mailConfig(ctx)
	mailRow := "not set up"
	if mail.Configured() {
		mailRow = fmt.Sprintf("%s:%d (%s), from %q <%s>", mail.Host, mail.Port, mail.Security, mail.FromName, mail.FromEmail)
	}
	sections := []infoSection{
		{"Libre Publish", [][2]string{
			{"version", a.cfg.Version}, {"go", runtime.Version()}, {"os", runtime.GOOS + "/" + runtime.GOARCH},
			{"up_since", a.startedAt.UTC().Format(time.RFC3339)}, {"cms_url", a.BaseURL()}, {"site_key", strOr(a.cfg.SiteKey, "—")},
			{"trust_proxy", yes(a.cfg.TrustProxy)}, {"delivery_rate_limit", a.limiter.Describe()},
		}},
		{"Site", [][2]string{
			{"title", strOr(str(g["title"]), "—")}, {"site_url", strOr(str(g["site_base_url"]), "not set")},
			{"serving", a.servingMode()}, {"default_locale", a.defaultLocale()}, {"locales", strings.Join(a.siteLocales(ctx), ", ")},
			{"allowed_origins", strconv.Itoa(len(strings.Fields(strings.ReplaceAll(str(g["cors_origins"]), ",", " "))))},
			{"email", mailRow},
		}},
		{"Content", [][2]string{
			{"pages", count(`SELECT count(*) FROM pages WHERE deleted_at IS NULL`) + " (" + count(`SELECT count(*) FROM pages WHERE deleted_at IS NULL AND status = 'published'`) + " live)"},
			{"collections", count(`SELECT count(*) FROM collections`)},
			{"entries", count(`SELECT count(*) FROM collection_entries WHERE deleted_at IS NULL`) + " (" + count(`SELECT count(*) FROM collection_entries WHERE deleted_at IS NULL AND status = 'published'`) + " live)"},
			{"globals", count(`SELECT count(*) FROM globals WHERE deleted_at IS NULL`)},
			{"block_types", count(`SELECT count(*) FROM block_types`)},
			{"assets", count(`SELECT count(*) FROM assets WHERE deleted_at IS NULL`)},
			{"in_review", count(`SELECT count(*) FROM revisions WHERE status = 'open'`)},
			{"in_trash", count(`SELECT (SELECT count(*) FROM pages WHERE deleted_at IS NOT NULL) + (SELECT count(*) FROM collection_entries WHERE deleted_at IS NOT NULL) + (SELECT count(*) FROM globals WHERE deleted_at IS NOT NULL) + (SELECT count(*) FROM assets WHERE deleted_at IS NOT NULL)`)},
			{"redirects", count(`SELECT count(*) FROM redirects`)},
		}},
		{"People and access", [][2]string{
			{"users", count(`SELECT count(*) FROM users`)}, {"roles", count(`SELECT count(*) FROM roles`)},
			{"service_tokens", count(`SELECT count(*) FROM service_tokens`)}, {"webhooks", count(`SELECT count(*) FROM webhooks`)},
			{"admin_sessions", count(`SELECT count(*) FROM admin_sessions`)},
		}},
		{"Storage", [][2]string{
			{"data_dir", a.cfg.DataDir}, {"database", humanBytes(dbSize)}, {"files", fmt.Sprintf("%d (%s)", files, humanBytes(storage))},
		}},
	}
	plugins := [][2]string{}
	for _, p := range a.plugins.List() {
		state := "off"
		if p.Enabled {
			state = "on"
		}
		grants := []string{}
		if p.Granted.ContentRead {
			grants = append(grants, "content:read")
		}
		if p.Granted.Storage {
			grants = append(grants, "storage")
		}
		if p.Granted.Mail {
			grants = append(grants, "mail")
		}
		if p.Granted.MediaWrite {
			grants = append(grants, "media:write")
		}
		if p.Granted.SiteRead {
			grants = append(grants, "site:read")
		}
		for _, h := range p.Granted.HTTP {
			grants = append(grants, "http:"+h)
		}
		plugins = append(plugins, [2]string{p.Key, fmt.Sprintf("%s, %s; grants %s", p.Version, state, strOr(strings.Join(grants, " "), "none"))})
	}
	if len(plugins) == 0 {
		plugins = append(plugins, [2]string{"plugins", "none"})
	}
	sections = append(sections, infoSection{"Plugins", plugins})
	checks := [][2]string{}
	for _, ch := range a.healthChecks(ctx) {
		checks = append(checks, [2]string{fmt.Sprint(ch.Get("key")), fmt.Sprintf("%s: %s", ch.Get("state"), ch.Get("message"))})
	}
	return append(sections, infoSection{"Checks", checks})
}

func fileSize(path string) int64 {
	var total int64
	for _, p := range []string{path, path + "-wal"} {
		if info, err := os.Stat(p); err == nil {
			total += info.Size()
		}
	}
	return total
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
