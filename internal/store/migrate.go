package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// migration is one numbered change to the schema, run once, in a
// transaction, when PRAGMA user_version says it hasn't been. Write it so
// it's safe on a database that already has the change (addColumn checks).
type migration func(ctx context.Context, tx *sql.Tx) error

// migrations are the numbered changes, oldest first; user_version is how
// many have run. Only ever append.
var migrations = []migration{
	// 1: plugins are Go compiled into the binary, not WebAssembly modules
	// kept in the database.
	func(ctx context.Context, tx *sql.Tx) error { return dropColumn(ctx, tx, "plugins", "wasm") },
}

// Version is the schema version this build migrates to (PRAGMA
// user_version): a database above it was written by a newer build.
func Version() int { return len(migrations) }

// Tables are the tables this build's schema has, sorted.
func Tables() []string {
	tables, err := schemaTables()
	if err != nil {
		return nil
	}
	return slices.Sorted(maps.Keys(tables))
}

// migrate brings db up to this build's schema: the columns existing tables
// lack, then schema.sql (new tables, indexes), then the numbered
// migrations not yet run.
func migrate(ctx context.Context, db *sql.DB, list []migration) error {
	if err := setAsideRailsTables(ctx, db); err != nil {
		return err
	}
	if err := ensureColumns(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(list) {
		return fmt.Errorf("schema: the database is at version %d, newer than this build knows (%d): run a newer librepublish", version, len(list))
	}
	for i := version; i < len(list); i++ {
		err := inTx(ctx, db, func(tx *sql.Tx) error {
			// Read again under the write lock: another process starting
			// on the same database may have run it since.
			var now int
			if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&now); err != nil {
				return err
			}
			if now > i {
				return nil
			}
			if err := list[i](ctx, tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("schema: migration %d: %w", i+1, err)
		}
	}
	return nil
}

func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	return (&DB{DB: db}).Tx(ctx, fn)
}

// column is one of a table's columns, as PRAGMA table_info has it.
type column struct {
	name, typ string
	notNull   bool
	dflt      sql.NullString
	pk        bool
}

func tableColumns(ctx context.Context, db Querier, table string) ([]column, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	return ScanAll(rows, func(r *sql.Rows) (column, error) {
		var c column
		var pk int
		err := r.Scan(&c.name, &c.typ, &c.notNull, &c.dflt, &pk)
		c.pk = pk > 0
		return c, err
	})
}

// schemaTables is schema.sql's tables and their columns, read from a
// scratch in-memory database it's run on.
var schemaTables = sync.OnceValues(func() (map[string][]column, error) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	names, err := Strings(ctx, db, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	out := map[string][]column{}
	for _, name := range names {
		if out[name], err = tableColumns(ctx, db, name); err != nil {
			return nil, err
		}
	}
	return out, nil
})

// railsOnly are tables the Rails core has under a name this schema uses for
// something else, each told apart by a column only the Rails one has. They're
// renamed out of the way (kept, with their rows) so schema.sql can create
// this core's own.
var railsOnly = []struct{ table, marker, renameTo string }{
	// The Rails core's pending API changes (revisable_type/_id, state,
	// payload), not this core's revisions (record_type/_id, status, changes).
	{"revisions", "revisable_type", "rails_revisions"},
}

func setAsideRailsTables(ctx context.Context, db *sql.DB) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		for _, t := range railsOnly {
			rails, err := hasColumn(ctx, tx, t.table, t.marker)
			if err != nil || !rails {
				if err != nil {
					return err
				}
				continue
			}
			taken, err := Count(ctx, tx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, t.renameTo)
			if err != nil {
				return err
			}
			if taken > 0 {
				return fmt.Errorf("schema: %s is the Rails core's and %s, where it would be set aside, already exists", t.table, t.renameTo)
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, quoteIdent(t.table), quoteIdent(t.renameTo))); err != nil {
				return fmt.Errorf("schema: setting aside %s: %w", t.table, err)
			}
		}
		return nil
	})
}

// ensureColumns adds the columns schema.sql declares that an existing table
// lacks (an older database, or one the Rails core wrote before the column
// existed). One that can't be added in place (NOT NULL without a default,
// part of the key) stops the start, saying which.
func ensureColumns(ctx context.Context, db *sql.DB) error {
	want, err := schemaTables()
	if err != nil {
		return err
	}
	return inTx(ctx, db, func(tx *sql.Tx) error {
		existing, err := Strings(ctx, tx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
		if err != nil {
			return err
		}
		var problems []error
		for _, table := range existing {
			if want[table] == nil {
				continue
			}
			cols, err := tableColumns(ctx, tx, table)
			if err != nil {
				return err
			}
			for _, c := range want[table] {
				if slices.ContainsFunc(cols, func(have column) bool { return strings.EqualFold(have.name, c.name) }) {
					continue
				}
				if c.pk || c.notNull && !c.dflt.Valid {
					problems = append(problems, fmt.Errorf("%s.%s", table, c.name))
					continue
				}
				if err := addColumn(ctx, tx, table, c.name, c.definition()); err != nil {
					return err
				}
			}
		}
		if len(problems) > 0 {
			return fmt.Errorf("schema: the database lacks columns that can't be added in place (NOT NULL without a default, or a key): %w", errors.Join(problems...))
		}
		return nil
	})
}

// definition is what ALTER TABLE … ADD COLUMN needs after the name.
func (c column) definition() string {
	def := c.typ
	if c.dflt.Valid {
		def += " DEFAULT " + c.dflt.String
	}
	if c.notNull {
		def += " NOT NULL"
	}
	return def
}

func hasColumn(ctx context.Context, db Querier, table, name string) (bool, error) {
	n, err := Count(ctx, db, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, name)
	return n > 0, err
}

// dropColumn drops table.name if it's there: what a migration removing a
// column uses.
func dropColumn(ctx context.Context, db Querier, table, name string) error {
	if have, err := hasColumn(ctx, db, table, name); err != nil || !have {
		return err
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s DROP COLUMN %s`, quoteIdent(table), quoteIdent(name)))
	if err != nil {
		return fmt.Errorf("schema: dropping %s.%s: %w", table, name, err)
	}
	return nil
}

// addColumn adds table.name (def is its type and constraints) unless it's
// already there: what a migration adding a column uses.
func addColumn(ctx context.Context, db Querier, table, name, def string) error {
	if have, err := hasColumn(ctx, db, table, name); err != nil || have {
		return err
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, quoteIdent(table), quoteIdent(name), def))
	if err != nil {
		return fmt.Errorf("schema: adding %s.%s: %w", table, name, err)
	}
	return nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
