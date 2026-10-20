package cms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/update"
)

// The update button (Administrators): back up into the data directory,
// put the newest release's binary there (update.Stage, checked against its
// checksums and run once), mark it as being tried, answer, then hand over to
// it (Config.HandOver): requests in flight finish, everything closes, and
// the process becomes the release, which confirms itself once it's serving.
// If it doesn't come up, the next start stays on the version before and the
// dashboard says so (update.Choose).

// keptBackups is how many before-update backups the data directory keeps.
const keptBackups = 3

func (a *App) updateRoutes(h func(string, handlerFunc, ...func(*route))) {
	h("GET /api/tools/update", a.updateStatus, tool("update_status", "Which version this is, the newest release, and whether this install can update itself to it.", query("check", "boolean", "Ask GitHub now rather than use the last answer.")))
	h("POST /api/tools/update", a.applyUpdate, tool("update_cms", "Update this CMS to the newest release: a backup first, then a restart on it (a few seconds offline). Administrators only.", note("It answers before restarting: wait, then get_manifest says the new version.")))
}

// updateStatus is Settings › Updates: this version, the newest release
// (asked of GitHub at most every ten minutes, or now with ?check=1), and
// what can be done about it here.
func (a *App) updateStatus(c *Ctx) error {
	if err := c.needSystem(); err != nil {
		return err
	}
	out := O("running", a.cfg.Version, "release_build", update.Newer("999999.0.0", a.cfg.Version), "can_update", a.cfg.HandOver != nil,
		"in_container", update.InContainer())
	if failed, at := update.Failed(a.cfg.DataDir); failed != "" {
		out = append(out, kv{"failed", O("version", failed, "at", at.UTC().Format(time.RFC3339))})
	}
	r, checkedAt, err := a.latestRelease(c, c.truthy("check"))
	if err != nil {
		out = append(out, kv{"check_error", err.Error()})
	} else {
		newer := update.Newer(r.Version, a.cfg.Version)
		if newer {
			a.newer.Store(r)
		}
		out = append(out, kv{"latest", O("version", r.Version, "url", r.URL, "published_at", r.PublishedAt.UTC().Format(time.RFC3339))}, kv{"newer", newer})
	}
	out = append(out, kv{"checked_at", checkedAt.UTC().Format(time.RFC3339)})
	return c.JSON(200, out)
}

// latestRelease is the newest release, asked again when the last answer is
// over ten minutes old (or now, when fresh).
func (a *App) latestRelease(c *Ctx, fresh bool) (*update.Release, time.Time, error) {
	a.releaseMu.Lock()
	defer a.releaseMu.Unlock()
	if !fresh && a.release != nil && time.Since(a.releaseAt) < 10*time.Minute {
		return a.release, a.releaseAt, nil
	}
	r, err := update.Latest(c.R.Context())
	if err != nil {
		return nil, time.Now(), err
	}
	a.release, a.releaseAt = &r, time.Now()
	return a.release, a.releaseAt, nil
}

func (a *App) applyUpdate(c *Ctx) error {
	if err := c.Need("tools:use"); err != nil {
		return err
	}
	if c.Actor.Role == nil || !c.Actor.Role.System {
		return &apierr.Error{Status: 403, Code: "forbidden", Message: "Only the Administrator role can update the CMS.", Capability: wildcard}
	}
	if a.cfg.HandOver == nil {
		return &apierr.Error{Status: 501, Code: "not_supported", Message: "This install can't update itself: run `librepublish update`, or deploy the new version."}
	}
	ctx := c.R.Context()
	r := a.newer.Load()
	if r == nil {
		latest, err := update.Latest(ctx)
		if err != nil {
			return &apierr.Error{Status: 502, Code: "unavailable", Message: "Couldn't ask GitHub for the latest release: " + err.Error()}
		}
		r = &latest
	}
	if !update.Newer(r.Version, a.cfg.Version) {
		return apierr.Conflict(fmt.Sprintf("This is %s, and the latest release is %s: nothing to update to.", a.cfg.Version, r.Version))
	}

	backup, err := a.backupBeforeUpdate(c, r.Version)
	if err != nil {
		return fmt.Errorf("backing up before the update: %w", err)
	}
	bin, err := update.Stage(ctx, *r, a.cfg.DataDir)
	if err != nil {
		return &apierr.Error{Status: 502, Code: "unavailable", Message: "Couldn't get " + r.Version + " ready, so nothing changed: " + err.Error()}
	}
	if err := update.Try(a.cfg.DataDir, r.Version); err != nil {
		return err
	}
	a.audit(ctx, c.Actor, "cms.updated", "Site", 0, r.Version, map[string]any{"from": a.cfg.Version, "to": r.Version, "backup": backup})
	a.log.Info("updating", "from", a.cfg.Version, "to", r.Version, "backup", backup)
	// After this answer is on its way: the hand-over closes the server.
	// Tracked, so Close waits for it; HandOver only signals, so it can't
	// wait on Close in turn.
	a.spawn(ctx, "hand over", func(ctx context.Context) {
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
			a.cfg.HandOver(bin)
		}
	})
	c.Summary = "Updating to " + r.Version + "."
	return c.JSON(202, O("updating_to", r.Version, "from", a.cfg.Version, "backup", backup))
}

// backupBeforeUpdate writes a backup (secret key and all: it stays in the
// site's own data directory) to DATA_DIR/backups/, keeping the newest few.
func (a *App) backupBeforeUpdate(c *Ctx, to string) (string, error) {
	dir := filepath.Join(a.cfg.DataDir, "backups")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	set, err := a.prepareBackup(c.R.Context(), true)
	if err != nil {
		return "", err
	}
	defer set.close()
	name := fmt.Sprintf("before-%s-%s.tar.gz", to, time.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // the site's own data directory
	if err != nil {
		return "", err
	}
	if err := a.writeBackup(f, set); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	// The oldest go, past the newest few.
	if entries, err := os.ReadDir(dir); err == nil {
		var before []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "before-") && strings.HasSuffix(e.Name(), ".tar.gz") {
				before = append(before, e.Name())
			}
		}
		slices.SortFunc(before, func(x, y string) int { return strings.Compare(stamp(x), stamp(y)) })
		for len(before) > keptBackups {
			os.Remove(filepath.Join(dir, before[0]))
			before = before[1:]
		}
	}
	return filepath.Join("backups", name), nil
}

// stamp is a before-update backup's time, from its name.
func stamp(name string) string {
	name = strings.TrimSuffix(name, ".tar.gz")
	if i := strings.LastIndex(name, "-"); i > 0 {
		if j := strings.LastIndex(name[:i], "-"); j > 0 {
			return name[j+1:]
		}
	}
	return name
}
