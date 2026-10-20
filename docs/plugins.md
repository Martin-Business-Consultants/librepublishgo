# Plugins: contract v4

A plugin is a Go package compiled into the binary. It builds a
`*plugin.Module` (`internal/plugin`): a manifest saying what it is and what
it needs, the handlers for its routes, and the events it hears. It reaches
the CMS only through the `*plugin.API` it's handed on each call, which holds
the capabilities a person granted it and nothing else.

Plugins used to be WebAssembly modules (contract v3). They're Go now: one
language, one build, no runtime between the CMS and the plugin, and a
plugin is reviewed and tested like the rest of the code. The trade: a
plugin is added by building the binary with it, not by uploading a file,
and the grants are a contract the host keeps, not a sandbox wall. A plugin
that calls only its `*plugin.API` can do no more than it was granted, but
Go doesn't stop one that imports `os` or `database/sql`, so review that a
plugin doesn't.

## Adding one

1. Write a package under `plugins/<key>` with a `Module() *plugin.Module`
   ([plugins/visits](../plugins/visits/visits.go) is a small example).
2. Add it to `builtinPlugins` in `internal/cms/plugins.go`.

A build with it lists it under Plugins › Available until someone installs
it. One with `Default: true` is installed, granted what it asks for and
switched on at first run.

## The module

```go
func Module() *plugin.Module {
	return &plugin.Module{
		Manifest: plugin.Manifest{
			Key:         "visits",
			Name:        "Visits",
			Version:     "1.0.0",
			Description: "Counts the page views a site reports.",
			Permissions: plugin.Grants{ContentRead: true, Storage: true},
			Capabilities: []plugin.Capability{{Key: "visits:read", Label: "See page views"}},
			Hooks:       []string{"page.published"},
			Routes: []plugin.Route{
				{Method: "POST", Path: "/hit", Export: "hit", Public: true},
				{Method: "GET", Path: "/stats", Export: "stats", Capability: "visits:read"},
			},
			Admin:      adminJSON,                               // json.RawMessage, below
			SiteConfig: json.RawMessage(`{"endpoint": "/api/x/visits/hit"}`),
		},
		Handlers: map[string]plugin.Handler{"hit": hit, "stats": stats},
		OnEvent:  onEvent,
	}
}
```

- `Key`: lowercase letters, digits and `_`. It is the plugin's namespace
  (storage, routes) and stays the same across versions.
- `Permissions`: what it asks for. `HTTP` names hosts (`api.example.com`),
  never `*` or URLs.
- `SiteConfig`: what `/api/v1/site` publishes under `plugin_config.<key>`
  for the site's build.
- `Capabilities`: what it adds to the role editor, each `<key>:<name>` with
  a label. Administrators have them; other roles get them when ticked.

A module whose manifest doesn't hold up, or whose routes name a handler it
doesn't have, stops the binary at start: that's a bug in the build.

### Routes

A route is served at `/api/x/<key><path>`, by the handler its `Export`
names:

```go
func hit(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error)
```

`req` has `Method`, `Path`, `Query`, `Headers` (Content-Type, Accept,
User-Agent, Referer, Origin, X-Requested-With), `Body` (up to the route's
limit), `IP` and `Actor` (`{Name, Type, Capabilities}`, nil on a public
route called by no one signed in).

A route that isn't `Public` needs a caller with its `Capability` (one of the
plugin's own, or `plugins:read`/`plugins:write`); without one, a GET needs
`plugins:read` and anything else `plugins:write`. A route takes a body of up
to 1 MB unless it sets `MaxBody` (at most 10 MB). Public routes take 30
calls a minute from one address, and browsers on the origins in Settings ›
General may call them (CORS).

It answers a `*plugin.Response` (`plugin.JSON(status, v)` for JSON). An
error answers 502 and is logged. `Set-Cookie` is dropped, and every answer
is served with `Content-Security-Policy: sandbox`, so a plugin can't run
script as the CMS's origin.

### Events

`OnEvent(ctx, api, event, data)` hears each event the manifest hooks, with
`data` the webhook payload as JSON. Events are delivered in the background,
so a slow or failing plugin never holds up a save. The events are
`page.published|updated|unpublished|deleted`,
`entry.published|updated|unpublished|deleted` and `global.updated`, and the
other plugins' events.

