# Launch checklist (design)

Status: **phases 1–3 built** (the CMS's checks, Forms' checks, the Launch
plugin and its screen; the build's checks and a site's own; the live site
asked what only it can see); phase 4 proposed.

Decided:

- The checklist **only shows**. Nothing blocks publishing or a deploy.
- Evidence is trusted for **30 days** (a test email, a build report, a
  token's use, a hook's last answer); a backup for 7.
- No guess at "run-state globals" (the old `gsc_status`): a heuristic would
  be wrong as often as right. Run state belongs in plugin storage.
- It's a plugin (`launch`, default). A site not built with
  `@librepublish/astro` can remove it and install a checklist of its own;
  `API.Checks` (`site:read`) is there for one.

## What phase 1 is

- `plugin.Check` and `Module.Checks` (internal/plugin): a plugin adds items;
  `API.Checks(ctx)` (the new `site:read` grant) answers the CMS's items,
  then every other enabled plugin's. A plugin whose checks panic or time out
  shows as one `unknown` item.
- The CMS's items, `internal/cms/launch.go`: `general.site_url`,
  `general.cms_url`, `general.origins`, `general.contact`, `email.smtp`,
  `deploy.hook`, `deploy.last_build`, `deploy.pending`, `tokens.production`,
  `tokens.admin`, `media.files`, `content.empty_blocks`,
  `content.redirect_links`, `content.site_health`, `globals.standard`,
  `globals.scripts_jsonld`, `backup.recent`.
- Forms' items, `plugins/forms/checks.go`: `forms.captcha`,
  `forms.recipients`, `forms.hook_site`, `forms.hook_answers`.
- The Launch plugin, `plugins/launch`: Tools › Launch (a table with the
  verdict first and To do / Blocks launch / All views, each item's detail
  with where to put it right), the menu badge (what blocks launch), and the
  manual items (`manual.domain`, `manual.test_submission`,
  `manual.tracking`, `manual.search_console`, `manual.tokens_rotated`),
  ticked with who and when.
- Tests: `TestLaunchChecklist` (internal/cms), `TestChecks`
  (internal/plugin). Contracts rewritten (the grant, the plugin);
  librepublish-astro re-pinned.

## What phase 2 is

- A build report's `checks` (`POST /api/frontend/builds`): kept with the
  report (`settings/frontend_build`), at most 100, each with a `build.*` or
  `site.*` key, a known state (or `manual: true`), bounded text and a fix
  link that's an admin address or https; anything else is dropped. Shown
  with `source: "build"` and how old the build is; once the report is older
  than 30 days, as unknown.
- `plugin.Check.Manual`: an item a person ticks. The Launch plugin ticks any
  manual item, the site's (`site.stella`, `build.runtime_env`) as well as
  its own.
