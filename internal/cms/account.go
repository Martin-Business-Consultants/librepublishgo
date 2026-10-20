package cms

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// ---- account links -------------------------------------------------------

// A forgotten password and a changed email address are both settled by a
// link mailed to the person: one use, a short life, only its digest kept.
// Asking for a reset says the same thing whether or not the address has an
// account, so the form can't be used to find out who does.

const (
	purposeReset = "password_reset"
	purposeEmail = "email_change"
	resetTTL     = time.Hour
	emailTTL     = 24 * time.Hour
)

// adminLink is an address in the admin (its hash route), for an email.
func (a *App) adminLink(route string) string {
	return strings.TrimRight(a.BaseURL(), "/") + "/#" + route
}

func (a *App) siteName(ctx context.Context) string {
	return strOr(str(a.general(ctx)["title"]), "Libre Publish")
}

func (a *App) issueAccountToken(ctx context.Context, userID int64, purpose, newEmail string, ttl time.Duration) (string, error) {
	token := secret.Token(32)
	now := store.Now()
	// One live link of each kind: a new one replaces the last.
	if _, err := a.store.ExecContext(ctx, `DELETE FROM account_tokens WHERE user_id = ? AND purpose = ? AND used_at IS NULL`, userID, purpose); err != nil {
		return "", err
	}
	var email any
	if newEmail != "" {
		email = newEmail
	}
	_, err := a.store.ExecContext(ctx, `INSERT INTO account_tokens (user_id, purpose, token_digest, new_email, expires_at, created_at) VALUES (?,?,?,?,?,?)`,
		userID, purpose, secret.Digest(token), email, store.Time{Time: now.Add(ttl), Valid: true}.DB(), now.DB())
	return token, err
}

// redeemAccountToken uses a link once: the person it's for and, for an
// email change, the address it confirms.
func (a *App) redeemAccountToken(ctx context.Context, token, purpose string) (userID int64, newEmail string, err error) {
	var id int64
	var expires store.Time
	var email sql.NullString
	err = a.store.QueryRowContext(ctx, `SELECT id, user_id, new_email, expires_at FROM account_tokens WHERE token_digest = ? AND purpose = ? AND used_at IS NULL`,
		secret.Digest(strings.TrimSpace(token)), purpose).Scan(&id, &userID, &email, &expires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && expires.Before(time.Now())) {
		return 0, "", &apierr.Error{Status: 410, Code: "link_expired", Message: "That link has expired or was already used. Ask for a new one."}
	}
	if err != nil {
		return 0, "", err
	}
	// Two requests with the same link race here: only one marks it used.
	res, err := a.store.ExecContext(ctx, `UPDATE account_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL`, store.Now().DB(), id)
	if err != nil {
		return 0, "", err
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, "", err
	} else if n != 1 {
		return 0, "", &apierr.Error{Status: 410, Code: "link_expired", Message: "That link has expired or was already used. Ask for a new one."}
	}
	return userID, email.String, nil
}

// forgotPassword mails a reset link to an account's address, and answers
// the same whether or not there is one.
func (a *App) forgotPassword(c *Ctx) error {
	var body struct {
		Email string `json:"email"`
	}
	if err := c.Body(&body); err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	answer := c.JSON(200, O("ok", true, "message", "If that address has an account, a link to choose a new password is on its way. It works for an hour."))
	if ok, _ := a.forgots.Allow("forgot:" + c.ip()); !ok {
		return answer
	}
	if ok, _ := a.forgots.Allow("forgot:" + email); !ok {
		return answer
	}
	ctx := c.R.Context()
	var id int64
	var name string
	if err := a.store.QueryRowContext(ctx, `SELECT id, name FROM users WHERE lower(email) = ?`, email).Scan(&id, &name); err != nil {
		return answer
	}
	token, err := a.issueAccountToken(ctx, id, purposeReset, "", resetTTL)
	if err != nil {
		return err
	}
	link := a.adminLink("/reset-password?token=" + token)
	err = a.sendMail(ctx, mail.Message{To: []string{email}, Subject: "Choose a new password for " + a.siteName(ctx),
		Text: fmt.Sprintf("Hi %s,\n\nSomeone (hopefully you) asked to reset the password for your %s account. Choose a new one here:\n\n%s\n\nThe link works once, for an hour. If you didn't ask, ignore this email: your password stays as it is.\n", name, a.siteName(ctx), link)})
	if err != nil {
		// Not told to the person asking: that would say the account exists.
		a.log.Warn("password reset email", "user", id, "err", err)
	}
	a.audit(ctx, nil, "password.reset_requested", "User", id, name, map[string]any{"ip": c.ip(), "sent": err == nil})
	return answer
}

