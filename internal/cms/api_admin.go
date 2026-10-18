package cms

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/fields"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func (a *App) adminRoutes(h func(string, handlerFunc, ...func(*route))) {
	h("GET /api/redirects", a.edgeRedirects)
	h("GET /api/redirects/all", a.allRedirects)
	h("GET /api/redirects/resolve", a.resolveRedirect)
	h("GET /api/redirects/export", a.exportRedirects)
	h("POST /api/redirects/import", a.importRedirects)
	h("POST /api/redirects", a.createRedirect)
	h("PATCH /api/redirects/{id}", a.updateRedirect)
	h("PUT /api/redirects/{id}", a.updateRedirect)
	h("DELETE /api/redirects/{id}", a.destroyRedirect)

	h("GET /api/settings", a.listSettings)
	h("POST /api/settings", a.createSetting)
	h("GET /api/settings/{key}", a.showSetting)
	h("PATCH /api/settings/{key}", a.updateSetting)
	h("POST /api/settings/email/test", a.sendTestMail)
	h("PUT /api/settings/{key}", a.updateSetting)
	h("DELETE /api/settings/{key}", a.destroySetting)

	h("GET /api/deploy", a.showDeploy)
	h("PATCH /api/deploy", a.updateDeploy)
	h("POST /api/deploy/trigger", a.triggerDeploy)
	h("GET /api/frontend", a.showFrontend)
	h("POST /api/frontend/builds", a.reportBuild)

	h("GET /api/audit_log", a.listAudit)

	h("GET /api/trash", a.listTrash)
	h("POST /api/trash/{kind}/{id}/restore", a.restoreTrash)
	h("DELETE /api/trash/{kind}/{id}", a.purgeTrash)

	h("GET /api/webhooks", a.listWebhooks)
	h("POST /api/webhooks", a.createWebhook)
	h("GET /api/webhooks/{id}", a.showWebhook)
	h("PATCH /api/webhooks/{id}", a.updateWebhook)
	h("PUT /api/webhooks/{id}", a.updateWebhook)
	h("DELETE /api/webhooks/{id}", a.destroyWebhook)
	h("POST /api/webhooks/{id}/rotate_secret", a.rotateWebhookSecret)
	h("POST /api/webhooks/{id}/test", a.testWebhook)
	h("GET /api/webhooks/{id}/deliveries", a.webhookDeliveries)

	h("GET /api/service_tokens", a.listServiceTokens)
	h("POST /api/service_tokens", a.createServiceToken)
	h("GET /api/service_tokens/{id}", a.showServiceToken)
	h("POST /api/service_tokens/{id}/reveal", a.revealServiceToken)
	h("POST /api/service_tokens/{id}/rotate", a.rotateServiceToken)
	h("POST /api/service_tokens/{id}/revoke", a.revokeServiceToken)

	h("GET /api/users", a.listUsers)
	h("POST /api/users", a.createUser)
	h("PATCH /api/users/{id}", a.updateUser)
	h("DELETE /api/users/{id}", a.destroyUser)
	h("GET /api/roles", a.listRoles)
	h("POST /api/roles", a.createRole)
	h("PATCH /api/roles/{id}", a.updateRole)
	h("DELETE /api/roles/{id}", a.destroyRole)

	h("POST /api/search", a.search)
	h("GET /api/search", a.search)
	h("GET /api/references", a.references)
	h("GET /api/sitemap", a.managementSitemap)
	h("GET /api/site_health", a.siteHealth)
	h("GET /api/tools/backup", a.backup)
	a.importRoutes(h)
	a.updateRoutes(h)

	h("POST /api/device/code", a.deviceCode, open)
	h("POST /api/device/token", a.deviceToken, open)
	h("POST /api/device/approve", a.deviceApprove)
	h("GET /api/device/{code}", a.deviceShow)

	h("GET /api/session", a.showSession, open)
	h("POST /api/session", a.createSession, open)
	h("DELETE /api/session", a.destroySession)
	h("POST /api/setup", a.setup, open)
	h("POST /api/password/forgot", a.forgotPassword, open)
	h("POST /api/password/reset", a.resetPassword, open)
	h("POST /api/email/confirm", a.confirmEmail, open)
	h("DELETE /api/users/{id}/pending_email", a.cancelEmailChange)
	h("POST /api/locks", a.acquireLock)
	h("DELETE /api/locks/{type}/{id}", a.releaseLock)
	h("GET /api/autosaves", a.showAutosave)
	h("PUT /api/autosaves", a.putAutosave)
	h("DELETE /api/autosaves", a.deleteAutosave)
	h("GET /api/me/preferences", a.showPreferences)
	h("PATCH /api/me/preferences", a.updatePreferences)
	h("GET /api/site_health/info", a.siteHealthInfo)

	a.pluginRoutes(h)
}

