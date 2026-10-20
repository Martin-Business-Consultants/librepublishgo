package cms

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// setting reads one settings key's data ({} when it isn't set), its
// secrets opened. Any other failure is an error, never {}: a write merging
// into {} would wipe the key.
func (a *App) setting(ctx context.Context, key string) (map[string]any, error) {
	m, err := a.storedSetting(ctx, key)
	if err != nil {
		return nil, err
	}
	a.openSecrets(m)
	return m, nil
}

// storedSetting is a key's data as it's stored: its secrets sealed.
func (a *App) storedSetting(ctx context.Context, key string) (map[string]any, error) {
	var data store.JSON
	err := a.store.QueryRowContext(ctx, `SELECT data FROM settings WHERE key = ?`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("setting %s: %w", key, err)
	}
	m := map[string]any{}
	if len(data) > 0 && string(data) != "null" {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("setting %s: %w", key, err)
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// settingOr is setting for what only reads (a page's title, CORS origins):
// a failure is logged and reads as {}.
func (a *App) settingOr(ctx context.Context, key string) map[string]any {
	m, err := a.setting(ctx, key)
	if err != nil {
		a.log.Error("setting", "key", key, "err", err)
		return map[string]any{}
	}
	return m
}

// general is Settings › General, kept from the first read until a write
// replaces it: serving one request reads it several times.
func (a *App) general(ctx context.Context) map[string]any {
	c := &a.generalCache
	c.mu.Lock()
	if c.data != nil {
		defer c.mu.Unlock()
		return maps.Clone(c.data)
	}
	gen := c.gen
	c.mu.Unlock()
	g, err := a.setting(ctx, "general")
	if err != nil {
		a.log.Error("setting", "key", "general", "err", err)
		return map[string]any{}
	}
	c.mu.Lock()
	if c.gen == gen { // nothing was written while it was read
		c.data = g
	}
	c.mu.Unlock()
	return maps.Clone(g)
}

// settingCache is one settings key's data, as last read.
type settingCache struct {
	mu   sync.Mutex
	gen  uint64
	data map[string]any
}

// forget drops what's kept: the key was written.
func (c *settingCache) forget() {
	c.mu.Lock()
	c.gen++
	c.data = nil
	c.mu.Unlock()
}

func (a *App) defaultLocale() string {
	if l := str(a.general(a.ctx)["default_locale"]); l != "" {
		return l
	}
	return "en"
}

// putSetting merges data into a key (null removes a key), keeping what a
// masked secret written back would otherwise overwrite, and seals its
// secrets. The branding is checked and replaced whole (cleanBranding).
func (a *App) putSetting(ctx context.Context, actor *Actor, key string, data map[string]any, replace bool) (map[string]any, error) {
	if key == brandingKey {
		clean, err := a.cleanBranding(ctx, data)
		if err != nil {
			return nil, err
		}
		data, replace = clean, true
	}
	old, err := a.storedSetting(ctx, key)
	if err != nil {
		return nil, err
	}
	data = a.unmaskURLs(old, data)
	merged := map[string]any{}
	if !replace {
		maps.Copy(merged, old)
	}
	keepMasked(merged, old, data)
	dropMovedSecret(key, merged, old, data)
	if key == "general" {
		u, err := cleanCMSURL(str(merged["cms_url"]))
		if err != nil {
			return nil, err
		}
		if u == "" {
			delete(merged, "cms_url")
		} else {
			merged["cms_url"] = u
		}
	}
	stored := a.sealSecrets(merged)
	now := store.Now().DB()
	_, err = a.store.ExecContext(ctx, `INSERT INTO settings (key, data, created_at, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, key, string(store.MustJSON(stored)), now, now)
	if err != nil {
		return nil, err
	}
	a.audit(ctx, actor, "setting.updated", "Setting", 0, key, map[string]any{"keys": keysOf(data)})
	a.openSecrets(merged)
	if key == "general" {
		a.generalCache.forget()
		a.setCMSURL(str(merged["cms_url"]))
	}
	if key == "general" || key == brandingKey {
		a.publish(ctx, change{Tags: []string{"site"}})
	}
	return merged, nil
}

// setCMSURL keeps Settings › General's CMS address for BaseURL.
func (a *App) setCMSURL(u string) {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	a.cmsURL.Store(&u)
}

// cleanCMSURL is the CMS address a person typed, as the base URLs start
// with: an absolute http(s) address, no query or fragment, no trailing
// slash; "" for none.
func cleanCMSURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		inv := apierr.Invalid{}
		inv.Add("cms_url", "must be this CMS's address, like https://cms.example.com")
		return "", inv.Err()
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func keepMasked(into, old, data map[string]any) {
	for k, v := range data {
		switch x := v.(type) {
		case nil:
			delete(into, k)
		case string:
			if strings.HasPrefix(x, "***") && (secretKey(k) || bearerURLKey(k)) {
				if ov, ok := old[k]; ok {
					into[k] = ov
				}
				continue
			}
			into[k] = x
		case map[string]any:
			sub, _ := old[k].(map[string]any)
			if sub == nil {
				sub = map[string]any{}
			}
			next := map[string]any{}
			maps.Copy(next, sub)
			keepMasked(next, sub, x)
			into[k] = next
		default:
			into[k] = v
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// secretPattern is the names a secret goes by: a setting's, a global
// field's, a header's (X-Api-Key, Authorization, Cookie, X-Auth-Token).
var secretPattern = regexp.MustCompile(`(?i)(token|secret|passw(or)?d|api[-_]?key|private[-_]?key|access[-_]?key|credential|authorization|bearer|(^|-)cookie$|(^|[-_])auth($|[-_]))`)

func secretKey(k string) bool { return secretPattern.MatchString(k) }

// bearerURLKey is whether k names an address that is itself the secret:
// a build hook runs a deploy for anyone who has it.
func bearerURLKey(k string) bool { return k == "hook_url" }

// mask is a secret as it's shown: "***" and the last four characters, or
// just "***" when it's too short to show any of.
func mask(s string) string {
	if len(s) <= 4 {
		return "***"
	}
	return "***" + s[len(s)-4:]
}

// maskURL is a bearer address as it's shown: where it goes, and the last
// four characters.
func maskURL(s string) string {
	tail := ""
	if len(s) > 12 {
		tail = s[len(s)-4:]
	}
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host + "/…" + tail
	}
	return "***" + tail
}

// redact masks secret-looking values.
func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if s, ok := val.(string); ok && secretKey(k) && s != "" {
				out[k] = mask(s)
			} else {
				out[k] = redact(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redact(val)
		}
		return out
	}
	return v
}

// redactURLs masks bearer addresses, for who can't change them.
func redactURLs(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if s, ok := val.(string); ok && bearerURLKey(k) && s != "" {
				out[k] = maskURL(s)
			} else {
				out[k] = redactURLs(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redactURLs(val)
		}
		return out
	}
	return v
}

// unmaskURLs is data with a bearer address written back as it was shown
// (maskURL) read as "keep what's there".
func (a *App) unmaskURLs(old, data map[string]any) map[string]any {
	var out map[string]any
	for k, v := range data {
		s, ok := v.(string)
		was, had := old[k].(string)
		if !ok || !had || !bearerURLKey(k) || s == "" || s != maskURL(a.secrets.Reveal(was)) {
			continue
		}
		if out == nil {
			out = maps.Clone(data)
		}
		out[k] = "***"
	}
	if out == nil {
		return data
	}
	return out
}

// pairedSecrets are the secrets sent to an address the same key holds:
// the key, the address's field, the secret's.
var pairedSecrets = map[string][2]string{
	"email":    {"smtp_host", "smtp_password"},
	"frontend": {"webhook_url", "secret"},
}

// dropMovedSecret forgets a secret whose address moved, unless the write
// gave a new one: kept, it would go to whoever chose the new address.
func dropMovedSecret(key string, merged, old, data map[string]any) {
	p, ok := pairedSecrets[key]
	if !ok {
		return
	}
	was := hostOf(old[p[0]])
	if was == "" || hostOf(merged[p[0]]) == was {
		return
	}
	if s, ok := data[p[1]].(string); ok && s != "" && !strings.HasPrefix(s, "***") && !secret.Sealed(s) {
		return
	}
	delete(merged, p[1])
}

// hostOf is where an address (a URL, or a bare host name) goes, to compare.
func hostOf(v any) string {
	s := strings.TrimSpace(str(v))
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return strings.ToLower(u.Host)
	}
	return strings.ToLower(s)
}

// sealSecrets is m with every secret in it sealed (secret.Seal), as
// settings keep them; one already sealed, or masked, is left as it is.
func (a *App) sealSecrets(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok && s != "" && (secretKey(k) || bearerURLKey(k)) && !strings.HasPrefix(s, "***") && !secret.Sealed(s) {
			out[k] = a.secrets.Seal(s)
		} else {
			out[k] = a.sealIn(v)
		}
	}
	return out
}

func (a *App) sealIn(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return a.sealSecrets(x)
	case []any:
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = a.sealIn(el)
		}
		return out
	}
	return v
}

// openSecrets opens, in place, every sealed value in v; one another key
// sealed reads as "".
func (a *App) openSecrets(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, el := range x {
			if s, ok := el.(string); ok && secret.Sealed(s) {
				x[k] = a.secrets.Reveal(s)
			} else {
				a.openSecrets(el)
			}
		}
	case []any:
		for i, el := range x {
			if s, ok := el.(string); ok && secret.Sealed(s) {
				x[i] = a.secrets.Reveal(s)
			} else {
				a.openSecrets(el)
			}
		}
	}
}

// sealStoredSecrets seals the secrets settings kept before they were
// sealed (or that an import brought in plain), leaving updated_at as it
// was: nothing a reader sees has changed.
func (a *App) sealStoredSecrets(ctx context.Context) error {
	keys, err := store.Strings(ctx, a.store.DB, `SELECT key FROM settings`)
	if err != nil {
		return err
	}
	for _, key := range keys {
		old, err := a.storedSetting(ctx, key)
		if err != nil {
			return err
		}
		sealed := store.MustJSON(a.sealSecrets(old))
		if string(sealed) == string(store.MustJSON(old)) {
			continue
		}
		if _, err := a.store.ExecContext(ctx, `UPDATE settings SET data = ? WHERE key = ?`, string(sealed), key); err != nil {
			return err
		}
	}
	return nil
}

// deploySettings is the deploy's view of the settings: what it records, it
// writes as the system.
type deploySettings struct{ a *App }

// Setting is the settings key's data.
func (s deploySettings) Setting(ctx context.Context, key string) (map[string]any, error) {
	return s.a.setting(ctx, key)
}

// ReplaceSetting writes data as the settings key, whole.
func (s deploySettings) ReplaceSetting(ctx context.Context, key string, data map[string]any) error {
	_, err := s.a.putSetting(ctx, nil, key, data, true)
	return err
}
