package cms

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Active Storage's checksum, below
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// Media files keep Active Storage's disk layout (storage/ab/cd/abcd…), so a
// Rails install's storage/ directory is served as it is.

var deliveryWidths = []int{640, 1280, 1920}

const maxUpload = 100 << 20

// Asset is one file in the media library, with its Active Storage blob.
type Asset struct {
	ID                       int64
	Name, Alt, Caption, Desc string
	Folder                   string
	FocalX, FocalY           float64
	DeletedAt, CreatedAt     store.Time
	UpdatedAt                store.Time
	BlobKey, Filename, Type  string
	ByteSize                 int64
	Width, Height            *int
}

const assetCols = `a.id, COALESCE(a.name, ''), COALESCE(a.alt, ''), COALESCE(a.caption, ''), COALESCE(a.description, ''), a.folder, a.focal_x, a.focal_y,
	a.deleted_at, a.created_at, a.updated_at, COALESCE(b.key, ''), COALESCE(b.filename, ''), COALESCE(b.content_type, ''), COALESCE(b.byte_size, 0), COALESCE(b.metadata, '')`

const assetFrom = ` FROM assets a
	LEFT JOIN active_storage_attachments att ON att.record_type = 'Asset' AND att.record_id = a.id AND att.name = 'file'
	LEFT JOIN active_storage_blobs b ON b.id = att.blob_id `

