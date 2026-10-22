<script>
  // Plugins: Go packages built into the binary, each asking for capabilities
  // that a person grants (never more than asked), switched on once granted.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Button from "../lib/components/Button.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import Checkbox from "../lib/components/Checkbox.svelte";
  import Notice from "../lib/components/Notice.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import { get, post, patch, del } from "../lib/api.js";
  import { session } from "../lib/session.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toastError } from "../lib/toast.svelte.js";
  let list = $state(null), available = $state([]), grants = $state({}), installing = $state(null);
  async function load() {
    const r = await get("/plugins");
    list = r.plugins;
    available = r.available || [];
    grants = Object.fromEntries(list.map((p) => [p.key, { "content:read": !!p.granted["content:read"], storage: !!p.granted.storage, mail: !!p.granted.mail, "media:write": !!p.granted["media:write"], "site:read": !!p.granted["site:read"], http: [...(p.granted.http || [])] }]));
  }
  load();
  async function install(m) {
    installing = m.key;
    try { const r = await post("/plugins", { plugin: { key: m.key } }); toast(`Installed ${r.plugin.name} ${r.plugin.version}. Grant what it asks for to switch it on.`); load(); } catch (e) { toastError(e); }
    installing = null;
  }
  async function configure(p, enabled) {
    try { await patch(`/plugins/${p.key}`, { plugin: { granted: grants[p.key], enabled } }); toast(enabled ? `${p.name} is on.` : `${p.name} is off.`); load(); } catch (e) { toastError(e); }
  }
  async function remove(p) {
    if (!(await confirm({ title: `Remove ${p.name}?`, message: "Its stored data is kept, so reinstalling it picks up where it left off.", confirmLabel: "Remove", destructive: true }))) return;
    try { await del(`/plugins/${p.key}`); load(); } catch (e) { toastError(e); }
  }
</script>

<PageHeader title="Plugins" subtitle="Each plugin can do only what it asks for and you grant: its own storage, email, the media library, and the network only to the hosts it names." />

{#if list && !list.length && !available.length}
  <Panel><EmptyState icon="package" title="No plugins">Plugins are Go packages built into Libre Publish; this build has none.</EmptyState></Panel>
{:else if list}
  <div class="grid gap-4 xl:grid-cols-2">
    {#each list as p (p.key)}
      {@const want = p.requested}
      {@const g = grants[p.key]}
      <Panel>
        {#snippet header()}
          <div class="min-w-0">
            <h2 class="flex items-center gap-2 text-base font-semibold text-foreground">{p.name}<span class="text-xs font-normal text-muted-foreground">{p.version}</span></h2>
            {#if p.description}<p class="text-sm text-muted-foreground">{p.description}</p>{/if}
          </div>
        {/snippet}
        {#snippet actions()}{#if !p.built_in}<Badge tone="warning">Not in this build</Badge>{:else}<Badge tone={p.enabled ? "success" : "neutral"}>{p.enabled ? "On" : "Off"}</Badge>{/if}{/snippet}
        <div class="flex flex-col gap-4 p-6">
          <div class="flex flex-col gap-2">
            <h3 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Asks for</h3>
            {#if want["content:read"] || want.storage || want.mail || want["media:write"] || want["site:read"] || (want.http || []).length}
              {#if want["content:read"]}<Checkbox bind:checked={g["content:read"]} label="Read live content" description="Pages, entries and globals as the site sees them." />{/if}
              {#if want.storage}<Checkbox bind:checked={g.storage} label="Its own storage" description="A private key–value store no other plugin can see." />{/if}
              {#if want.mail}<Checkbox bind:checked={g.mail} label="Send email" description="Through Settings › Email, at most 200 an hour." />{/if}
              {#if want["media:write"]}<Checkbox bind:checked={g["media:write"]} label="Add files to the media library" description="Up to 10 MB each, into its own folder." />{/if}
              {#if want["site:read"]}<Checkbox bind:checked={g["site:read"]} label="Read the site's launch checklist" description="Whether settings, email, deploys, tokens, files and the other plugins are set up: their state, never their secrets." />{/if}
              {#each want.http || [] as host}
                <Checkbox checked={g.http.includes(host)} onchange={(on) => (g.http = on ? [...g.http, host] : g.http.filter((x) => x !== host))} label="Call {host}" description="Outbound requests to this host only." />
              {/each}
            {:else}<p class="text-sm text-muted-foreground">Nothing beyond writing to the log.</p>{/if}
          </div>
          {#if p.hooks?.length}<p class="text-sm text-muted-foreground"><span class="font-medium text-foreground">Hears:</span> {p.hooks.join(", ")}</p>{/if}
          {#if p.routes?.length}
            <ul class="flex flex-col gap-1">
              {#each p.routes as r}<li class="font-mono text-xs text-muted-foreground">{r.method} /api/x/{p.key}{r.path}{#if r.public}<Badge class="ml-2">Public</Badge>{/if}</li>{/each}
            </ul>
          {/if}
          {#if p.admin?.screens?.length && p.enabled}
            <div class="flex flex-wrap gap-2">{#each p.admin.screens.filter((s) => s.menu || !p.admin.menu) as s}<Button size="sm" href="#/plugins/{p.key}/{s.id}">{s.menu || s.title}</Button>{/each}</div>
          {/if}
        </div>
        {#snippet footer()}
          {#if session.can("plugins:write")}
            <div class="flex items-center justify-between gap-2">
              <button type="button" class="font-medium text-destructive hover:underline" onclick={() => remove(p)}>Remove</button>
              <div class="flex gap-2">
                {#if p.enabled}<Button size="sm" onclick={() => configure(p, false)}>Switch off</Button><Button size="sm" variant="primary" onclick={() => configure(p, true)}>Save grants</Button>
                {:else if p.built_in}<Button size="sm" variant="primary" onclick={() => configure(p, true)}>Grant and switch on</Button>{/if}
              </div>
            </div>
          {/if}
        {/snippet}
      </Panel>
    {/each}
  </div>
  {#if available.length}
    <h2 class="mt-8 mb-3 text-sm font-medium uppercase tracking-wide text-muted-foreground">Available in this build</h2>
    <div class="grid gap-4 xl:grid-cols-2">
      {#each available as m (m.key)}
        <Panel>
          {#snippet header()}
            <div class="min-w-0">
              <h2 class="flex items-center gap-2 text-base font-semibold text-foreground">{m.name}<span class="text-xs font-normal text-muted-foreground">{m.version}</span></h2>
              {#if m.description}<p class="text-sm text-muted-foreground">{m.description}</p>{/if}
            </div>
          {/snippet}
          {#snippet footer()}
            {#if session.can("plugins:write")}
              <div class="flex justify-end"><Button size="sm" icon="add" loading={installing === m.key} onclick={() => install(m)}>Install</Button></div>
            {/if}
          {/snippet}
        </Panel>
      {/each}
    </div>
  {/if}
{/if}
