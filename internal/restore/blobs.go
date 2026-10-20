package restore

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

type blobRow struct {
	id       int64
	key      string
	filename string
	size     int64
	checksum sql.NullString
}

// fixBlobs puts the database's blobs in order with the files that came:
// a tenant's <tenant>/ prefix stripped from keys (the files are already
// stored by the bare key), each file checked against its size and
// checksum, and files no blob names removed. A key that isn't one (say
// "../../x") is replaced with a fresh one that names no file, so nothing
// later builds a path from it; its attachments stay, missing their file.
func fixBlobs(ctx context.Context, db *store.DB, staging string, got unpacked, rep *Report) error {
	rows, err := db.QueryContext(ctx, `SELECT id, key, filename, byte_size, checksum FROM active_storage_blobs ORDER BY id`)
	if err != nil {
		return err
	}
	blobs, err := store.ScanAll(rows, func(r *sql.Rows) (blobRow, error) {
		var b blobRow
		err := r.Scan(&b.id, &b.key, &b.filename, &b.size, &b.checksum)
		return b, err
	})
	if err != nil {
		return err
	}
	b := &rep.Blobs
	b.Total = len(blobs)
	named := map[string]bool{}
	var missing, mismatched, bad []string
	for _, blob := range blobs {
		key := blob.key
		if prefix, bare, ok := strings.Cut(blob.key, "/"); ok && prefix != "" && !strings.Contains(bare, "/") {
			key = bare
		}
		if !blobKey.MatchString(key) {
			b.BadKeys++
			bad = append(bad, blob.key)
			if _, err := db.ExecContext(ctx, `UPDATE active_storage_blobs SET key = ? WHERE id = ?`, freshKey(), blob.id); err != nil {
				return fmt.Errorf("blob #%d's key %q can't be replaced: %w", blob.id, blob.key, err)
			}
			continue
		}
		if key != blob.key {
			if _, err := db.ExecContext(ctx, `UPDATE active_storage_blobs SET key = ? WHERE id = ?`, key, blob.id); err != nil {
				rep.warn("blob #%d (%s): its key %q can't become %q: %v", blob.id, blob.filename, blob.key, key, err)
				continue
			}
			b.Renamed++
		}
		named[key] = true
		file, ok := got.blobs[key]
		switch {
		case !ok:
			b.Missing++
			missing = append(missing, blob.filename)
		case file.size != blob.size || (blob.checksum.String != "" && !sameSum(blob.checksum.String, file.md5)):
			b.Mismatched++
			mismatched = append(mismatched, blob.filename)
		default:
			b.Copied++
		}
	}
	for key := range got.blobs {
		if !named[key] {
			b.Unreferenced++
			os.Remove(filepath.Join(staging, "storage", key[0:2], key[2:4], key))
		}
	}
	if len(missing) > 0 {
		rep.warn("%d files the database names didn't come with the backup: %s", len(missing), strings.Join(first(missing, 10), ", "))
	}
	if len(mismatched) > 0 {
		rep.warn("%d files don't match their size or checksum (damaged in the backup?): %s", len(mismatched), strings.Join(first(mismatched, 10), ", "))
	}
	if len(bad) > 0 {
		rep.warn("%d blobs have keys this core can't store (left without a file): %s", len(bad), strings.Join(first(bad, 10), ", "))
	}
	if b.Unreferenced > 0 {
		rep.warn("%d files in the backup belong to no blob in its database and were left out", b.Unreferenced)
	}
	return nil
}

// freshKey is a new Active Storage key: 28 characters of base36.
func freshKey() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := secret.RandomBytes(28)
	for i, c := range out {
		out[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(out)
}

// sameSum is whether checksum (Active Storage's: MD5, base64) is sum's.
func sameSum(checksum string, sum []byte) bool {
	want, err := base64.StdEncoding.DecodeString(checksum)
	return err == nil && subtle.ConstantTimeCompare(want, sum) == 1
}
