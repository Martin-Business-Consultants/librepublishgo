package cms

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func (a *App) settingJSON(ctx context.Context, key string) (Obj, error) {
	var id int64
	var data store.JSON
	var created, updated store.Time
	err := a.store.QueryRowContext(ctx, `SELECT id, data, created_at, updated_at FROM settings WHERE key = ?`, key).Scan(&id, &data, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierr.NotFound("No setting " + key + ".")
		}
		return nil, err
	}
	return O("id", id, "key", key, "created_at", created, "updated_at", updated, "data", redact(data.Map())), nil
}

func (a *App) listSettings(c *Ctx) error {
	if err := c.Need("settings:read"); err != nil {
		return err
	}
	keys, err := store.Strings(c.R.Context(), a.store.DB, `SELECT key FROM settings ORDER BY key`)
	if err != nil {
		return err
	}
	out := []Obj{}
	for _, k := range keys {
		o, err := a.settingJSON(c.R.Context(), k)
		if isNotFound(err) {
			continue // deleted since the list was read
		} else if err != nil {
			return err
		}
		out = append(out, o)
	}
	return c.JSON(200, O("settings", out))
}

func (a *App) showSetting(c *Ctx) error {
	if err := c.Need("settings:read"); err != nil {
		return err
	}
	key := c.Param("key")
	// The frontend's repo lives under settings/github; reading an unset
	// key is {} rather than a 404, so `librepublish get_setting --key github` answers.
	o, err := a.settingJSON(c.R.Context(), key)
	if err != nil {
		if e, ok := errors.AsType[*apierr.Error](err); ok && e.Status == 404 {
			o = O("id", nil, "key", key, "created_at", nil, "updated_at", nil, "data", map[string]any{})
		} else {
			return err
		}
	}
	return c.JSON(200, O("setting", o))
}

func (a *App) writeSetting(c *Ctx, key string, replace bool) error {
	if err := c.Need("settings:write"); err != nil {
		return err
	}
	if key == "" || strings.ContainsAny(key, " /") {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"key": {"is invalid"}}}
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	data := body
	if s, ok := body["setting"].(map[string]any); ok {
		data = s
		if k := str(s["key"]); k != "" && key == "" {
			key = k
		}
	}
	if d, ok := data["data"].(map[string]any); ok {
		data = d
	}
	if _, err := a.putSetting(c.R.Context(), c.Actor, key, data, replace); err != nil {
		return err
	}
	o, err := a.settingJSON(c.R.Context(), key)
	if err != nil {
		return err
	}
	return c.JSON(200, O("setting", o))
}

func (a *App) createSetting(c *Ctx) error {
	if err := c.Need("settings:write"); err != nil {
		return err
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	s, _ := body["setting"].(map[string]any)
	key := str(s["key"])
	data, _ := s["data"].(map[string]any)
	if key == "" {
		return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"key": {"can't be blank"}}}
	}
	if _, err := a.putSetting(c.R.Context(), c.Actor, key, data, true); err != nil {
		return err
	}
	o, err := a.settingJSON(c.R.Context(), key)
	if err != nil {
		return err
	}
	return c.JSON(201, O("setting", o))
}

func (a *App) updateSetting(c *Ctx) error {
	return a.writeSetting(c, c.Param("key"), c.R.Method == http.MethodPut)
}

func (a *App) destroySetting(c *Ctx) error {
	if err := c.Need("settings:write"); err != nil {
		return err
	}
	if _, err := a.store.ExecContext(c.R.Context(), `DELETE FROM settings WHERE key = ? AND key != 'general'`, c.Param("key")); err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "setting.deleted", "Setting", 0, c.Param("key"), nil)
	return c.NoContent()
}
