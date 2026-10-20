<script>
  // Webhooks: where events go, how deliveries went, the signing secret.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import DataTable from "../lib/components/DataTable.svelte";
  import Cell from "../lib/components/Cell.svelte";
  import RowActions from "../lib/components/RowActions.svelte";
  import RowAction from "../lib/components/RowAction.svelte";
  import Button from "../lib/components/Button.svelte";
  import Dialog from "../lib/components/Dialog.svelte";
  import Field from "../lib/components/Field.svelte";
  import Input from "../lib/components/Input.svelte";
  import Checkbox from "../lib/components/Checkbox.svelte";
  import Switch from "../lib/components/Switch.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import Time from "../lib/components/Time.svelte";
  import SecretBox from "../lib/components/SecretBox.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import { get, post, patch, del } from "../lib/api.js";
  import { save } from "../lib/save.js";
  import { session } from "../lib/session.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toasts, toastError } from "../lib/toast.svelte.js";
  // The CMS's content events; GET /webhooks adds the enabled plugins' (form
  // submissions…), which a new webhook leaves off: they carry what people sent.
  const CONTENT_EVENTS = ["page.published", "page.updated", "page.unpublished", "page.deleted", "entry.published", "entry.updated", "entry.unpublished", "entry.deleted", "global.updated", "schema.changed"];
  let EVENTS = $state([...CONTENT_EVENTS]);
  let list = $state(null), editing = $state(null), open = $state(false), errors = $state({});
  let secret = $state(null), secretOpen = $state(false), deliveries = $state(null), logOpen = $state(false);
  const load = async () => { const r = await get("/webhooks"); list = r.webhooks; if (r.events?.length) EVENTS = r.events; };
  load();
  function edit(w) { editing = w ? { ...w, events: [...w.events] } : { name: "", url: "", events: [...CONTENT_EVENTS], active: true }; errors = {}; open = true; }
  async function submit() {
    const body = { webhook: { name: editing.name, url: editing.url, events: editing.events, active: editing.active } };
    const r = await save(() => (editing.id ? patch(`/webhooks/${editing.id}`, body) : post("/webhooks", body)));
    errors = r.errors || {};
    if (r.ok) { open = false; if (!editing.id) { secret = r.res.webhook.secret; secretOpen = true; } load(); }
  }
  async function test(w) {
    try { const r = await post(`/webhooks/${w.id}/test`); r.ok ? toast(`Delivered (HTTP ${r.response_status}).`) : toasts.show(`Failed: ${r.error || "HTTP " + r.response_status}`, "danger"); load(); } catch (e) { toastError(e); }
  }
  async function rotate(w) {
    if (!(await confirm({ title: "Rotate the signing secret?", message: "Deliveries are signed with the new one at once: give it to the receiver.", confirmLabel: "Rotate" }))) return;
    try { secret = (await post(`/webhooks/${w.id}/rotate_secret`)).webhook.secret; secretOpen = true; } catch (e) { toastError(e); }
  }
  async function remove(w) {
    if (!(await confirm({ title: `Delete ${w.name}?`, message: "It stops receiving events.", confirmLabel: "Delete", destructive: true }))) return;
    try { await del(`/webhooks/${w.id}`); load(); } catch (e) { toastError(e); }
  }
  async function log(w) { deliveries = { name: w.name, list: (await get(`/webhooks/${w.id}/deliveries`)).deliveries }; logOpen = true; }
  function toggle(ev, on) { editing.events = on ? [...editing.events, ev] : editing.events.filter((x) => x !== ev); }
</script>

