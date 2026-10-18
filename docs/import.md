# Importing a backup

An import replaces the whole site in a data directory (pages, entries, files,
people, settings, plugins) with a backup's. It never merges: what was there
is moved aside, whole, so it can be put back.

## What it reads

| Backup | What's in it |
| --- | --- |
| This core's, Tools › Backup or `GET /api/tools/backup` | `manifest.json`, `cms.sqlite3`, `storage/<k0k1>/<k2k3>/<key>`, and `secret.key` when it was asked for |
| The Rails core's site backup, its Tools › Backup | `manifest.json` (the tenant), `db/main.sqlite3`, `blobs/<key>` (keys perhaps `<tenant>/<key>`) |
| The Rails core's data directory backup (`cms-data-*.tar.gz`, from `bin/update` or the nightly job) | `production.sqlite3` (the cache, queue and cable databases are left out) and Active Storage's `<k0k1>/<k2k3>/<key>` |
| Any of them unpacked, or a data directory itself | the same files, in a directory |

A `.tar.gz` or a plain `.tar`. Backups from before this core wrote
`manifest.json` import the same way.

## From the command line

With the server stopped (it holds `DATA_DIR/.lock`; `import` refuses while it's
running, exit code 75):

```sh
librepublish import backup.tar.gz --data ./data --dry-run    # what it would do; changes nothing
librepublish import backup.tar.gz --data ./data              # do it
librepublish import backup.tar.gz --data ./data --force      # …over a site that has people
librepublish import backup.tar.gz --secret-key-file old/secret.key   # carry the old secrets over
librepublish import ./old-data --json                        # a directory; the report as JSON
```

The report goes to stdout; progress to stderr. It exits 0 when done, 1 on an
error, 2 for a wrong command line, 65 for a backup that can't be imported
(damaged, unsafe, from a newer version, a key that doesn't fit), and 75 when
the data directory is in use. A dry run can run beside a server.

## In the admin

Tools › Backup › **Import a backup**, for the Administrator (system) role
only: choose the file, and it's uploaded (with progress) and checked. The
screen shows what's in it: the format, the records, the files (whole, missing
or damaged), the forms converted, the secrets, and every warning. Nothing has
changed yet. Type REPLACE and **Replace this site** applies it without
restarting the server. While it does, every request but `/up` is answered
`503 {"error": "maintenance"}` with `Retry-After`, and the admin says so.
Everyone is signed out, you included: the sessions were the old site's.
Sign in with an account from the imported site.

Uploads are capped at `IMPORT_MAX_BYTES` (10 GiB by default). The same steps
are in the API:

| | |
| --- | --- |
| `POST /api/tools/import?filename=x.tar.gz` | the archive as the body: staged and checked; answers the report and its `id` |
| `GET /api/tools/import/{id}` | the report again |
| `POST /api/tools/import/{id}/apply` | `{"force": true, "secret_key": "…"}`: replaces the site |
| `DELETE /api/tools/import/{id}` | discards the staged copy |

Staged imports nobody applies are removed after a day.

## How it's done

1. **Unpacked into staging**, `DATA_DIR/imports/<id>/`. Only the entries a
   backup holds are written, each to a path built from what it is (the
   database, a blob by its key), never one taken from the archive. An entry
   that's absolute, climbs out with `..`, or is a link or a device fails the
   whole import, as does an archive past the size limits (unpacked, 64 GiB in
   all from the command line and four times `IMPORT_MAX_BYTES` in the admin,
   32 GiB a file, two million entries: a gzip bomb stops there).
2. **Checked**: `PRAGMA quick_check`, the tables a site has, and a schema
   version no newer than this build's. Then it's migrated (as `serve` would)
   in staging.
3. **Files**: a Rails tenant's `<tenant>/` prefix comes off each key, every
   file goes to `storage/<k0k1>/<k2k3>/<key>`, and each is checked against its
   size and MD5 checksum. Missing and damaged ones are reported, not hidden;
   files no blob names are left out.
4. **Forms** (a Rails backup): see below.
5. **Applied**: secrets re-sealed or cleared (below), sessions, edit locks,
   device logins and reset links cleared, `backup.imported` written to the
   audit log. Then the swap: `cms.sqlite3` (and its `-wal`, `-shm`) and
   `storage/` move into `DATA_DIR/replaced-<time>/` with a copy of
   `secret.key`, the staged ones move into place, and the `variants/` cache is
   emptied. A step that fails undoes the ones before it.

