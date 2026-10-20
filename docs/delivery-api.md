# The delivery API (`/api/v1`)

What a site reads to build and serve itself. It is read-only, frozen at v1,
and only ever shows what's live — a draft never reaches a site, whatever
token asks. `/api` stays the admin's and the CLI's (writes, drafts,
management).

Authenticate with the site's service token (Settings › Service tokens, the
"Production site" role): `Authorization: Bearer <token>`.

## On, or some of it

This CMS is headless, so the delivery API is on. Settings › General › How
the site is served chooses how much it serves:

| Mode | The delivery API |
| --- | --- |
| **API only** (the default) | Everything live. The CMS's own address shows a blank page, and `robots.txt` disallows all. |
| **Website + API** | What's chosen: each collection that's "Available over the API", and the pages with the globals (`api_pages`), all or none. The rest answers 404. |

A database moved over from the Rails core with **Website** set is served as
**Website + API**.

The management API (`/api/pages`… for the `cms` CLI, MCP and agents) is on
in every mode, as are the Forms plugin's public submissions and webhooks. A
404 says why to a token holder only; anyone else is refused before that.

## One answer shape

    {"data": …, "meta": {…}, "included": {"assets": {"<id>": {…}}}}

- `meta` on lists: `{page, per, total, next_page}` (`?page=`, `?per=` up to 100),
  and the same in headers: `X-Total-Count`, `X-Total-Pages`, and a `Link` with
  `first`, `prev`, `next` and `last` (exposed to browsers on allowed origins).
- `included.assets`: every asset the records point at, resolved — absolute
  URLs on the CMS host, `width`, `height`, `alt`, and `variants`
  (`w640`, `w1280`, `w1920`). Present when a media library is installed.
- `?include=` chooses what's in `included`: `assets` (the default),
  `entries` (live entries the records point at through `record_ref` fields,
  keyed `<collection>/<slug>`), `pages` (keyed by path), any of them comma-
  separated, or `none`.
- `?fields=` keeps only the keys named (and `id`) on each record, comma-
  separated; dot paths reach into objects: `?fields=title,url,seo.meta_title`.
- `OPTIONS` on `pages`, `pages/<path>`, `collections/<slug>/entries`,
  `collections/<slug>/entries/<entry>` and `globals/<slug>` answers the
  resource's JSON Schema (its collection's or global's own fields included)
  and the parameters it takes.
- `Cache-Tag` header: the tags a publish purges (below).
- `X-CMS-Schema-Digest` header on every answer to a token: the schema's
  digest (below), so a site can tell, from any read, that the types it was
  built with are out of date. Exposed to browsers on allowed origins.

## The schema digest

`GET /api/v1/schema` answers `meta.schema_digest`: `"sha256:"` and the hex
SHA-256 of `data` in canonical JSON — compact, every object's keys sorted
bytewise at every depth, strings escaped as Go's `encoding/json` does by
default (`<`, `>`, `&`, U+2028 and U+2029 as \uXXXX escapes), numbers as
written. The same digest is in every `/api/v1` answer's
`X-CMS-Schema-Digest` header. It moves when a block type, a collection, a
global, a live page's own fields or which collections are served changes,
and never for content alone. When it moves, the `schema.changed` webhook
event goes out (docs/webhooks.md) and the site is rebuilt or purged (cache
tag `schema`), as for a publish.

A build pins the digest it generated its types from and compares it with
the header on each read: a different one means regenerate.
`contracts/delivery/schema.json` and `contracts/index.json` hold a real one
to test a site's canonical JSON against.

## Only what the schema declares

What's delivered is what `/api/v1/schema` describes, whatever older data
carries (a Rails import, a field since removed):

- a page's and an entry's `frontmatter`, a global's `data`, and each
  block's `data` keep only the keys their fields declare, at every depth
  (groups, repeaters, blocks fields);
- `seo` keeps the keys `schema.seo` declares, `json_ld` and
  `exclude_from_sitemap` among them; an older `canonical` is delivered as
  `canonical_url` when that's empty;
