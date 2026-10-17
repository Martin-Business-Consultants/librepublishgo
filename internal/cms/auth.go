package cms

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// ---- capabilities --------------------------------------------------------

const wildcard = "manage:all"

// capCatalog is the capability catalog, grouped as the role editor shows it.
// The same strings the Rails core stores on roles.permissions.
var capCatalog = []struct {
	Group string
	Caps  []string
}{
	{"Pages", []string{"pages:read", "pages:write", "pages:delete", "pages:publish"}},
	{"Collections", []string{"collections:read", "collections:write", "collections:delete"}},
	{"Entries", []string{"entries:read", "entries:write", "entries:delete", "entries:publish"}},
	{"Block types", []string{"block_types:read", "block_types:write", "block_types:delete"}},
	{"Globals", []string{"globals:read", "globals:write", "globals:publish", "globals:delete", "globals:scripts"}},
	{"Assets", []string{"assets:read", "assets:write", "assets:delete"}},
	{"Webhooks", []string{"webhooks:read", "webhooks:write", "webhooks:delete"}},
	{"Redirects", []string{"redirects:read", "redirects:write", "redirects:delete"}},
	{"Users", []string{"users:read", "users:write", "users:delete"}},
	{"Roles", []string{"roles:read", "roles:write", "roles:delete"}},
	{"Settings", []string{"settings:read", "settings:write"}},
	{"Audit log", []string{"audit_log:read"}},
	{"Trash", []string{"trash:read", "trash:write"}},
	{"Tools", []string{"tools:use"}},
	{"Plugins", []string{"plugins:read", "plugins:write"}},
}

// allCapabilities is the catalog's, and the enabled plugins'.
func (a *App) allCapabilities() []string {
	var all []string
	for _, g := range capCatalog {
		all = append(all, g.Caps...)
	}
	for _, g := range a.plugins.CapGroups() {
		all = append(all, g.Caps...)
	}
	return all
}

var defaultRoles = []struct {
	Name, Description string
	System            bool
	Caps              []string
}{
	{"Administrator", "Everything, including users, roles and settings.", true, []string{wildcard}},
	{"Editor", "Writes and publishes content.", false, []string{
		"pages:read", "pages:write", "pages:publish", "collections:read", "entries:read", "entries:write", "entries:publish",
		"block_types:read", "globals:read", "globals:write", "globals:publish", "assets:read", "assets:write",
		"webhooks:read", "redirects:read", "settings:read", "audit_log:read", "trash:read", "trash:write"}},
	{"Author", "Writes drafts.", false, []string{
		"pages:read", "pages:write", "collections:read", "entries:read", "entries:write", "block_types:read",
		"globals:read", "assets:read", "assets:write", "settings:read"}},
	{"Production site", "What a published site reads: live content, nothing else.", false, []string{
		"pages:read", "collections:read", "entries:read", "block_types:read", "globals:read", "assets:read", "redirects:read"}},
	{"Agent", "An agent or script: writes drafts, never publishes them.", false, []string{
		"pages:read", "pages:write", "collections:read", "entries:read", "entries:write", "block_types:read",
		"globals:read", "globals:write", "assets:read", "assets:write", "redirects:read", "settings:read"}},
}

// Role is a named set of capabilities a user or service token acts with.
type Role struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Permissions []string   `json:"permissions"`
	System      bool       `json:"system"`
	CreatedAt   store.Time `json:"created_at"`
	UpdatedAt   store.Time `json:"updated_at"`
}

// Can is whether the role grants capability; a nil role grants nothing.
func (r *Role) Can(capability string) bool {
	if r == nil {
		return false
	}
	return r.System || slices.Contains(r.Permissions, wildcard) || slices.Contains(r.Permissions, capability)
}

// capabilitiesOf is everything role r grants, spelled out.
func (a *App) capabilitiesOf(r *Role) []string {
	if r == nil {
		return []string{}
	}
	all := a.allCapabilities()
	if r.System || slices.Contains(r.Permissions, wildcard) {
		return all
	}
	out := []string{}
	for _, c := range all {
		if slices.Contains(r.Permissions, c) {
			out = append(out, c)
		}
	}
	return out
}

// Actor is whoever a request acts as: a person (by session or personal
// token) or a service token, with its role.
type Actor struct {
	UserID    int64
	Name      string
	Email     string
	Role      *Role
	ServiceID int64 // a service token, with no person behind it
	TokenID   int64 // the personal token, when that's how it came
	Session   *adminSession
}

