package restore_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
	"golang.org/x/crypto/bcrypt"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/cms"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/restore"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func TestMain(m *testing.M) {
	secret.PasswordCost = bcrypt.MinCost
	cache, err := wazero.NewCompilationCacheWithDir(filepath.Join(os.TempDir(), "librepublish-test-wazero"))
	if err != nil {
		cache = wazero.NewCompilationCache()
	}
	plugin.CompilationCache = cache
	os.Exit(m.Run())
}

// ---- archives ------------------------------------------------------------

type entry struct {
	name string
	body []byte
	typ  byte // tar.TypeReg when 0
	link string
}

func tarball(t *testing.T, gz bool, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var z *gzip.Writer
	if gz {
		z = gzip.NewWriter(&buf)
		w = z
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		size := int64(len(e.body))
		if typ != tar.TypeReg {
			size = 0
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: size, Typeflag: typ, Linkname: e.link}); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if z != nil {
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func md5b64(b []byte) string {
	sum := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// ---- a Rails site ----------------------------------------------------------

// railsSchema is the Rails core's tables an import reads, as its
// db/schema.rb has them (on main), the forms plugin's included.
const railsSchema = `
CREATE TABLE schema_migrations (version varchar NOT NULL PRIMARY KEY);
CREATE TABLE roles (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, created_at datetime(6) NOT NULL, description varchar, name varchar NOT NULL, permissions json DEFAULT '[]' NOT NULL, system boolean DEFAULT 0 NOT NULL, updated_at datetime(6) NOT NULL);
CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, created_at datetime(6) NOT NULL, email varchar NOT NULL, locale varchar, name varchar NOT NULL, password_digest varchar NOT NULL, recovery_code_digests json DEFAULT '[]' NOT NULL, role_id integer, totp_enabled boolean DEFAULT 0 NOT NULL, totp_enabled_at datetime(6), totp_last_counter bigint, totp_secret varchar, updated_at datetime(6) NOT NULL, verified boolean DEFAULT 0 NOT NULL);
CREATE TABLE sessions (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, created_at datetime(6) NOT NULL, ip_address varchar, last_seen_at datetime(6), updated_at datetime(6) NOT NULL, user_agent varchar, user_id integer NOT NULL);
CREATE TABLE api_tokens (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, created_at datetime(6) NOT NULL, last_used_at datetime(6), last_used_ip varchar, prefix varchar NOT NULL, token text, token_digest varchar NOT NULL, updated_at datetime(6) NOT NULL, user_id integer NOT NULL);
CREATE TABLE pages (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, blocks json DEFAULT '[]' NOT NULL, category_entry_id integer, created_at datetime(6) NOT NULL, deleted_at datetime(6), depth integer DEFAULT 0 NOT NULL, frontmatter json DEFAULT '{}' NOT NULL, locale varchar DEFAULT 'en' NOT NULL, lock_version integer DEFAULT 0 NOT NULL, parent_id integer, path varchar NOT NULL, publish_at datetime(6), published_at datetime(6), schema json DEFAULT '{"fields":[]}' NOT NULL, seo json DEFAULT '{}' NOT NULL, slug varchar NOT NULL, status varchar DEFAULT 'draft' NOT NULL, title varchar NOT NULL, translation_group_id integer, unpublish_at datetime(6), updated_at datetime(6) NOT NULL);
CREATE TABLE settings (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, created_at datetime(6) NOT NULL, data json DEFAULT '{}' NOT NULL, key varchar NOT NULL, secrets text, updated_at datetime(6) NOT NULL);
CREATE TABLE assets (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, alt varchar, caption text, created_at datetime(6) NOT NULL, deleted_at datetime(6), description text, focal_x float DEFAULT 0.5 NOT NULL, focal_y float DEFAULT 0.5 NOT NULL, folder varchar DEFAULT '/' NOT NULL, name varchar, updated_at datetime(6) NOT NULL);
CREATE TABLE active_storage_blobs (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, byte_size bigint NOT NULL, checksum varchar, content_type varchar, created_at datetime(6) NOT NULL, filename varchar NOT NULL, key varchar NOT NULL, metadata text, service_name varchar NOT NULL);
CREATE UNIQUE INDEX index_active_storage_blobs_on_key ON active_storage_blobs (key);
CREATE TABLE active_storage_attachments (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, blob_id bigint NOT NULL, created_at datetime(6) NOT NULL, name varchar NOT NULL, record_id bigint NOT NULL, record_type varchar NOT NULL);
CREATE TABLE forms (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, created_at datetime(6) NOT NULL, deleted_at datetime(6), fields json DEFAULT '[]' NOT NULL, notify_webhook_url varchar, slug varchar NOT NULL, status varchar DEFAULT 'draft' NOT NULL, submit_label varchar DEFAULT 'Submit' NOT NULL, submit_url varchar, success_message text, title varchar NOT NULL, updated_at datetime(6) NOT NULL, webhook_body json DEFAULT '{}' NOT NULL);
CREATE TABLE form_submissions (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, created_at datetime(6) NOT NULL, data json DEFAULT '{}' NOT NULL, form_id integer NOT NULL, ip varchar, meta json DEFAULT '{}' NOT NULL, updated_at datetime(6) NOT NULL);
CREATE TABLE form_emails (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, blocks json DEFAULT '[]' NOT NULL, body text DEFAULT '' NOT NULL, created_at datetime(6) NOT NULL, enabled boolean DEFAULT 1 NOT NULL, form_id integer NOT NULL, from_field varchar, kind varchar NOT NULL, recipients varchar, site_template text, site_template_digest varchar, site_template_received_at datetime(6), subject varchar DEFAULT '' NOT NULL, updated_at datetime(6) NOT NULL);
CREATE TABLE quote_requests (id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, email varchar, created_at datetime(6) NOT NULL);
`

const (
	millToken = "abcdtoken1234567890"
	millBody  = "the mill in 1890"
	lostToken = "gonetoken000000"
	stamp     = "2026-05-10 12:00:00.000000"
)

// railsDB is a Rails site's database, its blob keys prefixed with prefix
// ("acme/" for the old multi-tenant deployment's, "" otherwise).
func railsDB(t *testing.T, prefix string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hash, _ := secret.HashPassword("password1234")
	fields, _ := json.Marshal([]any{
		map[string]any{"name": "Full Name", "label": "Full name", "type": "text", "required": true},
		map[string]any{"name": "email", "label": "Email", "type": "email", "required": true},
		map[string]any{"name": "topic", "label": "Topic", "type": "select", "options": []any{"Sales", map[string]any{"label": "Support", "value": "support"}}},
		map[string]any{"name": "sig", "label": "Signature", "type": "signature"},
	})
	stmts := []struct {
		q    string
		args []any
	}{
		{railsSchema, nil},
		{`INSERT INTO schema_migrations VALUES ('20260510000000')`, nil},
		{`INSERT INTO roles (id, name, permissions, system, created_at, updated_at) VALUES (1, 'Admin', '["manage:all"]', 1, ?, ?)`, []any{stamp, stamp}},
		{`INSERT INTO users (email, name, password_digest, role_id, totp_enabled, created_at, updated_at) VALUES ('owner@acme.test', 'Owner', ?, 1, 0, ?, ?), ('two@acme.test', 'Two', ?, 1, 1, ?, ?)`, []any{hash, stamp, stamp, hash, stamp, stamp}},
		{`INSERT INTO sessions (user_id, created_at, updated_at) VALUES (1, ?, ?)`, []any{stamp, stamp}},
		{`INSERT INTO api_tokens (user_id, prefix, token, token_digest, created_at, updated_at) VALUES (1, 'lp_old', '{"p":"c2VjcmV0","h":{"iv":"x","at":"y"}}', 'digest', ?, ?)`, []any{stamp, stamp}},
		{`INSERT INTO pages (slug, path, title, status, created_at, updated_at) VALUES ('about', 'about', 'About the mill', 'published', ?, ?)`, []any{stamp, stamp}},
		{`INSERT INTO settings (key, data, created_at, updated_at) VALUES ('general', '{"title":"Acme Mill"}', ?, ?)`, []any{stamp, stamp}},
		{`INSERT INTO assets (id, name, created_at, updated_at) VALUES (1, 'The mill', ?, ?), (2, 'Lost', ?, ?)`, []any{stamp, stamp, stamp, stamp}},
		{`INSERT INTO active_storage_blobs (id, key, filename, content_type, metadata, service_name, byte_size, checksum, created_at) VALUES (1, ?, 'mill.txt', 'text/plain', '{}', 'local', ?, ?, ?), (2, ?, 'lost.txt', 'text/plain', '{}', 'local', 1, ?, ?)`,
			[]any{prefix + millToken, len(millBody), md5b64([]byte(millBody)), stamp, prefix + lostToken, md5b64([]byte("x")), stamp}},
		{`INSERT INTO active_storage_attachments (name, record_type, record_id, blob_id, created_at) VALUES ('file', 'Asset', 1, 1, ?), ('file', 'Asset', 2, 2, ?)`, []any{stamp, stamp}},
		{`INSERT INTO forms (id, slug, title, status, fields, submit_label, success_message, submit_url, created_at, updated_at) VALUES (1, 'Contact', 'Contact us', 'published', ?, 'Send it', 'Ta!', 'https://acme.test/thanks', ?, ?)`, []any{string(fields), stamp, stamp}},
		{`INSERT INTO forms (id, slug, title, status, fields, created_at, updated_at, deleted_at) VALUES (2, 'old', 'Old', 'published', '[]', ?, ?, ?)`, []any{stamp, stamp, stamp}},
		{`INSERT INTO form_emails (form_id, kind, enabled, subject, body, recipients, created_at, updated_at) VALUES (1, 'notification', 1, '[{{form_title}}] New #{{submission_id}}', ?, 'a@acme.test, b@acme.test', ?, ?)`,
			[]any{"A new one from **{{Full Name}}**.\n\nSent from {{ip}}.", stamp, stamp}},
		{`INSERT INTO form_emails (form_id, kind, enabled, subject, body, from_field, created_at, updated_at) VALUES (1, 'confirmation', 1, 'Thanks', 'Hi {{Full Name}}', 'email', ?, ?)`, []any{stamp, stamp}},
		{`INSERT INTO form_submissions (form_id, data, meta, ip, created_at, updated_at) VALUES (1, '{"Full Name":"Ada","email":"ada@example.test","topic":"support"}', '{"user_agent":"Firefox","referer":"https://acme.test/contact"}', '192.0.2.1', '2026-06-01 09:00:00', ?)`, []any{stamp}},
		{`INSERT INTO form_submissions (form_id, data, meta, ip, created_at, updated_at) VALUES (1, '{"Full Name":"Bot"}', '{"spam":true}', '192.0.2.2', '2026-06-02 09:00:00', ?)`, []any{stamp}},
		{`INSERT INTO form_submissions (form_id, data, meta, created_at, updated_at) VALUES (2, '{}', '{}', ?, ?)`, []any{stamp, stamp}},
		{`INSERT INTO quote_requests (email, created_at) VALUES ('q@acme.test', ?)`, []any{stamp}},
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(t.Context(), s.q, s.args...); err != nil {
			t.Fatalf("%s: %v", s.q, err)
		}
	}
	db.Close()
	return readFile(t, path)
}

// apply stages src into dataDir and applies it.
func apply(t *testing.T, src string, opts restore.Options) *restore.Report {
	t.Helper()
	staged, err := restore.Stage(t.Context(), src, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Read back from disk, as the admin does between staging and applying:
	// the report must survive the round trip.
	imp, err := restore.Load(opts.DataDir, staged.ID)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := imp.Apply(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func openDB(t *testing.T, dataDir string) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(dataDir, "cms.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func kv(t *testing.T, db *store.DB, key string, v any) bool {
	t.Helper()
	var raw []byte
	err := db.QueryRowContext(t.Context(), `SELECT value FROM plugin_kv WHERE plugin_key = 'forms' AND key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := v.(*string); ok {
		*s = string(raw)
		return true
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
	return true
}

func TestRailsSiteBackup(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	manifest, _ := json.Marshal(map[string]any{"tenant": "acme", "generated_at": "2026-06-03T10:00:00Z", "includes": map[string]any{"db": true, "blobs": 1}, "missing_blobs": []string{"acme/" + lostToken}})
	archive := writeFile(t, dir, "cms-backup-acme.tar.gz", tarball(t, true,
		entry{name: "manifest.json", body: manifest},
		entry{name: "db/main.sqlite3", body: railsDB(t, "acme/")},
		entry{name: "blobs/acme/" + millToken, body: []byte(millBody)},
		entry{name: "blobs/acme/zzzzunreferenced", body: []byte("nobody's")},
		entry{name: "README.txt", body: []byte("hello")},
	))
	rep := apply(t, archive, restore.Options{DataDir: data, Actor: "test"})

	if rep.Format != restore.FormatRailsSite || rep.Manifest == nil || rep.Manifest.Tenant != "acme" || rep.Ignored != 1 {
		t.Fatalf("format: %+v", rep)
	}
	if b := rep.Blobs; b.Total != 2 || b.Copied != 1 || b.Missing != 1 || b.Renamed != 2 || b.Unreferenced != 1 {
		t.Fatalf("blobs: %+v", b)
	}
	if !slices.Contains(rep.Unused, "quote_requests") || rep.TwoFactorUsers != 1 || rep.Secrets.RailsEncrypted["api_tokens.token"] != 1 {
		t.Fatalf("survey: %+v", rep)
	}
	if rep.Cleared["sessions"] != 1 || !rep.Applied || rep.ReplacedDir == "" {
		t.Fatalf("apply: %+v", rep)
	}
	// The files are where this core keeps them, by the bare key.
	if got := readFile(t, filepath.Join(data, "storage", "ab", "cd", millToken)); string(got) != millBody {
		t.Fatalf("blob: %q", got)
	}
	if _, err := os.Stat(filepath.Join(data, "storage", "zz", "zz", "zzzzunreferenced")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an unreferenced file was kept")
	}
	db := openDB(t, data)
	keys, err := store.Strings(t.Context(), db, `SELECT key FROM active_storage_blobs ORDER BY id`)
	if err != nil || !slices.Equal(keys, []string{millToken, lostToken}) {
		t.Fatalf("keys: %v %v", keys, err)
	}
	if n, _ := store.Count(t.Context(), db, `SELECT count(*) FROM audit_logs WHERE action = 'backup.imported' AND actor_label = 'test'`); n != 1 {
		t.Fatal("no audit entry")
	}

	// The forms, as the Go plugin keeps them.
	if f := rep.Forms; f.Forms != 1 || f.Submissions != 2 || f.Emails != 2 || f.Skipped != 1 {
		t.Fatalf("forms: %+v", f)
	}
	notCarried := strings.Join(rep.Forms.NotCarried, "\n")
	for _, want := range []string{"{{ip}}", `"signature"`, "https://acme.test/thanks", "in the trash"} {
		if !strings.Contains(notCarried, want) {
			t.Errorf("not carried lacks %s:\n%s", want, notCarried)
		}
	}
	var form map[string]any
	if !kv(t, db, "form:contact", &form) {
		t.Fatal("no form:contact")
	}
	fields, _ := form["fields"].([]any)
	names := []string{}
	for _, f := range fields {
		names = append(names, f.(map[string]any)["name"].(string))
	}
	if !slices.Equal(names, []string{"full_name", "email", "topic", "sig"}) || form["status"] != "active" || form["submit_label"] != "Send it" {
		t.Fatalf("form: %v %v", names, form)
	}
	if form["admin_subject"] != "[{{ form_name }}] New #{{ submission_id }}" || !strings.Contains(form["admin_message"].(string), "<strong>{{ full_name }}</strong>") ||
		strings.Join(toStrings(form["admin_to"]), ",") != "a@acme.test,b@acme.test" || form["admin_enabled"] != true {
		t.Fatalf("admin email: %v", form)
	}
	if form["client_enabled"] != true || form["client_to_field"] != "email" || form["client_message"] != "<p>Hi {{ full_name }}</p>" {
		t.Fatalf("client email: %v", form)
	}
	if opts := toStrings(fields[2].(map[string]any)["options"]); !slices.Equal(opts, []string{"Sales", "support"}) {
		t.Fatalf("options: %v", opts)
	}
	var stats map[string]int
	if !kv(t, db, "stat:contact", &stats) || stats["read"] != 1 || stats["spam"] != 1 || stats["total"] != 2 {
		t.Fatalf("stats: %v", stats)
	}
	subs, _ := store.Strings(t.Context(), db, `SELECT key FROM plugin_kv WHERE plugin_key = 'forms' AND key LIKE 'sub:contact:%' ORDER BY key`)
	if len(subs) != 2 {
		t.Fatalf("submissions: %v", subs)
	}
	// Newest first, as the plugin lists them.
	var newest map[string]any
	kv(t, db, subs[0], &newest)
	if newest["status"] != "spam" {
		t.Fatalf("order: %v", newest)
	}
	var ada map[string]any
	kv(t, db, subs[1], &ada)
	if v, _ := ada["values"].(map[string]any); v["full_name"] != "Ada" || v["topic"] != "support" || ada["ip"] != "192.0.2.1" || ada["user_agent"] != "Firefox" || ada["submitted_at"] != "2026-06-01T09:00:00Z" {
		t.Fatalf("submission: %v", ada)
	}
	var slug string
	if !kv(t, db, "all:"+ada["id"].(string), &slug) || slug != "contact" {
		t.Fatalf("all: %q", slug)
	}
	db.Close()

	// And the site runs on it: the page, the file, the forms plugin.
	app, err := cms.NewApp(cms.Config{DataDir: data, BaseURL: "http://cms.test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	token, err := app.CreateServiceTokenForRole(t.Context(), "TEST", "Admin")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	body := fetch(t, srv.URL+"/api/x/forms/forms", token)
	if !strings.Contains(body, `"slug":"contact"`) || !strings.Contains(body, `"submissions":1`) {
		t.Fatalf("forms plugin: %s", body)
	}
	if body := fetch(t, srv.URL+"/files/"+millToken+"/mill.txt", ""); body != millBody {
		t.Fatalf("file: %q", body)
	}
	if body := fetch(t, srv.URL+"/api/pages", token); !strings.Contains(body, "About the mill") {
		t.Fatalf("pages: %s", body)
	}
}

func toStrings(v any) []string {
	out := []string{}
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func fetch(t *testing.T, url, token string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

func TestRailsDataBackup(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	// tar -czf … -C $CMS_DATA_DIR . : every database, the files at the
	// root by Active Storage's own sharding.
	archive := writeFile(t, dir, "cms-data-20260603-100000.tar.gz", tarball(t, true,
		entry{name: "./", typ: tar.TypeDir},
		entry{name: "./production.sqlite3", body: railsDB(t, "")},
		entry{name: "./production_cache.sqlite3", body: []byte("not needed")},
		entry{name: "./ab/", typ: tar.TypeDir},
		entry{name: "./ab/cd/" + millToken, body: []byte(millBody)},
	))
	rep := apply(t, archive, restore.Options{DataDir: data})
	if rep.Format != restore.FormatRailsData || rep.Blobs.Copied != 1 || rep.Blobs.Missing != 1 || rep.Blobs.Renamed != 0 || rep.Forms.Forms != 1 {
		t.Fatalf("report: %+v", rep)
	}
	if got := readFile(t, filepath.Join(data, "storage", "ab", "cd", millToken)); string(got) != millBody {
		t.Fatalf("blob: %q", got)
	}

	// A file that isn't what its checksum says is reported, not hidden.
	data2 := t.TempDir()
	archive = writeFile(t, dir, "damaged.tar", tarball(t, false,
		entry{name: "production.sqlite3", body: railsDB(t, "")},
		entry{name: "storage/ab/cd/" + millToken, body: []byte("the mill in 1891")},
	))
	rep = apply(t, archive, restore.Options{DataDir: data2})
	if rep.Blobs.Mismatched != 1 || !strings.Contains(strings.Join(rep.Warnings, "\n"), "mill.txt") {
		t.Fatalf("mismatch: %+v", rep)
	}
}

func TestUnsafeArchives(t *testing.T) {
	t.Parallel()
	db := railsDB(t, "")
	zeros := make([]byte, 8<<20)
	limits := restore.Limits{MaxTotal: 4 << 20, MaxFile: 1 << 20}
	tests := []struct {
		name    string
		archive []byte
		want    error
	}{
		{"climbs out", tarball(t, true, entry{name: "production.sqlite3", body: db}, entry{name: "storage/../../evil", body: []byte("x")}), restore.ErrInvalid},
		{"absolute", tarball(t, true, entry{name: "/tmp/evil", body: []byte("x")}), restore.ErrInvalid},
		{"symlink", tarball(t, true, entry{name: "cms.sqlite3", typ: tar.TypeSymlink, link: "/etc/passwd"}), restore.ErrInvalid},
		{"hard link", tarball(t, true, entry{name: "cms.sqlite3", typ: tar.TypeLink, link: "../outside"}), restore.ErrInvalid},
		{"device", tarball(t, true, entry{name: "cms.sqlite3", typ: tar.TypeChar}), restore.ErrInvalid},
		{"file over the limit", tarball(t, true, entry{name: "cms.sqlite3", body: make([]byte, 2<<20)}), restore.ErrTooLarge},
		{"gzip bomb", tarball(t, true, entry{name: "padding.bin", body: zeros}), restore.ErrTooLarge},
		{"not an archive", []byte("this is not a backup at all"), restore.ErrInvalid},
		{"no database", tarball(t, true, entry{name: "manifest.json", body: []byte(`{}`)}), restore.ErrInvalid},
		{"two databases", tarball(t, true, entry{name: "cms.sqlite3", body: db}, entry{name: "production.sqlite3", body: db}), restore.ErrInvalid},
		{"not a site", tarball(t, true, entry{name: "cms.sqlite3", body: emptySQLite(t)}), restore.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parent := t.TempDir()
			data := filepath.Join(parent, "data")
			src := writeFile(t, parent, "backup.tar.gz", tt.archive)
			_, err := restore.Stage(t.Context(), src, restore.Options{DataDir: data, Limits: limits})
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			// Nothing outside, and no staging left behind.
			if _, err := os.Stat(filepath.Join(parent, "evil")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("wrote outside the staging directory")
			}
			if left, _ := os.ReadDir(filepath.Join(data, "imports")); len(left) != 0 {
				t.Fatalf("staging left: %v", left)
			}
		})
	}
}

func emptySQLite(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE notes (body text)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return readFile(t, path)
}

func TestDirectorySourceRefusesLinks(t *testing.T) {
	t.Parallel()
	src, data := t.TempDir(), t.TempDir()
	writeFile(t, src, "production.sqlite3", railsDB(t, ""))
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "passwd")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if _, err := restore.Stage(t.Context(), src, restore.Options{DataDir: data}); !errors.Is(err, restore.ErrInvalid) {
		t.Fatalf("got %v", err)
	}
	os.Remove(filepath.Join(src, "passwd"))
	if rep := apply(t, src, restore.Options{DataDir: data}); rep.Format != restore.FormatRailsData {
		t.Fatalf("directory: %+v", rep)
	}
}

// ---- a Go site, its secrets, and the site it replaces ----------------------

// goSite is a Go core data directory's database, its secrets sealed with
// key: a webhook's signing secret and the SMTP password.
func goSite(t *testing.T, key *secret.Key) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cms.sqlite3")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := store.Now().DB()
	data, _ := json.Marshal(map[string]any{"smtp_host": "smtp.acme.test", "smtp_password": key.Seal("hunter22")})
	for _, q := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO roles (name, permissions, system, created_at, updated_at) VALUES ('Administrator', '["manage:all"]', 1, ?, ?)`, []any{now, now}},
		{`INSERT INTO users (email, name, password_digest, role_id, created_at, updated_at) VALUES ('a@acme.test', 'A', 'x', 1, ?, ?)`, []any{now, now}},
		{`INSERT INTO webhooks (name, url, secret, created_at, updated_at) VALUES ('Build', 'https://hooks.test', ?, ?, ?)`, []any{key.Seal("whsec"), now, now}},
		{`INSERT INTO settings (key, data, created_at, updated_at) VALUES ('email', ?, ?, ?)`, []any{string(data), now, now}},
		{`INSERT INTO admin_sessions (token_digest, csrf, user_id, created_at) VALUES ('d', 'c', 1, ?)`, []any{now}},
	} {
		if _, err := db.ExecContext(t.Context(), q.q, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	return readFile(t, path)
}

func secrets(t *testing.T, data string) (hook, smtp string) {
	t.Helper()
	db := openDB(t, data)
	if err := db.QueryRowContext(t.Context(), `SELECT secret FROM webhooks`).Scan(&hook); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRowContext(t.Context(), `SELECT data FROM settings WHERE key = 'email'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal([]byte(raw), &m)
	return hook, m["smtp_password"].(string)
}

func TestSealedSecrets(t *testing.T) {
	t.Parallel()
	old := secret.FromPassphrase("the old install's SECRET_KEY")
	db := goSite(t, old)
	dir := t.TempDir()
	plain := writeFile(t, dir, "plain.tar.gz", tarball(t, true, entry{name: "cms.sqlite3", body: db}))
	withKey := writeFile(t, dir, "with-key.tar.gz", tarball(t, true, entry{name: "secret.key", body: []byte(old.Hex() + "\n")}, entry{name: "cms.sqlite3", body: db}))

	target := func(t *testing.T) (string, *secret.Key) {
		t.Helper()
		data := t.TempDir()
		k, err := secret.Load(data, "")
		if err != nil {
			t.Fatal(err)
		}
		return data, k
	}

	t.Run("no key: cleared, never left sealed", func(t *testing.T) {
		t.Parallel()
		data, _ := target(t)
		rep := apply(t, plain, restore.Options{DataDir: data})
		if s := rep.Secrets.Done; s.Total != 2 || s.Cleared != 2 || s.Columns["webhooks.secret"] != 1 || s.Columns["settings.data"] != 1 {
			t.Fatalf("secrets: %+v", s)
		}
		if hook, smtp := secrets(t, data); hook != "" || smtp != "" {
			t.Fatalf("left: %q %q", hook, smtp)
		}
		if rep.Cleared["admin_sessions"] != 1 {
			t.Fatalf("sessions: %v", rep.Cleared)
		}
	})
	t.Run("the old SECRET_KEY: re-sealed", func(t *testing.T) {
		t.Parallel()
		data, k := target(t)
		rep := apply(t, plain, restore.Options{DataDir: data, SourceKey: "the old install's SECRET_KEY"})
		if s := rep.Secrets.Done; s.Resealed != 2 || s.Cleared != 0 {
			t.Fatalf("secrets: %+v", s)
		}
		hook, smtp := secrets(t, data)
		if p, ok := k.Open(hook); !ok || p != "whsec" {
			t.Fatalf("hook: %q", hook)
		}
		if p, ok := k.Open(smtp); !ok || p != "hunter22" {
			t.Fatalf("smtp: %q", smtp)
		}
	})
	t.Run("the key in the backup", func(t *testing.T) {
		t.Parallel()
		data, k := target(t)
		rep := apply(t, withKey, restore.Options{DataDir: data})
		if !rep.Secrets.KeyInBackup || rep.Secrets.Done.Resealed != 2 {
			t.Fatalf("secrets: %+v", rep.Secrets)
		}
		if hook, _ := secrets(t, data); hook == "" {
			t.Fatal("cleared")
		} else if p, _ := k.Open(hook); p != "whsec" {
			t.Fatalf("hook: %q", p)
		}
	})
	t.Run("a wrong key is refused before anything changes", func(t *testing.T) {
		t.Parallel()
		data, _ := target(t)
		opts := restore.Options{DataDir: data, SourceKey: strings.Repeat("ab", 32)}
		imp, err := restore.Stage(t.Context(), plain, opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := imp.Apply(t.Context(), opts); !errors.Is(err, restore.ErrWrongKey) {
			t.Fatalf("got %v", err)
		}
		if _, err := os.Stat(filepath.Join(data, "cms.sqlite3")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("applied anyway")
		}
	})
	t.Run("the same install's key: kept as is", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		rep := apply(t, plain, restore.Options{DataDir: data, SecretKey: "the old install's SECRET_KEY"})
		if s := rep.Secrets.Done; s.Kept != 2 || s.Cleared+s.Resealed != 0 {
			t.Fatalf("secrets: %+v", s)
		}
	})
}

func TestRefusesAnInstallWithUsersAndDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	dir, data := t.TempDir(), t.TempDir()
	existing := goSite(t, secret.FromPassphrase("mine"))
	writeFile(t, data, "cms.sqlite3", existing)
	if err := os.MkdirAll(filepath.Join(data, "variants"), 0o750); err != nil {
		t.Fatal(err)
	}
	keyBefore := readFile(t, writeFile(t, data, "secret.key", []byte(strings.Repeat("cd", 32))))
	archive := writeFile(t, dir, "rails.tar.gz", tarball(t, true, entry{name: "db/main.sqlite3", body: railsDB(t, "")}))

	// Staging is the dry run: it says what's there, and changes nothing.
	opts := restore.Options{DataDir: data}
	imp, err := restore.Stage(t.Context(), archive, opts)
	if err != nil {
		t.Fatal(err)
	}
	if imp.Report.TargetUsers != 1 || imp.Report.Applied {
		t.Fatalf("report: %+v", imp.Report)
	}
	var text bytes.Buffer
	imp.Report.WriteText(&text)
	if !strings.Contains(text.String(), "Staged (nothing changed yet)") || !strings.Contains(text.String(), "forms:") {
		t.Fatalf("text:\n%s", text.String())
	}
	if loaded, err := restore.Load(data, imp.ID); err != nil || loaded.Report.Format != restore.FormatRailsSite {
		t.Fatalf("load: %v", err)
	}
	if _, err := imp.Apply(t.Context(), opts); !errors.Is(err, restore.ErrHasUsers) {
		t.Fatalf("got %v", err)
	}
	if err := imp.Discard(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, filepath.Join(data, "cms.sqlite3")), existing) || !bytes.Equal(readFile(t, filepath.Join(data, "secret.key")), keyBefore) {
		t.Fatal("the dry run changed the site")
	}
	if matches, _ := filepath.Glob(filepath.Join(data, "replaced-*")); len(matches) != 0 {
		t.Fatalf("replaced: %v", matches)
	}
	if left, _ := os.ReadDir(filepath.Join(data, "imports")); len(left) != 0 {
		t.Fatalf("staging left: %v", left)
	}

	// With force it's replaced, and what it replaced is kept whole, ready
	// to put back.
	opts.Force = true
	rep := apply(t, archive, opts)
	if !bytes.Equal(readFile(t, filepath.Join(rep.ReplacedDir, "cms.sqlite3")), existing) {
		t.Fatal("the replaced database wasn't kept")
	}
	if _, err := os.Stat(filepath.Join(data, "variants")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the variants cache was kept")
	}
	if err := restore.Rollback(data, rep.ReplacedDir); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, filepath.Join(data, "cms.sqlite3")), existing) {
		t.Fatal("rollback didn't put it back")
	}
}
