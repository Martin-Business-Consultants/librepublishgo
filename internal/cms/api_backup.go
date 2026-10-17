package cms

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/restore"
)

// backup streams a .tar.gz: manifest.json (what made it, and when), a
// consistent copy of the database and every uploaded file. `librepublish
// import`, or Tools › Backup › Import, restores it (docs/import.md).
//
// ?include_secret_key=1 adds secret.key, the key the database's sealed
// values (token plaintexts, webhook secrets, the SMTP password) were sealed
// with, so an import elsewhere can re-seal them. Whoever holds the archive
// then holds those secrets too, so only the system role may ask.
func (a *App) backup(c *Ctx) error {
	if err := c.Need("tools:use"); err != nil {
		return err
	}
	withKey := c.truthy("include_secret_key")
	if withKey && !c.Actor.Role.System {
		return &apierr.Error{Status: 403, Code: "forbidden", Message: "Only the Administrator role can include the secret key in a backup.", Capability: wildcard}
	}
	tmp, err := os.MkdirTemp("", "lp-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, "cms.sqlite3")
	if _, err := a.store.ExecContext(c.R.Context(), `VACUUM INTO ?`, snap); err != nil {
		return err
	}
	root := filepath.Join(a.cfg.DataDir, "storage")
	var files []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	var version int
	if err := a.store.QueryRowContext(c.R.Context(), `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(restore.Manifest{Format: restore.ManifestFormat, Version: 1, CMSVersion: a.cfg.Version,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339), SchemaVersion: version, Blobs: len(files), SecretKey: withKey}, "", "  ")
	if err != nil {
		return err
	}
	a.audit(c.R.Context(), c.Actor, "backup.downloaded", "Site", 0, "backup", map[string]any{"secret_key": withKey})
	c.W.Header().Set("Content-Type", "application/gzip")
	c.W.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="librepublish-%s.tar.gz"`, time.Now().UTC().Format("20060102-150405")))
	gz := gzip.NewWriter(c.W)
	tw := tar.NewWriter(gz)
	addBytes := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	add := func(path, name string) error {
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(st, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path) //nolint:gosec // the snapshot, or a file found walking the storage directory
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}
	// The download has begun: a failure from here can't be a JSON error,
	// so the connection is cut and the client sees a broken archive rather
	// than a short one that looks whole.
	abort := func(err error) {
		a.log.Error("backup", "err", err)
		panic(http.ErrAbortHandler)
	}
	if err := addBytes("manifest.json", manifest); err != nil {
		abort(err)
	}
	if withKey {
		if err := addBytes("secret.key", []byte(a.secrets.Hex()+"\n")); err != nil {
			abort(err)
		}
	}
	if err := add(snap, "cms.sqlite3"); err != nil {
		abort(err)
	}
	for _, p := range files {
		rel, err := filepath.Rel(a.cfg.DataDir, p)
		if err != nil {
			abort(err)
		}
		if err := add(p, filepath.ToSlash(rel)); err != nil {
			abort(err)
		}
	}
	if err := tw.Close(); err != nil {
		abort(err)
	}
	if err := gz.Close(); err != nil {
		abort(err)
	}
	return nil
}
