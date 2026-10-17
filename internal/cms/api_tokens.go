package cms

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func (a *App) serviceTokenJSON(ctx context.Context, id int64) (Obj, error) {
	var name, prefix string
	var desc, ip sql.NullString
	var roleID int64
	var created, used, revoked store.Time
	err := a.store.QueryRowContext(ctx, `SELECT name, description, prefix, role_id, created_at, last_used_at, last_used_ip, revoked_at FROM service_tokens WHERE id = ?`, id).
		Scan(&name, &desc, &prefix, &roleID, &created, &used, &ip, &revoked)
	if err != nil {
		return nil, store.NotFound(err)
	}
	roleName := ""
	if r, err := a.role(ctx, roleID); err == nil {
		roleName = r.Name
	}
	return O("id", id, "name", name, "description", nullStr(desc), "prefix", prefix, "role", O("id", roleID, "name", roleName),
		"created_at", created.ISO(), "last_used_at", used.ISO(), "last_used_ip", nullStr(ip), "revoked_at", revoked.ISO(), "active", !revoked.Valid), nil
}

func (a *App) listServiceTokens(c *Ctx) error {
	if err := c.Need("settings:read"); err != nil {
		return err
	}
	ids, err := store.Int64s(c.R.Context(), a.store.DB, `SELECT id FROM service_tokens ORDER BY id`)
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, id := range ids {
		o, err := a.serviceTokenJSON(c.R.Context(), id)
		if isNotFound(err) {
			continue
		} else if err != nil {
			return err
		}
		out = append(out, o)
	}
	roles, err := a.rolesList(c.R.Context())
	if err != nil {
		return err
	}
	roleNames := []Obj{}
	for _, r := range roles {
		roleNames = append(roleNames, O("id", r.ID, "name", r.Name))
	}
	return c.JSON(200, O("service_tokens", out, "roles", roleNames))
}

func (a *App) showServiceToken(c *Ctx) error {
	if err := c.Need("settings:read"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	o, err := a.serviceTokenJSON(c.R.Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(200, O("service_token", o))
}

// issueServiceToken issues one on a role; the plaintext is returned once.
func (a *App) issueServiceToken(ctx context.Context, actor *Actor, name, description string, roleID int64) (int64, string, error) {
	plain := "lps_" + secret.Token(32)
	var by any
	if actor != nil && actor.UserID != 0 {
		by = actor.UserID
	}
	res, err := a.store.ExecContext(ctx, `INSERT INTO service_tokens (name, description, role_id, created_by_id, prefix, token, token_digest, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		name, description, roleID, by, plain[:12], a.secrets.Seal(plain), secret.Digest(plain), store.Now().DB(), store.Now().DB())
	if err != nil {
		return 0, "", err
	}
	id, _ := res.LastInsertId()
	a.audit(ctx, actor, "service_token.created", "ServiceToken", id, name, nil)
	return id, plain, nil
}

func (a *App) createServiceToken(c *Ctx) error {
	if err := c.Need("settings:write"); err != nil {
		return err
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	p := body
	if s, ok := body["service_token"].(map[string]any); ok {
		p = s
	}
	name := strings.TrimSpace(str(p["name"]))
	if name == "" {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"name": {"can't be blank"}}}
	}
	var role *Role
	var err error
	if id, ok := intOf(p["role_id"]); ok {
		role, err = a.role(c.R.Context(), id)
	} else {
		roleName := str(p["role"])
		if roleName == "" {
			roleName = "Production site"
		}
		role, err = a.roleByName(c.R.Context(), roleName)
	}
	if err != nil {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"role": {"isn't a role"}}}
	}
	if missing := c.lacks(role); missing != "" {
		return apierr.Forbidden(missing)
	}
	id, plain, err := a.issueServiceToken(c.R.Context(), c.Actor, name, str(p["description"]), role.ID)
	if err != nil {
		return err
	}
	o, _ := a.serviceTokenJSON(c.R.Context(), id)
	c.Summary = "Issued. The secret is shown once here: store it in the site's environment, never in a repo."
	return c.JSON(201, O("service_token", append(o, kv{"token", plain})))
}

func (a *App) revealServiceToken(c *Ctx) error {
	if err := c.Need("settings:write"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	var sealed sql.NullString
	var roleID int64
	err = a.store.QueryRowContext(c.R.Context(), `SELECT token, role_id FROM service_tokens WHERE id = ? AND revoked_at IS NULL`, id).Scan(&sealed, &roleID)
	if errors.Is(err, sql.ErrNoRows) {
		return apierr.NotFound("No active service token " + c.Param("id") + ".")
	} else if err != nil {
		return err
	}
	if err := a.mayHoldToken(c, roleID); err != nil {
		return err
	}
	plain, ok := a.secrets.Open(sealed.String)
	if !ok {
		return apierr.Conflict("This token can't be shown again (it was issued before this install's key): rotate it for a new one.")
	}
	o, _ := a.serviceTokenJSON(c.R.Context(), id)
	a.audit(c.R.Context(), c.Actor, "service_token.revealed", "ServiceToken", id, str(o.Get("name")), nil)
	return c.JSON(200, O("service_token", append(o, kv{"token", plain})))
}

func (a *App) rotateServiceToken(c *Ctx) error {
	if err := c.Need("settings:write"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	var roleID int64
	err = a.store.QueryRowContext(c.R.Context(), `SELECT role_id FROM service_tokens WHERE id = ? AND revoked_at IS NULL`, id).Scan(&roleID)
	if errors.Is(err, sql.ErrNoRows) {
		return apierr.NotFound("No active service token " + c.Param("id") + ".")
	} else if err != nil {
		return err
	}
	if err := a.mayHoldToken(c, roleID); err != nil {
		return err
	}
	plain := "lps_" + secret.Token(32)
	res, err := a.store.ExecContext(c.R.Context(), `UPDATE service_tokens SET prefix = ?, token = ?, token_digest = ?, updated_at = ? WHERE id = ? AND revoked_at IS NULL`,
		plain[:12], a.secrets.Seal(plain), secret.Digest(plain), store.Now().DB(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierr.NotFound("No active service token " + c.Param("id") + ".")
	}
	o, _ := a.serviceTokenJSON(c.R.Context(), id)
	a.audit(c.R.Context(), c.Actor, "service_token.rotated", "ServiceToken", id, str(o.Get("name")), nil)
	c.Summary = "Rotated: the old secret stopped working just now."
	return c.JSON(200, O("service_token", append(o, kv{"token", plain})))
}

// mayHoldToken refuses a token's secret to anyone whose role doesn't cover
// the token's: holding it is acting as it.
func (a *App) mayHoldToken(c *Ctx, roleID int64) error {
	role, err := a.role(c.R.Context(), roleID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // a token without a role can do nothing
	} else if err != nil {
		return err
	}
	if missing := c.lacks(role); missing != "" {
		return apierr.Forbidden(missing)
	}
	return nil
}

func (a *App) revokeServiceToken(c *Ctx) error {
	if err := c.Need("settings:write"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	if _, err := a.store.ExecContext(c.R.Context(), `UPDATE service_tokens SET revoked_at = ?, token = NULL WHERE id = ? AND revoked_at IS NULL`, store.Now().DB(), id); err != nil {
		return err
	}
	o, err := a.serviceTokenJSON(c.R.Context(), id)
	if err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "service_token.revoked", "ServiceToken", id, str(o.Get("name")), nil)
	return c.JSON(200, O("service_token", o))
}