func (a *App) assets(ctx context.Context, where string, args ...any) ([]*Asset, error) {
	if where == "" {
		where = "1=1"
	}
	if !strings.Contains(where, "deleted_at") {
		where = scoped("a.deleted_at IS NULL", where)
	}
	rows, err := a.store.QueryContext(ctx, "SELECT "+assetCols+assetFrom+"WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Asset{}
	for rows.Next() {
		as := &Asset{}
		var meta string
		if err := rows.Scan(&as.ID, &as.Name, &as.Alt, &as.Caption, &as.Desc, &as.Folder, &as.FocalX, &as.FocalY, &as.DeletedAt, &as.CreatedAt, &as.UpdatedAt,
			&as.BlobKey, &as.Filename, &as.Type, &as.ByteSize, &meta); err != nil {
			return nil, err
		}
		var m map[string]any
		if json.Unmarshal([]byte(meta), &m) == nil {
			if w, ok := intOf(m["width"]); ok {
				wi := int(w)
				as.Width = &wi
			}
			if h, ok := intOf(m["height"]); ok {
				hi := int(h)
				as.Height = &hi
			}
		}
		out = append(out, as)
	}
	return out, rows.Err()
}

func (a *App) assetByID(ctx context.Context, id int64, withTrashed bool) (*Asset, error) {
	where := "a.id = ?"
	if withTrashed {
		where += " AND (a.deleted_at IS NULL OR a.deleted_at IS NOT NULL)"
	}
	list, err := a.assets(ctx, where, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apierr.NotFound("Couldn't find asset " + strconv.FormatInt(id, 10) + ".")
	}
	return list[0], nil
}

// Image is whether the asset is an image of any kind.
func (as *Asset) Image() bool { return strings.HasPrefix(as.Type, "image/") }

// variantable: images this core can resize (not SVG, not animated GIF
// guesses — GIFs resize to their first frame).
func (as *Asset) variantable() bool {
	switch as.Type {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

func (a *App) fileURL(as *Asset, variant string) string {
	if as.BlobKey == "" {
		return ""
	}
	p := "/files/" + as.BlobKey
	if variant != "" {
		p += "/" + variant
	}
	return a.BaseURL() + p + "/" + urlPathEscape(as.Filename)
}

func urlPathEscape(s string) string { return url.PathEscape(s) }

// delivery is an asset as a site renders it: absolute URLs on the CMS's own
// host, its size, and width-limited renditions.
func (a *App) delivery(as *Asset) Obj {
	url := a.fileURL(as, "")
	o := O("id", as.ID, "url", url, "original", url, "filename", as.Filename, "content_type", as.Type, "byte_size", as.ByteSize,
		"width", as.Width, "height", as.Height, "alt", as.Alt, "folder", as.Folder)
	if as.Image() {
		variants := map[string]string{}
		srcset := []Obj{}
		thumb := url
		if as.variantable() {
			for _, w := range deliveryWidths {
				variants["w"+strconv.Itoa(w)] = a.fileURL(as, "w"+strconv.Itoa(w))
				srcset = append(srcset, O("width", w, "descriptor", strconv.Itoa(w)+"w", "url", a.fileURL(as, "w"+strconv.Itoa(w))))
			}
			thumb = a.fileURL(as, "w256")
		}
		o = append(o, kv{"variants", variants}, kv{"thumb_url", thumb}, kv{"srcset", srcset}, kv{"focal_x", as.FocalX}, kv{"focal_y", as.FocalY})
	}
	return o
}

func (a *App) assetJSON(ctx context.Context, as *Asset) Obj {
	// A count that can't be read shows as 0: it's for display.
	uses, _ := store.Count(ctx, a.store, `SELECT count(DISTINCT owner_type || ':' || owner_id) FROM content_references WHERE ref_type = 'asset' AND ref_id = ?`, strconv.FormatInt(as.ID, 10))
	return append(a.delivery(as), kv{"name", as.Name}, kv{"caption", as.Caption}, kv{"description", as.Desc}, kv{"created_at", as.CreatedAt.ISO()}, kv{"usage_count", uses})
}

// resolveAssets is {id: delivery} for the ids that exist.
func (a *App) resolveAssets(ctx context.Context, ids []int64) map[string]Obj {
	out := map[string]Obj{}
	if len(ids) == 0 {
		return out
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	list, err := a.assets(ctx, "a.id IN ("+store.Placeholders(len(ids))+")", args...)
	if err != nil {
		return out
	}
	for _, as := range list {
		out[strconv.FormatInt(as.ID, 10)] = a.delivery(as)
	}
	return out
}

// blobKey is an Active Storage key as both cores make them: lowercase
// base36. Anything else in the key column (an imported "../../x") names no
// file of ours.
var blobKey = regexp.MustCompile(`^[a-z0-9]{4,128}$`)

// blobPath is key's file, and false for a key that isn't one.
func (a *App) blobPath(key string) (string, bool) {
	if !blobKey.MatchString(key) {
		return "", false
	}
	return filepath.Join(a.cfg.DataDir, "storage", key[0:2], key[2:4], key), true
}

// uploadAsset stores an upload and makes its asset.
func (a *App) uploadAsset(ctx context.Context, actor *Actor, filename, contentType string, r io.Reader, fields map[string]string) (*Asset, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxUpload+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxUpload {
		return nil, &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"file": {"is larger than 100 MB"}}}
	}
	if len(data) == 0 {
		return nil, &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"file": {"is empty"}}}
	}
	filename = path.Base(strings.ReplaceAll(filename, "\\", "/"))
	contentType = mediaType(contentType)
	if contentType == "" || contentType == "application/octet-stream" {
		if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); t != "" {
			contentType = mediaType(t)
		} else {
			contentType = mediaType(http.DetectContentType(data))
		}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if contentType == "text/html" || contentType == "application/xhtml+xml" ||
		slices.Contains([]string{".htm", ".html", ".xhtml"}, strings.ToLower(filepath.Ext(filename))) {
		return nil, &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"file": {"HTML files can't be uploaded"}}}
	}
	meta := map[string]any{"identified": true, "analyzed": true}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		// A few KB can claim gigapixels (a decompression bomb). Big photos
		// are fine (they're served as they are, unresized); absurd ones aren't.
		if pixels(cfg) > maxUploadPixels {
			return nil, &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{"file": {"is an image of over 250 megapixels"}}}
		}
		meta["width"], meta["height"] = cfg.Width, cfg.Height
	}
	key := strings.ToLower(strings.TrimRight(base32Key(), "="))
	sum := md5.Sum(data) //nolint:gosec // Active Storage's checksum column is MD5; it's an integrity check, not security
	p, _ := a.blobPath(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, data, 0o640); err != nil { //nolint:gosec // 0640, as the database is: never world-readable
		return nil, err
	}
	folder := normalizeFolder(fields["folder"])
	now := store.Now().DB()
	var id int64
	err = a.store.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO active_storage_blobs (key, filename, content_type, metadata, service_name, byte_size, checksum, created_at) VALUES (?,?,?,?,?,?,?,?)`,
			key, filename, contentType, string(store.MustJSON(meta)), "local", len(data), base64.StdEncoding.EncodeToString(sum[:]), now)
		if err != nil {
			return err
		}
		blobID, _ := res.LastInsertId()
		name := fields["name"]
		if name == "" {
			name = strings.TrimSuffix(filename, filepath.Ext(filename))
		}
		res, err = tx.ExecContext(ctx, `INSERT INTO assets (name, alt, caption, description, folder, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			name, fields["alt"], fields["caption"], fields["description"], folder, now, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, `INSERT INTO active_storage_attachments (name, record_type, record_id, blob_id, created_at) VALUES ('file', 'Asset', ?, ?, ?)`, id, blobID, now)
		return err
	})
	if err != nil {
		os.Remove(p)
		return nil, err
	}
	as, err := a.assetByID(ctx, id, false)
	if err == nil {
		a.audit(ctx, actor, "asset.created", "Asset", id, filename, nil)
	}
	return as, err
}