// resetPassword sets a new password from a mailed link, signs the person
// out everywhere else, and signs them in here.
func (a *App) resetPassword(c *Ctx) error {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := c.Body(&body); err != nil {
		return err
	}
	if len(body.Password) < 10 {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"password": {"is too short (minimum is 10 characters)"}}}
	}
	ctx := c.R.Context()
	id, _, err := a.redeemAccountToken(ctx, body.Token, purposeReset)
	if err != nil {
		return err
	}
	hash, err := secret.HashPassword(body.Password)
	if err != nil {
		return err
	}
	err = a.store.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_digest = ?, updated_at = ? WHERE id = ?`, hash, store.Now().DB(), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	user, err := a.actorForUser(ctx, id)
	if err != nil {
		return err
	}
	a.audit(ctx, user, "password.reset", "User", id, user.Label(), map[string]any{"ip": c.ip()})
	var totp bool
	if err := a.store.QueryRowContext(ctx, `SELECT totp_enabled FROM users WHERE id = ?`, id).Scan(&totp); err != nil {
		return store.NotFound(err)
	}
	if totp {
		return c.JSON(200, O("ok", true, "signed_in", false, "message", "Your password is changed. Sign in with it."))
	}
	csrf, err := a.signIn(c, id)
	if err != nil {
		return err
	}
	return c.JSON(200, O("ok", true, "signed_in", true, "csrf", csrf))
}

// requestEmailChange keeps the current address until the new one is
// confirmed from a link sent to it; the old address is told.
func (a *App) requestEmailChange(ctx context.Context, actor *Actor, id int64, newEmail string) error {
	newEmail = strings.ToLower(strings.TrimSpace(newEmail))
	if _, err := netmail.ParseAddress(newEmail); err != nil || strings.ContainsAny(newEmail, " <>") {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"email": {"is invalid"}}}
	}
	var name, current string
	if err := a.store.QueryRowContext(ctx, `SELECT name, email FROM users WHERE id = ?`, id).Scan(&name, &current); err != nil {
		return store.NotFound(err)
	}
	if newEmail == strings.ToLower(current) {
		_, err := a.store.ExecContext(ctx, `DELETE FROM account_tokens WHERE user_id = ? AND purpose = ? AND used_at IS NULL`, id, purposeEmail)
		return err
	}
	var clash int
	if err := a.store.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE lower(email) = ? AND id != ?`, newEmail, id).Scan(&clash); err != nil {
		return err
	}
	if clash > 0 {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"email": {"has already been taken"}}}
	}
	token, err := a.issueAccountToken(ctx, id, purposeEmail, newEmail, emailTTL)
	if err != nil {
		return err
	}
	link := a.adminLink("/confirm-email?token=" + token)
	if err := a.sendMail(ctx, mail.Message{To: []string{newEmail}, Subject: "Confirm your new email address for " + a.siteName(ctx),
		Text: fmt.Sprintf("Hi %s,\n\nConfirm that %s is your new address for %s:\n\n%s\n\nUntil you do, %s stays the address on the account. The link works for a day.\n", name, newEmail, a.siteName(ctx), link, current)}); err != nil {
		if _, derr := a.store.ExecContext(ctx, `DELETE FROM account_tokens WHERE user_id = ? AND purpose = ? AND used_at IS NULL`, id, purposeEmail); derr != nil {
			return derr
		}
		return &apierr.Error{Status: 422, Code: "mail_failed", Message: "The confirmation email couldn't be sent, so the address is unchanged: " + err.Error()}
	}
	if err := a.sendMail(ctx, mail.Message{To: []string{current}, Subject: "Your email address for " + a.siteName(ctx) + " is changing",
		Text: fmt.Sprintf("Hi %s,\n\nA change of your account's email address to %s was asked for. It happens once the new address is confirmed. If you didn't ask for this, sign in and cancel it, and change your password.\n", name, newEmail)}); err != nil {
		// The change still waits on the new address; the notice is a courtesy.
		a.log.Warn("email change notice", "user", id, "err", err)
	}
	a.audit(ctx, actor, "user.email_change_requested", "User", id, name, map[string]any{"new_email": newEmail})
	return nil
}

func (a *App) pendingEmail(ctx context.Context, id int64) any {
	var email string
	var expires store.Time
	err := a.store.QueryRowContext(ctx, `SELECT new_email, expires_at FROM account_tokens WHERE user_id = ? AND purpose = ? AND used_at IS NULL ORDER BY id DESC LIMIT 1`, id, purposeEmail).Scan(&email, &expires)
	if err != nil || expires.Before(time.Now()) {
		return nil
	}
	return O("email", email, "expires_at", expires)
}

// confirmEmail settles a pending change from its link.
func (a *App) confirmEmail(c *Ctx) error {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.Body(&body); err != nil {
		return err
	}
	ctx := c.R.Context()
	id, email, err := a.redeemAccountToken(ctx, body.Token, purposeEmail)
	if err != nil {
		return err
	}
	var clash int
	if err := a.store.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE lower(email) = ? AND id != ?`, email, id).Scan(&clash); err != nil {
		return err
	}
	if clash > 0 {
		return apierr.Conflict("Another account has that address now.")
	}
	if _, err := a.store.ExecContext(ctx, `UPDATE users SET email = ?, updated_at = ? WHERE id = ?`, email, store.Now().DB(), id); err != nil {
		return err
	}
	user, _ := a.actorForUser(ctx, id)
	a.audit(ctx, user, "user.email_changed", "User", id, user.Label(), map[string]any{"email": email})
	return c.JSON(200, O("ok", true, "email", email, "message", "Your email address is now "+email+"."))
}

// cancelEmailChange drops a pending change: the person's own, or anyone's
// for someone with users:write.
func (a *App) cancelEmailChange(c *Ctx) error {
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	if id != c.Actor.UserID {
		if err := c.Need("users:write"); err != nil {
			return err
		}
	}
	if _, err := a.store.ExecContext(c.R.Context(), `DELETE FROM account_tokens WHERE user_id = ? AND purpose = ? AND used_at IS NULL`, id, purposeEmail); err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "user.email_change_cancelled", "User", id, "", nil)
	return c.NoContent()
}