<PageHeader title="Webhooks" cli="librepublish list_webhooks">
  {#snippet actions()}{#if session.can("webhooks:write")}<Button icon="add" onclick={() => edit(null)}>New webhook</Button>{/if}{/snippet}
</PageHeader>
{#if list}
  <DataTable rows={list} columns={[{ label: "Name" }, { label: "Events" }, { label: "Last delivery" }, { label: "" }]}>
    {#snippet row(w)}
      <Cell>
        <span class="font-medium text-foreground">{w.name}</span>{#if !w.active}<Badge class="ml-2">Off</Badge>{/if}
        <p class="max-w-sm truncate font-mono text-xs text-muted-foreground">{w.url}</p>
        <RowActions>
          {#if session.can("webhooks:write")}<RowAction onclick={() => edit(w)}>Edit</RowAction><RowAction onclick={() => test(w)}>Send a test</RowAction><RowAction onclick={() => rotate(w)}>Rotate secret</RowAction>{/if}
          <RowAction onclick={() => log(w)}>Deliveries</RowAction>
          {#if session.can("webhooks:delete")}<RowAction danger onclick={() => remove(w)}>Delete</RowAction>{/if}
        </RowActions>
      </Cell>
      <Cell muted>{CONTENT_EVENTS.every((e) => w.events.includes(e)) && w.events.length === CONTENT_EVENTS.length ? "Every content event" : w.events.length === EVENTS.length ? "Every event" : w.events.join(", ")}</Cell>
      <Cell nowrap>{#if w.last_delivery_at}<Badge tone={w.failure_count ? "danger" : "success"}>{w.failure_count ? `${w.failure_count} failing` : "OK"}</Badge> <Time value={w.last_delivery_at} class="text-xs text-muted-foreground" />{:else}<span class="text-muted-foreground">Never</span>{/if}</Cell>
      <Cell></Cell>
    {/snippet}
    {#snippet empty()}<EmptyState icon="bolt" title="No webhooks">Tell another system when content changes: a search index, analytics, a chat channel. Each delivery is signed.</EmptyState>{/snippet}
  </DataTable>
{/if}

<Dialog bind:open title={editing?.id ? "Edit webhook" : "Add webhook"}>
  {#if editing}
    <Field label="Name" error={errors.name}>{#snippet children(id, invalid)}<Input {id} {invalid} bind:value={editing.name} placeholder="Search index" />{/snippet}</Field>
    <Field label="URL" help="The CMS POSTs each event here, signed X-CMS-Signature: sha256=<HMAC of the body>." error={errors.url}>{#snippet children(id, invalid)}<Input {id} {invalid} mono icon="link" bind:value={editing.url} placeholder="https://…" />{/snippet}</Field>
    <fieldset class="flex flex-col gap-2">
      <legend class="mb-2 text-sm font-semibold text-foreground">Events</legend>
      <div class="grid gap-2 sm:grid-cols-2">{#each EVENTS as ev}<Checkbox label={ev} checked={editing.events.includes(ev)} onchange={(on) => toggle(ev, on)} />{/each}</div>
    </fieldset>
    <Switch bind:checked={editing.active} label="Active" />
  {/if}
  {#snippet actions()}<Button onclick={() => (open = false)}>Cancel</Button><Button variant="primary" onclick={submit}>Save webhook</Button>{/snippet}
</Dialog>

<Dialog bind:open={secretOpen} title="Signing secret" size="sm">
  {#if secret}<SecretBox value={secret} note="Shown once. The receiver checks each delivery's X-CMS-Signature with it." />{/if}
  {#snippet actions()}<Button variant="primary" onclick={() => (secretOpen = false)}>Done</Button>{/snippet}
</Dialog>

<Dialog bind:open={logOpen} title="Deliveries · {deliveries?.name || ''}" sheet size="lg">
  {#if deliveries}
    <DataTable rows={deliveries.list} columns={[{ label: "Event" }, { label: "Result" }, { label: "Time", num: true }, { label: "When" }]} emptyText="Nothing delivered yet.">
      {#snippet row(d)}
        <Cell mono>{d.event}</Cell>
        <Cell>{#if d.success}<Badge tone="success">{d.response_status}</Badge>{:else}<Badge tone="danger">{d.response_status || "Failed"}</Badge><p class="max-w-xs truncate text-xs text-destructive">{d.error}</p>{/if}</Cell>
        <Cell num muted>{d.duration_ms ?? "—"} ms</Cell>
        <Cell muted nowrap><Time value={d.created_at} class="text-xs" /></Cell>
      {/snippet}
    </DataTable>
  {/if}
</Dialog>
