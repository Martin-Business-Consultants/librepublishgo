package restore_test

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/restore"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// A blob whose key is a path ("../../x") comes in with a fresh key that
// names no file, so nothing later builds a path out of it.
func TestBadBlobKeysAreReplaced(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	dbFile := writeFile(t, dir, "main.sqlite3", railsDB(t, ""))
	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO active_storage_blobs (id, key, filename, content_type, metadata, service_name, byte_size, created_at) VALUES (3, '../../../../cms.sqlite3', 'x.txt', 'text/plain', '{}', 'local', 1, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	db.Close()
	archive := writeFile(t, dir, "cms-data.tar", tarball(t, false,
		entry{name: "production.sqlite3", body: readFile(t, dbFile)},
		entry{name: "storage/ab/cd/" + millToken, body: []byte(millBody)},
	))
	rep := apply(t, archive, restore.Options{DataDir: data})
	if rep.Blobs.BadKeys != 1 {
		t.Fatalf("blobs: %+v", rep.Blobs)
	}
	keys, err := store.Strings(t.Context(), openDB(t, data), `SELECT key FROM active_storage_blobs ORDER BY id`)
	if err != nil || len(keys) != 3 || !slices.Equal(keys[:2], []string{millToken, lostToken}) || !regexp.MustCompile(`^[a-z0-9]{28}$`).MatchString(keys[2]) {
		t.Fatalf("keys: %v %v", keys, err)
	}
	if got := readFile(t, filepath.Join(data, "storage", "ab", "cd", millToken)); string(got) != millBody {
		t.Fatalf("blob: %q", got)
	}
}
