package cms

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// ---- editing together ----------------------------------------------------

// An editor open in the admin holds a lock on its record, refreshed by a
// heartbeat; one nobody has refreshed for lockTTL is free. Someone else who
// opens the record is told who has it and may take it over (the first
// person is told on their next heartbeat, and keeps what they typed as an
// autosave). A write through the API to a record someone else holds is
// refused with 409 unless it says override_lock=1, so agents and scripts
// don't overwrite a person mid-edit.

const lockTTL = 2 * time.Minute

var lockTypes = map[string]string{"page": "pages:write", "entry": "entries:write", "global": "globals:write"}

type editLock struct {
	UserID     int64
	UserName   string
	SessionID  int64
	AcquiredAt store.Time
	Heartbeat  store.Time
}

func (l *editLock) fresh() bool { return l != nil && time.Since(l.Heartbeat.Time) < lockTTL }

// JSON is the lock as actor sees it: mine when actor holds it.
func (l *editLock) JSON(actor *Actor) Obj {
	mine := actor != nil && actor.UserID == l.UserID && (actor.Session == nil || l.SessionID == 0 || actor.Session.ID == l.SessionID)
	return O("user_id", l.UserID, "user_name", l.UserName, "since", l.AcquiredAt, "heartbeat_at", l.Heartbeat, "mine", mine)
}

func (a *App) lockOn(ctx context.Context, typ string, id int64) *editLock {
	l := &editLock{}
	var sid store.NullInt
	err := a.store.QueryRowContext(ctx, `SELECT user_id, user_name, session_id, acquired_at, heartbeat_at FROM edit_locks WHERE record_type = ? AND record_id = ?`, typ, id).
		Scan(&l.UserID, &l.UserName, &sid, &l.AcquiredAt, &l.Heartbeat)
	if err != nil {
		return nil
	}
	l.SessionID = sid.Int64
	if !l.fresh() {
		return nil
	}
	return l
}

// heldByOther is the fresh lock on a record held by someone other than the
// actor (another person, or the same person in another browser), or nil.
func (a *App) heldByOther(ctx context.Context, actor *Actor, typ string, id int64) *editLock {
	l := a.lockOn(ctx, typ, id)
	if l == nil {
		return nil
	}
	if actor != nil && actor.UserID == l.UserID && (actor.Session == nil || l.SessionID == 0 || actor.Session.ID == l.SessionID) {
		return nil
	}
	return l
}

// checkLock refuses a write to a record someone else is editing, unless the
// caller asks to override it.
func (a *App) checkLock(c *Ctx, typ string, id int64) error {
	if c.truthy("override_lock") {
		return nil
	}
	l := a.heldByOther(c.R.Context(), c.Actor, typ, id)
	if l == nil {
		return nil
	}
	return &apierr.Error{Status: 409, Code: "locked", Message: l.UserName + " is editing this in the admin right now. Try again later, or send override_lock=1 to save anyway.",
		Extra: map[string]any{"lock": l.JSON(c.Actor)}}
}

// lockedBy is {record id: name} of a type's records locked by someone other
// than the actor, for lists.
func (a *App) lockedBy(ctx context.Context, actor *Actor, typ string) (map[int64]string, error) {
	out := map[int64]string{}
	rows, err := a.store.QueryContext(ctx, `SELECT record_id, user_id, user_name, session_id, heartbeat_at FROM edit_locks WHERE record_type = ?`, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, uid int64
		var name string
		var sid store.NullInt
		var hb store.Time
		if err := rows.Scan(&id, &uid, &name, &sid, &hb); err != nil {
			return nil, err
		}
		l := &editLock{UserID: uid, SessionID: sid.Int64, Heartbeat: hb}
		if !l.fresh() || (actor != nil && actor.UserID == uid && (actor.Session == nil || sid.Int64 == 0 || actor.Session.ID == sid.Int64)) {
			continue
		}
		out[id] = name
	}
	return out, rows.Err()
}

// acquireLock takes (or refreshes) the lock on a record:
// POST /api/locks {"type", "id", "take_over"}. It answers the lock when it's
// the caller's, 409 with the holder's when it isn't (lost: someone took it
// over since the caller's last heartbeat).
func (a *App) acquireLock(c *Ctx) error {
	var body struct {
		Type     string `json:"type"`
		ID       int64  `json:"id"`
		TakeOver bool   `json:"take_over"`
		Held     bool   `json:"held"`
	}
	if err := c.Body(&body); err != nil {
		return err
	}
	need, ok := lockTypes[body.Type]
	if !ok || body.ID == 0 {
		return apierr.BadRequest(`Send {"type": "page" | "entry" | "global", "id": …}.`)
	}
	if err := c.Need(need); err != nil {
		return err
	}
	if c.Actor.UserID == 0 {
		return apierr.BadRequest("Locks are for people editing in the admin.")
	}
	ctx := c.R.Context()
	if other := a.heldByOther(ctx, c.Actor, body.Type, body.ID); other != nil && !body.TakeOver {
		code := "locked"
		if body.Held {
			code = "lock_lost"
		}
		return &apierr.Error{Status: 409, Code: code, Message: other.UserName + " is editing this.", Extra: map[string]any{"lock": other.JSON(c.Actor)}}
	}
	now := store.Now()
	sid := sessionID(c)
	prior := a.lockOn(ctx, body.Type, body.ID)
	_, err := a.store.ExecContext(ctx, `INSERT INTO edit_locks (record_type, record_id, user_id, user_name, session_id, acquired_at, heartbeat_at) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(record_type, record_id) DO UPDATE SET heartbeat_at = excluded.heartbeat_at,
		  acquired_at = CASE WHEN edit_locks.user_id = excluded.user_id AND edit_locks.session_id IS excluded.session_id THEN edit_locks.acquired_at ELSE excluded.acquired_at END,
		  user_id = excluded.user_id, user_name = excluded.user_name, session_id = excluded.session_id`,
		body.Type, body.ID, c.Actor.UserID, c.Actor.Name, nullable(sid), now.DB(), now.DB())
	if err != nil {
		return err
	}
	if body.TakeOver && prior != nil && prior.UserID != c.Actor.UserID {
		a.audit(ctx, c.Actor, "lock.taken_over", body.Type, body.ID, "", map[string]any{"from": prior.UserName})
	}
	return c.JSON(200, O("lock", a.lockOn(ctx, body.Type, body.ID).JSON(c.Actor)))
}

