// Package store is the one database (SQLite): opening and migrating it,
// transactions, and the column types every table uses (Time, JSON,
// NullInt).
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// The schema keeps the Rails app's table and column names, so a database the
// Rails core wrote opens here as it is (docs: go/README.md, "Moving over").
// Every statement is CREATE … IF NOT EXISTS: on a Rails database the tables
// are already there and only the Go core's own (revisions, plugins, …) are
// added. A column schema.sql has that an existing table lacks is added
// before it runs (ensureColumns), and changes that need more than that are
// numbered migrations (migrations).
//
//go:embed schema.sql
var schemaSQL string

// DB is the one database: content, users, settings, everything.
type DB struct {
	*sql.DB
}

// Open opens (creating it when needed) and migrates the database at path.
func Open(path string) (*DB, error) {
	// SQLite would make it world-readable; it holds password hashes.
	if f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o640); err != nil { //nolint:gosec // the operator's own --data path
		return nil, err
	} else if err := f.Close(); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	// As many kept idle, so a burst doesn't close and reopen connections
	// (each one re-runs the pragmas).
	db.SetMaxIdleConns(8)
	if err := migrate(context.Background(), db, migrations); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &DB{DB: db}, nil
}

// Tx runs fn in one write transaction.
func (s *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// After Commit this is a no-op; before it (an error, a panic) it undoes.
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ScanAll reads every row with scan, closes rows, and reports the first
// error, the iteration's included.
func ScanAll[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Int64s is a one-column query's integers.
func Int64s(ctx context.Context, db Querier, query string, args ...any) ([]int64, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return ScanAll(rows, func(r *sql.Rows) (int64, error) {
		var v int64
		err := r.Scan(&v)
		return v, err
	})
}

// Strings is a one-column query's strings.
func Strings(ctx context.Context, db Querier, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return ScanAll(rows, func(r *sql.Rows) (string, error) {
		var v string
		err := r.Scan(&v)
		return v, err
	})
}

// Count is a SELECT count(*)'s answer.
func Count(ctx context.Context, db Querier, query string, args ...any) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// Querier is what both *sql.DB and *sql.Tx answer to.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ErrNotFound is a lookup that found nothing.
var ErrNotFound = errors.New("not found")

// NotFound is err, with sql.ErrNoRows read as ErrNotFound.
func NotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// IsBusy is whether err is the database being busy or locked past the busy
// timeout: worth trying again shortly, not a fault.
func IsBusy(err error) bool {
	e, ok := errors.AsType[*sqlite.Error](err)
	if !ok {
		return false
	}
	code := e.Code() & 0xff // the primary code under an extended one
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

// Placeholders is n comma-separated question marks, for IN (…).
func Placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
