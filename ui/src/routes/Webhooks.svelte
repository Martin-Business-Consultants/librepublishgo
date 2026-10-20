<script>
  // Webhooks: where events go, how deliveries went, the signing secret. An
  // internal one is a receiver you run, sent the CMS's envelope, signed; an
  // external one is another service (a CRM), sent the body its fields map
  // from the event's data. Either can take an event only when its data
  // says so (a submission, only the quote form's).
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
  import Select from "../lib/components/Select.svelte";
  import Tabs from "../lib/components/Tabs.svelte";
  import TagInput from "../lib/components/TagInput.svelte";
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
  let secret = $state(null), secretOpen = $state(false), deliveries = $state(null), logOpen = $state(false), shown = $state(null);
  const KINDS = [{ value: "internal", label: "Internal" }, { value: "external", label: "External" }];
  // A field's transform; "" (as it is) is the Select's placeholder.
  const TRANSFORMS = [{ value: "slug", label: "slug: keep-it" }, { value: "lowercase", label: "lowercase" }, { value: "uppercase", label: "UPPERCASE" },
    { value: "date_us", label: "MM/DD/YYYY" }, { value: "number", label: "Number" }, { value: "text", label: "Fixed text" }];
  // event_filters {event: {path: [values]}} as rows to edit, and back.
  const filterRows = (f) => Object.entries(f || {}).flatMap(([event, paths]) => Object.entries(paths).map(([path, values]) => ({ event, path, values: [...values] })));
  function filtersOf(rows) {
    const out = {};
    for (const r of rows) if (r.event && r.path.trim() && r.values.length) (out[r.event] ||= {})[r.path.trim()] = r.values;
    return out;
  }
  const load = async () => { const r = await get("/webhooks"); list = r.webhooks; if (r.events?.length) EVENTS = r.events; };
  load();
  function edit(w) {
    editing = w
      ? { ...w, events: [...w.events], filters: filterRows(w.event_filters), body_map: (w.body_map || []).map((f) => ({ ...f })) }
      : { name: "", url: "", kind: "internal", events: [...CONTENT_EVENTS], active: true, filters: [], body_map: [] };
    errors = {}; open = true;
  }
  async function submit() {
    const external = editing.kind === "external";
    const body = { webhook: { name: editing.name, url: editing.url, kind: editing.kind, events: editing.events, active: editing.active,
      event_filters: filtersOf(editing.filters), body_map: external ? editing.body_map.map((f) => ({ ...f, transform: f.transform || "" })) : [] } };
    const r = await save(() => (editing.id ? patch(`/webhooks/${editing.id}`, body) : post("/webhooks", body)));
    errors = r.errors || {};
    if (r.ok) { open = false; if (!editing.id && !external) { secret = r.res.webhook.secret; secretOpen = true; } load(); }
  }
  async function test(w) {
    if (w.kind === "external" && !(await confirm({ title: "Send a test?", message: "The service treats it like a real event: a CRM gets a lead with these fields mostly blank.", confirmLabel: "Send" }))) return;
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
  async function log(w) { shown = null; deliveries = { name: w.name, list: (await get(`/webhooks/${w.id}/deliveries`)).deliveries }; logOpen = true; }
  function toggle(ev, on) { editing.events = on ? [...editing.events, ev] : editing.events.filter((x) => x !== ev); }
  const fieldError = (i, key) => errors[`body_map[${i}].${key}`]?.[0];
</script>

<PageHeader title="Webhooks" cli="librepublish list_webhooks">
  {#snippet actions()}{#if session.can("webhooks:write")}<Button icon="add" onclick={() => edit(null)}>New webhook</Button>{/if}{/snippet}
</PageHeader>
{#if list}
  <DataTable rows={list} columns={[{ label: "Name" }, { label: "Events" }, { label: "Last delivery" }, { label: "" }]}>
    {#snippet row(w)}
      <Cell>
        <span class="font-medium text-foreground">{w.name}</span>{#if w.kind === "external"}<Badge class="ml-2">External</Badge>{/if}{#if !w.active}<Badge class="ml-2">Off</Badge>{/if}
        <p class="max-w-sm truncate font-mono text-xs text-muted-foreground">{w.url}</p>
        <RowActions>
          {#if session.can("webhooks:write")}<RowAction onclick={() => edit(w)}>Edit</RowAction><RowAction onclick={() => test(w)}>Send a test</RowAction>{#if w.kind !== "external"}<RowAction onclick={() => rotate(w)}>Rotate secret</RowAction>{/if}{/if}
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

<Dialog bind:open title={editing?.id ? "Edit webhook" : "Add webhook"} size={editing?.kind === "external" ? "lg" : "md"}>
  {#if editing}
    <div class="flex flex-col gap-2">
      <Tabs tabs={KINDS} bind:value={editing.kind} />
      <p class="text-xs text-muted-foreground">{editing.kind === "external"
        ? "Another service's address (a CRM, a mailing list): it's sent the body you map below from each event, unsigned."
        : "A receiver you run: it's sent the CMS's envelope ({event, tenant, delivered_at, data}), signed."}</p>
    </div>
    <Field label="Name" error={errors.name}>{#snippet children(id, invalid)}<Input {id} {invalid} bind:value={editing.name} placeholder={editing.kind === "external" ? "Stella" : "Search index"} />{/snippet}</Field>
    <Field label="URL" help={editing.kind === "external" ? "The CMS POSTs the body here as JSON." : "The CMS POSTs each event here, signed X-CMS-Signature: sha256=<HMAC of the body>."} error={errors.url}>{#snippet children(id, invalid)}<Input {id} {invalid} mono icon="link" bind:value={editing.url} placeholder="https://…" />{/snippet}</Field>
    <fieldset class="flex flex-col gap-2">
      <legend class="mb-2 text-sm font-semibold text-foreground">Events</legend>
      <div class="grid gap-2 sm:grid-cols-2">{#each EVENTS as ev}<Checkbox label={ev} checked={editing.events.includes(ev)} onchange={(on) => toggle(ev, on)} />{/each}</div>
    </fieldset>
    <fieldset class="flex flex-col gap-2">
      <legend class="text-sm font-semibold text-foreground">Only when</legend>
      <p class="text-xs text-muted-foreground">Leave empty to take every one of its events. A field is a path in the event's data: <code>form</code> is a submission's form, <code>values.service_type</code> one of its answers.</p>
      {#each editing.filters as f, i}
        <div class="grid items-start gap-2 sm:grid-cols-[11rem_9rem_minmax(0,1fr)_auto]">
          <Select bind:value={f.event} options={editing.events} placeholder="Event" aria-label="Event" />
          <Input bind:value={f.path} mono placeholder="form" aria-label="Field" />
          <TagInput bind:value={f.values} placeholder="is one of… (Enter)" />
          <Button variant="plain" iconOnly icon="trash" title="Remove" class="hover:bg-destructive/10 hover:text-destructive" onclick={() => editing.filters.splice(i, 1)} />
        </div>
      {/each}
      <div><Button size="sm" icon="add" onclick={() => editing.filters.push({ event: editing.events.find((e) => e === "submission.created") || editing.events[0] || "", path: "form", values: [] })}>Add a condition</Button></div>
    </fieldset>
    {#if editing.kind === "external"}
      <fieldset class="flex flex-col gap-2">
        <legend class="text-sm font-semibold text-foreground">Body</legend>
        <p class="text-xs text-muted-foreground">Each row is a key of the JSON the service is sent. Value is a path in the event's data — <code>values.first_name</code> (an answer), <code>hook.quote_id</code> (what the form's hook added), <code>form</code>, <code>submission_id</code> — or, with Fixed text, the text itself. No rows sends the event's data as it is.</p>
        {#if editing.body_map.length}
          <div class="hidden gap-2 text-xs font-medium text-muted-foreground sm:grid sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_11rem_auto]"><span>Key</span><span>Value</span><span>Transform</span><span class="w-10"></span></div>
        {/if}
        {#each editing.body_map as f, i}
          <div class="grid items-start gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_11rem_auto]">
            <div><Input bind:value={f.key} mono placeholder="first_name" aria-label="Key" invalid={!!fieldError(i, "key")} />{#if fieldError(i, "key")}<p class="mt-1 text-xs text-destructive">Key {fieldError(i, "key")}</p>{/if}</div>
            <div><Input bind:value={f.value} mono placeholder={f.transform === "text" ? "website" : "values.first_name"} aria-label="Value" invalid={!!fieldError(i, "value")} />{#if fieldError(i, "value")}<p class="mt-1 text-xs text-destructive">Value {fieldError(i, "value")}</p>{/if}</div>
            <Select bind:value={f.transform} options={TRANSFORMS} placeholder="As it is" aria-label="Transform" invalid={!!fieldError(i, "transform")} />
            <Button variant="plain" iconOnly icon="trash" title="Remove" class="hover:bg-destructive/10 hover:text-destructive" onclick={() => editing.body_map.splice(i, 1)} />
          </div>
        {/each}
        <div><Button size="sm" icon="add" onclick={() => editing.body_map.push({ key: "", value: "", transform: "" })}>Add a field</Button></div>
      </fieldset>
    {/if}
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
        <Cell mono>{d.event}{#if d.payload}<button type="button" class="ml-2 font-sans text-xs text-primary hover:underline" onclick={() => (shown = shown?.id === d.id ? null : d)}>{shown?.id === d.id ? "Hide body" : "Body"}</button>{/if}</Cell>
        <Cell>{#if d.success}<Badge tone="success">{d.response_status}</Badge>{:else}<Badge tone="danger">{d.response_status || "Failed"}</Badge><p class="max-w-xs truncate text-xs text-destructive">{d.error}</p>{/if}</Cell>
        <Cell num muted>{d.duration_ms ?? "—"} ms</Cell>
        <Cell muted nowrap><Time value={d.created_at} class="text-xs" /></Cell>
      {/snippet}
    </DataTable>
    {#if shown}
      <p class="mt-4 mb-2 text-sm font-semibold text-foreground">What {shown.event} was sent, <Time value={shown.created_at} class="font-normal text-muted-foreground" /></p>
      <pre class="max-h-96 overflow-auto rounded-md bg-muted p-3 text-xs">{JSON.stringify(shown.payload, null, 2)}</pre>
    {/if}
  {/if}
</Dialog>