// mediaType is a Content-Type's bare type, lowercased ("Text/HTML;
// charset=x" is text/html), and "" for one that doesn't parse.
func mediaType(ct string) string {
	t, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return t
}

func base32Key() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := secret.RandomBytes(28)
	out := make([]byte, 28)
	for i, c := range b {
		out[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(out)
}

func normalizeFolder(f string) string {
	f = "/" + strings.Trim(strings.TrimSpace(f), "/")
	return path.Clean(f)
}

func (a *App) saveAsset(ctx context.Context, actor *Actor, as *Asset, p map[string]any) error {
	shown := [...]any{as.Alt, as.Caption, as.Desc, as.FocalX, as.FocalY}
	for k, dst := range map[string]*string{"name": &as.Name, "alt": &as.Alt, "caption": &as.Caption, "description": &as.Desc} {
		if has(p, k) {
			*dst = str(p[k])
		}
	}
	if has(p, "folder") {
		as.Folder = normalizeFolder(str(p["folder"]))
	}
	for k, dst := range map[string]*float64{"focal_x": &as.FocalX, "focal_y": &as.FocalY} {
		if v, ok := p[k].(float64); ok {
			if v < 0 || v > 1 {
				return &apierr.Error{Status: 422, Code: "invalid", Message: "Validation failed", Errors: apierr.Invalid{k: {"must be between 0 and 1"}}}
			}
			*dst = v
		}
	}
	if shown != [...]any{as.Alt, as.Caption, as.Desc, as.FocalX, as.FocalY} {
		if err := a.needLivePublish(ctx, actor, as.ID); err != nil {
			return err
		}
	}
	_, err := a.store.ExecContext(ctx, `UPDATE assets SET name=?, alt=?, caption=?, description=?, folder=?, focal_x=?, focal_y=?, updated_at=? WHERE id=?`,
		as.Name, as.Alt, as.Caption, as.Desc, as.Folder, as.FocalX, as.FocalY, store.Now().DB(), as.ID)
	if err == nil {
		a.audit(ctx, actor, "asset.updated", "Asset", as.ID, as.Filename, nil)
	}
	return err
}

// liveUsers is, for each kind of record that can use an asset, when it's
// live and the capability that publishes it.
var liveUsers = []struct{ ownerType, live, capability string }{
	{"Page", `SELECT 1 FROM pages o WHERE o.id = r.owner_id AND o.status = 'published' AND o.deleted_at IS NULL`, "pages:publish"},
	{"CollectionEntry", `SELECT 1 FROM collection_entries o WHERE o.id = r.owner_id AND o.status = 'published' AND o.deleted_at IS NULL`, "entries:publish"},
	{"Global", `SELECT 1 FROM globals o WHERE o.id = r.owner_id AND o.deleted_at IS NULL`, "globals:publish"},
}

// needLivePublish refuses a change to what the site shows of an asset (its
// alt text, caption, description, focal point) unless the actor could
// publish every live record using it: otherwise the change would reach the
// site without the review a change to that record gets. Assets have no
// revisions to hold it in, so it's refused.
func (a *App) needLivePublish(ctx context.Context, actor *Actor, id int64) error {
	for _, u := range liveUsers {
		if actor != nil && actor.Can(u.capability) {
			continue
		}
		n, err := store.Count(ctx, a.store, `SELECT count(*) FROM content_references r WHERE r.ref_type = 'asset' AND r.ref_id = ? AND r.owner_type = ? AND EXISTS (`+u.live+`)`,
			strconv.FormatInt(id, 10), u.ownerType)
		if err != nil {
			return err
		}
		if n > 0 {
			e := apierr.Forbidden(u.capability)
			e.Message = "This file is used by live content, so changing its alt text, caption, description or focal point needs " + u.capability + ", which your role doesn't grant."
			return e
		}
	}
	return nil
}

func (a *App) purgeAsset(ctx context.Context, actor *Actor, as *Asset) error {
	err := a.store.Tx(ctx, func(tx *sql.Tx) error {
		var blobID int64
		err := tx.QueryRowContext(ctx, `SELECT blob_id FROM active_storage_attachments WHERE record_type = 'Asset' AND record_id = ?`, as.ID).Scan(&blobID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, s := range []string{
			`DELETE FROM active_storage_attachments WHERE record_type = 'Asset' AND record_id = ?`,
			`DELETE FROM assets WHERE id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, s, as.ID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM active_storage_blobs WHERE id = ?`, blobID)
		return err
	})
	if err != nil {
		return err
	}
	// A key that isn't one is left alone: it names no file of ours, and as
	// a glob it could name others'.
	if p, ok := a.blobPath(as.BlobKey); ok {
		os.Remove(p)
		matches, _ := filepath.Glob(filepath.Join(a.cfg.DataDir, "variants", as.BlobKey+"-*"))
		for _, m := range matches {
			os.Remove(m)
		}
	}
	a.audit(ctx, actor, "asset.purged", "Asset", as.ID, as.Filename, nil)
	return nil
}

// ---- serving -------------------------------------------------------------

// keyedLocks is a mutex per key, there only while someone holds or waits
// for it.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	sync.Mutex
	refs int
}

// lock takes key's mutex; the func it returns lets it go.
func (k *keyedLocks) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// serveFile answers /files/<key>/<filename> and /files/<key>/w640/<filename>.
// Keys are unguessable; a file is public once uploaded, as Active Storage's
// were.
func (a *App) serveFile(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	variant, _, ok := strings.Cut(r.PathValue("rest"), "/")
	if !ok {
		variant = ""
	}
	var as Asset
	err := a.store.QueryRowContext(r.Context(), `SELECT key, filename, COALESCE(content_type, '') FROM active_storage_blobs WHERE key = ?`, key).Scan(&as.BlobKey, &as.Filename, &as.Type)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, ok := a.blobPath(key)
	if !ok {
		http.NotFound(w, r)
		return
	}
	original := file
	if variant != "" && as.variantable() {
		width, err := strconv.Atoi(strings.TrimPrefix(variant, "w"))
		if err != nil || !strings.HasPrefix(variant, "w") || (width != 256 && !slices.Contains(deliveryWidths, width)) {
			http.NotFound(w, r)
			return
		}
		file, err = a.variant(as, width)
		if err != nil {
			a.log.Warn("variant", "key", key, "err", err)
			file = original
		}
	}
	// Opened within the data directory, so no path can leave it.
	rel, err := filepath.Rel(a.cfg.DataDir, file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.OpenInRoot(a.cfg.DataDir, rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "unreadable", http.StatusInternalServerError)
		return
	}
	ct := mediaType(as.Type)
	if ct == "" {
		ct = "application/octet-stream"
	}
	if strings.HasSuffix(file, ".jpg") {
		ct = "image/jpeg"
	} else if strings.HasSuffix(file, ".png") {
		ct = "image/png"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	if !inline(ct) {
		h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", as.Filename))
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// inline is whether a file of type ct is shown in the browser rather than
// downloaded: only what can't carry script. SVG can, so it downloads.
func inline(ct string) bool {
	switch ct {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/avif", "image/bmp", "image/x-icon", "image/vnd.microsoft.icon", "application/pdf":
		return true
	}
	return strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/")
}

// decodes bounds how many images are decoded and resized at once: each can
// take 160 MB, and the requests that start them needn't be signed in.
var decodes = make(chan struct{}, 2)

// Decoding an image takes 4 bytes a pixel whatever its file size: past
// maxVariantPixels the original is served rather than resized, and an upload
// past maxUploadPixels is refused.
const (
	maxVariantPixels = 40_000_000
	maxUploadPixels  = 250_000_000
)

func pixels(cfg image.Config) int64 { return int64(cfg.Width) * int64(cfg.Height) }

// variant makes (once) and returns the path of a width-limited copy; the
// original's path when it's already narrow enough or too big to decode.
func (a *App) variant(as Asset, width int) (string, error) {
	ext := ".jpg"
	if as.Type == "image/png" || as.Type == "image/gif" {
		ext = ".png"
	}
	out := filepath.Join(a.cfg.DataDir, "variants", fmt.Sprintf("%s-w%d%s", as.BlobKey, width, ext))
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	defer a.variants.lock(out)()
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	orig, ok := a.blobPath(as.BlobKey)
	if !ok {
		return "", errors.New("not a blob key")
	}
	src, err := os.Open(orig) //nolint:gosec // a path built from a key checked as one
	if err != nil {
		return "", err
	}
	defer src.Close()
	cfg, _, err := image.DecodeConfig(src)
	if err != nil {
		return "", err
	}
	if cfg.Width <= width || pixels(cfg) > maxVariantPixels {
		return orig, nil
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	decodes <- struct{}{}
	defer func() { <-decodes }()
	img, _, err := image.Decode(src)
	if err != nil {
		return "", err
	}
	b := img.Bounds()
	if b.Dx() <= width {
		return orig, nil
	}
	height := b.Dy() * width / b.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return "", err
	}
	tmp := out + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640) //nolint:gosec // 0640, as the blobs are
	if err != nil {
		return "", err
	}
	if ext == ".png" {
		err = png.Encode(f, dst)
	} else {
		err = jpeg.Encode(f, dst, &jpeg.Options{Quality: 82})
	}
	// A failed close can mean the bytes never landed: don't rename a
	// truncated file into place.
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return out, os.Rename(tmp, out)
}

var _ = gif.Decode // registers GIF decoding
