<script>
  import PageHeader from "../lib/components/PageHeader.svelte";
  import SearchBox from "../lib/components/SearchBox.svelte";
  import QuickFilter from "../lib/components/QuickFilter.svelte";
  import DataTable from "../lib/components/DataTable.svelte";
  import Cell from "../lib/components/Cell.svelte";
  import Pagination from "../lib/components/Pagination.svelte";
  import Time from "../lib/components/Time.svelte";
  import { get } from "../lib/api.js";
  import { router } from "../lib/router.svelte.js";
  const actor = router.query.get("s") || "", action = router.query.get("action") || "", page = Number(router.query.get("page") || 1);
  let data = $state(null);
  const p = new URLSearchParams({ per: "50", page: String(page) });
  if (actor) p.set("actor", actor);
  if (action) p.set("action", action);
  get("/audit_log?" + p).then((r) => (data = r));
</script>

<PageHeader title="Audit log" cli="librepublish list_audit_log" />
<div class="flex flex-wrap items-center justify-between gap-2">
  <QuickFilter title="What" param="action" options={[{ label: "Everything", value: "" }, { label: "Pages", value: "page." }, { label: "Entries", value: "entry." }, { label: "Globals", value: "global." }, { label: "Reviews", value: "revision." }, { label: "Media", value: "asset." }, { label: "Settings", value: "setting." }, { label: "People & tokens", value: "user." }, { label: "Plugins", value: "plugin." }]} />
  <SearchBox placeholder="Who…" />
</div>
{#if data}
  <DataTable rows={data.audit_logs} columns={[{ label: "When" }, { label: "Who" }, { label: "Did" }, { label: "To" }]} emptyText="Nothing recorded.">
    {#snippet row(l)}
      <Cell muted nowrap><Time value={l.created_at} class="text-xs" /></Cell>
      <Cell>{l.actor.label}</Cell>
      <Cell mono>{l.action}</Cell>
      <Cell muted>{l.target.label}</Cell>
    {/snippet}
  </DataTable>
  <Pagination {page} per={data.per} total={data.total} />
{/if}
