// Package restore imports a backup from a previous Libre Publish install
// into a data directory, replacing the site that's there:
//
//   - a Go core backup (Tools › Backup, GET /api/tools/backup): cms.sqlite3,
//     storage/<k0k1>/<k2k3>/<key>, and from this version on manifest.json
//     (and secret.key when it was asked for);
//   - a Rails core site backup (Tools › Backup there): manifest.json,
//     db/main.sqlite3 and blobs/<key>, keys perhaps prefixed <tenant>/;
//   - a Rails core data directory backup (bin/update, the nightly job):
//     production.sqlite3 and <k0k1>/<k2k3>/<key>;
//   - any of those unpacked, or a data directory itself.
//
// Stage unpacks one into DATA_DIR/imports/<id>/ (whitelisted entries only,
// within Limits), checks the database, migrates it, puts the files where
// this core keeps them (checking each against its checksum), turns the
// Rails Forms plugin's tables into the Go one's records, and reports what
// it found. Nothing outside imports/ changes, so staging is the dry run.
// Apply then re-seals or clears the secrets, signs everyone out, and swaps
// the staged database and files in, keeping what they replace in
// DATA_DIR/replaced-<time>/.
package restore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// The formats a backup comes in.
const (
	FormatGo        = "go"
	FormatRailsSite = "rails-site"
	FormatRailsData = "rails-data"
)

// FormatLabel is how a format is named to a person.
func FormatLabel(f string) string {
	switch f {
	case FormatGo:
		return "Libre Publish backup"
	case FormatRailsSite:
		return "Libre Publish (Rails) site backup"
	case FormatRailsData:
		return "Libre Publish (Rails) data directory backup"
	}
	return f
}

var (
	// ErrInvalid is a source that isn't a backup this can import, or one
	// that's unsafe to (a path outside the archive, a link).
	ErrInvalid = errors.New("not an importable backup")
	// ErrTooLarge is an archive over Limits.
	ErrTooLarge = errors.New("the backup is too large")
	// ErrHasUsers refuses to replace a site people use without force.
	ErrHasUsers = errors.New("this install already has users: importing replaces its whole site")
	// ErrWrongKey is a source key that opens none of the backup's sealed
	// values.
	ErrWrongKey = errors.New("that key doesn't open the backup's sealed values")
	// ErrNotFound is an import id with nothing staged under it.
	ErrNotFound = errors.New("no such import")
)

// ManifestFormat is what a Go backup's manifest.json says it is.
const ManifestFormat = "librepublish-go"

// Manifest is a backup's manifest.json: a Go backup's, or a Rails site
// backup's (tenant, includes, missing_blobs).
type Manifest struct {
	Format        string   `json:"format,omitempty"`
	Version       int      `json:"version,omitempty"`
	CMSVersion    string   `json:"cms_version,omitempty"`
	GeneratedAt   string   `json:"generated_at,omitempty"`
	SchemaVersion int      `json:"schema_version,omitempty"`
	Blobs         int      `json:"blobs,omitempty"`
	SecretKey     bool     `json:"secret_key,omitempty"`
	Tenant        string   `json:"tenant,omitempty"`
	MissingBlobs  []string `json:"missing_blobs,omitempty"`
}

// Options are an import's: where to, with what key, by whom.
type Options struct {
	DataDir string
	// SecretKey is the SECRET_KEY the target runs with ("" for
	// DATA_DIR/secret.key).
	SecretKey string
	// SourceKey is the old install's key, to re-seal what it sealed: its
	// secret.key's contents, or its SECRET_KEY.
	SourceKey string
	// Force replaces a site that has users.
	Force bool
	// Actor names who imported it, in the audit log.
	Actor  string
	Limits Limits
}

// Import is one staged backup, under DATA_DIR/imports/<ID>.
type Import struct {
	ID     string
	Dir    string
	Report *Report
}

