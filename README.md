# Libre Publish

A headless CMS as one file: the API, the admin, SQLite and a WebAssembly
plugin host, compiled into a single static binary (about 20 MB, no cgo) for
Linux, macOS and Windows.

```sh
curl -fsSL https://raw.githubusercontent.com/Martin-Business-Consultants/librepublishgo/main/install.sh | sh
librepublish serve --data ./data
# open http://localhost:8080/cms/ and make the first administrator
```

That is the whole install. No Ruby, no Docker (though there is a Dockerfile), no separate database, no
restart to add a plugin. It idles at about 28 MB of memory. The script picks
the build for your machine from the latest release and checks it against the
release's checksums. Without it, download `librepublish-<os>-<arch>` from
[Releases](https://github.com/Martin-Business-Consultants/librepublishgo/releases)
and run it.

This is the Go core of Libre Publish, a rewrite of the Rails core (which
stays at [libre-publish](https://github.com/Martin-Business-Consultants/libre-publish)
as the reference). It opens a Rails core's database as it is and imports its
backups (docs/import.md); its delivery API and webhooks are held to
`contracts/`, which the Astro integration (`packages/astro`) is tested
against.

## Running it

From source (Go 1.26; `mise install` sets it up):

```sh
make run                      # go run …serve --data ./data, on :8080
make build                    # dist/librepublish for this machine
make test                     # vet + the integration tests
make dist                     # every platform, with checksums.txt
make plugin                   # dist/visits.wasm, the example plugin
```

Releases: set `VERSION` to the release's number (it's what an image built
from the repository reports, and what updates compare), commit it, push a
matching `vX.Y.Z` tag, and `.github/workflows/release.yml` tests,
builds every platform and publishes them with `checksums.txt`, which
`install.sh` downloads.

| Flag | Variable | What |
| --- | --- | --- |
| `--data` | `DATA_DIR` | Where everything lives: `cms.sqlite3`, `storage/` (uploads), `variants/`, `secret.key`. Back this directory up and you've backed up the site. |
| `--addr` | `ADDR` / `PORT` | Where to listen (`:8080`). |
| `--url` | `APP_URL` | The CMS's own address. Asset URLs start with it. |
| | `SITE_KEY` | The tenant name webhooks carry (default: the first label of the URL's host). |
| | `SECRET_KEY` | Instead of `DATA_DIR/secret.key`: the key that seals token plaintexts and webhook secrets. |
| | `CMS_DELIVERY_RATE_LIMIT` | Requests a minute per token on `/api/v1` (1200; `0` for none). |
| | `TRUST_PROXY=1` | Behind Caddy, nginx or a load balancer: read the client's address from `X-Forwarded-For`. |
| | `LOG_LEVEL` | `debug`, `info` (what `serve` logs by default), `warn` or `error`. |
| | `LOG_FORMAT` | `text` (the default) or `json`. Each request's line carries its `request_id`, which is also the `X-Request-ID` answered (a proxy's own is kept). |

Put TLS in front of it (Caddy is one line: `cms.example.com { reverse_proxy :8080 }`).

On a host that runs containers, the `Dockerfile` builds the same binary
into a small image that runs it as an unprivileged user, whoever owns the
volume the host mounts (`docker/librepublish` hands it over). It listens on
port 80, where Kamal-style hosts (kamal-proxy) send traffic and health
checks (`PORT` moves it), logs JSON, and `GET /up` is its health check. Set
`APP_URL` (or let the admin record its address in Settings › General) and,
behind the host's proxy, `TRUST_PROXY=1`.

**Mount a named volume at `/data`, one per site.** `/data` is everything
the site keeps, and a container's own disk, or an anonymous volume, is gone
with the container at the next deploy. So the CMS refuses to start in a
container without a persistent volume there (`EPHEMERAL_DATA=1` allows a
trial that may lose it), and the deploy fails before anything is lost.

```sh
docker build -t librepublish --build-arg VERSION=$(git describe --tags --always) .
docker run -p 8080:80 -v oldmill-data:/data -e SITE_KEY=oldmill -e APP_URL=https://cms.oldmillbrew.com librepublish
docker exec -it <container> librepublish user create --email you@example.com --name You --password -
```

#### Several sites on one server

Each site is its own app: its own container, its own **named volume**
(`oldmill-data`, `acme-data`, never shared), its own `SITE_KEY`, `APP_URL`
and host name. Set `SITE_KEY` on every one: the first start records it in
the volume (`/data/.site`), and a container with another `SITE_KEY` refuses
that volume, so a copied config can't put two sites on one database. Two
containers of the same site may share its volume for the moment a deploy
overlaps them; an import still needs it to itself. Back each volume up
(Tools › Backup, or a copy of the volume with the site stopped).

### Updating

Versions are [releases](https://github.com/Martin-Business-Consultants/librepublishgo/releases):
a `vX.Y.Z` tag, its notes, every platform's binary with `checksums.txt`,
and the container image `ghcr.io/martin-business-consultants/librepublishgo:X.Y.Z`
(and `:latest`). The admin's dashboard tells an Administrator when a newer
one is out (it asks GitHub once a day; `UPDATE_CHECK=0` turns that off).
Take a backup first (Tools › Backup), then:

- **From the admin**, press **Update now** on the dashboard (Administrators):
  it backs up into `DATA_DIR/backups/`, downloads the release into
  `DATA_DIR/releases/` (checked against `checksums.txt`, run once to be sure),
  and restarts on it in place: a few seconds offline. Every start runs the
  newer of that release and the binary started (a newer image deployed later
  takes over), and a release that doesn't come up is gone back from, with
  the dashboard saying so.
- **On a container host**, deploy the new version: the image tag, or the
  `vX.Y.Z` git tag if the host builds from the repository. The new
  container starts beside the old one on the same `/data` and takes over
  once `/up` answers, so there's no downtime.
- **As a binary**, `librepublish update` downloads the newest release for
  the machine, checks it against `checksums.txt` and puts it in place
  (keeping the old one as `librepublish.old`); restart the service.

The database is brought up to date on start, by itself. Going back to an
older version is deploying it again, unless a release's notes say it
changed the database, in which case it's restoring the backup.

### Working on a site: the command line and AI agents

The same binary works on a site, from a terminal, a script or an AI app
(Claude Code, Codex, Cursor, …), with the site's own tools:

```sh
librepublish login https://cms.example.com     # approve it in the admin
librepublish tools                             # what the site has
librepublish list_pages --status draft
librepublish mcp setup                         # add it to your AI apps
```

Every site is also an MCP server at `/mcp`. [docs/agents.md](docs/agents.md)
has the rest.

From the command line, without the admin:

```sh
librepublish user create --email you@example.com --name You --password -     # --role Editor, …
librepublish user password --email you@example.com --password -              # locked out
librepublish token create --name PRODUCTION --role "Production site"          # the site's read token
librepublish seed-blocks                                                      # the starter block types
librepublish import backup.tar.gz --dry-run                                   # what an import would do
librepublish update --check                                                   # is there a newer release?
```

`--password -` reads the password from the first line of stdin; leave
`--password` out and it comes from `LIBREPUBLISH_PASSWORD`. Either keeps it
out of the process list and shell history (`--password '…'` still works).

`GET /api/tools/backup` (the `tools:use` capability) streams a `.tar.gz` of
a consistent copy of the database and every upload. `librepublish import
backup.tar.gz` (with the server stopped) or Tools › Backup › Import a backup
restores it, replacing the site that's there ([docs/import.md](docs/import.md)).

## Moving over from the Rails core

Import its backup: Tools › Backup's `.tar.gz` (the Rails site backup), a data
directory backup (`cms-data-*.tar.gz`), or either unpacked. From the command
line, with the server stopped:

```sh
./librepublish import cms-backup-acme.tar.gz --data data --dry-run   # what it would do
./librepublish import cms-backup-acme.tar.gz --data data             # do it
./librepublish serve --data data --url https://cms.example.com
```

or in the admin, Tools › Backup › Import a backup, which replaces the running
site without a restart. The same import takes this core's own backups, to
move a site between installs. [docs/import.md](docs/import.md) has the
formats, what carries over, the secrets, and putting the previous site back.

The schema keeps Rails' table and column names, so the Go core reads the
database as it is and only adds its own tables (`admin_sessions`,
`revisions`, `plugins`, `plugin_kv`). Pages, entries, collections, globals,
block types, redirects, settings, roles, users, the audit log, content
references and media all carry over, and media URLs move to `/files/<key>/…`.
Rails' Settings › Branding (the `branding` setting: logo, favicon, colors,
font, corners, shadow, top bar) is Settings › Brand: the admin wears it, and
`GET /api/v1/site` delivers it as `branding`. The brief agents write by (the
`brand` setting, read by `cms brand` and the manifest) is Settings › AI.

What doesn't carry over is what Rails encrypted with its own key
(`config/credentials`). The Go core can't read those values:

- **Personal and service tokens** still authenticate, because both cores look
  them up by SHA-256 digest. They just can't be revealed again: rotate one to
  see a new secret. `cms login` hands a person a fresh token for the same reason.
- **Webhook secrets** can't sign. Rotate each webhook's secret (Webhooks › …,
  or `POST /api/webhooks/<id>/rotate_secret`) and give the receiver the new one.
- **Two-factor sign-in.** An account with it on is refused at sign-in rather
  than let in on its password alone. `librepublish user disable-2fa --email …`
  turns it off, knowingly. The Go core has no 2FA of its own yet.
- **Settings' secrets** (the GitHub token, mail credentials) need entering again.
- **The Forms plugin's forms and submissions** become the Go Forms plugin's;
  the import reports what has nowhere to go (a form's own webhook). Commerce,
  Leads and the other Rails plugins' tables are kept but unused.

Passwords carry over: both cores use bcrypt.

The serving modes: this core has no theme, so it is headless. `api_only`
serves everything live over `/api/v1`. `website_api`, and a Rails install's
`website`, serve only what was chosen: collections marked "Available over
the API", plus pages and globals when `api_pages` is on.

## What's in it

**The management API (`/api`)**, frozen at the Rails core's shapes, which the
CLI, MCP and agents read:

- Pages (the tree, paths, bulk delete and status, page schemas) and
  collections with entries (frontmatter schemas, taxonomy, `toggle_field`,
  `update_field`), globals and block types (with the starter pack, exported
  from the Rails core's `BlockType::Defaults`).
- Assets: upload, metadata, folders, `?unused=1`, `used_by`, and on-demand
  width variants (`w640`, `w1280`, `w1920`).
- Redirects (`all`, `resolve`, CSV export and import), settings (secrets
  masked, masked values written back left alone), deploy (`GET`, `PATCH`,
  `trigger`), the audit log, trash (restore, purge; a page takes its children),
  webhooks (deliveries, test, secret rotation), service tokens (reveal,
  rotate, revoke), users, roles, search, references, the manifest,
  `api_tokens/me`, the device login (`cms login`), site health and backup.
- The agent envelope (`X-Agent-Envelope: 1` or `?envelope=1`), with
  breadcrumbs naming real records.
- One error shape: `{"error": code, "message": …}`, with `errors` on a 422
  and `capability` on a 403.

**The publish gate and review.** A write to something live from a role
without `<resource>:publish` is held as a revision, not applied: **202**
`{"status": "pending_review", "revision": {"id", "fields"}, "review_url"}`,
with `ignored_fields` when the write tried to change status. A second
proposal for the same record is a **409**. A write that changes nothing is
`{"status": "unchanged"}`. A person approves the revision in the admin
(Review) or with `POST /api/revisions/<id>/approve`, which applies it as
theirs. In the Rails core this lives in a plugin. Here it's core, because
agents depend on it.

**The delivery API (`/api/v1`)**: `site`, `content` (with `?since=` cursors
and `removed`), `pages`, `collections`, `entries`, `globals`, `schema`,
`sitemap`, `robots(.txt)`, `redirects(.txt)` (a Cloudflare `_redirects`) and
`site_health`. It uses one answer shape, `{data, meta, included}`, trims
records to `?fields=`, embeds what they point at with `?include=`, says how
many there are in `X-Total-Count`/`Link` headers, answers `OPTIONS` with each
resource's JSON Schema,
returns live content only, and expands blocks (`collection_list`,
`contact_info`, synced `pattern`). Every answer carries an `ETag`, honours
`If-None-Match` and sends `Cache-Tag` headers, all under a per-token rate limit.

**Webhooks** signed `X-CMS-Signature: sha256=<HMAC>`, with the envelope and
payload keys `contracts/webhook_contract.json` pins (copied from the Rails
core, which is held to the same file). The tests read it.

**Scheduling.** `publish_at` and `unpublish_at` are acted on within 30 seconds.

**The admin**: Svelte 5, Tailwind 4 and shadcn-svelte (`ui/`), built into
`internal/admin/dist` and embedded in the binary. Its primitives come from
shadcn-svelte's registry (`ui/src/lib/components/ui`, added with
`npx shadcn-svelte@latest add <name>`), in its stock neutral theme: the
sidebar, buttons, inputs, selects, the command menu, dialogs and sheets,
alert dialogs, toasts (Sonner), tables, tabs and badges. The admin's own
pieces (`ui/src/lib/components`) are built from them: fields, a searchable
combobox, tag input, confirmations, postboxes and the Publish box, status
filters, quick filters, bulk and row actions, pagination and ⌘K search. Forms
are drawn from field schemas (every field type, nested), Markdown is written
as rich text (Tiptap, kept as Markdown, with the source a tab away), blocks are edited in
place with a block picker, media is picked or uploaded inline, and blocks,
schema fields and repeater items are reordered by dragging their grip (or with
the arrow keys on it). Schemas are edited as rows rather than JSON.
People can edit together: an open editor holds a lock (shown in lists, with
Take over, and refused to other writers through the API unless they pass
`override_lock=1`), and what's typed is autosaved and offered back after a
crash or a takeover. Trash and status changes are undone from the toast rather
than confirmed; lists have Quick Edit and remembered view options (columns,
rows a page, density); a first publish runs a checklist of fixable
suggestions, then shows where it's live. Lost passwords and new email
addresses are settled by mailed links, and the dashboard copies a Site Health
report for support. It follows the system's light or dark mode, and works on a phone. `make ui`
rebuilds it, and `make ui-dev` serves it with hot reload against `make run`.
The built `dist/` is committed, so `go install` needs no Node.

## Plugins: WebAssembly, with declared permissions

A plugin is one `.wasm` file built with [Extism](https://extism.org), so it
can be written in Go, Rust, TypeScript, Python, C# or Zig. Its `manifest`
export says what it is and what it needs. Installing it means uploading it.
It stays off until a person grants what it asks for, and it is loaded without
a restart. It can reach only the host's fixed capabilities: live content,
its own storage, email through Settings › Email, files into the media
library, outbound HTTP to the hosts it named, and logging. It can't touch the
database, the files, other plugins or the rest of the network, and each call
has a time limit. It can add capabilities to the role editor, and its admin
screens (its menu, tables, forms with a visual form builder, detail views)
are data that the admin draws, not code.

**Forms** ships inside the binary as the first default plugin
([plugins/forms](plugins/forms), [docs/forms.md](docs/forms.md)): forms built
by dragging fields in, submissions kept and exported as CSV, an email to you
and a confirmation to the visitor (each with its own from name and address,
over the defaults in Settings › Email), and spam kept out by a honeypot, a
time check and optional Cloudflare Turnstile.

The contract is in [docs/plugins.md](docs/plugins.md), and
[plugins/visits](plugins/visits) is a small example in Go. The tests build
it, install it, grant it, call its public route, watch it hear
`page.published`, and check that it's refused storage once that grant is
taken back; and they drive Forms from building a form to the emails it
sends.

## How it's put together

```
cmd/librepublish/       the binary: serve, user, token, seed-blocks
contracts/              what clients rely on, pinned (from the Rails core)
install.sh              the one-command install
internal/cms/           the core: the App, content, structure, the HTTP API
  content.go            pages and entries: writes, the publish gate, tree, trash
  events.go             what a write tells webhooks, the deploy and plugins
  scheduler.go          scheduled publishing and cleanup
  revisions.go          held writes and review
  structure.go          collections, globals, block types, seeding
  settings.go audit.go  settings (secrets masked) and the audit log
  auth.go               capabilities, roles, tokens, sessions
  assets.go             uploads, Active Storage layout, variants, file serving
  delivery.go           /api/v1
  api_*.go              /api
  plugin_env.go         what plugins reach of the CMS
internal/store/         SQLite (modernc.org/sqlite, pure Go), Rails-compatible tables, migrations
internal/fields/        the field DSL: validation, JSON Schema, references (Rails' rules)
internal/plugin/        the WebAssembly host
internal/webhook/       signed webhook deliveries
internal/deploy/        build hooks and purges after publishing
internal/mail/          email: MIME and SMTP
internal/secret/        the install's key: sealing, tokens, password hashes
internal/apierr/        the /api error shape
internal/outbound/      the HTTP client webhooks and deploys post with
internal/ratelimit/     per-minute rate limits
internal/jsonv/         reading loosely typed JSON values
internal/admin/         the admin, embedded (dist/, built from ui/)
ui/                     the admin's source: Svelte 5 + Tailwind 4
plugins/visits/         an example plugin
```

## Not yet

These Rails-core features aren't in the Go core, so the CLI commands that
reach them get a 404:

- **The plugins that were Rails engines**: Forms (submissions), Commerce
  (quote requests, invoices), Leads, AI and Market. These are the first to
  port to the WebAssembly contract (docs/plugins.md, "Porting the Rails
  engines").
- **Agent runs and reporting**: `cms work`, `run-*`, `recommend`, `findings`,
  `reports`. **Brand** is read from the `brand` setting (Settings › AI, and
  the manifest), but `cms brand` has no endpoint of its own yet.
- **Themes and website mode**: by design, as this core is headless.
- **The rest:** email (password reset, notifications), two-factor sign-in,
  block patterns' CRUD (synced patterns still expand), version history
  browsing and restore (versions are still recorded), the Build board,
  the image optimizer and bulk uploads, full-text search (search uses
  `LIKE`), IndexNow, CSP reports, the update checker, the MCP server, and the
  agent bootstrap (`/agent/install.sh`).
