# The API contract

These files are the contract between the CMS and the sites built on it.
They're recorded from a real install, not written by hand: the Go tests
and the Astro integration (`packages/astro`) both run against them, so
neither side can drift from the other without a test failing.

| Path | What |
| --- | --- |
| `delivery/*.json` | Bodies of `/api/v1` answers: `schema`, `site`, `content-full`, `content-delta` (`?since=` a cursor), `pages`, `page-home`, `page-about`, `entries-posts`, `entries-projects`, `globals`, `global-nav`, `sitemap`, `robots`, `redirects`, `site-health`. |
| `forms/*.json` | The Forms plugin's public routes: `form-list`, `form-definition`, and submit answers `form-submit-200`, `form-submit-422` (every kind of invalid answer) and `form-submit-410` (a closed form). |
| `plugins/*.json` | The plugins API (`/api/plugins`, `/api/plugin_menus`), as an administrator calls it: `plugins` (installed and `available`), `plugin-forms`, `plugin-forms-admin` (its screens), `plugin-menus`, `plugin-install-201` (an available plugin installed, off and granted nothing), `plugin-configure` (granting more than asked grants what was asked), `plugin-remove-204` (no body: the file is `null`), and the errors `plugin-not-found`, `plugin-install-404` (not in this build) and `plugin-install-400`. The sites built on the CMS don't call these; the admin, the CLI and agents do. |
| `webhooks/<event>.json` | One delivery's body for each event the seeded site sends, `schema.changed` included. |
| `index.json` | For each file above: the request that produced it (method, path, body), the status, and the headers that are part of the contract (`Content-Type`, `Cache-Tag`, `Cache-Control`, `Location`, `X-CMS-Schema-Digest`). |
| `webhook_contract.json` | What a webhook receiver can rely on: the envelope, the signature, the events and each payload's keys. |

## How they're made

`internal/cms/contract_test.go` (`TestContract`) boots a fresh install,
seeds a small site (the starter block types, a collection with most field
types and one with blocks, live and draft pages and entries, a synced
pattern, an asset, a global, redirects, and two forms with every field type
— plus keys left over from older data, which mustn't be delivered), makes
each request and compares the answer with its file. Its build has the
example plugin (`plugins/visits`) beside Forms, so the plugins API has one
to show as available and to install.

What changes from run to run is normalized so the files are stable:
timestamps (`published_at`, `updated_at`, `created_at`, `lastmod`,
`delivered_at`, `submitted_at`) become `2026-01-01T00:00:00Z` in their own
format, `/content` cursors `<cursor>`, media file keys `/files/<blob>/`, and
a form post's `_ts` a fixed time. The CMS's address is
`https://cms.acme.example` and the site's `https://acme.example`. Keys are
written sorted. Digests are real: `X-CMS-Schema-Digest`,
`meta.schema_digest` and a form's `digest` are checked against the data
they're of, so a site can test its own digest code against these files.

## The drift gate

Two halves, both in CI, both reading these same files:

- **The Go half**: `TestContract` (`internal/cms/contract_test.go`, part of
  `make test` and CI's Test job) answers every request in `index.json` from
  a freshly seeded install and fails on any difference from its file.
- **The integration half**: `packages/astro`'s tests (`make astro`, CI's
  `astro` job) run on these files: the schema and form digests recomputed
  with its own canonical JSON must equal the recorded ones; its form
  validator must reach the recorded 422's errors (and pass the 200's post);
  `<Form>` must render every field of `form-definition.json`; its loaders,
  Zod schemas and generated types must take every recorded record; and
  `/_cms/webhook` must read every recorded webhook. Its
  `test/contracts.sha256` pins the files it was last checked against, so any
  change here fails it until the integration is updated and re-pinned.

So a change on the Go side that moves a file fails `TestContract` until the
files are rewritten, and rewritten files fail the integration until it
follows.

## Changing the contract

    go test ./internal/cms -run TestContract            # the Go half: fails on any difference
    go test ./internal/cms -run TestContract -update    # rewrite the files from what the CMS answers now

A change to any file here is a change to the API every site sees. Make it
on purpose, in one change:

1. Change the Go side.
2. `go test ./internal/cms -run TestContract -update`, and review the diff
   of `contracts/`.
3. Update `packages/astro` for it: its types (`src/types.ts`,
   `src/forms/types.ts`), its validation (`src/schema/`, `src/forms/rules.ts`)
   and its tests; then `npm run contracts:pin` in `packages/astro`.
4. Both halves pass: `make test` and `make astro` (and `make astro-e2e`
   against a real binary).
