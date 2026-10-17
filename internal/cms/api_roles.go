package cms

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func (a *App) rolesList(ctx context.Context) ([]*Role, error) {
	ids, err := store.Int64s(ctx, a.store.DB, `SELECT id FROM roles ORDER BY system DESC, name`)
	if err != nil {
		return nil, err
	}
	out := []*Role{}
	for _, id := range ids {
		r, err := a.role(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue // deleted since the list was read
		} else if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (a *App) listRoles(c *Ctx) error {
	if err := c.Need("roles:read"); err != nil {
		return err
	}
	roles, err := a.rolesList(c.R.Context())
	if err != nil {
		return err
	}
	catalog := []Obj{}
	for _, g := range capCatalog {
		catalog = append(catalog, O("group", g.Group, "capabilities", g.Caps))
	}
	for _, g := range a.plugins.CapGroups() {
		catalog = append(catalog, O("group", g.Group, "capabilities", g.Caps, "labels", g.Labels, "plugin", true))
	}
	return c.JSON(200, O("roles", roles, "catalog", catalog))
}

func (a *App) saveRole(c *Ctx, r *Role, p map[string]any) error {
	// Below the system role, no one edits their own role or one above it,
	// nor grants what they don't hold themselves.
	if !c.Actor.Role.System {
		if r.ID != 0 && r.ID == c.Actor.Role.ID {
			return apierr.Conflict("You can't change your own role.")
		}
		if missing := c.lacks(r); missing != "" {
			return apierr.Forbidden(missing)
		}
	}
	if has(p, "name") {
		r.Name = strings.TrimSpace(str(p["name"]))
	}
	if has(p, "description") {
		r.Description = str(p["description"])
	}
	if has(p, "permissions") {
		if r.System {
			return apierr.Conflict("The Administrator role holds everything; its permissions can't change.")
		}
		r.Permissions = []string{}
		all := a.allCapabilities()
		for _, v := range asList(p["permissions"]) {
			if s := str(v); slices.Contains(all, s) {
				r.Permissions = append(r.Permissions, s)
			}
		}
		if missing := c.lacks(r); missing != "" {
			return apierr.Forbidden(missing)
		}
	}
	if r.Name == "" {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"name": {"can't be blank"}}}
	}
	perms := store.MustJSON(r.Permissions)
	now := store.Now()
	if r.ID == 0 {
		res, err := a.store.ExecContext(c.R.Context(), `INSERT INTO roles (name, description, permissions, system, created_at, updated_at) VALUES (?,?,?,0,?,?)`, r.Name, r.Description, string(perms), now.DB(), now.DB())
		if err != nil {
			return err
		}
		r.ID, _ = res.LastInsertId()
		r.CreatedAt = now
	} else if _, err := a.store.ExecContext(c.R.Context(), `UPDATE roles SET name=?, description=?, permissions=?, updated_at=? WHERE id=?`, r.Name, r.Description, string(perms), now.DB(), r.ID); err != nil {
		return err
	}
	r.UpdatedAt = now
	a.audit(c.R.Context(), c.Actor, "role.saved", "Role", r.ID, r.Name, map[string]any{"permissions": r.Permissions})
	return nil
}

func (a *App) createRole(c *Ctx) error {
	if err := c.Need("roles:write"); err != nil {
		return err
	}
	p, err := c.Wrapped("role")
	if err != nil {
		return err
	}
	r := &Role{Permissions: []string{}}
	if err := a.saveRole(c, r, p); err != nil {
		return err
	}
	return c.JSON(201, O("role", r))
}

func (a *App) updateRole(c *Ctx) error {
	if err := c.Need("roles:write"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	r, err := a.role(c.R.Context(), id)
	if err != nil {
		return err
	}
	p, err := c.Wrapped("role")
	if err != nil {
		return err
	}
	if err := a.saveRole(c, r, p); err != nil {
		return err
	}
	return c.JSON(200, O("role", r))
}

func (a *App) destroyRole(c *Ctx) error {
	if err := c.Need("roles:delete"); err != nil {
		return err
	}
	id, err := c.IntParam("id")
	if err != nil {
		return err
	}
	r, err := a.role(c.R.Context(), id)
	if err != nil {
		return err
	}
	users, err := store.Count(c.R.Context(), a.store.DB, `SELECT count(*) FROM users WHERE role_id = ?`, id)
	if err != nil {
		return err
	}
	tokens, err := store.Count(c.R.Context(), a.store.DB, `SELECT count(*) FROM service_tokens WHERE role_id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if r.System || users > 0 || tokens > 0 {
		return apierr.Conflict(fmt.Sprintf("%s is in use (%d users, %d service tokens) or built in.", r.Name, users, tokens))
	}
	if _, err := a.store.ExecContext(c.R.Context(), `DELETE FROM roles WHERE id = ?`, id); err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "role.deleted", "Role", id, r.Name, nil)
	return c.NoContent()
}