A plugin sends events of its own with `api.Emit(ctx, event, data)`: to the
webhooks that take them (Settings › Webhooks offers every enabled plugin's)
and the plugins that hook them. Only the events its manifest's `Events` names
(Forms: `submission.created`).

Each call's context ends after 10 seconds, and a call that panics answers an
error instead of taking the server down.

## The host's capabilities

`*plugin.API`'s methods. One that wasn't granted answers an error (`storage
wasn't granted to this plugin`) and does nothing.

| Method | Needs | Does |
| --- | --- | --- |
| `Log(msg)` | always | writes to the server log, under the plugin's key |
| `Site(ctx)` | always | `plugin.Site{Name, URL, CMSURL, Locale, ContactEmail, Origins}` (`Origins`: the site's own, as CORS allows them) |
| `Content(ctx, q)` | `content:read` | live content, shaped as `/api/v1` shapes it: `ContentQuery{Page: "about"}`, `{Pages: true}`, `{Collection: "posts"}`, `{Collection: "posts", Entry: "hello"}` or `{Global: "nav"}` |
| `KVGet(ctx, key)` | `storage` | the value, and whether there is one |
| `KVSet(ctx, key, value)` | `storage` | value ≤ 1 MB, ≤ 100,000 keys |
| `KVDelete(ctx, key)` | `storage` | |
| `KVList(ctx, prefix, after, limit)` | `storage` | keys in order, ≤ 1000 a page, and the next page's `after` (`""` at the end) |
| `KVAll(ctx, prefix)`, `KVCount(ctx, prefix)` | `storage` | every key under a prefix; how many |
| `Mail(ctx, mail.Message)` | `mail` | sent through Settings › Email (from its defaults where blank); ≤ 50 recipients, ≤ 200 messages an hour |
| `PutMedia(ctx, plugin.Media)` | `media:write` | a file (≤ 10 MB) into the media library, answered as the media API shows it; the folder defaults to `Plugins/<key>` |
| `HTTP()` | `http` | an `*http.Client` that reaches only the granted hosts (redirects included) and reads ≤ 4 MB of a response; `@site` (`plugin.SiteHosts`) among them grants the site's own hosts, its public URL's and its allowed origins', as they are at each request |
| `Emit(ctx, event, data)` | the event in `Events` | sends it to webhooks and plugins, in the background |

Storage is the plugin's own (`plugin_kv`, keyed by its `key`). Other plugins
can't see it, and it survives upgrades. Removing a plugin keeps its storage
unless removal is asked to purge it.

## Installing, granting, upgrading

```
GET    /api/plugins[/<key>]                                            plugins:read
POST   /api/plugins            {"plugin": {"key": "visits"}}             plugins:write
PATCH  /api/plugins/<key>      {"plugin": {"granted": {…}, "enabled": true}}  plugins:write
DELETE /api/plugins/<key>[?purge_data=1]                               plugins:write
```

`GET /api/plugins` answers the installed `plugins` and the ones this build
has that aren't installed, `available`. Installing stores a plugin off,
granted nothing. A grant is never wider than what's asked: granting more
than the manifest requests grants what it requests.

A new build whose plugin's manifest changed upgrades it in place at start:
it keeps the grants it still asks for, and if it asks for more it's off
until someone grants the rest. A plugin installed by an older build that
this one doesn't have (a WebAssembly one, say) is kept, off, and shows as
"Not in this build"; its storage is kept.

## Admin screens, as data

The admin draws what `admin` describes; a plugin ships no code into it.

```json
"admin": {
  "menu": { "label": "Forms", "icon": "form", "group": "Content", "badge": "/submissions?count=new" },
  "screens": [ … ]
}
```

`menu` puts the plugin in the sidebar (in `group`: Content, Insights, Tools,
Access or Settings), its submenu the screens that have a `menu` label;
`badge` is a GET route answering `{"count": n}`. Someone sees a screen when
they may call its `source` route, so a role with the plugin's capabilities
but not `plugins:read` still gets the plugin's screens.

A screen is `{id, title, source, …}` and one of `table`, `form` or `detail`.
Its query string (`#/plugins/<key>/<id>?id=…`) is passed to `source`.

- **table**: `rows` (the key in the source's answer), `key`, `columns`
  (`{key, label, link: {screen, params}, sub, num, badge, time}`),
  `row_actions` (`{label, screen, params}` or `{label, post, params, body,
  confirm, danger}`), `status_links` and `filters` (`{param, options}`, the
  options a key in the answer), `cursor` (`{param, next}` for paging) and
  `empty` (`{title, text}`).
- **form**: `record` (the key holding it), `save` (a POST route, sent
  `{<record>: …}` with the screen's `id`; a 422's `errors` mark the fields;
  `redirect: {screen, params}` moves on), and sections in `top` (full
  width), `main` and `side`, each `{title, fields}` in the CMS's field
  types, or `{title, readonly: "<key>"}` for items to show. Plugins add
  `form_builder` (a visual builder over `palette: [{type, label, icon,
  input, props}]`), `password`, and `select` with `options_from: {builder,
  types}` (choices taken from the record's builder fields). A section's
  `actions` (`[{label, icon, post, send: "record", help, confirm}]`) are
  buttons under its fields that POST the record as it is in the editor,
  saved or not (Forms' Send test); a 422's `errors` mark the fields, and the
  answer's `message` is shown.
- **detail**: `record`, `main` and `side` sections of `items`
  (`[{label, value, href, multiline, time, badge, tone, screen, params}]`),
  and `actions` (`post` actions, with `hide_if`/`show_if` on the record and
  `then` a screen to go to).

Screens can carry `actions` too: `{label, screen}`, `{label, download,
keep}` (a GET route, keeping the given query params) or `{label, post}`.

A sandboxed iframe served from a route is still the escape hatch for what
data can't describe.

## Default plugins

A module with `Default: true` is installed on first run, granted what it
asks for and switched on. A new build upgrades it in place, granted what it
asks for and on or off as it was. Removing one removes it for good: it isn't
put back (though it can be installed again from Available). Today that's
**Forms** ([plugins/forms](../plugins/forms), [docs/forms.md](forms.md)).

## Porting the Rails engines

The engines map onto this contract like this:

- **Forms**: done, as the first default plugin (`plugins/forms`).
- **Commerce (quotes and invoices)**: a public `POST /quote` route, quotes and
  invoices in storage, and admin screens. Sending an invoice uses `Mail`.
- **Leads, AI, Market**: these need `content:read`, storage and `http` to
  their APIs. AI's writes to content need a `content:write` capability that
  goes through the same publish gate as any other writer, so its writes to
  live content come back as revisions.
