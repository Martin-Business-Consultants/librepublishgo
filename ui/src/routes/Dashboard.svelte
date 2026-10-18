<script>
  import { updateTo } from "../lib/update.js";
  // The dashboard: four cards (pages, collections, globals, working with
  // the API), what needs doing (starter blocks, changes waiting for review),
  // then recent activity beside site health.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import Button from "../lib/components/Button.svelte";
  import Icon from "../lib/components/Icon.svelte";
  import Time from "../lib/components/Time.svelte";
  import Notice from "../lib/components/Notice.svelte";
  import Spinner from "../lib/components/Spinner.svelte";
  import { get, post } from "../lib/api.js";
  import { session } from "../lib/session.svelte.js";

  // The update button (lib/update.js).
  let updating = $state("");
  async function updateNow() {
    try {
      await updateTo(session.me.update.version, (msg) => (updating = msg));
    } catch (e) {
      updating = "";
      toastError(e);
    }
  }
  import { toast, toastError } from "../lib/toast.svelte.js";
  import { blockTypes } from "../lib/blocks.svelte.js";
  import { plural } from "../lib/format.js";

  let data = $state(null);
  async function load() {
    const [m, health, revisions, audit, tokens] = await Promise.all([
      get("/manifest"),
      get("/site_health").catch(() => null),
      get("/revisions").catch(() => ({ revisions: [] })),
      session.can("audit_log:read") ? get("/audit_log?per=8").catch(() => null) : null,
      session.can("settings:read") ? get("/service_tokens").catch(() => null) : null,
    ]);
    data = { m, health, revisions: revisions.revisions, audit: audit?.audit_logs || [], tokens: tokens?.service_tokens || [] };
  }
  load();
  async function seed() {
    try { const r = await post("/block_types/seed"); toast(`Installed ${r.count} block types.`); blockTypes.load(true); load(); } catch (e) { toastError(e); }
  }
  // Everything someone helping with the install asks first, to paste.
  let copied = $state(false);
  async function copyInfo() {
    try {
      const { text } = await get("/site_health/info");
      await navigator.clipboard.writeText(text);
      copied = true; setTimeout(() => (copied = false), 2000);
      toast("Site info copied: paste it into a support request.");
    } catch (e) { toastError(e); }
  }
  const recentTokens = $derived((data?.tokens || []).filter((t) => t.last_used_at && Date.now() - new Date(t.last_used_at) < 7 * 864e5));
</script>

<PageHeader title="Dashboard" cli="librepublish doctor" />

