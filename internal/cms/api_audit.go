package cms

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func (a *App) listAudit(c *Ctx) error {
	if err := c.Need("audit_log:read"); err != nil {
		return err
	}
	where, args := []string{"1=1"}, []any{}
	if v := c.Query("actor"); v != "" {
		where, args = append(where, "actor_label LIKE ?"), append(args, "%"+v+"%")
	}
	if v := c.Query("action"); v != "" {
		where, args = append(where, "action LIKE ?"), append(args, v+"%")
	}
	if v := c.Query("target_type"); v != "" {
		where, args = append(where, "target_type = ?"), append(args, v)
	}
	page, per := c.paging(50, 200)
	cond := strings.Join(where, " AND ")
	total, err := store.Count(c.R.Context(), a.store, "SELECT count(*) FROM audit_logs WHERE "+cond, args...)
	if err != nil {
		return err
	}
	rows, err := a.store.QueryContext(c.R.Context(), `SELECT id, action, actor_type, actor_id, actor_label, target_type, target_id, target_label, metadata, ip, created_at
		FROM audit_logs WHERE `+cond+fmt.Sprintf(" ORDER BY id DESC LIMIT %d OFFSET %d", per, (page-1)*per), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []Obj{}
	for rows.Next() {
		var id int64
		var action, actorLabel, targetLabel string
		var actorType, targetType, ip sql.NullString
		var actorID, targetID store.NullInt
		var meta store.JSON
		var at store.Time
		if err := rows.Scan(&id, &action, &actorType, &actorID, &actorLabel, &targetType, &targetID, &targetLabel, &meta, &ip, &at); err != nil {
			return err
		}
		out = append(out, O("id", id, "action", action, "actor", O("type", nullStr(actorType), "id", actorID, "label", actorLabel),
			"target", O("type", nullStr(targetType), "id", targetID, "label", targetLabel), "metadata", meta.Or("{}"), "ip", nullStr(ip), "created_at", at))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(200, O("audit_logs", out, "page", page, "per", per, "total", total))
}
