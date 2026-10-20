package cms

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// Redirect sends requests for one path (or, wildcard, a prefix) elsewhere.
type Redirect struct {
	ID                   int64
	Source, Destination  string
	Status               int
	Active, Wildcard     bool
	Hits                 int
	LastHitAt            store.Time
	Notes                string
	CreatedAt, UpdatedAt store.Time
}

// JSON is the redirect as the API answers it.
func (r *Redirect) JSON() Obj {
	return O("id", r.ID, "source_path", r.Source, "destination_url", r.Destination, "status_code", r.Status, "active", r.Active,
		"wildcard", r.Wildcard, "hit_count", r.Hits, "last_hit_at", r.LastHitAt, "notes", r.Notes, "created_at", r.CreatedAt, "updated_at", r.UpdatedAt)
}

func (a *App) redirects(ctx context.Context, where string, args ...any) ([]*Redirect, error) {
	// Not while hits are being written: they'd be neither pending nor read.
	a.hits.flushing.RLock()
	defer a.hits.flushing.RUnlock()
	rows, err := a.store.QueryContext(ctx, `SELECT id, source_path, destination_url, status_code, active, wildcard, hit_count, last_hit_at, COALESCE(notes, ''), created_at, updated_at
		FROM redirects WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Redirect{}
	for rows.Next() {
		r := &Redirect{}
		if err := rows.Scan(&r.ID, &r.Source, &r.Destination, &r.Status, &r.Active, &r.Wildcard, &r.Hits, &r.LastHitAt, &r.Notes, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	a.hits.apply(out)
	return out, rows.Err()
}

// edgeRedirects is the build's shape: only what applying a rule needs.
func (a *App) edgeRedirects(c *Ctx) error {
	if err := c.Need("redirects:read"); err != nil {
		return err
	}
	list, err := a.redirects(c.R.Context(), "active = 1 ORDER BY wildcard, length(source_path) DESC, id")
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, r := range list {
		out = append(out, O("source", r.Source, "destination", r.Destination, "status", r.Status, "wildcard", r.Wildcard))
	}
	return c.JSON(200, O("redirects", out))
}

func (a *App) allRedirects(c *Ctx) error {
	if err := c.Need("redirects:read"); err != nil {
		return err
	}
	list, err := a.redirects(c.R.Context(), "1=1 ORDER BY id")
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, r := range list {
		out = append(out, r.JSON())
	}
	c.Summary = fmt.Sprintf("%d redirect %s.", len(out), plural(len(out), "rule"))
	return c.JSON(200, O("redirects", out))
}

// matchRedirect finds the rule for path: exact first, then the longest
// wildcard ("/blog/*").
func (a *App) matchRedirect(ctx context.Context, path string) (*Redirect, string) {
	list, err := a.redirects(ctx, "active = 1 ORDER BY wildcard, length(source_path) DESC, id")
	if err != nil {
		return nil, ""
	}
	path = "/" + strings.Trim(path, "/")
	for _, r := range list {
		src := "/" + strings.Trim(strings.TrimSuffix(r.Source, "*"), "/")
		if !r.Wildcard && src == path {
			return r, r.Destination
		}
		if r.Wildcard && (path == src || strings.HasPrefix(path, strings.TrimSuffix(src, "/")+"/")) {
			splat := strings.TrimPrefix(strings.TrimPrefix(path, strings.TrimSuffix(src, "/")), "/")
			return r, strings.ReplaceAll(strings.ReplaceAll(r.Destination, ":splat", splat), "*", splat)
		}
	}
	return nil, ""
}

func (a *App) resolveRedirect(c *Ctx) error {
	if err := c.Need("redirects:read"); err != nil {
		return err
	}
	r, dest := a.matchRedirect(c.R.Context(), c.Query("path"))
	if r == nil {
		return apierr.NotFound("No redirect for " + c.Query("path") + ".")
	}
	// Counted in memory and written every few seconds (flushHits): a public
	// hit shouldn't wait on a write.
	a.hits.add(r.ID)
	return c.JSON(200, O("source", r.Source, "destination", dest, "status", r.Status))
}

// redirectHits are hits not yet written: per redirect, how many and the
// latest.
type redirectHits struct {
	mu      sync.Mutex
	pending map[int64]redirectHit
	// flushing is held while a batch is written, and read while rules are.
	flushing sync.RWMutex
}

type redirectHit struct {
	n  int
	at store.Time
}

func (h *redirectHits) add(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending == nil {
		h.pending = map[int64]redirectHit{}
	}
	p := h.pending[id]
	h.pending[id] = redirectHit{n: p.n + 1, at: store.Now()}
}

// apply adds the hits not yet written to rules read from the database, so
// a listing's counts are current.
func (h *redirectHits) apply(list []*Redirect) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range list {
		if p, ok := h.pending[r.ID]; ok {
			r.Hits += p.n
			if !r.LastHitAt.Valid || p.at.After(r.LastHitAt.Time) {
				r.LastHitAt = p.at
			}
		}
	}
}

// hitFlushEvery is how often counted hits are written.
const hitFlushEvery = 10 * time.Second

// countHits writes counted hits every hitFlushEvery; Close writes the rest.
func (a *App) countHits(ctx context.Context) {
	tick := time.NewTicker(hitFlushEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			a.flushHits(ctx)
		}
	}
}

// flushHits writes the hits counted since the last time, in one
// transaction. What fails to write is put back for the next.
func (a *App) flushHits(ctx context.Context) {
	a.hits.flushing.Lock()
	defer a.hits.flushing.Unlock()
	a.hits.mu.Lock()
	batch := a.hits.pending
	a.hits.pending = nil
	a.hits.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	err := a.store.Tx(ctx, func(tx *sql.Tx) error {
		for id, p := range batch {
			if _, err := tx.ExecContext(ctx, `UPDATE redirects SET hit_count = hit_count + ?, last_hit_at = ? WHERE id = ?`, p.n, p.at.DB(), id); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return
	}
	// The hit count is a nicety: a failure is logged and tried again.
	a.log.Warn("redirect hit count", "err", err)
	a.hits.mu.Lock()
	defer a.hits.mu.Unlock()
	if a.hits.pending == nil {
		a.hits.pending = map[int64]redirectHit{}
	}
	for id, p := range batch {
		q := a.hits.pending[id]
		q.n += p.n
		if !q.at.Valid || p.at.After(q.at.Time) {
			q.at = p.at
		}
		a.hits.pending[id] = q
	}
}

func (a *App) saveRedirect(c *Ctx, r *Redirect, p map[string]any) error {
	creating := r.ID == 0
	if creating {
		r.Status, r.Active = 301, true
	}
	if has(p, "source_path") {
		r.Source = strings.TrimSpace(str(p["source_path"]))
	}
	if has(p, "destination_url") {
		r.Destination = strings.TrimSpace(str(p["destination_url"]))
	}
	if v, ok := intOf(p["status_code"]); ok {
		r.Status = int(v)
	}
	if has(p, "active") {
		r.Active = boolOf(p["active"])
	}
	if has(p, "notes") {
		r.Notes = str(p["notes"])
	}
	r.Wildcard = strings.HasSuffix(r.Source, "*")
	inv := apierr.Invalid{}
	if !strings.HasPrefix(r.Source, "/") {
		inv.Add("source_path", "must start with /")
	}
	if r.Destination == "" {
		inv.Add("destination_url", "can't be blank")
	}
	if !slices.Contains([]int{301, 302, 307, 308, 410}, r.Status) {
		inv.Add("status_code", "must be 301, 302, 307, 308 or 410")
	}
	if strings.TrimRight(r.Source, "/") == strings.TrimRight(r.Destination, "/") {
		inv.Add("destination_url", "can't be the same as the source")
	}
	clash, err := store.Count(c.R.Context(), a.store.DB, `SELECT count(*) FROM redirects WHERE source_path = ? AND id != ?`, r.Source, r.ID)
	if err != nil {
		return err
	}
	if clash > 0 {
		inv.Add("source_path", "has already been taken")
	}
	if err := inv.Err(); err != nil {
		return err
	}
	now := store.Now()
	if creating {
		res, err := a.store.ExecContext(c.R.Context(), `INSERT INTO redirects (source_path, destination_url, status_code, active, wildcard, notes, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
			r.Source, r.Destination, r.Status, r.Active, r.Wildcard, r.Notes, now.DB(), now.DB())
		if err != nil {
			return err
		}
		r.ID, _ = res.LastInsertId()
		r.CreatedAt = now
	} else if _, err := a.store.ExecContext(c.R.Context(), `UPDATE redirects SET source_path=?, destination_url=?, status_code=?, active=?, wildcard=?, notes=?, updated_at=? WHERE id=?`,
		r.Source, r.Destination, r.Status, r.Active, r.Wildcard, r.Notes, now.DB(), r.ID); err != nil {
		return err
	}
	r.UpdatedAt = now
	a.audit(c.R.Context(), c.Actor, map[bool]string{true: "redirect.created", false: "redirect.updated"}[creating], "Redirect", r.ID, r.Source+" → "+r.Destination, nil)
	a.publish(c.R.Context(), change{Tags: []string{"redirects"}})
	return nil
}