{#if session.me?.update?.failed}
  {@const u = session.me.update}
  <Notice tone="warning" class="mb-6">
    The update to <strong>{u.failed}</strong> didn't come up, so this is still {u.running}. Nothing was lost: a backup from just before it is in the data directory's <code class="font-mono text-xs">backups/</code>. The logs say why it stopped.
  </Notice>
{:else if session.me?.update}
  {@const u = session.me.update}
  <Notice tone="info" class="mb-6">
    <div class="flex flex-wrap items-center gap-3">
      <span>Libre Publish <strong>{u.version}</strong> is out (this is {session.version}): <a href={u.url} target="_blank" rel="noopener noreferrer" class="font-medium underline underline-offset-4">what's new</a>.</span>
      {#if u.button}<Button size="sm" variant="primary" loading={updating} onclick={updateNow}>Update now</Button>{/if}
    </div>
    {#if updating}<p class="mt-2">{updating}</p>{:else if !u.button}<p class="mt-2">Install it with <code class="font-mono text-xs">librepublish update</code>, then restart the service.</p>{/if}
  </Notice>
{/if}

{#if !data}
  <div class="flex justify-center py-16"><Spinner /></div>
{:else}
  {@const c = data.m.counts}
  {@const entries = data.m.collections.reduce((n, col) => n + (col.entry_count || 0), 0)}
  {#snippet card(href, icon, name, count, line)}
    <a {href} class="group bg-card hover:bg-muted/40 flex flex-col gap-3 rounded-xl border p-5 transition-colors">
      <span class="text-muted-foreground flex items-center justify-between text-sm font-medium">
        <span class="text-foreground">{name}</span>
        <span class="bg-muted flex size-8 items-center justify-center rounded-md"><Icon name={icon} /></span>
      </span>
      <span class="text-3xl font-semibold tabular-nums tracking-tight">{count}</span>
      <span class="text-muted-foreground line-clamp-1 text-xs">{line}</span>
    </a>
  {/snippet}

  <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
    {@render card("#/pages", "document", "Pages", c.pages, plural(c.pages, "page") + " in the site")}
    {@render card("#/collections", "stack", "Collections", data.m.collections.length, plural(entries, "entry", "entries") + " across them")}
    {@render card("#/globals", "world", "Globals", c.globals, "Navigation, footer and other shared content")}
    {@render card(session.can("settings:read") ? "#/settings/tokens" : "#/connect", "ai", "Working with the API", recentTokens.length,
      recentTokens.length ? `${plural(recentTokens.length, "token")} used it this week: ${recentTokens.map((t) => t.name).join(", ")}` : "No token used it this week")}
  </div>

  {#if c.block_types === 0 && session.can("block_types:write")}
    <Notice tone="info">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <span><strong class="font-semibold">Start with the starter blocks.</strong> Pages are built from block types: a hero, text, an image, a list of entries and 18 more.</span>
        <Button variant="primary" size="sm" onclick={seed}>Install them</Button>
      </div>
    </Notice>
  {/if}
  {#if data.revisions.length}
    <Notice tone="warning">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <span><strong class="font-semibold">{plural(data.revisions.length, "change")} waiting for review</strong>: {data.revisions.slice(0, 3).map((r) => r.record_label).join(", ")}{data.revisions.length > 3 ? "…" : ""}</span>
        <Button size="sm" href="#/revisions">Review</Button>
      </div>
    </Notice>
  {/if}

  <div class="grid items-start gap-6 lg:grid-cols-2">
    <Panel title="Recent activity">
      {#snippet actions()}{#if session.can("audit_log:read")}<Button size="sm" href="#/audit">Audit log</Button>{/if}{/snippet}
      {#if data.audit.length}
        <ul class="divide-y divide-border">
          {#each data.audit as l}
            <li class="flex items-center justify-between gap-4 px-5 py-2.5 text-sm">
              <span class="flex min-w-0 items-center gap-2"><span class="shrink-0 text-foreground">{l.actor.label}</span><Badge>{l.action}</Badge><span class="truncate text-foreground/80">{l.target.label}</span></span>
              <Time value={l.created_at} class="shrink-0 text-xs text-muted-foreground" />
            </li>
          {/each}
        </ul>
      {:else}
        <p class="text-muted-foreground px-5 py-8 text-center text-sm">{session.can("audit_log:read") ? "Nothing has happened yet." : "Your role can't see the activity log."}</p>
      {/if}
    </Panel>
    <Panel title="Site health">
      {#snippet actions()}
        {#if data.health}{#if data.health.failing}<Badge tone="danger">{data.health.failing} failing</Badge>{:else}<Badge tone="success">All good</Badge>{/if}{/if}
        {#if session.can("settings:read")}<Button size="sm" icon={copied ? "check" : "clipboard"} onclick={copyInfo}>{copied ? "Copied" : "Copy site info"}</Button>{/if}
      {/snippet}
      {#if data.health}
        <ul class="divide-y divide-border">
          {#each data.health.checks as ch}
            <li class="flex items-start gap-3 px-5 py-2.5 text-sm">
              <Icon name={ch.state === "passes" ? "check-circle" : ch.state === "fails" ? "close-circle" : "help-circle"} size="md" class={ch.state === "passes" ? "text-emerald-600 dark:text-emerald-400" : ch.state === "fails" ? "text-destructive" : "text-amber-600 dark:text-amber-400"} />
              <span class="text-foreground/80">{ch.message}</span>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="text-muted-foreground px-5 py-8 text-center text-sm">Site health isn't available.</p>
      {/if}
    </Panel>
  </div>
{/if}