In the admin, the running app is closed first (its background work drained
and its database closed), and a new one is opened on what's there after.
If that new one won't open, the previous site is put back and reopened.

## Secrets

This core seals some values with its key (`DATA_DIR/secret.key`, or
`SECRET_KEY`): token plaintexts (for "reveal"), webhook signing secrets, the
SMTP password. Another install's key can't open them. For each:

- readable with this install's key (a backup of this same install): kept;
- readable with the old key: re-sealed with this one. The old key is the
  backup's own `secret.key` when it has one, or the one given
  (`--secret-key-file`, or the admin's field): the old data directory's
  `secret.key`, or the `SECRET_KEY` it ran with;
- otherwise: **cleared**, never left looking like a value that works.
  Tokens still sign in (by digest), but can't be revealed; a webhook with a
  cleared secret won't deliver until it's rotated; the SMTP password has to
  be entered again.

A key given that opens none of them is refused before anything changes.

`?include_secret_key=1` on the backup (Administrator only; the checkbox in
Tools › Backup) puts `secret.key` in the archive. Whoever has that archive
then has every secret in it, not just the data: keep it as you would a
password, and leave it out of backups that are copied off the server.

What the Rails core encrypted (with `config/credentials`) can't be read by
this core with any key: the report counts it. See the README's "Moving over".

## What carries over from the Rails core

Everything the Go core reads: pages, entries, collections, globals, block
types and patterns, redirects, settings, roles, users (with their passwords),
tokens, webhooks, the audit log, content references, media.

The Rails **Forms** plugin's tables become the Go Forms plugin's records:

- each form (not those in the trash): title, slug, status (`published` is
  open; anything else is closed), fields (names made into `lowercase_with_`
  underscores), choices, submit label, success message;
- its emails: `notification` becomes the email to you (recipients, subject,
  body), `confirmation` the one to the visitor (sent to the email field it
  named), with merge tags rewritten (`{{form_title}}` is `{{ form_name }}`,
  `{{submitted_at}}` is `{{ date }}`, field names as renamed). Markdown bodies
  become paragraphs;
- its submissions, marked read (spam stays spam), with their address, browser
  and page, newest first, and the counts.

Reported, not carried: a form's own webhook and body, its submit URL, a field
type the Go plugin lacks (it becomes a text field), a merge tag it doesn't
have, the site-designed email templates, and files uploaded with submissions
(kept in storage; a submission shows the file's name). Settings › Forms (the
sender, captcha keys) are the Rails plugin's; set Settings › Email and each
form's spam settings again.

Kept but unused: the tables of what this core doesn't do (Commerce's
invoices, Leads' quote requests, the chats and AI tables…) and the Rails
forms tables themselves. The report lists them. The Rails core's own
`revisions` (pending API changes) has another shape than this core's, so it's
renamed `rails_revisions`, rows and all, and this core's is created beside it.

Two-factor sign-in can't be checked here yet: people with it on are listed;
`librepublish user disable-2fa --email …` lets them in.

## The content, brought onto this core's terms

Every import cleans up the content on the staged copy, before it's swapped
in, so an imported site is ready as it lands (the report lists each field
it changed):

- **Prose becomes rich text.** `text` and `markdown` fields become
  `richtext` fields, edited with the admin's editor, and their values its
  document: HTML typed into a textarea, Markdown and plain text alike.
  Fields holding markup on purpose (a pasted analytics script) become
  `code`; addresses, alt text and the site's description stay plain text.
  A repeater of paragraphs becomes one rich text field, `body`.
- **Images are their image fields.** `image_url` fields and keys go (an
  image URL that's the only copy of an image is kept, and the report says
  where), and an alt text field beside an image goes onto the image in the
  media library, where alt text lives.
- **An entry's body** (Markdown) holding HTML becomes the Markdown the
  editor writes.
- **SEO's old keys** (`seo_title`, `seo_description`, `no_index`) become the
  standard ones.

The site's templates then draw rich text with the Astro integration's
`<RichText>`. For a site that's already running, Tools › Clean up content
(or `ui/scripts/richtext.mjs`) does the same.

## Putting the previous site back

Everything it replaced is in `DATA_DIR/replaced-<time>/`. With the server
stopped:

```sh
cd data
mkdir failed && mv cms.sqlite3* storage failed/
mv replaced-20261017-120000/cms.sqlite3* replaced-20261017-120000/storage .
```

The replaced site's secrets were sealed with the key in that directory's
`secret.key`; it's this install's own unless the key changed since. Delete
`replaced-*` directories once you're sure.