- synced patterns are expanded wherever blocks are delivered (pages,
  entries, `/content`, a blocks field's lists), so `pattern` isn't among
  `schema.blocks` and a site never gets one;
- a page with no fields of its own delivers `frontmatter: {}`.

Objects in the schema are closed (`additionalProperties: false`, groups and
repeater items too), so a site can validate strictly.

## Text and rich text

Text comes in two kinds, and the schema says which:

- **Plain text**: `string` and `text` fields (`x-cms-type` `string`,
  `text`). Show them as text, escaped, never as HTML.
- **Rich text**: `richtext` fields (`x-cms-type: "richtext"`), the
  admin's editor's own document — Tiptap JSON:
  `{"type": "doc", "content": [{"type": "paragraph", "content": [{"type":
  "text", "text": "Built in "}, {"type": "text", "text": "1869", "marks":
  [{"type": "bold"}]}]}]}`. Its nodes are `paragraph`, `heading` (`level`),
  `bulletList`, `orderedList` (`start`), `listItem`, `taskList`,
  `taskItem` (`checked`), `blockquote`, `codeBlock` (`language`),
  `hardBreak`, `horizontalRule` and `text`; its marks `bold`, `italic`,
  `strike`, `code` and `link` (`href`: http(s), mailto:, tel: or a path).
  The CMS refuses any other node, mark or attribute on save, so a site
  that draws these draws everything. The integration's `<RichText>` does.
- **Markdown**: `markdown` fields (`x-cms-type: "markdown"`,
  `contentMediaType: "text/markdown"`) and an entry's `body_markdown`.
  They're Markdown, written in the admin's editor (bold, italic,
  strikethrough, links, headings, lists, quotes, code, rules, line breaks as
  two trailing spaces), and the site renders them to HTML — with raw HTML
  escaped and links kept to http(s), mailto:, tel: and paths. The Astro
  integration's `<RichText>` / `renderRichText` does exactly that.

HTML is refused on save in both (a `422` naming the field): "Formatting
isn't allowed here: use a Markdown (rich text) field" in a plain text field,
(a field that needs formatting becomes a `richtext` field),
"Use the editor's formatting, not HTML tags" in a Markdown one. A tag is `<`
and the name of an HTML (or SVG/MathML) element, then `>` or attributes up
to one, or a closing `</name>`; a comment, doctype, CDATA or processing
instruction counts too. So `a < b`, `<3`, `List<T>`, `Dear <name>` and
autolinks such as `<https://example.com>` or `<you@example.com>` are fine,
and in Markdown so is anything in a code span, a fenced code block or after
a backslash (`\<p>`). `code` fields are exempt. The rule is for what a save
writes: content saved before it (or imported) keeps being delivered as it
is, and saves that leave its HTML as it was — a new title, a status change,
a scheduled publish, moving a block — go through. Only a field whose text
changes is checked, so new HTML is refused and cleaning it up to Markdown
is accepted; a field that needs formatting becomes a `markdown` field.

## Errors

Every `/api` error — the delivery API's and the management API's — is JSON
in one shape:

    {"error": "<code>", "message": "…"}

with `errors` (per field) on a 422 and `capability` (what the role lacks) on
a 403. The codes: `bad_request` (400: a missing parameter, a body that isn't
JSON), `unauthorized` (401), `forbidden` (403), `not_found` (404, also for an
`/api` path no route matches), `not_acceptable` (406: a format the endpoint
doesn't serve), `conflict` (409), `invalid` (422), `rate_limited` (429, with
`Retry-After`) and `internal_error` (500, never with a backtrace).

## Saving without overwriting someone else (`lock_version`)

The management API's pages, entries and globals (`GET /api/pages/<path>`,
`/api/collections/<slug>/entries/<slug>`, `/api/globals/<slug>`, and what
a create or a save answers) carry `lock_version`: a number that goes up by
one with every save. A client that read a record and wants its save to fail
rather than overwrite a change made since sends it back in the record's
params:

    PATCH /api/pages/about
    {"page": {"title": "About us", "lock_version": 4}}

