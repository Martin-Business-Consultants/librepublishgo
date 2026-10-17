package cms

import (
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func (a *App) userJSON(ctx context.Context, id int64) (Obj, error) {
	var name, email string
	var roleID store.NullInt
	var created store.Time
	var totp bool
	if err := a.store.QueryRowContext(ctx, `SELECT name, email, role_id, totp_enabled, created_at FROM users WHERE id = ?`, id).Scan(&name, &email, &roleID, &totp, &created); err != nil {
		return nil, store.NotFound(err)
	}
	var role any
	if roleID.Valid {
		if r, err := a.role(ctx, roleID.Int64); err == nil {
			role = O("id", r.ID, "name", r.Name)
		}
	}
	return O("id", id, "name", name, "email", email, "pending_email", a.pendingEmail(ctx, id), "role", role, "two_factor", totp, "created_at", created), nil
}

func (a *App) listUsers(c *Ctx) error {
	if err := c.Need("users:read"); err != nil {
		return err
	}
	ids, err := store.Int64s(c.R.Context(), a.store.DB, `SELECT id FROM users ORDER BY name`)
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, id := range ids {
		o, err := a.userJSON(c.R.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return err
		}
		out = append(out, o)
	}
	return c.JSON(200, O("users", out))
}

// addUser makes a person who can sign in.
func (a *App) addUser(ctx context.Context, actor *Actor, name, email, password string, roleID int64) (int64, error) {
	id, err := a.insertUser(ctx, a.store.DB, name, email, password, roleID)
	if err != nil {
		return 0, err
	}
	a.audit(ctx, actor, "user.created", "User", id, name, nil)
	return id, nil
}

// insertUser checks and inserts a user through db (a transaction's, or not).
func (a *App) insertUser(ctx context.Context, db store.Querier, name, email, password string, roleID int64) (int64, error) {
	inv := apierr.Invalid{}
	email = strings.ToLower(strings.TrimSpace(email))
	if strings.TrimSpace(name) == "" {
		inv.Add("name", "can't be blank")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		inv.Add("email", "is invalid")
	}
	if len(password) < 10 {
		inv.Add("password", "is too short (minimum is 10 characters)")
	}
	var clash int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE lower(email) = ?`, email).Scan(&clash); err != nil {
		return 0, err
	}
	if clash > 0 {
		inv.Add("email", "has already been taken")
	}
	if err := inv.Err(); err != nil {
		return 0, err
	}
	hash, err := secret.HashPassword(password)
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `INSERT INTO users (name, email, password_digest, role_id, verified, created_at, updated_at) VALUES (?,?,?,?,1,?,?)`,
		strings.TrimSpace(name), email, hash, roleID, store.Now().DB(), store.Now().DB())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (a *App) createUser(c *Ctx) error {
	if err := c.Need("users:write"); err != nil {
		return err
	}
	p, err := c.Wrapped("user")
	if err != nil {
		return err
	}
	roleID, _ := intOf(p["role_id"])
	r, err := a.role(c.R.Context(), roleID)
	if err != nil {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"role_id": {"isn't a role"}}}
	}
	if missing := c.lacks(r); missing != "" {
		return apierr.Forbidden(missing)
	}
	id, err := a.addUser(c.R.Context(), c.Actor, str(p["name"]), str(p["email"]), str(p["password"]), roleID)
	if err != nil {
		return err
	}
	o, _ := a.userJSON(c.R.Context(), id)
	return c.JSON(201, O("user", o))
}

func (a *App) updateUser(c *Ctx) error {
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	ctx := c.R.Context()
	// Anyone may change their own name and password; someone else's takes
	// users:write and a role that covers theirs (an Editor with users:write
	// doesn't get to reset an Administrator's password).
	if id != c.Actor.UserID {
		if err := c.Need("users:write"); err != nil {
			return err
		}
		target, err := a.userRole(ctx, id)
		if err != nil {
			return err
		}
		if missing := c.lacks(target); missing != "" {
			return apierr.Forbidden(missing)
		}
	}
	p, err := c.Wrapped("user")
	if err != nil {
		return err
	}
	if has(p, "name") && strings.TrimSpace(str(p["name"])) != "" {
		if _, err := a.store.ExecContext(ctx, `UPDATE users SET name = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(str(p["name"])), store.Now().DB(), id); err != nil {
			return err
		}
	}
	// A new address waits for its confirmation link (account.go).
	if has(p, "email") && strings.TrimSpace(str(p["email"])) != "" {
		if err := a.requestEmailChange(ctx, c.Actor, id, str(p["email"])); err != nil {
			return err
		}
	}
	if pw := str(p["password"]); pw != "" {
		if err := a.changePassword(c, id, pw, str(p["current_password"])); err != nil {
			return err
		}
	}
	if roleID, ok := intOf(p["role_id"]); ok && c.can("users:write") {
		if id == c.Actor.UserID {
			return apierr.Conflict("You can't change your own role.")
		}
		r, err := a.role(ctx, roleID)
		if errors.Is(err, store.ErrNotFound) {
			return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"role_id": {"isn't a role"}}}
		} else if err != nil {
			return err
		}
		if missing := c.lacks(r); missing != "" {
			return apierr.Forbidden(missing)
		}
		if _, err := a.store.ExecContext(ctx, `UPDATE users SET role_id = ?, updated_at = ? WHERE id = ?`, roleID, store.Now().DB(), id); err != nil {
			return err
		}
	}
	a.audit(ctx, c.Actor, "user.updated", "User", id, "", map[string]any{"keys": keysOf(p)})
	o, err := a.userJSON(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(200, O("user", o))
}