func (a *App) createRedirect(c *Ctx) error {
	if err := c.Need("redirects:write"); err != nil {
		return err
	}
	p, err := c.Wrapped("redirect")
	if err != nil {
		return err
	}
	r := &Redirect{}
	if err := a.saveRedirect(c, r, p); err != nil {
		return err
	}
	c.Summary = fmt.Sprintf("%s → %s (%d).", r.Source, r.Destination, r.Status)
	return c.JSON(201, O("redirect", r.JSON()))
}

func (a *App) redirectByID(c *Ctx) (*Redirect, error) {
	id, err := c.IntParam("id")
	if err != nil {
		return nil, err
	}
	list, err := a.redirects(c.R.Context(), "id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apierr.NotFound("Couldn't find redirect " + c.Param("id") + ".")
	}
	return list[0], nil
}

func (a *App) updateRedirect(c *Ctx) error {
	if err := c.Need("redirects:write"); err != nil {
		return err
	}
	r, err := a.redirectByID(c)
	if err != nil {
		return err
	}
	p, err := c.Wrapped("redirect")
	if err != nil {
		return err
	}
	if err := a.saveRedirect(c, r, p); err != nil {
		return err
	}
	return c.JSON(200, O("redirect", r.JSON()))
}

func (a *App) destroyRedirect(c *Ctx) error {
	if err := c.Need("redirects:delete"); err != nil {
		return err
	}
	r, err := a.redirectByID(c)
	if err != nil {
		return err
	}
	if _, err := a.store.ExecContext(c.R.Context(), `DELETE FROM redirects WHERE id = ?`, r.ID); err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "redirect.deleted", "Redirect", r.ID, r.Source, nil)
	a.publish(c.R.Context(), change{Tags: []string{"redirects"}})
	return c.NoContent()
}