If the record is still at 4, it's saved (and the answer has `lock_version`
5). If someone saved it since, nothing is written and the answer is a 409:

    {"error": "conflict", "message": "Someone else saved this since you read it (lock_version 4, now 5). Read it again and redo the change."}

Read it again, apply the change to what's there now, and save with the new
`lock_version`. A save without `lock_version` isn't checked (the last
write wins), and the check holds for saves racing each other too: the row
is only written while it's still at the version read. A write that's held
for review (202, a revision) isn't checked until it's approved, when it's
applied to the record as it is then. The delivery API (`/api/v1`) doesn't
carry `lock_version`: it's for writing.

## Caching and limits

- **Conditional GET.** Every answer carries an `ETag`; send it back as
  `If-None-Match` and an unchanged answer is `304 Not Modified`. For
  `/content` the tag stands for the content it was built from, not its
  bytes (each read carries a fresh `meta.cursor`): on a 304, keep what you
  have, cursor included.
- **The CMS caches `/content`** while nothing it's built from changes
  (DeliveryVersion); `included.assets` is resolved on every read.
- **Rate limit.** Each token gets 1,200 requests a minute across `/api/v1`
  (`CMS_DELIVERY_RATE_LIMIT`, requests a minute, `0` for none); past it,
  `429` with `Retry-After`. A build reads about ten times; a site rendered on
  demand reads on each cache miss, so raise it for a busy one.
- **Lists.** Pages and entries are paged (`meta.next_page`); `/content`,
  `/globals`, `/collections`, `/redirects`, `/schema` and `/sitemap` answer
  whole — they're a site's configuration and what a build reads in one go,
  bounded by the site itself.

**`/api` pages differently, on purpose.** The management API's lists keep the
shape they've always had, `{"<items>": […], "page", "per", "total"}` at the
top level (references add `total_pages`); `/api/v1` puts paging in `meta`
with `next_page`. `/api`'s contract is frozen — the CLI, integrations and agents
read it — so it won't move to v1's shape; a new client reading lists should
use `/api/v1`.

## Endpoints

| Endpoint | What |
|---|---|
| `GET /api/v1/site` | Name, public URL, `default_locale`, `locales` (every one but the default is a path prefix: `/fr/…`), contact details, enabled plugins, `plugin_config` (Forms: where its list, definitions and submissions are; Commerce: where quote requests post), `image_widths`: the widths of an image's `variants` and `srcset`, ascending (`[640, 1280, 1920]`), and `branding` (below). `meta.cms_url` is the CMS's own address (`APP_URL`), which asset URLs start with; `meta.cms_version` is the build's version. |
| `GET /api/v1/content[?since=<cursor>]` | Every live page (blocks expanded), entry and global — or, with a previous read's `meta.cursor`, only what changed, plus `data.removed` (what stopped being live). `meta.full: true` means replace what you have. A cursor older than 30 days gets a full answer. |
| `GET /api/v1/pages[?locale=]`, `/api/v1/pages/<path>` | Live pages, blocks expanded (below), with `translations: [{locale, path}]`. |
| `GET /api/v1/collections`, `/collections/<slug>` | Collections and their fields. |
| `GET /api/v1/collections/<slug>/entries[?locale=]`, `…/entries/<slug>` | Live entries, with translations. |
| `GET /api/v1/globals`, `/globals/<slug>` | Globals' data (secret-looking values masked). |
| `GET /api/v1/schema` | JSON Schema (2020-12) for each block type's data (but `pattern`, which is expanded), each collection's fields, each global, pages with fields, and SEO. Each field carries `title` and `x-cms-type` (markdown, asset, link, blocks…) — what a build generates its types from — and a `markdown` field `contentMediaType: "text/markdown"` (above). `meta.schema_digest` (above) and `meta.cms_version`. |
| `GET /api/v1/sitemap` | URLs on the public site, with `lastmod` and hreflang `alternates`. |
| `GET /api/v1/robots` | The site's robots.txt (`data.text`, and `data.mode`: `generated` or `custom`); `/api/v1/robots.txt` is the file itself. Cache tag `robots`. |
| `GET /api/v1/redirects`, `/api/v1/redirects.txt` | Active rules; the `.txt` is a Cloudflare `_redirects` file (exact rules first, wildcards longest first, `:splat`). |
| `GET /api/v1/site_health` | Site health's checks (`state`: passes, fails, yours_to_judge) for a build to hold the site to. |

