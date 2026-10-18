# Working on a site with AI agents

The `librepublish` binary that runs a site also works on one: from a
terminal, a script, or an AI app (Claude Code, Codex, Claude Desktop,
Cursor, anything that speaks MCP). Every site carries its own catalogue of
**tools**, and both the command line and MCP use it, so what an agent can
do always matches the site's version.

## Sign in

```sh
librepublish login https://cms.example.com
```

It prints an address and a code; approve it in the admin (it opens on its
own when it can). The credential acts as you, with your role, and is kept in
`~/.config/librepublish/profiles.json`, readable by you alone. Several
sites: `--name` each one, then `--profile NAME` (or `LIBREPUBLISH_PROFILE`)
picks one; `librepublish profiles` lists them. In CI, set `LIBREPUBLISH_URL`
and `LIBREPUBLISH_TOKEN` (a service token, Settings › Service tokens)
instead.

`librepublish doctor` checks the whole chain: the address, the token, the
tools, MCP.

## Tools

```sh
librepublish tools                       # what this site has
librepublish tools update_page           # one, in full: its flags and its traps
librepublish whoami                      # the site, you, your role
librepublish list_pages --status draft
librepublish get_page about              # a required path argument can come first
librepublish create_page --page '{"title":"Our story","slug":"story"}'
librepublish publish_page story
librepublish update_entry --slug beers --entry pils --entry @pils.json --lock-version 4
librepublish upload_asset --file ./mill.jpg --alt "The mill in 1890"
librepublish api_request --method GET --path /api/redirects/export
```

Objects and lists are JSON: inline, `@file.json`, or `-` for stdin. A tool
answers with a one-line summary, the data, and **next steps**: real
commands, filled in from what just came back. `--json` prints the answer as
the site sent it (`{status, summary, data, breadcrumbs}`), which is what a
script or an agent wants. Exit codes: 0 done, 2 a mistake on the command
line, 3 the site refused (a 403 is your role, a 404 a missing record, a 409
someone saved since you read).

The traps a tool's description warns about:

- **Rich text** is the editor's document (Tiptap JSON:
  `{"type":"doc","content":[…]}`), not HTML; plain text fields refuse HTML.
- **Saves carry `lock_version`** from the read they're based on: a 409 means
  someone else saved in between. Read again, then save.
- **Field lists replace**: `update_collection_schema` and
  `update_block_type`'s `fields` are the whole list, not an addition.

A tool can never do more than the API lets the signed-in role do: it runs
through the same routes, as you.

## MCP

Every site is an MCP server at `/mcp` (Streamable HTTP, a token in the
`Authorization` header). And `librepublish mcp` is a local one (stdio) that
signs in with your profile, for apps that start a server themselves.
`librepublish mcp setup` prints the lines for your apps:

```sh
# Claude Code, through your profile:
claude mcp add --scope user librepublish -- librepublish mcp --profile cms.example.com
# or straight to the site:
claude mcp add --transport http librepublish https://cms.example.com/mcp --header "Authorization: Bearer <token>"
# Codex:
codex mcp add librepublish -- librepublish mcp --profile cms.example.com
```

The server tells an agent where to start (`get_manifest`), and every tool's
description carries its traps. Its answers are the same envelope, with each
next step as a tool and its arguments, ready to call.

## For whoever adds a route

A tool is declared on its route, next to the pattern:

```go
h("GET /api/pages/{path...}", a.showPage, tool("get_page", "One page, its blocks expanded, …"))
```

Path parameters come from the pattern; `query(…)`, `body(…)`, `bodyList(…)`,
`file(…)` and `note(…)` add the rest. `GET /api/agent/tools` is the
catalogue; `api_request` reaches any route without a tool. Answers say what
to do next through `c.Crumbs = []Crumb{step("…", "tool", "arg", value)}`.
