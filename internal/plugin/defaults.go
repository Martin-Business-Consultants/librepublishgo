package plugin

import (
	"context"
	"slices"
)

// EnsureDefaults installs the default plugins this binary has: on first
// run they're granted what they ask for and switched on (a new build
// upgrades them in place, in LoadAll). Someone who removes one has removed
// it: it isn't put back. settings/default_plugins keeps track: {key:
// {"version", "removed"}}.
func (h *Host) EnsureDefaults(ctx context.Context) {
	state, err := h.env.Setting(ctx, "default_plugins")
	if err != nil {
		// Unreadable state isn't "nothing installed": leave things be.
		h.log.Warn("default plugins", "err", err)
		return
	}
	keys := make([]string, 0, len(h.modules))
	for key, m := range h.modules {
		if m.Default {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	changed := false
	for _, key := range keys {
		m := h.modules[key]
		prev, _ := state[key].(map[string]any)
		if prev["removed"] == true {
			continue
		}
		if p := h.Get(key); p != nil {
			if prev["version"] != p.Version {
				state[key] = map[string]any{"version": p.Version}
				changed = true
			}
			continue
		}
		p, err := h.Install(ctx, nil, key)
		if err != nil {
			h.log.Warn("default plugin", "plugin", key, "err", err)
			continue
		}
		on := true
		if _, err := h.Configure(ctx, nil, p.Key, &m.Manifest.Permissions, &on); err != nil {
			h.log.Warn("default plugin", "plugin", key, "err", err)
		}
		state[key] = map[string]any{"version": m.Manifest.Version}
		changed = true
	}
	if changed {
		if err := h.env.ReplaceSetting(ctx, "default_plugins", state); err != nil {
			h.log.Warn("default plugins", "err", err)
		}
	}
}

// forgetDefault records that a default plugin was removed on purpose.
func (h *Host) forgetDefault(ctx context.Context, key string) error {
	if m := h.modules[key]; m == nil || !m.Default {
		return nil
	}
	state, err := h.env.Setting(ctx, "default_plugins")
	if err != nil {
		return err
	}
	state[key] = map[string]any{"removed": true}
	return h.env.ReplaceSetting(ctx, "default_plugins", state)
}