**Branding.** `data.branding` is the visual identity from Settings › Brand,
with only what's set: `logo` and `favicon` (asset objects, as in
`included.assets`: `url`, `width`, `height`, `alt`, `variants`, `srcset`…),
`primary_color` and `secondary_color` (hex, `#rgb` or `#rrggbb`,
lowercase), `font` (`Inter`, `DM Sans`, `Manrope`, `Playfair Display` or
`Lora`: a Google Fonts family the site loads itself), `border_radius` and
`box_shadow` (`none`, `small`, `medium` or `large`). Every value is checked
when it's saved (a `422` names what's wrong; the setting is replaced whole,
so a cleared field is gone) and again when it's delivered, so imported data
that fails the checks is left out — but a site putting it into CSS should
check it too, as the Astro integration's `<BrandStyles />` does. A logo or
favicon that's been deleted, or isn't an image, is left out. It's part of
`site`, so it's purged with the `site` cache tag. It's stored under the
`branding` setting (the Rails core's key; `/api/settings/branding`).

Forms aren't under `/api/v1`: they're the Forms plugin's public routes,
`GET /api/x/forms/list` (every form: `slug`, `name`, `open`, `digest`),
`GET /api/x/forms/definition?form=<slug>` (a form's fields, each with the
`rules` the server checks, and its `digest`) and `POST
/api/x/forms/submit?form=<slug>`. They need no token. docs/forms.md has
their shapes. Forms and quote requests post straight from the browser to
the CMS; CORS allows the origins in Settings › General.

**Expanded blocks.** A block the CMS fills in carries `resolved` beside its
`data`: a `collection_list` gets `{collection, entries, total, groups?}` —
each entry with its `collection` and `url`, and only published entries, even
when the block is set to show any status (that's for the admin's preview) —
and a `contact_info` gets `{contact}`: the business name, phone, email and
address from Settings › General. A page's `Cache-Tag` names what its blocks
pull in (`collection:<slug>`, `site`), so publishing an entry purges the pages
listing it.

## Builds and publishing

A build reports itself to `POST /api/frontend/builds` with how it renders
(`render`: static, server or hybrid), its `webhook_url` and the
`content_cursor` it built from (docs/install.md, "Static or on demand").
Publishing then rebuilds a static site through the deploy provider (GitHub,
a build hook, or Cloudflare), sends an on-demand (`server`) site a signed
purge (`X-CMS-Signature: sha256=<HMAC>`, and `X-CMS-Signature-V2` with
`X-CMS-Timestamp`, as webhooks are: docs/webhooks.md) naming what changed, and does both
for a `hybrid` one (static pages rebuilt, on-demand ones purged). A report can only
keep a site rebuilt on publish; one that switches it to purges, or moves where
they go, waits for approval in Settings › Deploy (`POST
/api/frontend/delivery_approval`, settings:write) — the site's read-only
token can't make that change on its own.

Cache tags, the same in `Cache-Tag` headers and purges: `page:<path>`,
`entry:<collection>/<slug>`, `collection:<slug>`, `global:<slug>`, `pages`,
`sitemap`, `redirects`, `robots`, `site` (Settings › General's name, URL and contact
details: changing them rebuilds or purges like a publish), `schema` (the
schema's digest moved), and, from Forms, `form:<slug>`.