- In `@librepublish/astro` (its README, "Launch checklist"): `build.schema_lock`,
  `build.prerendered` (against `cms({ onDemand })`), `build.no_secrets`,
  `build.media_local` (with `localFiles`, on by default: the CMS's files
  copied into the build, as each site's `scripts/cms-static.mjs` did),
  `build.site_files`, `build.redirects`, `build.forms`, `build.env` and
  `build.runtime_env` (`cms({ env })`), and the site's `cms({ checks })`.
- Tests: `TestLaunchChecklist` (a build's checks, a bad one dropped, a site's
  manual item ticked); the integration's `test/launch-checks.test.js` and its
  e2e (a real core: the build's checks arrive in the checklist, no page links
  the CMS).

## What phase 3 is

- `@librepublish/astro` serves `POST /_cms/checks` on a site with an
  adapter (beside `/_cms/webhook`) and reports where (`checks_url` in the
  build report). Signed as the CMS signs a webhook, with
  `CMS_WEBHOOK_SECRET` (Settings › Deploy's purge secret) or
  `CMS_FORM_HOOK_SECRET` (a form's hook secret), it answers which secret
  verified it and, for each variable in `cms({ env: { runtime } })`, only
  whether it's set. Anything else gets a bare 401.
- `deploy.live_site` (the CMS): asks `checks_url` with the purge secret.
  Passes when the site verifies it, fails when it refuses (the Worker's
  `CMS_WEBHOOK_SECRET` isn't Settings › Deploy's), unknown when it can't be
  reached or predates the route, n/a for a static site.
- `build.runtime_env` turns from a person's tick into a check: the live
  site's answer says which of its runtime variables are set.
- `forms.hook_secret` (Forms): asks each hooked form's site with the form's
  own hook secret. Fails when the site's `CMS_FORM_HOOK_SECRET` isn't the
  form's: the mismatch that makes every hook call fail its signature.
- Answers are kept a minute, so the screen and its badge ask once.
- Tests: `TestLaunchChecklist` against a fake live site (matching secrets,
  a wrong hook secret, a rotated purge secret); the integration's
  `test/runtime-checks.test.js` and its e2e (unsigned: 401; signed: the
  secret that verified it).

The sections below are the design as proposed; where phase 1 differs (key
names, `email.recent_failures` and `globals.no_state` left out, the badge on
the menu rather than the dashboard), the list above is what's built.

Every site on Libre Publish goes live the same way and breaks in the same few
ways: email never set up, a form left on Turnstile's test keys or pointing its
hook at a preview, an origin missing from Allowed origins, no deploy hook, a
token shared in a chat and never rotated, a media record whose file is gone.
Today that knowledge lives in people's heads and in per-site checklists
(docs/going-live.html covers standing the CMS up; nothing covers the site
side). This proposes a **Launch** checklist in every CMS, on by default: one
screen that says whether the site is ready to go live and stays ready, where
most items check themselves and show their evidence, and only the few things
no machine can see are ticked by a person.

It came out of moving five Astro sites (four Mule Box, InBox Portable Storage)
onto Libre Publish with `@librepublish/astro`; every check below is something
that was missed, or nearly missed, on one of them.

## Goals

- **Mostly automatic.** An item the CMS or the site's build can verify is
  verified, never ticked.
- **Evidence, not ticks.** Each item says how it knows: "Last test send:
  delivered, 22 Oct 14:02", "Built 640 pages from schema sha256:c517…, 3 min
  ago". A green item can be trusted without re-checking it.
- **Abstract defaults, site-specific extras.** The defaults know about the CMS
  and the Astro integration, nothing about a CRM, a host or a CDN. A site adds
  its own items (Stella got a lead, the Lily Pad ids are set) from its repo.
- **Not just launch day.** The same items going red later (SMTP password
  changed, a form switched back to test keys) is the point.

Non-goals: running anything that spends money or changes the site; checking
content quality (that stays Site health, which this links to); monitoring
uptime.

## Where it lives

A default plugin, `plugins/launch` (Default: true), with its admin screen
**Tools › Launch** and a badge on the dashboard ("3 to do before launch").

Most checks need to read CMS state a plugin can't reach through
`plugin.API` today: settings, forms' configuration, tokens, deploy and build
records. Two ways:

1. **Checks in core, screen in the plugin.** Core gains
   `a.launchChecks(ctx)` beside `a.healthChecks(ctx)` (delivery.go), returning
   the same `{key, state, message, …}` objects; the plugin renders them,
   stores manual ticks and site checks, and adds the dashboard badge.
2. **A read-only `SiteState` grant** on `plugin.API` (settings minus
   secrets, form summaries, token summaries, build records) so the plugin can
   compute them itself.

Recommendation: (1). The checks are core knowledge (what a valid form hook or
deploy setup is), they evolve with core, and it avoids a new broad grant.
Forms-specific checks are contributed by the forms plugin through a small
`Checks()` hook on `plugin.Module`, so the checklist doesn't hard-code
another plugin's internals.

## The model

```json
{
  "key": "forms.captcha_live",
  "group": "forms",
  "title": "Forms use real captcha keys",
  "state": "fails",
  "source": "cms",
  "evidence": "landing-quote, support-request: Turnstile test key (1x0000…AA)",
  "checked_at": "2026-10-22T01:30:00Z",
  "fix": { "label": "Open Landing page quote › Spam", "screen": "form", "params": { "id": "landing-quote" } },
  "launch": true
}
```

- `state`: `passes`, `fails`, `warns` (works but not as it should), `unknown`
  (no evidence yet, e.g. no build has reported), `todo` / `done` (manual
  items), `n_a` (doesn't apply: no forms, no on-demand routes).
- `source`: `cms`, `build` (from the latest build report), `site` (a check
  the site declared), `manual`.
- `launch`: blocks "ready to go live". Others are advice.
- `fix`: where to go, as an admin screen and params (the forms plugin's
  manifest already describes screens this way).
- Manual items store who ticked them and when; a tick older than a set age
  (manual items can say `"expires_days": 90`) goes back to `todo`.

**Ready to go live** = no `launch: true` item is `fails`, `unknown` or
`todo`. The screen shows that verdict at the top.

## Default checks: the CMS

| Key | Passes when | Why |
|---|---|---|
| `general.site_url` | Settings › General has the public site URL, https | Canonicals, sitemap, email links |
| `general.cms_url` | `APP_URL` set, https, not an IP/sslip host when the site is live | Forms are read from it by visitors' browsers |
| `general.origins` | Allowed origins include the site URL's origin(s); `warns` on localhost, `*.workers.dev` or other preview origins | Forms load across origins; stale previews linger |
| `general.contact` | A contact email is set | Forms' fallback recipient, `contact_info` |
| `email.smtp` | SMTP and a from address set, and the last test send succeeded within 30 days | Every form's email depends on it |
| `email.recent_failures` | No email failed in the last 7 days (`warns` otherwise, with the count) | A changed password shows up here first |
| `deploy.hook` | A deploy hook or purge webhook is set for the site's rendering | Publishing must rebuild the site |
| `deploy.last_build` | The last build reported in the last 30 days and succeeded | |
| `deploy.pending` | No rendering or webhook change awaits approval | Hybrid sites trip this once |
| `tokens.production` | A token on the Production site role exists and was used by the last build | Builds shouldn't run on admin tokens |
| `tokens.admin` | `warns` per Administrator token unused for 30 days | Tokens issued for setup and forgotten |
| `media.files` | Every asset the live content uses has its file | Imports lose files (7 on InBox) |
| `content.empty_blocks` | No live page or entry has a block with no values | Draws as an empty section |
| `content.redirect_links` | No link in live content points at a redirect's source | One hop wasted, sometimes a loop |
| `globals.standard` | `nav`, `footer` and `scripts` exist in the standard shape (below) | Every site's chrome is the same model |
| `globals.no_state` | No global holds machine-written run state (`warns`; heuristic: written only by tokens, never by a person) | It's served to the public site and part of its schema |
| `scripts.no_jsonld` | No `scripts` entry holds `application/ld+json` (`warns`) | Structured data belongs in a page's SEO › JSON-LD |
| `backup.recent` | A backup was downloaded or scheduled in the last 7 days | |
| `health` | Site health has no failing checks (`warns`, links there) | |

**Standard globals** (what `globals.standard` checks; also what
`seed-blocks` should create on an empty site):

| Global | Shape |
|---|---|
| `nav` | `items[]`: `label` (string), `link` (link) |
| `footer` | `columns[]`: `heading`, `items[]` {`label`, `link`}; `address` (text); `copyright` (string) |
| `scripts` | `head[]` and `body[]`: `label` (string, required), `code` (code). `head` goes into `<head>`, `body` right after `<body>`. |

### From the forms plugin (`Checks()`)

| Key | Passes when |
|---|---|
| `forms.captcha_live` | Every open form has a captcha, with keys that aren't a provider's test keys |
| `forms.recipients` | Every open form with its notification on has somewhere to send it |
| `forms.hook_origin` | Every hook URL is on the site URL's origin (`fails` on a preview or localhost origin) |
| `forms.hook_ok` | Every hook's last call (test or real) in the last 30 days was accepted; `unknown` if never called |
| `forms.confirmation` | Every form with a visitor confirmation has its email field set and a subject |

## Default checks: the build (`@librepublish/astro`)

The integration already POSTs `/api/frontend/builds` after each build
(`BuildReport`: render, webhook URL, cursor, versions, pages, site URL,
duration, schema digest). It gains a `checks` list with the same model,
`source: "build"`, from checks it runs itself — today most of these are in
each site's `scripts/verify-build.mjs` and belong in the integration:

| Key | Passes when |
|---|---|
| `build.schema_lock` | `cms.lock.json` matched the CMS's schema |
| `build.prerendered` | Every route is prerendered except the site's declared on-demand routes (listed in the evidence) |
| `build.no_secrets` | The CMS token appears nowhere in the output; the CMS address nowhere in server code |
| `build.media_local` | No page links a file on the CMS host (static sites; `n_a` for on-demand) |
| `build.site_files` | sitemap.xml (or the site's own), robots.txt and a 404 page exist |
| `build.redirects` | `_redirects` came from the CMS and nothing else |
| `build.forms_reachable` | Every form a page draws exists and is open in the CMS (`<StaticForm>`/`<FormIsland>` slugs, collected at build) |
| `build.env` | The variables the site declares it needs (below) were set — **names only, never values** |

Runtime variables the build can't see (a Worker's secrets) are reported as
`expects: ["CMS_FORM_HOOK_SECRET", "STELLA_WEBHOOK_URL"]` and shown as
manual items ("Set on the host: …") until a site check confirms them (below).

## Site checks (per site)

A site declares its own items in its repo, sent with the build report:

```js
// astro.config.mjs
cms({
  checks: [
    { key: "stella", title: "Stella receives landing leads", manual: true, launch: true },
    { key: "lilypad", title: "Lily Pad ids set for every state", run: () => franchisesComplete() },
  ],
  env: { build: ["CMS_BASE_URL", "CMS_API_TOKEN", "SITE_URL"], runtime: ["CMS_FORM_HOOK_SECRET", "STELLA_WEBHOOK_URL"] },
})
```

- `run` checks execute at build and report `passes`/`fails` with evidence.
- `manual` items appear in the CMS for a person to tick.
- **Runtime checks:** a site with on-demand routes can expose
  `/_cms/checks` (the integration provides it, signed like the purge webhook)
  so the CMS can ask the live site whether its runtime variables are set —
  booleans only. That turns "Set `CMS_FORM_HOOK_SECRET` on the Worker" from a
  manual item into a verified one, and catches the Worker and the CMS
  disagreeing about the hook secret (the CMS signs a test, the site says
  whether it verified).

## Default manual items

| Key | Item |
|---|---|
| `manual.dns` | The site's domain points at the new deploy; the old deploy is noted for rollback |
| `manual.test_submission` | A real submission on each form was received end to end |
| `manual.search_console` | The sitemap is submitted to Search Console |
| `manual.tokens_rotated` | Tokens shared during setup (in chats, tickets) are revoked |
| `manual.tracking` | Analytics and tags fire on the live site |

## API

```
GET  /api/x/launch/checks            every item, grouped, with the verdict   (launch:read)
POST /api/x/launch/manual            {key, done: true|false, note?}           (launch:write)
POST /api/x/launch/recheck           re-run the CMS checks now                (launch:read)
```

Also an agent tool (`launch_checks`), so `cms` / `librepublish` can answer
"is this site ready?" with the same list, and the Hook log / Site health
pattern of steps with status and detail is reused for evidence.

## Phasing

1. **CMS checks in core + the screen** (no integration change): general,
   email, deploy, tokens, media, content, globals, backup, and the forms
   plugin's `Checks()`.
2. **Build checks**: move `verify-build`'s checks into `@librepublish/astro`
   and report them; site `checks` and `env` options.
3. **Runtime checks** (`/_cms/checks`) and the hook-secret handshake.
4. **Standard globals** created by `seed-blocks`; a one-click migration for
   an old `scripts` shape (`items[] {name, head, body}` → `head[]`/`body[]`).

## Open questions

- Should `fails` on a `launch: true` item block **Publish** of the first
  deploy, or only show? (Proposal: only show; the verdict is advice.)
- How long is evidence trusted (30 days for hook calls and test sends)?
  Per-check `max_age`.
- Is `globals.no_state` worth a heuristic, or should run state simply get a
  home of its own (plugin storage) and the check look for known names?
- Where does a site's check list live when the site isn't Astro: a
  `librepublish.checks.json` the CMS reads from the build report, framework-
  agnostic?