// The staging layout.
const (
	stagedDir  = "staged"
	stagedDB   = "cms.sqlite3"
	reportFile = "report.json"
	sourceKey  = "source.key"
)

var importID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func importsDir(dataDir string) string { return filepath.Join(dataDir, "imports") }

// Stage stages src: a backup archive (.tar.gz or .tar) or a directory.
func Stage(ctx context.Context, src string, opts Options) (*Import, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		if same(filepath.Join(src, "cms.sqlite3"), filepath.Join(opts.DataDir, "cms.sqlite3")) {
			return nil, fmt.Errorf("%w: %s is this install's own data directory", ErrInvalid, src)
		}
		return stage(ctx, src, opts, func(w *writer) error { return w.fromDir(ctx, src) })
	}
	f, err := os.Open(src) //nolint:gosec // the operator's own archive
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return StageArchive(ctx, f, filepath.Base(src), opts)
}

// StageArchive stages the archive r streams (an upload); label names it
// in the report.
func StageArchive(ctx context.Context, r io.Reader, label string, opts Options) (*Import, error) {
	return stage(ctx, label, opts, func(w *writer) error { return w.fromArchive(ctx, r) })
}

func stage(ctx context.Context, label string, opts Options, unpack func(*writer) error) (imp *Import, err error) {
	if err := os.MkdirAll(importsDir(opts.DataDir), 0o700); err != nil {
		return nil, err
	}
	sweep(opts.DataDir)
	id := hex.EncodeToString(randomBytes(8))
	dir := filepath.Join(importsDir(opts.DataDir), id)
	staging := filepath.Join(dir, stagedDir)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	w, err := newWriter(staging, opts.Limits)
	if err != nil {
		return nil, err
	}
	err = unpack(w)
	err = errors.Join(err, w.close())
	if err != nil {
		return nil, err
	}
	got := w.out
	if got.db == "" {
		return nil, fmt.Errorf("%w: no database in it (cms.sqlite3, db/main.sqlite3 or production.sqlite3)", ErrInvalid)
	}
	rep := &Report{ID: id, Source: label, Format: databases[got.db], Manifest: got.manifest, StagedAt: time.Now().UTC(),
		Tables: map[string]int{}, Cleared: map[string]int{}}
	rep.FormatLabel = FormatLabel(rep.Format)
	if m := got.manifest; m != nil && m.Format != "" && m.Format != ManifestFormat {
		rep.warn("manifest.json says it's %q, not a format this knows; importing it as a %s", m.Format, rep.FormatLabel)
	}
	if got.key != "" {
		if _, err := secret.FromHex(got.key); err != nil {
			rep.warn("the backup's secret.key isn't a key (64 hex characters); ignored")
		} else {
			if err := os.WriteFile(filepath.Join(dir, sourceKey), []byte(got.key), 0o600); err != nil {
				return nil, err
			}
			rep.Secrets.KeyInBackup = true
		}
	}
	if n := len(got.ignored); n > 0 {
		rep.Ignored = n
		rep.warn("%d entries in it aren't part of a site and were left out (%s)", n, strings.Join(first(got.ignored, 5), ", "))
	}
	imp = &Import{ID: id, Dir: dir, Report: rep}
	if err := imp.prepare(ctx, opts, got); err != nil {
		return nil, err
	}
	return imp, imp.save()
}

