package cms

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

const devicePollInterval = 5

func (a *App) deviceCode(c *Ctx) error {
	var body map[string]any
	if err := c.OptionalBody(&body); err != nil {
		return err
	}
	purpose := str(body["purpose"])
	if purpose != "site" {
		purpose = "user"
	}
	device := secret.Token(32)
	letters := "BCDFGHJKLMNPQRSTVWXZ"
	raw := secret.RandomBytes(8)
	code := make([]byte, 0, 9)
	for i, b := range raw {
		if i == 4 {
			code = append(code, '-')
		}
		code = append(code, letters[int(b)%len(letters)])
	}
	expires := store.TimeOf(time.Now().Add(15 * time.Minute))
	_, err := a.store.ExecContext(c.R.Context(), `INSERT INTO device_authorizations (device_code, user_code, hostname, label, purpose, expires_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
		secret.Digest(device), string(code), str(body["hostname"]), str(body["label"]), purpose, expires.DB(), store.Now().DB(), store.Now().DB())
	if err != nil {
		return err
	}
	return c.JSON(200, O("status", "ok", "user_code", string(code), "device_code", device,
		"verification_url", a.BaseURL()+"/cms/#/connect?code="+string(code), "interval", devicePollInterval,
		"expires_in", int(time.Until(expires.Time).Seconds()), "account", a.tenant()))
}

func (a *App) deviceShow(c *Ctx) error {
	var host, label, purpose sql.NullString
	var expires store.Time
	err := a.store.QueryRowContext(c.R.Context(), `SELECT hostname, label, purpose, expires_at FROM device_authorizations WHERE user_code = ? AND approved_at IS NULL AND denied_at IS NULL`,
		strings.ToUpper(c.Param("code"))).Scan(&host, &label, &purpose, &expires)
	if err != nil || time.Now().After(expires.Time) {
		return apierr.NotFound("That code has expired or was already used. Run `cms login` again.")
	}
	return c.JSON(200, O("hostname", nullStr(host), "label", nullStr(label), "purpose", nullStr(purpose)))
}

func (a *App) deviceApprove(c *Ctx) error {
	if c.Actor.UserID == 0 {
		return apierr.Forbidden("a person's sign-in")
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	code := strings.ToUpper(strings.TrimSpace(str(body["user_code"])))
	var purpose string
	if err := a.store.QueryRowContext(c.R.Context(), `SELECT purpose FROM device_authorizations WHERE user_code = ? AND approved_at IS NULL AND denied_at IS NULL AND expires_at > ?`, code, store.Now().DB()).Scan(&purpose); err != nil {
		return apierr.NotFound("That code has expired or was already used.")
	}
	if purpose == "site" {
		if err := c.Need("settings:write"); err != nil {
			return err
		}
	}
	col := "approved_at"
	if body["approve"] != nil && !boolOf(body["approve"]) {
		col = "denied_at"
	}
	// Only a code still undecided: two approvals racing can't both land.
	res, err := a.store.ExecContext(c.R.Context(), `UPDATE device_authorizations SET `+col+` = ?, user_id = ?, updated_at = ? WHERE user_code = ? AND approved_at IS NULL AND denied_at IS NULL`, store.Now().DB(), c.Actor.UserID, store.Now().DB(), code)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return apierr.NotFound("That code has expired or was already used.")
	}
	a.audit(c.R.Context(), c.Actor, "device."+strings.TrimSuffix(col, "_at"), "DeviceAuthorization", 0, code, nil)
	return c.JSON(200, O("ok", true, "status", strings.TrimSuffix(col, "_at")))
}

func (a *App) deviceToken(c *Ctx) error {
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	fail := func(code string) error { return c.JSON(400, O("status", "error", "error", code)) }
	var id int64
	var purpose string
	var label, host sql.NullString
	var userID store.NullInt
	var approved, denied, expires store.Time
	err := a.store.QueryRowContext(c.R.Context(), `SELECT id, purpose, label, hostname, user_id, approved_at, denied_at, expires_at FROM device_authorizations WHERE device_code = ?`,
		secret.Digest(str(body["device_code"]))).Scan(&id, &purpose, &label, &host, &userID, &approved, &denied, &expires)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fail("invalid_grant")
	case err != nil:
		return err
	case denied.Valid:
		return fail("access_denied")
	case time.Now().After(expires.Time):
		return fail("expired_token")
	case !approved.Valid:
		return c.JSON(200, O("status", "pending", "interval", devicePollInterval))
	}
	// One use: the code is spent once it hands out a credential, and of
	// two polls racing only the one whose delete lands gets it.
	res, err := a.store.ExecContext(c.R.Context(), `DELETE FROM device_authorizations WHERE id = ? AND approved_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fail("invalid_grant")
	}
	user, err := a.actorForUser(c.R.Context(), userID.Int64)
	if err != nil {
		return err
	}
	if user == nil {
		return fail("invalid_grant")
	}
	out := O("status", "ok")
	if purpose == "site" {
		role, err := a.roleByName(c.R.Context(), "Production site")
		if err != nil {
			return err
		}
		name := label.String
		if name == "" {
			name = "PRODUCTION"
		}
		_, plain, err := a.issueServiceToken(c.R.Context(), user, name, "Issued by `cms login` on "+host.String, role.ID)
		if err != nil {
			return err
		}
		out = append(out, kv{"token", plain}, kv{"purpose", "site"}, kv{"token_name", name})
	} else {
		plain, err := a.userToken(c.R.Context(), user.UserID)
		if err != nil {
			return err
		}
		out = append(out, kv{"token", plain}, kv{"purpose", "user"})
	}
	return c.JSON(200, append(out, kv{"user", O("name", user.Name, "email", user.Email)}, kv{"account", a.tenant()}, kv{"api_url", a.BaseURL()}))
}
