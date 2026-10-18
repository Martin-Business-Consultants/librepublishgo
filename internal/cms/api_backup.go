package cms

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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
	set, err := a.prepareBackup(c.R.Context(), withKey)
	if err != nil {
		return err
	}
	defer set.close()
	a.audit(c.R.Context(), c.Actor, "backup.downloaded", "Site", 0, "backup", map[string]any{"secret_key": withKey})
	c.W.Header().Set("Content-Type", "application/gzip")
	c.W.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="librepublish-%s.tar.gz"`, time.Now().UTC().Format("20060102-150405")))
	// The download has begun: a failure from here can't be a JSON error,
	// so the connection is cut and the client sees a broken archive rather
	// than a short one that looks whole.
	if err := a.writeBackup(c.W, set); err != nil {
		a.log.Error("backup", "err", err)
		panic(http.ErrAbortHandler)
	}
	return nil
}

// backupSet is what a backup holds, gathered before it's written: a
// consistent copy of the database, the uploaded files, the manifest, and
// the secret key when asked for.
type backupSet struct {
	tmp, snap string
	files     []string
	manifest  []byte
	key       []byte
}

func (b *backupSet) close() { _ = os.RemoveAll(b.tmp) }

func (a *App) prepareBackup(ctx context.Context, withKey bool) (*backupSet, error) {
	tmp, err := os.MkdirTemp("", "lp-backup-")
	if err != nil {
		return nil, err
	}
	b := &backupSet{tmp: tmp, snap: filepath.Join(tmp, "cms.sqlite3")}
	if _, err := a.store.ExecContext(ctx, `VACUUM INTO ?`, b.snap); err != nil {
		b.close()
		return nil, err
	}
	root := filepath.Join(a.cfg.DataDir, "storage")
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			b.files = append(b.files, p)
		}
		return nil
	})
	if err != nil {
		b.close()
		return nil, err
	}
	var version int
	if err := a.store.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		b.close()
		return nil, err
	}
	b.manifest, err = json.MarshalIndent(restore.Manifest{Format: restore.ManifestFormat, Version: 1, CMSVersion: a.cfg.Version,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339), SchemaVersion: version, Blobs: len(b.files), SecretKey: withKey}, "", "  ")
	if err != nil {
		b.close()
		return nil, err
	}
	if withKey {
		b.key = []byte(a.secrets.Hex() + "\n")
	}
	return b, nil
}

// writeBackup writes b as a .tar.gz to w.
func (a *App) writeBackup(w io.Writer, b *backupSet) error {
	gz := gzip.NewWriter(w)
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
	if err := addBytes("manifest.json", b.manifest); err != nil {
		return err
	}
	if b.key != nil {
		if err := addBytes("secret.key", b.key); err != nil {
			return err
		}
	}
	if err := add(b.snap, "cms.sqlite3"); err != nil {
		return err
	}
	for _, p := range b.files {
		rel, err := filepath.Rel(a.cfg.DataDir, p)
		if err != nil {
			return err
		}
		if err := add(p, filepath.ToSlash(rel)); err != nil {
			return err
		}
	}
	return errors.Join(tw.Close(), gz.Close())
}
