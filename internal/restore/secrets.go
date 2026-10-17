package restore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// A text column, and whether it may be NULL.
type textColumn struct {
	table, name string
	nullable    bool
}

// textColumns are every table's columns that can hold text, in tables with
// rowids, but for a plugin's module.
func textColumns(ctx context.Context, db store.Querier) ([]textColumn, error) {
	rows, err := db.QueryContext(ctx, `SELECT m.name, c.name, c."notnull" FROM sqlite_master m JOIN pragma_table_info(m.name) c
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' AND m.name <> 'plugins' AND upper(m.sql) NOT LIKE '%WITHOUT ROWID%'
		AND upper(c.type) NOT LIKE '%BLOB%' AND upper(c.type) NOT LIKE '%INT%' AND upper(c.type) NOT IN ('BOOLEAN', 'FLOAT', 'REAL', 'DECIMAL')`)
	if err != nil {
		return nil, err
	}
	return store.ScanAll(rows, func(r *sql.Rows) (textColumn, error) {
		var c textColumn
		var notNull bool
		err := r.Scan(&c.table, &c.name, &notNull)
		c.nullable = !notNull
		return c, err
	})
}

// sealed finds every value sealed with an install's key (secret.Seal's
// "go1:…"), as a column's whole value or a string inside its JSON. One the
// target's key opens is kept; one a source key opens is re-sealed under the
// target's; one no key opens is cleared, never left looking like a value
// that works. Only with write does it change anything.
func sealed(ctx context.Context, db *store.DB, target *secret.Key, sources []*secret.Key, write bool) (SealedCount, error) {
	out := SealedCount{Columns: map[string]int{}}
	cols, err := textColumns(ctx, db)
	if err != nil {
		return out, err
	}
	// What one sealed value becomes, counted.
	decide := func(where, v string) string {
		out.Total++
		if target != nil {
			if _, ok := target.Open(v); ok {
				out.Kept++
				return v
			}
		}
		for _, k := range sources {
			if plain, ok := k.Open(v); ok {
				out.Resealed++
				if target == nil {
					return v // staging: what it would be
				}
				return target.Seal(plain)
			}
		}
		out.Cleared++
		out.Columns[where]++
		return ""
	}
	for _, c := range cols {
		changes, err := sealedIn(ctx, db, c, func(v string) string { return decide(c.table+"."+c.name, v) })
		if err != nil {
			return out, err
		}
		if !write {
			continue
		}
		for _, ch := range changes {
			if _, err := db.ExecContext(ctx, `UPDATE `+quote(c.table)+` SET `+quote(c.name)+` = ? WHERE rowid = ?`, ch.value, ch.rowid); err != nil {
				return out, err
			}
		}
	}
	if len(out.Columns) == 0 {
		out.Columns = nil
	}
	return out, nil
}

// change is a row's new value for a column.
type change struct {
	rowid int64
	value any
}

// sealedIn is what decide makes of column c's sealed values: the rows
// whose value changes, as the whole value or a string in its JSON.
func sealedIn(ctx context.Context, db *store.DB, c textColumn, decide func(string) string) ([]change, error) {
	rows, err := db.QueryContext(ctx, `SELECT rowid, CAST(`+quote(c.name)+` AS TEXT) FROM `+quote(c.table)+` WHERE instr(`+quote(c.name)+`, ?) > 0`, secret.SealedPrefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []change
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		if strings.HasPrefix(v, secret.SealedPrefix) {
			switch next := decide(v); {
			case next == v:
			case next == "" && c.nullable:
				changes = append(changes, change{id, nil})
			default:
				changes = append(changes, change{id, next})
			}
			continue
		}
		// JSON with sealed strings in it (a setting's data).
		var doc any
		if json.Unmarshal([]byte(v), &doc) != nil {
			continue
		}
		changed := false
		doc = walkStrings(doc, func(s string) string {
			if !strings.HasPrefix(s, secret.SealedPrefix) {
				return s
			}
			next := decide(s)
			changed = changed || next != s
			return next
		})
		if changed {
			raw, err := json.Marshal(doc)
			if err != nil {
				return nil, err
			}
			changes = append(changes, change{id, string(raw)})
		}
	}
	return changes, rows.Err()
}

// walkStrings is v with every string in it passed through fn.
func walkStrings(v any, fn func(string) string) any {
	switch x := v.(type) {
	case string:
		return fn(x)
	case map[string]any:
		for k, el := range x {
			x[k] = walkStrings(el, fn)
		}
	case []any:
		for i, el := range x {
			x[i] = walkStrings(el, fn)
		}
	}
	return v
}

// railsEncrypted counts the values Active Record Encryption wrote
// ({"p": …, "h": …}): the Rails core's key would be needed to read them,
// and this core never has it.
func railsEncrypted(ctx context.Context, db *store.DB) (map[string]int, error) {
	cols, err := textColumns(ctx, db)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, c := range cols {
		n, err := store.Count(ctx, db, `SELECT count(*) FROM `+quote(c.table)+` WHERE CAST(`+quote(c.name)+` AS TEXT) LIKE '{"p":%' AND CAST(`+quote(c.name)+` AS TEXT) LIKE '%"h":%'`)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			out[c.table+"."+c.name] = n
		}
	}
	return out, nil
}
