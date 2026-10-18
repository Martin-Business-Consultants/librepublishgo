<script>
  import PageHeader from "../lib/components/PageHeader.svelte";
  import StatusLinks from "../lib/components/StatusLinks.svelte";
  import DataTable from "../lib/components/DataTable.svelte";
  import Cell from "../lib/components/Cell.svelte";
  import RowActions from "../lib/components/RowActions.svelte";
  import RowAction from "../lib/components/RowAction.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import Time from "../lib/components/Time.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import { get, post, del } from "../lib/api.js";
  import { router } from "../lib/router.svelte.js";
  import { session } from "../lib/session.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toastError } from "../lib/toast.svelte.js";
  const kind = router.query.get("kind") || "";
  let data = $state(null);
  const load = async () => (data = await get("/trash"));
  load();
  const rows = $derived((data?.entries || []).filter((e) => !kind || e.kind === kind).map((e) => ({ ...e, key: e.kind + e.id })));
  const counts = $derived(Object.fromEntries((data?.counts ? Object.entries(data.counts) : [])));
  async function restore(e) { try { await post(`/trash/${e.kind}/${e.id}/restore`); toast("Restored."); load(); } catch (err) { toastError(err); } }
  async function purge(e) {
    if (!(await confirm({ title: `Delete “${e.label}” for good?`, message: "There's no undo.", confirmLabel: "Delete forever", destructive: true }))) return;
    try { await del(`/trash/${e.kind}/${e.id}`); toast("Deleted for good."); load(); } catch (err) { toastError(err); }
  }
  const names = { page: "Pages", entry: "Entries", global: "Globals", asset: "Media" };
</script>

<PageHeader title="Trash" cli="librepublish list_trash" />
{#if data}
  <StatusLinks param="kind" options={[{ label: "All", value: "", count: data.entries.length }, ...Object.keys(names).filter((k) => counts[k]).map((k) => ({ label: names[k], value: k, count: counts[k] }))]} />
  <DataTable {rows} key="key" columns={[{ label: "Item" }, { label: "Kind" }, { label: "Trashed" }]}>
    {#snippet row(e)}
      <Cell>
        <span class="font-medium text-foreground">{e.label}</span>
        {#if session.can("trash:write")}<RowActions><RowAction onclick={() => restore(e)}>Restore</RowAction><RowAction danger onclick={() => purge(e)}>Delete forever</RowAction></RowActions>{/if}
      </Cell>
      <Cell><Badge>{e.kind}</Badge></Cell>
      <Cell muted nowrap><Time value={e.deleted_at} class="text-xs" /></Cell>
    {/snippet}
    {#snippet empty()}<EmptyState icon="trash" title="The trash is empty">What's deleted waits here, restorable.</EmptyState>{/snippet}
  </DataTable>
{/if}
