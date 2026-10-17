package cms

import (
	"context"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func (a *App) audit(ctx context.Context, actor *Actor, action, targetType string, targetID int64, targetLabel string, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	var actorID, actorType any
	label := "system"
	if actor != nil {
		actorID, actorType, label = actor.ID(), actor.Type(), actor.Label()
	}
	var tid any
	if targetID != 0 {
		tid = targetID
	}
	_, err := a.store.ExecContext(context.WithoutCancel(ctx), `INSERT INTO audit_logs (action, actor_id, actor_type, actor_label, target_id, target_type, target_label, metadata, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, action, actorID, actorType, label, tid, targetType, targetLabel, string(store.MustJSON(meta)), store.Now().DB())
	if err != nil {
		a.log.Warn("audit", "err", err)
	}
}
