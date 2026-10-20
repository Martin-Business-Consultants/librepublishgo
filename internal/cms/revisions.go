package cms

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// A Revision is a write to something live from a caller whose role can't
// publish it: held for a person instead of applied (202 pending_review). The
// live content is unchanged until someone with the publish capability
// approves it, which applies it as theirs.
type Revision struct {
	ID          int64
	RecordType  string // Page, CollectionEntry, Global
	RecordID    int64
	RecordLabel string
	Changes     store.JSON
	Fields      []string
	Status      string // open, approved, rejected
	AuthorLabel string
	Note        string
	DecidedAt   store.Time
	CreatedAt   store.Time
}

func (a *App) holdWrite(ctx context.Context, actor *Actor, rec *Content, params map[string]any) (*writeResult, error) {
	rev, ignored, err := a.fileRevision(ctx, actor, rec.TaggableType(), rec.ID, rec.Label(), params, rec.permitted())
	if err != nil {
		return nil, err
	}
	return &writeResult{Record: rec, Held: rev, Ignored: ignored}, nil
}

// fileRevision keeps the write's permitted keys, minus status: the gate
// exists so a write can't publish itself.
func (a *App) fileRevision(ctx context.Context, actor *Actor, recordType string, recordID int64, label string, params map[string]any, permitted []string) (*Revision, []string, error) {
	var open int64
	err := a.store.QueryRowContext(ctx, `SELECT id FROM revisions WHERE record_type = ? AND record_id = ? AND status = 'open' LIMIT 1`, recordType, recordID).Scan(&open)
	if err == nil {
		return nil, nil, &apierr.Error{Status: 409, Code: "conflict", Message: fmt.Sprintf("A proposed change to this is already waiting for review (revision %d).", open),
			Extra: map[string]any{"revision": O("id", open), "review_url": a.reviewURL(open)}}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	changes := map[string]any{}
	fields := []string{}
	ignored := []string{}
	allowed := map[string]bool{}
	for _, k := range permitted {
		allowed[k] = true
	}
	for k, v := range params {
		switch {
		case k == "status" || k == "publish_at" || k == "unpublish_at" || k == "published_at":
			ignored = append(ignored, k)
		case allowed[k]:
			changes[k] = v
			fields = append(fields, k)
		}
	}
	slices.Sort(fields)
	slices.Sort(ignored)
	if len(fields) == 0 {
		return nil, nil, &apierr.Error{Status: 422, Code: "invalid", Message: "Nothing in this write can be proposed for review.", Errors: apierr.Invalid{"base": {"only status changed, which a review can't carry"}}}
	}
	res, err := a.store.ExecContext(ctx, `INSERT INTO revisions (record_type, record_id, record_label, changes, fields, status, author_label, created_at) VALUES (?,?,?,?,?,'open',?,?)`,
		recordType, recordID, label, string(store.MustJSON(changes)), string(store.MustJSON(fields)), actor.Label(), store.Now().DB())
	if err != nil {
		return nil, nil, err
	}
	id, _ := res.LastInsertId()
	a.audit(ctx, actor, "revision.proposed", recordType, recordID, label, map[string]any{"revision": id, "fields": fields})
	return &Revision{ID: id, RecordType: recordType, RecordID: recordID, Fields: fields, Status: "open"}, ignored, nil
}

func (a *App) reviewURL(id int64) string {
	return a.BaseURL() + "/#/revisions/" + strconv.FormatInt(id, 10)
}

// heldJSON is the 202 the CLI and agents read.
func (a *App) heldJSON(rev *Revision, ignored []string) Obj {
	o := O("status", "pending_review", "revision", O("id", rev.ID, "fields", rev.Fields), "review_url", a.reviewURL(rev.ID))
	if len(ignored) > 0 {
		o = o.Set("ignored_fields", ignored)
	}
	return o
}

func (a *App) revisions(ctx context.Context, where string, args ...any) ([]*Revision, error) {
	rows, err := a.store.QueryContext(ctx, `SELECT id, record_type, record_id, record_label, changes, fields, status, author_label, COALESCE(note, ''), decided_at, created_at
		FROM revisions WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Revision
	for rows.Next() {
		r := &Revision{}
		var fields store.JSON
		if err := rows.Scan(&r.ID, &r.RecordType, &r.RecordID, &r.RecordLabel, &r.Changes, &fields, &r.Status, &r.AuthorLabel, &r.Note, &r.DecidedAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		for _, f := range fields.List() {
			r.Fields = append(r.Fields, str(f))
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// revisionCap is the capability a revision's record takes to see ("read")
// or decide ("publish").
func revisionCap(recordType, verb string) string {
	prefix := map[string]string{"Page": "pages", "CollectionEntry": "entries", "Global": "globals"}[recordType]
	if prefix == "" {
		return wildcard
	}
	return prefix + ":" + verb
}

// JSON is the revision as the API answers it: a global's changes with
// their secrets masked, as the global itself shows them.
func (r *Revision) JSON() Obj {
	var changes any = r.Changes
	if r.RecordType == "Global" {
		changes = redact(r.Changes.Map())
	}
	return O("id", r.ID, "record_type", r.RecordType, "record_id", r.RecordID, "record_label", r.RecordLabel, "fields", r.Fields,
		"changes", changes, "status", r.Status, "author", r.AuthorLabel, "note", r.Note, "decided_at", r.DecidedAt, "created_at", r.CreatedAt)
}

// settleRevision approves (applying the change as the reviewer) or rejects.
func (a *App) settleRevision(ctx context.Context, actor *Actor, id int64, approve bool, note string) (*Revision, error) {
	list, err := a.revisions(ctx, "id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apierr.NotFound("Couldn't find revision " + strconv.FormatInt(id, 10) + ".")
	}
	rev := list[0]
	// Rejecting takes what approving does: either decides what goes live.
	if need := revisionCap(rev.RecordType, "publish"); !actor.Can(need) {
		return nil, apierr.Forbidden(need)
	}
	if rev.Status != "open" {
		return nil, apierr.Conflict("That revision was already " + rev.Status + ".")
	}
	status := map[bool]string{true: "approved", false: "rejected"}[approve]
	var reviewer any
	if actor.UserID != 0 {
		reviewer = actor.UserID
	}
	// Claimed before it's applied: of two reviewers deciding at once, only
	// the one whose update finds it still open goes on. (Applying it runs
	// its own transaction, so a failure there puts the claim back.)
	res, err := a.store.ExecContext(ctx, `UPDATE revisions SET status = ?, note = ?, reviewer_id = ?, decided_at = ? WHERE id = ? AND status = 'open'`, status, note, reviewer, store.Now().DB(), id)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, apierr.Conflict("That revision was already decided.")
	}
	if approve {
		if err := a.applyRevision(ctx, actor, rev); err != nil {
			if _, uerr := a.store.ExecContext(context.WithoutCancel(ctx), `UPDATE revisions SET status = 'open', note = NULL, reviewer_id = NULL, decided_at = NULL WHERE id = ? AND status = ?`, id, status); uerr != nil {
				return nil, errors.Join(err, fmt.Errorf("reopening revision %d: %w", id, uerr))
			}
			return nil, err
		}
	}
	a.audit(ctx, actor, "revision."+status, rev.RecordType, rev.RecordID, rev.RecordLabel, map[string]any{"revision": id})
	rev.Status = status
	rev.Note = note
	return rev, nil
}

// applyRevision writes an approved revision's change as the reviewer.
func (a *App) applyRevision(ctx context.Context, actor *Actor, rev *Revision) error {
	{
		changes := rev.Changes.Map()
		switch rev.RecordType {
		case "Page", "CollectionEntry":
			kind := map[string]string{"Page": "page", "CollectionEntry": "entry"}[rev.RecordType]
			rec, err := a.contentByID(ctx, kind, rev.RecordID, false)
			if errors.Is(err, store.ErrNotFound) {
				return apierr.Conflict("The record it changes is gone.")
			} else if err != nil {
				return err
			}
			if !actor.Can(rec.Prefix() + ":publish") {
				return apierr.Forbidden(rec.Prefix() + ":publish")
			}
			if _, err := a.saveContent(ctx, actor, rec, changes); err != nil {
				return err
			}
		case "Global":
			g, err := a.globalByID(ctx, rev.RecordID)
			if errors.Is(err, store.ErrNotFound) {
				return apierr.Conflict("The global it changes is gone.")
			} else if err != nil {
				return err
			}
			if _, err := a.saveGlobal(ctx, actor, g, changes); err != nil {
				return err
			}
		}
	}
	return nil
}
