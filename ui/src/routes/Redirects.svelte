<script>
  // Redirects: the rules, with their hits; add and edit in a dialog; export
  // and import as CSV.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import DataTable from "../lib/components/DataTable.svelte";
  import Cell from "../lib/components/Cell.svelte";
  import RowActions from "../lib/components/RowActions.svelte";
  import RowAction from "../lib/components/RowAction.svelte";
  import Button from "../lib/components/Button.svelte";
  import Dialog from "../lib/components/Dialog.svelte";
  import Field from "../lib/components/Field.svelte";
  import Input from "../lib/components/Input.svelte";
  import Select from "../lib/components/Select.svelte";
  import Switch from "../lib/components/Switch.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import Time from "../lib/components/Time.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import { get, post, patch, del, api } from "../lib/api.js";
  import { save } from "../lib/save.js";
  import { session } from "../lib/session.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toastError } from "../lib/toast.svelte.js";
  let list = $state(null), editing = $state(null), open = $state(false), errors = $state({}), importInput = $state();
  const load = async () => (list = (await get("/redirects/all")).redirects);
  load();
  function edit(r) { editing = r ? { ...r } : { source_path: "", destination_url: "", status_code: 301, active: true, notes: "" }; errors = {}; open = true; }
  async function submit() {
    const body = { redirect: { source_path: editing.source_path, destination_url: editing.destination_url, status_code: Number(editing.status_code), active: editing.active, notes: editing.notes } };
    const r = await save(() => (editing.id ? patch(`/redirects/${editing.id}`, body) : post("/redirects", body)));
    errors = r.errors || {};
    if (r.ok) { open = false; load(); }
  }
  async function remove(r) {
    if (!(await confirm({ title: `Delete the redirect from ${r.source_path}?`, message: "Links to the old address stop forwarding.", confirmLabel: "Delete", destructive: true }))) return;
    try { await del(`/redirects/${r.id}`); toast("Deleted."); load(); } catch (e) { toastError(e); }
  }
  async function importCSV(file) {
    const fd = new FormData(); fd.append("file", file);
    try { const r = await post("/redirects/import", fd); toast(`Imported ${r.created}${r.skipped ? `, skipped ${r.skipped}` : ""}.`); load(); } catch (e) { toastError(e); }
  }
</script>

<PageHeader title="Redirects" cli="librepublish list_redirects">
  {#snippet actions()}
    <Button href="/api/redirects/export" icon="download">Export CSV</Button>
    {#if session.can("redirects:write")}
      <input type="file" accept=".csv,text/csv" class="hidden" bind:this={importInput} onchange={(e) => { if (e.currentTarget.files[0]) importCSV(e.currentTarget.files[0]); e.currentTarget.value = ""; }} />
      <Button icon="upload" onclick={() => importInput.click()}>Import</Button>
      <Button icon="add" onclick={() => edit(null)}>New redirect</Button>
    {/if}
  {/snippet}
</PageHeader>
{#if list}
  <DataTable rows={list} columns={[{ label: "From" }, { label: "To" }, { label: "Type" }, { label: "Hits", num: true }, { label: "Last hit" }]}>
    {#snippet row(r)}
      <Cell>
        <span class="font-mono text-xs text-foreground">{r.source_path}</span>
        {#if !r.active}<Badge class="ml-2">Off</Badge>{/if}
        <RowActions>
          {#if session.can("redirects:write")}<RowAction onclick={() => edit(r)}>Edit</RowAction>{/if}
          {#if session.can("redirects:delete")}<RowAction danger onclick={() => remove(r)}>Delete</RowAction>{/if}
        </RowActions>
      </Cell>
      <Cell mono>{r.destination_url}</Cell>
      <Cell><Badge tone={r.status_code === 410 ? "danger" : r.status_code === 301 || r.status_code === 308 ? "info" : "neutral"}>{r.status_code}</Badge></Cell>
      <Cell num>{r.hit_count}</Cell>
      <Cell muted nowrap><Time value={r.last_hit_at} class="text-xs" /></Cell>
    {/snippet}
    {#snippet empty()}<EmptyState icon="arrow-right" title="No redirects">Changing a page's address? Add a redirect so links to the old one still arrive.</EmptyState>{/snippet}
  </DataTable>
{/if}

<Dialog bind:open title={editing?.id ? "Edit redirect" : "Add redirect"} size="sm">
  {#if editing}
    <Field label="From" help="A path on the site; end it with * to match everything under it." error={errors.source_path}>{#snippet children(id, invalid)}<Input {id} {invalid} mono placeholder="/old-page" bind:value={editing.source_path} />{/snippet}</Field>
    <Field label="To" help="A path or an address; :splat stands for what * matched." error={errors.destination_url}>{#snippet children(id, invalid)}<Input {id} {invalid} mono placeholder="/new-page" bind:value={editing.destination_url} />{/snippet}</Field>
    <Field label="Type" error={errors.status_code}>{#snippet children(id)}<Select {id} bind:value={editing.status_code} options={[{ value: 301, label: "301 · Moved for good" }, { value: 302, label: "302 · Moved for now" }, { value: 307, label: "307 · Temporary (keeps the method)" }, { value: 308, label: "308 · Permanent (keeps the method)" }, { value: 410, label: "410 · Gone" }]} />{/snippet}</Field>
    <Field label="Notes">{#snippet children(id)}<Input {id} bind:value={editing.notes} />{/snippet}</Field>
    <Switch bind:checked={editing.active} label="Active" />
  {/if}
  {#snippet actions()}<Button onclick={() => (open = false)}>Cancel</Button><Button variant="primary" onclick={submit}>Save redirect</Button>{/snippet}
</Dialog>