// ---- identity ------------------------------------------------------------

func (a *App) getMe(c *Ctx) error {
	ctx := c.R.Context()
	body := O()
	var token any
	if c.Actor.TokenID != 0 {
		token = a.tokenRow(ctx, "api_tokens", c.Actor.TokenID)
	} else if c.Actor.ServiceID != 0 {
		token = a.tokenRow(ctx, "service_tokens", c.Actor.ServiceID)
	}
	caps := []string{}
	if c.Actor.Role != nil {
		caps = a.capabilitiesOf(c.Actor.Role)
	}
	body = append(body, kv{"token", token}, kv{"capabilities", caps})
	if c.Actor.ServiceID != 0 {
		roleName := ""
		if c.Actor.Role != nil {
			roleName = c.Actor.Role.Name
		}
		body = append(body, kv{"service", O("id", c.Actor.ServiceID, "name", c.Actor.Name, "role", roleName)})
	} else {
		body = append(body, kv{"user", O("id", c.Actor.UserID, "name", c.Actor.Name, "email", c.Actor.Email)})
	}
	c.Summary = fmt.Sprintf("%s, with %d capabilities.", c.Actor.Label(), len(caps))
	c.Crumbs = []Crumb{crumb("The shape of this CMS", "cms manifest"), crumb("The house voice, before writing copy", "cms brand")}
	return c.JSON(200, body)
}

func (a *App) tokenRow(ctx context.Context, table string, id int64) Obj {
	var prefix string
	var created, used store.Time
	var ip sql.NullString
	if a.store.QueryRowContext(ctx, `SELECT prefix, created_at, last_used_at, last_used_ip FROM `+table+` WHERE id = ?`, id).Scan(&prefix, &created, &used, &ip) != nil {
		return nil
	}
	return O("id", id, "prefix", prefix, "created_at", created.ISO(), "last_used_at", used.ISO(), "last_used_ip", nullStr(ip))
}

