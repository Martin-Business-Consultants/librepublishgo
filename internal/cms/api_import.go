package cms

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/restore"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// Importing a backup from another install (docs/import.md): upload it,
// read what staging it found (nothing has changed yet), then apply, which
// replaces this whole site, people and all, without a restart. Only the
// system role may: it hands the site to whoever's in the backup.

const defaultImportMaxBytes = 10 << 30

func (a *App) importRoutes(h func(string, handlerFunc, ...func(*route))) {
	h("POST /api/tools/import", a.stageImport)
	h("GET /api/tools/import/{id}", a.showImport)
	h("POST /api/tools/import/{id}/apply", a.applyImport)
	h("DELETE /api/tools/import/{id}", a.discardImport)
}

// needSystem refuses anyone without tools:use and the system role.
func (c *Ctx) needSystem() error {
	if err := c.Need("tools:use"); err != nil {
		return err
	}
	if c.Actor.Role == nil || !c.Actor.Role.System {
		return &apierr.Error{Status: 403, Code: "forbidden", Message: "Only the Administrator role can import a backup: it replaces the whole site.", Capability: wildcard}
	}
	return nil
}

func (a *App) restoreOptions(c *Ctx) restore.Options {
	return restore.Options{DataDir: a.cfg.DataDir, SecretKey: a.cfg.SecretKey, Actor: c.Actor.Label()}
}

// stageImport takes the backup as the request body (a .tar.gz, streamed
// to staging, never held in memory) and answers what staging found.
func (a *App) stageImport(c *Ctx) error {
	if err := c.needSystem(); err != nil {
		return err
	}
	limit := a.cfg.ImportMaxBytes
	if limit <= 0 {
		limit = defaultImportMaxBytes
	}
	if c.R.ContentLength > limit {
		return apierr.TooLarge(limit)
	}
	// A backup takes longer to send and unpack than the server's timeouts
	// allow a request.
	rc := http.NewResponseController(c.W)
	_ = rc.SetReadDeadline(time.Now().Add(6 * time.Hour))
	_ = rc.SetWriteDeadline(time.Now().Add(6 * time.Hour))
	label := filepath.Base(strings.TrimSpace(c.Query("filename")))
	if label == "." || label == "/" || label == "" {
		label = "upload"
	}
	opts := a.restoreOptions(c)
	opts.Limits = restore.Limits{MaxTotal: 4 * limit}
	imp, err := restore.StageArchive(c.R.Context(), http.MaxBytesReader(c.W, c.R.Body, limit), label, opts)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return apierr.TooLarge(limit)
		}
		return importError(err)
	}
	a.audit(c.R.Context(), c.Actor, "backup.import_staged", "Site", 0, label, map[string]any{"import": imp.ID, "format": imp.Report.Format})
	c.Summary = "Staged: nothing has changed yet. Apply it to replace this site."
	return c.JSON(201, O("import", imp.Report))
}

func (a *App) showImport(c *Ctx) error {
	if err := c.needSystem(); err != nil {
		return err
	}
	imp, err := restore.Load(a.cfg.DataDir, c.Param("id"))
	if err != nil {
		return importError(err)
	}
	return c.JSON(200, O("import", imp.Report))
}

func (a *App) discardImport(c *Ctx) error {
	if err := c.needSystem(); err != nil {
		return err
	}
	if err := restore.Discard(a.cfg.DataDir, c.Param("id")); err != nil {
		return importError(err)
	}
	return c.NoContent()
}

// applyImport replaces the site with the staged one. Everyone is signed
// out (the sessions were the old site's), the caller included.
func (a *App) applyImport(c *Ctx) error {
	if err := c.needSystem(); err != nil {
		return err
	}
	var body struct {
		Force     bool   `json:"force"`
		SecretKey string `json:"secret_key"`
	}
	if err := c.OptionalBody(&body); err != nil {
		return err
	}
	imp, err := restore.Load(a.cfg.DataDir, c.Param("id"))
	if err != nil {
		return importError(err)
	}
	if a.server == nil {
		return apierr.Conflict("This install isn't run by `librepublish serve`, so it can't replace itself: import with `librepublish import` instead.")
	}
	opts := a.restoreOptions(c)
	opts.Force, opts.SourceKey = body.Force, strings.TrimSpace(body.SecretKey)
	// Checked before anything stops: a missing force or a wrong key
	// shouldn't cost the site its requests.
	ctx := context.WithoutCancel(c.R.Context())
	users, err := store.Count(ctx, a.store, `SELECT count(*) FROM users`)
	if err != nil {
		return err
	}
	if users > 0 && !opts.Force {
		return importError(restore.ErrHasUsers)
	}
	if err := imp.CheckKey(ctx, opts); err != nil {
		return importError(err)
	}
	a.audit(ctx, c.Actor, "backup.import_applied", "Site", 0, imp.Report.Source, map[string]any{"import": imp.ID, "format": imp.Report.Format})
	var rep *restore.Report
	err = a.server.Replace(ctx, func() (func() error, error) {
		var err error
		if rep, err = imp.Apply(ctx, opts); err != nil {
			return nil, err
		}
		return func() error { return restore.Rollback(opts.DataDir, rep.ReplacedDir) }, nil
	})
	if err != nil {
		return importError(err)
	}
	a.setSessionCookie(c, "", -1)
	c.Summary = "Imported. Everyone was signed out: sign in with an account from the imported site."
	return c.JSON(200, O("import", rep, "signed_out", true))
}

// importError is err as the API answers it.
func importError(err error) error {
	switch {
	case errors.Is(err, restore.ErrNotFound):
		return apierr.NotFound("No such import: it was applied, discarded, or swept after a day.")
	case errors.Is(err, restore.ErrTooLarge):
		return &apierr.Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: err.Error()}
	case errors.Is(err, restore.ErrInvalid):
		return &apierr.Error{Status: 422, Code: "invalid_backup", Message: err.Error()}
	case errors.Is(err, restore.ErrWrongKey):
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"secret_key": {"doesn't open the backup's sealed values: it's the old install's secret.key, or its SECRET_KEY"}}}
	case errors.Is(err, restore.ErrHasUsers):
		return &apierr.Error{Status: 409, Code: "has_users", Message: "This site has people and content: importing replaces all of it. Send force to go ahead."}
	case errors.Is(err, errBusy):
		return &apierr.Error{Status: 503, Code: "busy", Message: err.Error()}
	}
	return err
}