// Can is whether the actor's role grants capability.
func (a *Actor) Can(capability string) bool { return a.Role.Can(capability) }

// lacks is a capability role r grants that the actor doesn't hold, "" when
// there's none: no one hands out, takes over, or acts for a role above their
// own. Only the system role reaches the system role.
func (c *Ctx) lacks(r *Role) string {
	if r == nil {
		return ""
	}
	if c.Actor == nil || c.Actor.Role == nil {
		return wildcard
	}
	if c.Actor.Role.System {
		return ""
	}
	if r.System {
		return wildcard
	}
	for _, p := range r.Permissions {
		if !c.Actor.Can(p) {
			return p
		}
	}
	return ""
}

// userRole is a user's role, nil for none; ErrNotFound for no such user.
func (a *App) userRole(ctx context.Context, userID int64) (*Role, error) {
	var roleID store.NullInt
	if err := a.store.QueryRowContext(ctx, `SELECT role_id FROM users WHERE id = ?`, userID).Scan(&roleID); err != nil {
		return nil, store.NotFound(err)
	}
	if !roleID.Valid {
		return nil, nil
	}
	r, err := a.role(ctx, roleID.Int64)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return r, err
}

// Label is who the actor is, in words, for the audit log: "system" for
// nil.
func (a *Actor) Label() string {
	if a == nil {
		return "system"
	}
	if a.ServiceID != 0 {
		return a.Name + " (service token)"
	}
	return a.Name
}

// Type is the Rails class the audit log names the actor by.
func (a *Actor) Type() string {
	if a.ServiceID != 0 {
		return "ServiceToken"
	}
	return "User"
}

// ID is the actor's user or service token id.
func (a *Actor) ID() int64 {
	if a.ServiceID != 0 {
		return a.ServiceID
	}
	return a.UserID
}

// ---- authentication ------------------------------------------------------

const sessionCookie = "lp_session"

func (a *App) authenticate(c *Ctx) (*Actor, error) {
	if h := c.R.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		plain := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		actor, err := a.actorForToken(c.R.Context(), plain, c.ip())
		if err != nil {
			return nil, err
		}
		if actor == nil {
			return nil, &apierr.Error{Status: 401, Code: "unauthorized", Message: "That token isn't valid: it was rotated, revoked, or never existed."}
		}
		return actor, nil
	}
	// Cookie's only error is http.ErrNoCookie: no session.
	cookie, _ := c.R.Cookie(sessionCookie)
	if cookie == nil || cookie.Value == "" {
		return nil, nil
	}
	actor, err := a.actorForSession(c.R.Context(), cookie.Value)
	if err != nil || actor == nil {
		return nil, err
	}
	// A write signed in by the browser's cookie must carry the session's
	// CSRF token, or a sibling site's page could act as whoever is signed in.
	if c.R.Method != http.MethodGet && c.R.Method != http.MethodHead {
		if subtle.ConstantTimeCompare([]byte(c.R.Header.Get("X-CSRF-Token")), []byte(actor.Session.CSRF)) != 1 {
			return nil, &apierr.Error{Status: 422, Code: "invalid_authenticity_token", Message: "A write signed in by the browser's session needs the page's CSRF token (X-CSRF-Token), or use a Bearer token."}
		}
	}
	return actor, nil
}