func nullStr(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// mintUserToken replaces the user's personal token (one per user: rotating
// is revoking) and returns the plaintext.
func (a *App) mintUserToken(ctx context.Context, userID int64) (string, int64, error) {
	plain := "lp_" + secret.Token(32)
	now := store.Now().DB()
	var id int64
	err := a.store.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, userID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (user_id, prefix, token, token_digest, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
			userID, plain[:11], a.secrets.Seal(plain), secret.Digest(plain), now, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return plain, id, err
}

// userToken is the user's token as it stands, minting one when there's none
// or it can't be shown again.
func (a *App) userToken(ctx context.Context, userID int64) (string, error) {
	var sealed sql.NullString
	err := a.store.QueryRowContext(ctx, `SELECT token FROM api_tokens WHERE user_id = ?`, userID).Scan(&sealed)
	if err == nil && sealed.Valid {
		if plain, ok := a.secrets.Open(sealed.String); ok {
			return plain, nil
		}
	}
	plain, _, err := a.mintUserToken(ctx, userID)
	return plain, err
}

func (a *App) rotateMyToken(c *Ctx) error {
	if c.Actor.UserID == 0 {
		return &apierr.Error{Status: 403, Code: "forbidden", Message: "A service token is rotated from Settings › Service tokens (cms service-token rotate <id>)."}
	}
	plain, id, err := a.mintUserToken(c.R.Context(), c.Actor.UserID)
	if err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "api_token.rotated", "ApiToken", id, c.Actor.Name, nil)
	c.Summary = "Rotated: the old token stopped working just now."
	return c.JSON(200, O("token", append(a.tokenRow(c.R.Context(), "api_tokens", id), kv{"token", plain})))
}

// ---- manifest ------------------------------------------------------------

func (a *App) getManifest(c *Ctx) error {
	ctx := c.R.Context()
	var countErr error
	count := func(q string, args ...any) int {
		n, err := store.Count(ctx, a.store, q, args...)
		countErr = cmp.Or(countErr, err)
		return n
	}
	bts, err := a.blockTypesList(ctx, "deprecated = 0 ORDER BY category, label")
	if err != nil {
		return err
	}
	blocks := []Obj{}
	for _, b := range bts {
		blocks = append(blocks, O("slug", b.Slug, "label", b.Label, "description", b.Description, "category", b.Category, "icon", b.Icon,
			"version", b.Version, "fields", b.Fields, "defaults", b.Defaults, "json_schema", b.Schema()))
	}
	gs, err := a.globals(ctx, "1=1 ORDER BY name")
	if err != nil {
		return err
	}
	globals := []Obj{}
	for _, g := range gs {
		globals = append(globals, O("slug", g.Slug, "name", g.Name, "description", g.Description, "fields", g.FieldsJSON(), "json_schema", fields.JSONSchema(g.Fields())))
	}
	cols, err := a.collections(ctx, "1=1 ORDER BY slug")
	if err != nil {
		return err
	}
	collections := []Obj{}
	for _, col := range cols {
		n := count(`SELECT count(*) FROM collection_entries WHERE collection_id = ? AND deleted_at IS NULL`, col.ID)
		collections = append(collections, O("slug", col.Slug, "name", col.Name, "enable_blocks", col.EnableBlocks, "api_enabled", col.APIEnabled,
			"fields", col.FieldsJSON(), "frontmatter_json_schema", fields.JSONSchema(col.Fields()), "entry_count", n,
			"categories_collection_id", col.CategoriesCollectionID, "tags_collection_id", col.TagsCollectionID))
	}
	plugins := []Obj{}
	for _, p := range a.plugins.List() {
		plugins = append(plugins, O("key", p.Key, "name", p.Name, "version", p.Version, "enabled", p.Enabled))
	}
	c.Summary = fmt.Sprintf("%d collections, %d block types, %d globals.", len(collections), len(blocks), len(globals))
	c.Crumbs = []Crumb{crumb("Every page", "cms pages"), crumb("The house voice", "cms brand")}
	counts := O("pages", count(`SELECT count(*) FROM pages WHERE deleted_at IS NULL`), "collections", count(`SELECT count(*) FROM collections`),
		"entries", count(`SELECT count(*) FROM collection_entries WHERE deleted_at IS NULL`), "block_types", count(`SELECT count(*) FROM block_types`),
		"globals", count(`SELECT count(*) FROM globals WHERE deleted_at IS NULL`))
	if countErr != nil {
		return countErr
	}
	return c.JSON(200, O(
		"tenant", a.tenant(),
		"generated_at", time.Now().UTC().Format(time.RFC3339),
		"brand", brandBrief(a.settingOr(ctx, "brand")),
		"counts", counts,
		"block_types", blocks,
		"globals", globals,
		"collections", collections,
		"version", a.cfg.Version,
		"plugins", plugins,
	))
}

// brandBrief is the brief the owners wrote: voice, audience, facts, house
// style — the keys that are filled in.
func brandBrief(data map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"brand_voice", "audience", "key_facts", "style_notes"} {
		if v := strings.TrimSpace(str(data[k])); v != "" {
			out[k] = v
		}
	}
	return out
}
