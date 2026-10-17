package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/datalock"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/restore"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// backup is a Go backup of an empty site.
func backup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "cms.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "cms.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "backup.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "cms.sqlite3", Mode: 0o600, Size: int64(len(raw)), Typeflag: tar.TypeReg})
	tw.Write(raw)
	tw.Close()
	gz.Close()
	return path
}

func TestImportWhileServing(t *testing.T) {
	data, archive := t.TempDir(), backup(t)
	t.Setenv("LOG_LEVEL", "error")
	lock, err := datalock.Acquire(data)
	if err != nil {
		t.Fatal(err)
	}
	// A server has the data directory: a second one and an import are
	// refused (75, try later), a dry run isn't.
	err = run(t.Context(), func() {}, []string{"serve", "--data", data, "--addr", "127.0.0.1:0"})
	if !errors.Is(err, datalock.ErrLocked) {
		t.Fatalf("serve: %v", err)
	}
	err = run(t.Context(), func() {}, []string{"import", archive, "--data", data})
	if !errors.Is(err, datalock.ErrLocked) || exitCode(err) != exitTempFail {
		t.Fatalf("import: %v", err)
	}
	if err := run(t.Context(), func() {}, []string{"import", "--dry-run", "--data", data, archive}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "cms.sqlite3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the dry run imported")
	}
	lock.Release()

	if err := run(t.Context(), func() {}, []string{"import", archive, "--data", data, "--json"}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "cms.sqlite3")); err != nil {
		t.Fatal("not imported:", err)
	}
	// A backup that isn't one exits 65; usage, 2.
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	os.WriteFile(bad, []byte("nope"), 0o600)
	if err := run(t.Context(), func() {}, []string{"import", bad, "--data", t.TempDir()}); !errors.Is(err, restore.ErrInvalid) || exitCode(err) != exitDataErr {
		t.Fatalf("bad: %v", err)
	}
	if err := run(t.Context(), func() {}, []string{"import", "--data", data}); exitCode(err) != 2 {
		t.Fatalf("usage: %v", err)
	}
}