func (a *App) releaseLock(c *Ctx) error {
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	if _, err := a.store.ExecContext(c.R.Context(), `DELETE FROM edit_locks WHERE record_type = ? AND record_id = ? AND user_id = ? AND session_id IS ?`,
		c.Param("type"), id, c.Actor.UserID, nullable(sessionID(c))); err != nil {
		return err
	}
	return c.NoContent()
}

func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// ---- autosaves -----------------------------------------------------------

const maxAutosave = 4 << 20

func autosaveParams(c *Ctx) (typ, key string, err error) {
	typ, key = c.Query("type"), c.Query("key")
	if _, ok := lockTypes[typ]; !ok || key == "" {
		return "", "", apierr.BadRequest("Name the record: ?type=page|entry|global&key=<its id, or new:<where>>.")
	}
	if c.Actor.UserID == 0 {
		return "", "", apierr.BadRequest("Autosaves are for people editing in the admin.")
	}
	return typ, key, nil
}

// showAutosave is the caller's autosave of a record, if any.
func (a *App) showAutosave(c *Ctx) error {
	typ, key, err := autosaveParams(c)
	if err != nil {
		return err
	}
	var data store.JSON
	var updated store.Time
	err = a.store.QueryRowContext(c.R.Context(), `SELECT data, updated_at FROM autosaves WHERE record_type = ? AND record_key = ? AND user_id = ?`, typ, key, c.Actor.UserID).Scan(&data, &updated)
	if err != nil {
		return c.JSON(200, O("autosave", nil))
	}
	return c.JSON(200, O("autosave", O("data", json.RawMessage(data), "updated_at", updated)))
}

func (a *App) putAutosave(c *Ctx) error {
	typ, key, err := autosaveParams(c)
	if err != nil {
		return err
	}
	if err := c.Need(lockTypes[typ]); err != nil {
		return err
	}
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	if err := c.Body(&body); err != nil {
		return err
	}
	if len(body.Data) == 0 || len(body.Data) > maxAutosave {
		return apierr.BadRequest("Send {\"data\": …} (at most 4 MB).")
	}
	now := store.Now()
	_, err = a.store.ExecContext(c.R.Context(), `INSERT INTO autosaves (record_type, record_key, user_id, data, created_at, updated_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(record_type, record_key, user_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		typ, key, c.Actor.UserID, string(body.Data), now.DB(), now.DB())
	if err != nil {
		return err
	}
	return c.JSON(200, O("ok", true, "updated_at", now))
}

func (a *App) deleteAutosave(c *Ctx) error {
	typ, key, err := autosaveParams(c)
	if err != nil {
		return err
	}
	if _, err := a.store.ExecContext(c.R.Context(), `DELETE FROM autosaves WHERE record_type = ? AND record_key = ? AND user_id = ?`, typ, key, c.Actor.UserID); err != nil {
		return err
	}
	return c.NoContent()
}

// ---- preferences ---------------------------------------------------------

func (a *App) preferences(ctx context.Context, userID int64) map[string]any {
	var data store.JSON
	if err := a.store.QueryRowContext(ctx, `SELECT data FROM user_preferences WHERE user_id = ?`, userID).Scan(&data); err != nil {
		return map[string]any{}
	}
	return data.Map()
}

func (a *App) showPreferences(c *Ctx) error {
	if c.Actor.UserID == 0 {
		return c.JSON(200, O("preferences", map[string]any{}))
	}
	return c.JSON(200, O("preferences", a.preferences(c.R.Context(), c.Actor.UserID)))
}

// updatePreferences merges keys into the caller's preferences (null
// removes one).
func (a *App) updatePreferences(c *Ctx) error {
	if c.Actor.UserID == 0 {
		return apierr.BadRequest("Preferences are a person's.")
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	if p, ok := body["preferences"].(map[string]any); ok {
		body = p
	}
	ctx := c.R.Context()
	prefs := a.preferences(ctx, c.Actor.UserID)
	for k, v := range body {
		if v == nil {
			delete(prefs, k)
		} else {
			prefs[k] = v
		}
	}
	raw := store.MustJSON(prefs)
	if len(raw) > 256<<10 {
		return apierr.BadRequest("Preferences are at most 256 KB.")
	}
	_, err := a.store.ExecContext(ctx, `INSERT INTO user_preferences (user_id, data, updated_at) VALUES (?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, c.Actor.UserID, string(raw), store.Now().DB())
	if err != nil {
		return err
	}
	return c.JSON(200, O("preferences", prefs))
}
