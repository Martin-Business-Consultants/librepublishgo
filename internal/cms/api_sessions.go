package cms

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/ratelimit"
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
		"capabilities", caps, "role", roleName(c.Actor), "system", c.Actor.Role != nil && c.Actor.Role.System, "cms_address_set", a.addressKnown(), "update", a.updateNoticeFor(c.Actor), "site", str(a.general(c.R.Context())["title"]), "version", a.cfg.Version, "preferences", a.preferences(c.R.Context(), c.Actor.UserID),
		"branding", a.brandingJSON(c.R.Context(), true)))
}

func (a *App) createSession(c *Ctx) error {
	limited := func(wait int) error {
		c.W.Header().Set("Retry-After", strconv.Itoa(wait))
		return &apierr.Error{Status: 429, Code: "rate_limited", Message: "Too many sign-in attempts. Try again in a minute."}
	}
	if ok, wait := a.signIns.Allow("signin:" + ratelimit.IPKey(c.ip())); !ok {
		return limited(wait)
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(str(body["email"])))
	if ok, wait := a.signInAccounts.Allow("signin-email:" + email); !ok {
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

// setupTokenFile, in the data directory, holds the one-time code that
// claims an empty install. Without it, whoever reached a new install's
// address first would be its administrator; with it, only someone who can
// read the server's log or its data directory is.
const setupTokenFile = "setup.token"

// SetupToken is the code POST /api/setup takes while the install has no
// users, made the first time it's asked for and the same until it's used;
// "" once there's a user.
func (a *App) SetupToken(ctx context.Context) (string, error) {
	path := filepath.Join(a.cfg.DataDir, setupTokenFile)
	var users int
	if err := a.store.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return "", err
	}
	if users > 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return "", nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the data directory's own file
	if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		return strings.TrimSpace(string(raw)), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	token := secret.Token(24)
	// Two processes asking at once: one writes it, the other reads that.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // as above
	if errors.Is(err, os.ErrExist) {
		raw, err := os.ReadFile(path) //nolint:gosec // as above
		return strings.TrimSpace(string(raw)), err
	} else if err != nil {
		return "", err
	}
	_, err = f.WriteString(token + "\n")
	return token, errors.Join(err, f.Close())
}

// SetupURL is the admin's setup screen with token filled in.
func (a *App) SetupURL(token string) string {
	return strings.TrimRight(a.BaseURL(), "/") + "/#/setup?token=" + url.QueryEscape(token)
}

// announceSetup logs the setup address while there's no one to sign in.
func (a *App) announceSetup(ctx context.Context) {
	token, err := a.SetupToken(ctx)
	if err != nil {
		a.log.Warn("setup token", "err", err)
		return
	}
	if token != "" {
		a.log.Warn("NO ADMINISTRATOR YET: open the setup address to make one (`librepublish setup-token` shows it again)", "setup_url", a.SetupURL(token))
	}
}

// setup makes the first administrator, once, on an empty install, for
// whoever has its setup token.
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
	want, err := a.SetupToken(ctx)
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(str(body["token"])); want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return &apierr.Error{Status: 403, Code: "setup_token", Message: "That setup token isn't this install's. It's in the server's log, and `librepublish setup-token` shows it.",
			Errors: apierr.Invalid{"token": {"isn't this install's setup token"}}}
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
	if err := os.Remove(filepath.Join(a.cfg.DataDir, setupTokenFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.log.Warn("setup token", "err", err)
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

// roleName is the actor's role, as people see it.
func roleName(actor *Actor) string {
	if actor == nil || actor.Role == nil {
		return ""
	}
	return actor.Role.Name
}