func (a *App) exportRedirects(c *Ctx) error {
	if err := c.Need("redirects:read"); err != nil {
		return err
	}
	list, err := a.redirects(c.R.Context(), "1=1 ORDER BY id")
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.W.Header().Set("Content-Disposition", `attachment; filename="redirects.csv"`)
	w := csv.NewWriter(c.W)
	// A csv.Writer keeps its first error; Error reports it after Flush.
	_ = w.Write([]string{"source_path", "destination_url", "status_code", "active", "notes"})
	for _, r := range list {
		_ = w.Write([]string{r.Source, r.Destination, strconv.Itoa(r.Status), strconv.FormatBool(r.Active), r.Notes})
	}
	w.Flush()
	return w.Error()
}

func (a *App) importRedirects(c *Ctx) error {
	if err := c.Need("redirects:write"); err != nil {
		return err
	}
	var src io.Reader = c.R.Body
	if strings.HasPrefix(c.R.Header.Get("Content-Type"), "multipart/") {
		f, _, err := c.R.FormFile("file")
		if err != nil {
			return apierr.BadRequest("Upload the CSV as \"file\".")
		}
		defer f.Close()
		src = f
	}
	rows, err := csv.NewReader(src).ReadAll()
	if err != nil {
		return apierr.BadRequest("That isn't CSV: " + err.Error())
	}
	created, skipped, problems := 0, 0, []Obj{}
	for i, row := range rows {
		if len(row) < 2 || (i == 0 && row[0] == "source_path") {
			continue
		}
		p := map[string]any{"source_path": row[0], "destination_url": row[1]}
		if len(row) > 2 && row[2] != "" {
			p["status_code"] = row[2]
		}
		if len(row) > 4 {
			p["notes"] = row[4]
		}
		if err := a.saveRedirect(c, &Redirect{}, p); err != nil {
			if e, ok := errors.AsType[*apierr.Error](err); ok && e.Status == 422 {
				skipped++
				problems = append(problems, O("row", i+1, "errors", e.Errors))
				continue
			}
			return err
		}
		created++
	}
	return c.JSON(200, O("ok", true, "created", created, "skipped", skipped, "errors", problems))
}
