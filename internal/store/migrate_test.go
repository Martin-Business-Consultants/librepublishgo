package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// openRaw is a database file with the given statements run on it, as an
// older install (or the Rails core) left it.
func openRaw(t *testing.T, stmts string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cms.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), stmts); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenStoreAddsMissingColumns(t *testing.T) {
	t.Parallel()
	// users as it might have been before locale, verified and the
	// recovery codes.
	path := openRaw(t, `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
		email varchar NOT NULL,
		name varchar NOT NULL,
		password_digest varchar NOT NULL,
		role_id integer,
		totp_enabled boolean DEFAULT 0 NOT NULL,
		totp_enabled_at datetime(6),
		totp_last_counter bigint,
		totp_secret varchar,
		created_at datetime(6) NOT NULL,
		updated_at datetime(6) NOT NULL
	);
	INSERT INTO users (email, name, password_digest, created_at, updated_at) VALUES ('a@site.test', 'A', 'x', '2024-01-01', '2024-01-01');`)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	for _, col := range []string{"locale", "verified", "recovery_code_digests"} {
		if ok, err := hasColumn(ctx, db.DB, "users", col); err != nil || !ok {
			t.Fatalf("users.%s wasn't added (%v)", col, err)
		}
	}
	var verified bool
	var codes string
	if err := db.DB.QueryRowContext(t.Context(), `SELECT verified, recovery_code_digests FROM users`).Scan(&verified, &codes); err != nil {
		t.Fatal(err)
	}
	if verified || codes != "[]" {
		t.Fatalf("defaults: verified %v, recovery codes %q", verified, codes)
	}
	// And the Go core's own tables are there too.
	if ok, err := hasColumn(ctx, db.DB, "revisions", "id"); err != nil || !ok {
		t.Fatalf("revisions wasn't created (%v)", err)
	}
}

// A database from when plugins were WebAssembly loses the modules, and
// keeps the plugins' records and storage.
func TestOpenStoreDropsPluginWasm(t *testing.T) {
	t.Parallel()
	path := openRaw(t, `CREATE TABLE plugins (
		id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
		key varchar NOT NULL UNIQUE,
		name varchar NOT NULL,
		version varchar NOT NULL,
		description text,
		manifest json DEFAULT '{}' NOT NULL,
		granted json DEFAULT '{}' NOT NULL,
		enabled boolean DEFAULT 0 NOT NULL,
		wasm blob NOT NULL,
		created_at datetime(6) NOT NULL,
		updated_at datetime(6) NOT NULL
	);
	INSERT INTO plugins (key, name, version, wasm, enabled, created_at, updated_at) VALUES ('forms', 'Forms', '1', x'0061736d', 1, '2024-01-01', '2024-01-01');`)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	if ok, err := hasColumn(ctx, db.DB, "plugins", "wasm"); err != nil || ok {
		t.Fatalf("plugins.wasm is still there (%v)", err)
	}
	if n, err := Count(ctx, db.DB, `SELECT count(*) FROM plugins WHERE key = 'forms' AND enabled`); err != nil || n != 1 {
		t.Fatalf("the plugin's record: %d (%v)", n, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO plugins (key, name, version, created_at, updated_at) VALUES ('visits', 'Visits', '1', '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatal(err)
	}
}

func TestOpenStoreRefusesColumnItCantAdd(t *testing.T) {
	t.Parallel()
	// No email, and email is NOT NULL with no default.
	path := openRaw(t, `CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, name varchar NOT NULL,
		password_digest varchar NOT NULL, created_at datetime(6) NOT NULL, updated_at datetime(6) NOT NULL);`)
	_, err := Open(path)
	if err == nil || !strings.Contains(err.Error(), "users.email") {
		t.Fatalf("want an error naming users.email, got %v", err)
	}
}

func TestMigrationsRunOnce(t *testing.T) {
	t.Parallel()
	path := openRaw(t, `SELECT 1`)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	runs := 0
	list := []migration{
		func(ctx context.Context, tx *sql.Tx) error {
			runs++
			return addColumn(ctx, tx, "redirects", "hits", "integer DEFAULT 0 NOT NULL")
		},
		func(ctx context.Context, tx *sql.Tx) error {
			runs++
			_, err := tx.ExecContext(ctx, `UPDATE redirects SET hits = 1`)
			return err
		},
	}
	for range 2 {
		if err := migrate(ctx, db, list); err != nil {
			t.Fatal(err)
		}
	}
	var version int
	db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version)
	if runs != 2 || version != 2 {
		t.Fatalf("ran %d migrations, user_version %d; want 2 and 2", runs, version)
	}
	// A database a newer build migrated isn't opened by an older one.
	if err := migrate(ctx, db, list[:1]); err == nil {
		t.Fatal("an older build opened a newer database")
	}
}

func TestOpenStoreSetsAsideRailsRevisions(t *testing.T) {
	t.Parallel()
	// The Rails core's revisions: pending API changes, another shape.
	path := openRaw(t, `CREATE TABLE revisions (
		id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
		revisable_type varchar NOT NULL,
		revisable_id integer NOT NULL,
		state varchar DEFAULT 'pending' NOT NULL,
		payload json DEFAULT '{}' NOT NULL,
		created_at datetime(6) NOT NULL,
		updated_at datetime(6) NOT NULL
	);
	INSERT INTO revisions (revisable_type, revisable_id, created_at, updated_at) VALUES ('Page', 1, '2026-01-01', '2026-01-01');`)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	if n, err := Count(ctx, db.DB, `SELECT count(*) FROM rails_revisions`); err != nil || n != 1 {
		t.Fatalf("rails_revisions has %d rows (%v), want the 1 kept", n, err)
	}
	if ok, err := hasColumn(ctx, db.DB, "revisions", "record_type"); err != nil || !ok {
		t.Fatalf("revisions isn't this core's (%v)", err)
	}
	db.Close()
	// Opening again leaves both alone.
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}
