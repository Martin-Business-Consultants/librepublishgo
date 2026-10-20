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
  import Textarea from "../lib/components/Textarea.svelte";
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
  // Pickers: the plugins' declared choices for their events (Forms: which
  // forms a submission.created is for), each with its choices read from the
  // plugin, or null when they can't be (then its values are typed).
  let PICKERS = $state([]);
  const load = async () => {
    const r = await get("/webhooks");
    list = r.webhooks;
    if (r.events?.length) EVENTS = r.events;
    PICKERS = await Promise.all((r.event_filters || []).map(async (p) => {
      try { return { ...p, choices: ((await get(p.source))[p.rows] || []).map((x) => ({ value: String(x[p.value]), label: x[p.text] || x[p.value] })) }; }
      catch { return { ...p, choices: null }; }
    }));
  };
  const isPicker = (r) => PICKERS.some((p) => p.event === r.event && p.field === r.path);
  // A picker's values on the webhook being edited, and set.
  const picked = (p) => editing.filters.find((r) => r.event === p.event && r.path === p.field)?.values || [];
  function pick(p, values) {
    const rest = editing.filters.filter((r) => !(r.event === p.event && r.path === p.field));
    editing.filters = values.length ? [...rest, { event: p.event, path: p.field, values }] : rest;
  }
  // A webhook's events in the list, with what its pickers narrow them to.
  function eventsSummary(w) {
    const base = CONTENT_EVENTS.every((e) => w.events.includes(e)) && w.events.length === CONTENT_EVENTS.length ? "Every content event" : w.events.length === EVENTS.length ? "Every event" : w.events.join(", ");
    const only = PICKERS.map((p) => {
      const vals = w.event_filters?.[p.event]?.[p.field];
      if (!vals?.length || !(w.events.includes(p.event) || w.events.includes("*"))) return "";
      return `${p.label}: ${vals.map((v) => p.choices?.find((c) => c.value === v)?.label || v).join(", ")}`;
    }).filter(Boolean);
    return only.length ? `${base} · ${only.join(" · ")}` : base;
  }
  load();
  function edit(w) {
    editing = w
      ? { ...w, testing_url: w.testing_url || "", kind: w.kind || "internal", events: [...w.events], filters: filterRows(w.event_filters), body_map: (w.body_map || []).map((f) => ({ ...f })) }
      : { name: "", url: "", testing_url: "", kind: "internal", events: [...CONTENT_EVENTS], active: true, filters: [], body_map: [] };
    errors = {}; open = true;
  }
  async function submit() {
    const external = editing.kind === "external";
    const body = { webhook: { name: editing.name, url: editing.url, testing_url: editing.testing_url || "", kind: editing.kind, events: editing.events, active: editing.active,
      event_filters: filtersOf(editing.filters), body_map: external ? editing.body_map.map((f) => ({ ...f, transform: f.transform || "" })) : [] } };
    const r = await save(() => (editing.id ? patch(`/webhooks/${editing.id}`, body) : post("/webhooks", body)));
    errors = r.errors || {};
    if (r.ok) { open = false; if (!editing.id && !external) { secret = r.res.webhook.secret; secretOpen = true; } load(); }
  }
  // Test webhook: the delivery an event would make, exactly (URL, headers,
  // body), from the latest real one's data or a sample, editable; sent to
  // the testing URL, or, asked twice, to the real one.
  let trying = $state(null), tryOpen = $state(false), tryError = $state(""), sending = $state(false);
  const FROM = { delivery: "The latest of these it took, as it was delivered.", sample: "Made-up data in the event's shape: edit it to look like yours.", given: "Your data." };
  async function preview(event, data) {
    tryError = "";
    try {
      const r = await post(`/webhooks/${trying.webhook.id}/preview`, data === undefined ? { event } : { event, data });
      trying = { ...trying, ...r, text: data === undefined ? JSON.stringify(r.data, null, 2) : trying.text };
    } catch (e) { tryError = e.message; }
  }
  async function test(w) {
    trying = { webhook: w, event: "", text: "" };
    tryOpen = true;
    await preview("");
  }
  function edited() {
    try { return JSON.parse(trying.text); } catch { tryError = "The data isn't JSON."; return null; }
  }
  async function send(to) {
    const data = edited();
    if (!data) return;
    if (to === "live" && !(await confirm({ title: `Send to ${trying.webhook.name}?`, message: trying.webhook.kind === "external" ? "It goes to the real service, which treats it like a real event: a CRM gets a lead." : "It goes to the real receiver, marked as a test in Deliveries.", confirmLabel: "Send" }))) return;
    sending = true;
    try {
      const r = await post(`/webhooks/${trying.webhook.id}/test`, { event: trying.event, data, to });
      r.ok ? toast(`Sent to ${r.sent_to} (HTTP ${r.response_status}).`) : toasts.show(`Failed: ${r.error || "HTTP " + r.response_status}`, "danger");
    } catch (e) { toastError(e); } finally { sending = false; }
  }
  const tryEvents = (w) => [...new Set([...w.events.filter((e) => e !== "*"), "webhook.test"])];
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
          {#if session.can("webhooks:write")}<RowAction onclick={() => edit(w)}>Edit</RowAction><RowAction onclick={() => test(w)}>Test webhook</RowAction>{#if w.kind !== "external"}<RowAction onclick={() => rotate(w)}>Rotate secret</RowAction>{/if}{/if}
          <RowAction onclick={() => log(w)}>Deliveries</RowAction>
          {#if session.can("webhooks:delete")}<RowAction danger onclick={() => remove(w)}>Delete</RowAction>{/if}
        </RowActions>
      </Cell>
      <Cell muted>{eventsSummary(w)}</Cell>
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
    <Field label="Testing URL" help="Optional. Where Test webhook sends, so you can see a delivery without the real receiver getting it: a request catcher (webhook.site, a RequestBin) or a staging copy." error={errors.testing_url}>{#snippet children(id, invalid)}<Input {id} {invalid} mono icon="link" bind:value={editing.testing_url} placeholder="https://webhook.site/…" />{/snippet}</Field>
    <fieldset class="flex flex-col gap-2">
      <legend class="mb-2 text-sm font-semibold text-foreground">Events</legend>
      <div class="grid gap-2 sm:grid-cols-2">{#each EVENTS as ev}<Checkbox label={ev} checked={editing.events.includes(ev)} onchange={(on) => toggle(ev, on)} />{/each}</div>
    </fieldset>
    {#each PICKERS.filter((p) => editing.events.includes(p.event) || editing.events.includes("*")) as p (p.event + p.field)}
      <fieldset class="flex flex-col gap-2">
        <legend class="text-sm font-semibold text-foreground">{p.label}</legend>
        <p class="text-xs text-muted-foreground">Which {p.label.toLowerCase()} this webhook takes <code>{p.event}</code> for. None ticked: every one{#if !picked(p).length}, including ones added later{/if}.</p>
        {#if p.choices}
          <div class="grid gap-2 sm:grid-cols-2">
            {#each p.choices as c (c.value)}<Checkbox label={c.label} checked={picked(p).includes(c.value)} onchange={(on) => pick(p, on ? [...picked(p), c.value] : picked(p).filter((v) => v !== c.value))} />{/each}
            {#each picked(p).filter((v) => !p.choices.some((c) => c.value === v)) as gone (gone)}<Checkbox label="{gone} (no longer there)" checked onchange={() => pick(p, picked(p).filter((v) => v !== gone))} />{/each}
          </div>
          {#if !p.choices.length}<p class="text-xs text-muted-foreground">There are none yet.</p>{/if}
        {:else}
          <TagInput bind:value={() => picked(p), (v) => pick(p, v)} placeholder="{p.field} values (Enter)" />
        {/if}
      </fieldset>
    {/each}
    <fieldset class="flex flex-col gap-2">
      <legend class="text-sm font-semibold text-foreground">Only when</legend>
      <p class="text-xs text-muted-foreground">Leave empty to take every one of its events. A field is a path in the event's data: <code>values.service_type</code> is one of a submission's answers.</p>
      {#each editing.filters as f, i}{#if !isPicker(f)}
        <div class="grid items-start gap-2 sm:grid-cols-[11rem_9rem_minmax(0,1fr)_auto]">
          <Select bind:value={f.event} options={editing.events} placeholder="Event" aria-label="Event" />
          <Input bind:value={f.path} mono placeholder="form" aria-label="Field" />
          <TagInput bind:value={f.values} placeholder="is one of… (Enter)" />
          <Button variant="plain" iconOnly icon="trash" title="Remove" class="hover:bg-destructive/10 hover:text-destructive" onclick={() => editing.filters.splice(i, 1)} />
        </div>
      {/if}{/each}
      <div><Button size="sm" icon="add" onclick={() => editing.filters.push({ event: editing.events[0] || "", path: "", values: [] })}>Add a condition</Button></div>
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

<Dialog bind:open={tryOpen} title="Test webhook · {trying?.webhook?.name || ''}" size="xl">
  {#if trying}
    <div class="grid gap-6 lg:grid-cols-2">
      <div class="flex min-w-0 flex-col gap-3">
        <Field label="Event">{#snippet children(id)}<Select {id} value={trying.event} options={tryEvents(trying.webhook)} onchange={(v) => preview(v)} />{/snippet}</Field>
        <Field label="Event data" help={FROM[trying.data_from] || ""}>{#snippet children(id)}<Textarea {id} mono rows={18} bind:value={trying.text} />{/snippet}</Field>
        <div class="flex flex-wrap gap-2">
          <Button size="sm" icon="refresh" onclick={() => { const d = edited(); if (d) preview(trying.event, d); }}>Preview with this data</Button>
          <Button size="sm" variant="plain" onclick={() => preview(trying.event)}>Reset</Button>
        </div>
        {#if tryError}<p class="text-xs text-destructive">{tryError}</p>{/if}
      </div>
      <div class="flex min-w-0 flex-col gap-3">
        <p class="text-sm font-semibold text-foreground">What it sends</p>
        <p class="break-all font-mono text-xs"><span class="font-semibold">POST</span> {trying.url}</p>
        <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 font-mono text-xs">
          {#each Object.entries(trying.headers || {}).sort() as [k, v]}<dt class="text-muted-foreground">{k}</dt><dd class="break-all">{v}</dd>{/each}
        </dl>
        <pre class="max-h-[28rem] overflow-auto rounded-md bg-muted p-3 text-xs">{trying.body ? JSON.stringify(trying.body, null, 2) : trying.body_text}</pre>
        <p class="text-xs text-muted-foreground">Exactly what's posted{trying.webhook.kind === "external" ? "" : " (delivered_at and the signature are made again when it's sent)"}. Previewing sends nothing.</p>
      </div>
    </div>
  {/if}
  {#snippet actions()}
    <Button onclick={() => (tryOpen = false)}>Close</Button>
    {#if trying && session.can("webhooks:write")}
      <Button variant="danger" loading={sending} onclick={() => send("live")}>Send to its URL</Button>
      <Button variant="primary" loading={sending} disabled={!trying.testing_url} title={trying.testing_url ? trying.testing_url : "Add a testing URL in Edit"} onclick={() => send("testing")}>Send to testing URL</Button>
    {/if}
  {/snippet}
</Dialog>

<Dialog bind:open={secretOpen} title="Signing secret" size="sm">
  {#if secret}<SecretBox value={secret} note="Shown once. The receiver checks each delivery's X-CMS-Signature with it." />{/if}
  {#snippet actions()}<Button variant="primary" onclick={() => (secretOpen = false)}>Done</Button>{/snippet}
</Dialog>

<Dialog bind:open={logOpen} title="Deliveries · {deliveries?.name || ''}" sheet size="lg">
  {#if deliveries}
    <DataTable rows={deliveries.list} columns={[{ label: "Event" }, { label: "Result" }, { label: "Time", num: true }, { label: "When" }]} emptyText="Nothing delivered yet.">
      {#snippet row(d)}
        <Cell mono>{d.event}{#if d.test}<Badge class="ml-2 font-sans">Test</Badge>{/if}{#if d.payload}<button type="button" class="ml-2 font-sans text-xs text-primary hover:underline" onclick={() => (shown = shown?.id === d.id ? null : d)}>{shown?.id === d.id ? "Hide body" : "Body"}</button>{/if}</Cell>
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