func sessionID(c *Ctx) int64 {
	if c.Actor.Session != nil {
		return c.Actor.Session.ID
	}
	return 0
}

func (a *App) destroyUser(c *Ctx) error {
	if err := c.Need("users:delete"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	if id == c.Actor.UserID {
		return apierr.Conflict("You can't delete yourself.")
	}
	ctx := c.R.Context()
	target, err := a.userRole(ctx, id)
	if err != nil {
		return err
	}
	if missing := c.lacks(target); missing != "" {
		return apierr.Forbidden(missing)
	}
	err = a.store.Tx(ctx, func(tx *sql.Tx) error {
		for _, s := range []string{`DELETE FROM api_tokens WHERE user_id = ?`, `DELETE FROM admin_sessions WHERE user_id = ?`, `DELETE FROM sessions WHERE user_id = ?`,
			`UPDATE page_versions SET author_id = NULL WHERE author_id = ?`, `UPDATE collection_entry_versions SET author_id = NULL WHERE author_id = ?`,
			`DELETE FROM device_authorizations WHERE user_id = ?`, `DELETE FROM users WHERE id = ?`} {
			if _, err := tx.ExecContext(ctx, s, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.audit(ctx, c.Actor, "user.deleted", "User", id, "", nil)
	return c.NoContent()
}

// changePassword sets user id's password to pw and signs out its other
// sessions. Changing one's own needs the current password.
func (a *App) changePassword(c *Ctx, id int64, pw, current string) error {
	ctx := c.R.Context()
	if len(pw) < 10 {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"password": {"is too short (minimum is 10 characters)"}}}
	}
	if id == c.Actor.UserID {
		var digest string
		if err := a.store.QueryRowContext(ctx, `SELECT password_digest FROM users WHERE id = ?`, id).Scan(&digest); err != nil {
			return store.NotFound(err)
		}
		if !secret.CheckPassword(digest, current) {
			return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"current_password": {"is wrong"}}}
		}
	}
	hash, err := secret.HashPassword(pw)
	if err != nil {
		return err
	}
	// The new password and signing out its other sessions land together.
	return a.store.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_digest = ?, updated_at = ? WHERE id = ?`, hash, store.Now().DB(), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id = ? AND (? = 0 OR id != ?)`, id, sessionID(c), sessionID(c))
		return err
	})
}