func (a *App) actorForToken(ctx context.Context, plain, ip string) (*Actor, error) {
	if plain == "" {
		return nil, nil
	}
	d := secret.Digest(plain)
	db := a.store.DB
	var tokenID, userID int64
	var lastUsed store.Time
	err := db.QueryRowContext(ctx, `SELECT id, user_id, last_used_at FROM api_tokens WHERE token_digest = ?`, d).Scan(&tokenID, &userID, &lastUsed)
	if err == nil {
		actor, err := a.actorForUser(ctx, userID)
		if err != nil || actor == nil {
			return actor, err
		}
		actor.TokenID = tokenID
		if !lastUsed.Valid || time.Since(lastUsed.Time) > time.Minute {
			a.touch(ctx, `UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, store.Now().DB(), ip, tokenID)
		}
		return actor, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var st struct {
		id, roleID int64
		name       string
	}
	err = db.QueryRowContext(ctx, `SELECT id, role_id, name, last_used_at FROM service_tokens WHERE token_digest = ? AND revoked_at IS NULL`, d).Scan(&st.id, &st.roleID, &st.name, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	role, err := a.role(ctx, st.roleID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if !lastUsed.Valid || time.Since(lastUsed.Time) > time.Minute {
		a.touch(ctx, `UPDATE service_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, store.Now().DB(), ip, st.id)
	}
	return &Actor{ServiceID: st.id, Name: st.name, Role: role}, nil
}

// touch records a last-used time. It's bookkeeping: a failure is logged,
// and the request it came with goes on.
func (a *App) touch(ctx context.Context, query string, args ...any) {
	if _, err := a.store.ExecContext(ctx, query, args...); err != nil {
		a.log.Warn("touch", "err", err)
	}
}

func (a *App) actorForUser(ctx context.Context, userID int64) (*Actor, error) {
	var actor Actor
	var roleID store.NullInt
	err := a.store.QueryRowContext(ctx, `SELECT id, name, email, role_id FROM users WHERE id = ?`, userID).Scan(&actor.UserID, &actor.Name, &actor.Email, &roleID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if roleID.Valid {
		role, err := a.role(ctx, roleID.Int64)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		actor.Role = role
	}
	return &actor, nil
}

func (a *App) role(ctx context.Context, id int64) (*Role, error) {
	var r Role
	var perms store.JSON
	err := a.store.QueryRowContext(ctx, `SELECT id, name, COALESCE(description, ''), permissions, system, created_at, updated_at FROM roles WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Description, &perms, &r.System, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, store.NotFound(err)
	}
	r.Permissions = []string{}
	for _, p := range perms.List() {
		r.Permissions = append(r.Permissions, str(p))
	}
	return &r, nil
}

// roleByName finds a role; "Administrator" (or "Admin") is the system role
// whatever it's called, as a Rails install names it "Admin".
func (a *App) roleByName(ctx context.Context, name string) (*Role, error) {
	var id int64
	err := a.store.QueryRowContext(ctx, `SELECT id FROM roles WHERE lower(name) = lower(?)`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) && (strings.EqualFold(name, "Administrator") || strings.EqualFold(name, "Admin")) {
		err = a.store.QueryRowContext(ctx, `SELECT id FROM roles WHERE system = 1 ORDER BY id LIMIT 1`).Scan(&id)
	}
	if err != nil {
		return nil, store.NotFound(err)
	}
	return a.role(ctx, id)
}

// ---- admin sessions ------------------------------------------------------

type adminSession struct {
	ID   int64
	CSRF string
}

func (a *App) actorForSession(ctx context.Context, cookie string) (*Actor, error) {
	var s adminSession
	var userID int64
	var seen store.Time
	err := a.store.QueryRowContext(ctx, `SELECT id, csrf, user_id, last_seen_at FROM admin_sessions WHERE token_digest = ?`, secret.Digest(cookie)).Scan(&s.ID, &s.CSRF, &userID, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	// Two weeks idle signs a session out.
	if seen.Valid && time.Since(seen.Time) > 14*24*time.Hour {
		if _, err := a.store.ExecContext(ctx, `DELETE FROM admin_sessions WHERE id = ?`, s.ID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if !seen.Valid || time.Since(seen.Time) > time.Minute {
		a.touch(ctx, `UPDATE admin_sessions SET last_seen_at = ? WHERE id = ?`, store.Now().DB(), s.ID)
	}
	actor, err := a.actorForUser(ctx, userID)
	if actor != nil {
		actor.Session = &s
	}
	return actor, err
}

// signIn opens a session for the user and sets its cookie.
func (a *App) signIn(c *Ctx, userID int64) (string, error) {
	cookie := secret.Token(32)
	csrf := secret.Token(24)
	_, err := a.store.ExecContext(c.R.Context(), `INSERT INTO admin_sessions (token_digest, csrf, user_id, ip, user_agent, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		secret.Digest(cookie), csrf, userID, c.ip(), c.R.UserAgent(), store.Now().DB(), store.Now().DB())
	if err != nil {
		return "", err
	}
	a.setSessionCookie(c, cookie, 14*24*3600)
	return csrf, nil
}

// setSessionCookie sets the session cookie (maxAge -1 clears it): HttpOnly,
// SameSite=Lax, and Secure whenever the site is reached over HTTPS.
func (a *App) setSessionCookie(c *Ctx, value string, maxAge int) {
	secure := c.R.TLS != nil || (a.cfg.TrustProxy && c.R.Header.Get("X-Forwarded-Proto") == "https") || strings.HasPrefix(a.BaseURL(), "https://")
	//nolint:gosec // Secure over HTTPS only: a plain-HTTP localhost install couldn't sign in otherwise
	http.SetCookie(c.W, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}