// prepare checks and migrates the staged database, and puts its files and
// forms in order.
func (imp *Import) prepare(ctx context.Context, opts Options, got unpacked) error {
	rep := imp.Report
	path := filepath.Join(imp.Dir, stagedDir, stagedDB)
	from, err := check(ctx, path)
	if err != nil {
		return err
	}
	rep.SchemaFrom, rep.SchemaTo = from, store.Version()
	db, err := store.Open(path)
	if err != nil {
		return fmt.Errorf("%w: the database can't be brought up to date: %w", ErrInvalid, err)
	}
	defer db.Close()
	if err := fixBlobs(ctx, db, filepath.Join(imp.Dir, stagedDir), got, rep); err != nil {
		return err
	}
	if err := convertForms(ctx, db, rep); err != nil {
		return fmt.Errorf("converting forms: %w", err)
	}
	if err := cleanContent(ctx, db, rep); err != nil {
		return fmt.Errorf("cleaning up the content: %w", err)
	}
	if err := survey(ctx, db, rep); err != nil {
		return err
	}
	target, sources, err := imp.keys(opts, false)
	if err != nil {
		return err
	}
	if rep.Secrets.Plan, err = sealed(ctx, db, target, sources, false); err != nil {
		return err
	}
	rep.Secrets.KeyGiven = opts.SourceKey != ""
	if rep.TargetUsers, err = TargetUsers(ctx, opts.DataDir); err != nil {
		return err
	}
	if rep.TargetUsers > 0 {
		rep.warn("this site already has %s and content of its own: importing replaces all of it (it's kept in replaced-<time>/ in the data directory)", plural(rep.TargetUsers, "person", "people"))
	}
	_, err = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// check is the database's own word that it's whole and one this build
// can run: its schema version (PRAGMA user_version).
func check(ctx context.Context, path string) (int, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return 0, fmt.Errorf("%w: the database can't be read (%w)", ErrInvalid, err)
	}
	if result != "ok" {
		return 0, fmt.Errorf("%w: the database is damaged (%s)", ErrInvalid, result)
	}
	tables, err := store.Strings(ctx, db, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return 0, err
	}
	var missing []string
	for _, t := range []string{"users", "roles", "pages", "settings"} {
		if !slices.Contains(tables, t) {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return 0, fmt.Errorf("%w: the database isn't a Libre Publish site's (it has no %s table)", ErrInvalid, strings.Join(missing, ", "))
	}
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, err
	}
	if version > store.Version() {
		return 0, fmt.Errorf("%w: it was written by a newer Libre Publish (schema %d; this one knows %d): import it with a newer librepublish", ErrInvalid, version, store.Version())
	}
	// Folds a copied directory's WAL in, so the file is the whole database.
	_, err = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return version, err
}

// keys are the target's key and the ones the backup's sealed values may
// have been sealed with: the backup's own secret.key, and the one given.
// Without create, a target with no key yet has none (nil): staging writes
// nothing outside imports/.
func (imp *Import) keys(opts Options, create bool) (*secret.Key, []*secret.Key, error) {
	var target *secret.Key
	_, statErr := os.Stat(filepath.Join(opts.DataDir, "secret.key"))
	if create || opts.SecretKey != "" || statErr == nil {
		var err error
		if target, err = secret.Load(opts.DataDir, opts.SecretKey); err != nil {
			return nil, nil, err
		}
	}
	var sources []*secret.Key
	if raw, err := os.ReadFile(filepath.Join(imp.Dir, sourceKey)); err == nil {
		if k, err := secret.FromHex(string(raw)); err == nil {
			sources = append(sources, k)
		}
	}
	return target, append(sources, secret.Candidates(opts.SourceKey)...), nil
}

func (imp *Import) save() error {
	raw, err := json.MarshalIndent(imp.Report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(imp.Dir, reportFile), raw, 0o600)
}

// Load is the import staged under id.
func Load(dataDir, id string) (*Import, error) {
	if !importID.MatchString(id) {
		return nil, ErrNotFound
	}
	dir := filepath.Join(importsDir(dataDir), id)
	raw, err := os.ReadFile(filepath.Join(dir, reportFile)) //nolint:gosec // id is 16 hex characters
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var rep Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, err
	}
	return &Import{ID: id, Dir: dir, Report: &rep}, nil
}

