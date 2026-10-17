package cms

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
)

func (a *App) showSession(c *Ctx) error {
	var users int
	if err := a.store.QueryRowContext(c.R.Context(), `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return err
	}
	if c.Actor == nil || c.Actor.Session == nil {
		return c.JSON(200, O("signed_in", false, "needs_setup", users == 0, "site", str(a.general(c.R.Context())["title"]), "version", a.cfg.Version,
			"branding", a.brandingJSON(c.R.Context(), true)))
	}
	caps := []string{}
	if c.Actor.Role != nil {
		caps = a.capabilitiesOf(c.Actor.Role)
	}
	return c.JSON(200, O("signed_in", true, "csrf", c.Actor.Session.CSRF, "user", O("id", c.Actor.UserID, "name", c.Actor.Name, "email", c.Actor.Email, "pending_email", a.pendingEmail(c.R.Context(), c.Actor.UserID)),
		"capabilities", caps, "system", c.Actor.Role != nil && c.Actor.Role.System, "cms_address_set", a.addressKnown(), "site", str(a.general(c.R.Context())["title"]), "version", a.cfg.Version, "preferences", a.preferences(c.R.Context(), c.Actor.UserID),
		"branding", a.brandingJSON(c.R.Context(), true)))
}

func (a *App) createSession(c *Ctx) error {
	limited := func(wait int) error {
		c.W.Header().Set("Retry-After", strconv.Itoa(wait))
		return &apierr.Error{Status: 429, Code: "rate_limited", Message: "Too many sign-in attempts. Try again in a minute."}
	}
	if ok, wait := a.signIns.Allow("signin:" + c.ip()); !ok {
		return limited(wait)
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(str(body["email"])))
	if ok, wait := a.signIns.Allow("signin-email:" + email); !ok {
		return limited(wait)
	}
	var id int64
	var hash string
	var totp bool
	err := a.store.QueryRowContext(c.R.Context(), `SELECT id, password_digest, totp_enabled FROM users WHERE lower(email) = lower(?)`, email).Scan(&id, &hash, &totp)
	if errors.Is(err, sql.ErrNoRows) {
		secret.CheckPassword(secret.DummyHash(), str(body["password"]))
		return &apierr.Error{Status: 401, Code: "unauthorized", Message: "That email and password don't match."}
	} else if err != nil {
		return err
	}
	if !secret.CheckPassword(hash, str(body["password"])) {
		return &apierr.Error{Status: 401, Code: "unauthorized", Message: "That email and password don't match."}
	}
	// Two-factor secrets were sealed with the Rails core's key, which this
	// core can't read: signing in on the password alone would quietly drop
	// the second factor, so it doesn't.
	if totp {
		return &apierr.Error{Status: 403, Code: "two_factor_unavailable", Message: "This account uses two-factor sign-in, which this core doesn't support yet. An administrator can turn it off with `librepublish user disable-2fa --email …`, knowingly."}
	}
	csrf, err := a.signIn(c, id)
	if err != nil {
		return err
	}
	user, err := a.actorForUser(c.R.Context(), id)
	if err != nil {
		return err
	}
	a.audit(c.R.Context(), user, "session.created", "User", id, user.Label(), map[string]any{"ip": c.ip()})
	return c.JSON(201, O("signed_in", true, "csrf", csrf))
}

func (a *App) destroySession(c *Ctx) error {
	if c.Actor.Session != nil {
		if _, err := a.store.ExecContext(c.R.Context(), `DELETE FROM admin_sessions WHERE id = ?`, c.Actor.Session.ID); err != nil {
			return err
		}
	}
	a.setSessionCookie(c, "", -1)
	return c.NoContent()
}

// setup makes the first administrator, once, on an empty install.
func (a *App) setup(c *Ctx) error {
	ctx := c.R.Context()
	taken := apierr.Forbidden("setup (this install already has an administrator)")
	var users int
	if err := a.store.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return err
	}
	if users > 0 {
		return taken
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	admin, err := a.roleByName(ctx, "Administrator")
	if err != nil {
		return err
	}
	// Counted again inside the write: two setups racing make one admin.
	var id int64
	err = a.store.Tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
			return err
		}
		if users > 0 {
			return taken
		}
		id, err = a.insertUser(ctx, tx, str(body["name"]), str(body["email"]), str(body["password"]), admin.ID)
		return err
	})
	if err != nil {
		return err
	}
	a.audit(ctx, nil, "user.created", "User", id, str(body["name"]), nil)
	if t := strings.TrimSpace(str(body["site"])); t != "" {
		if _, err := a.putSetting(ctx, nil, "general", map[string]any{"title": t}, false); err != nil {
			return err
		}
	}
	csrf, err := a.signIn(c, id)
	if err != nil {
		return err
	}
	return c.JSON(201, O("signed_in", true, "csrf", csrf))
}