// Discard removes the import staged under id.
func Discard(dataDir, id string) error {
	if !importID.MatchString(id) {
		return ErrNotFound
	}
	dir := filepath.Join(importsDir(dataDir), id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return os.RemoveAll(dir)
}

// Discard removes this import's staging.
func (imp *Import) Discard() error { return os.RemoveAll(imp.Dir) }

// sweep removes imports staged over a day ago and never applied.
func sweep(dataDir string) {
	entries, err := os.ReadDir(importsDir(dataDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && importID.MatchString(e.Name()) && time.Since(info.ModTime()) > 24*time.Hour {
			os.RemoveAll(filepath.Join(importsDir(dataDir), e.Name()))
		}
	}
}

// TargetUsers is how many people the data directory's site has: 0 for
// none, or no site yet.
func TargetUsers(ctx context.Context, dataDir string) (int, error) {
	path := filepath.Join(dataDir, "cms.sqlite3")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if n, err := store.Count(ctx, db, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'users'`); err != nil || n == 0 {
		return 0, err
	}
	return store.Count(ctx, db, `SELECT count(*) FROM users`)
}

// CheckKey refuses a source key that opens none of the sealed values the
// target's key can't: a typo, or another install's.
func (imp *Import) CheckKey(ctx context.Context, opts Options) error {
	if opts.SourceKey == "" {
		return nil
	}
	db, err := imp.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	target, _, err := imp.keys(Options{DataDir: opts.DataDir, SecretKey: opts.SecretKey}, false)
	if err != nil {
		return err
	}
	plan, err := sealed(ctx, db, target, secret.Candidates(opts.SourceKey), false)
	if err != nil {
		return err
	}
	if plan.Total > plan.Kept && plan.Resealed == 0 {
		return ErrWrongKey
	}
	return nil
}

func (imp *Import) open(ctx context.Context) (*store.DB, error) {
	path := filepath.Join(imp.Dir, stagedDir, stagedDB)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("the staged import is gone: %w", err)
	}
	db, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// Apply puts the staged site in place of the data directory's: secrets
// re-sealed under this install's key (or cleared), everyone signed out,
// then the database and files swapped in. Nothing may have the data
// directory's database open. What it replaced is in Report.ReplacedDir.
func (imp *Import) Apply(ctx context.Context, opts Options) (*Report, error) {
	rep := imp.Report
	users, err := TargetUsers(ctx, opts.DataDir)
	if err != nil {
		return nil, err
	}
	if users > 0 && !opts.Force {
		return nil, ErrHasUsers
	}
	if err := imp.CheckKey(ctx, opts); err != nil {
		return nil, err
	}
	if err := imp.finish(ctx, opts); err != nil {
		return nil, err
	}
	replaced, err := swap(opts.DataDir, filepath.Join(imp.Dir, stagedDir), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	rep.ReplacedDir, rep.Applied, rep.AppliedAt = replaced, true, time.Now().UTC()
	if err := imp.Discard(); err != nil {
		rep.warn("the staging directory %s couldn't be removed: %v", imp.Dir, err)
	}
	return rep, nil
}

// finish does the writes to the staged database that wait for Apply.
func (imp *Import) finish(ctx context.Context, opts Options) error {
	rep := imp.Report
	db, err := imp.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	target, sources, err := imp.keys(opts, true)
	if err != nil {
		return err
	}
	if rep.Secrets.Done, err = sealed(ctx, db, target, sources, true); err != nil {
		return err
	}
	rep.Secrets.KeyGiven = opts.SourceKey != ""
	// Signed in to the old site is signed in to nothing here; a lock or a
	// reset link from there means nothing either. (A report read back from
	// disk has no Cleared: it's omitted while empty.)
	if rep.Cleared == nil {
		rep.Cleared = map[string]int{}
	}
	for _, t := range []string{"admin_sessions", "sessions", "edit_locks", "device_authorizations", "account_tokens"} {
		if n, err := clearTable(ctx, db, t); err != nil {
			return err
		} else if n > 0 {
			rep.Cleared[t] = n
		}
	}
	meta, err := json.Marshal(map[string]any{"format": rep.Format, "source": rep.Source, "blobs": rep.Blobs,
		"forms": rep.Forms.Forms, "submissions": rep.Forms.Submissions, "secrets": rep.Secrets.Done})
	if err != nil {
		return err
	}
	actor := or(opts.Actor, "librepublish import")
	if _, err := db.ExecContext(ctx, `INSERT INTO audit_logs (action, actor_label, target_type, target_label, metadata, created_at) VALUES (?,?,?,?,?,?)`,
		"backup.imported", actor, "Site", rep.FormatLabel, string(meta), store.Now().DB()); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	return imp.save()
}

func clearTable(ctx context.Context, db *store.DB, table string) (int, error) {
	n, err := store.Count(ctx, db, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table)
	if err != nil || n == 0 {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM `+quote(table))
	if err != nil {
		return 0, err
	}
	rows, err := res.RowsAffected()
	return int(rows), err
}

// What a data directory holds that an import replaces.
var swapped = []string{"cms.sqlite3", "cms.sqlite3-wal", "cms.sqlite3-shm", "storage"}

// swap moves the data directory's database and files into
// replaced-<time>/ and the staged ones into their place, undoing what it
// did if a step fails. The variants cache goes: it was the old files'.
func swap(dataDir, staged string, now time.Time) (string, error) {
	replaced, err := uniqueDir(filepath.Join(dataDir, "replaced-"+now.Format("20060102-150405")))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(staged, "storage"), 0o750); err != nil {
		return "", err
	}
	var done [][2]string
	move := func(from, to string) error {
		if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		done = append(done, [2]string{from, to})
		return nil
	}
	undo := func(err error) (string, error) {
		for _, m := range slices.Backward(done) {
			err = errors.Join(err, os.Rename(m[1], m[0]))
		}
		os.Remove(replaced)
		return "", fmt.Errorf("swapping the import in (undone): %w", err)
	}
	for _, name := range swapped {
		if err := move(filepath.Join(dataDir, name), filepath.Join(replaced, name)); err != nil {
			return undo(err)
		}
	}
	for _, name := range []string{"cms.sqlite3", "storage"} {
		if err := move(filepath.Join(staged, name), filepath.Join(dataDir, name)); err != nil {
			return undo(err)
		}
	}
	// A copy of the key the replaced values were sealed with, so the
	// replaced site can be put back whole.
	if raw, err := os.ReadFile(filepath.Join(dataDir, "secret.key")); err == nil { //nolint:gosec // the data directory's own key
		_ = os.WriteFile(filepath.Join(replaced, "secret.key"), raw, 0o600) //nolint:gosec // replaced is the directory swap just made
	}
	os.RemoveAll(filepath.Join(dataDir, "variants"))
	return replaced, nil
}

// Rollback puts back the site an Apply replaced (from replaced, its
// ReplacedDir), keeping the imported one in failed-<time>/.
func Rollback(dataDir, replaced string) error {
	failed, err := uniqueDir(filepath.Join(dataDir, "failed-"+time.Now().UTC().Format("20060102-150405")))
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range swapped {
		if _, err := os.Lstat(filepath.Join(dataDir, name)); err == nil {
			errs = append(errs, os.Rename(filepath.Join(dataDir, name), filepath.Join(failed, name)))
		}
	}
	for _, name := range swapped {
		if _, err := os.Lstat(filepath.Join(replaced, name)); err == nil {
			errs = append(errs, os.Rename(filepath.Join(replaced, name), filepath.Join(dataDir, name)))
		}
	}
	os.RemoveAll(filepath.Join(dataDir, "variants"))
	return errors.Join(errs...)
}

// uniqueDir makes base, or base-2, base-3… when it's taken.
func uniqueDir(base string) (string, error) {
	dir := base
	for i := 2; ; i++ {
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, os.ErrExist) || i > 100 {
			return "", err
		}
		dir = fmt.Sprintf("%s-%d", base, i)
	}
}

func same(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return b
}

func first(list []string, n int) []string { return list[:min(n, len(list))] }

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
