<script>
  import PageHeader from "../lib/components/PageHeader.svelte";
  import DataTable from "../lib/components/DataTable.svelte";
  import Cell from "../lib/components/Cell.svelte";
  import RowActions from "../lib/components/RowActions.svelte";
  import RowAction from "../lib/components/RowAction.svelte";
  import QuickFilter from "../lib/components/QuickFilter.svelte";
  import Button from "../lib/components/Button.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import { get, post, del } from "../lib/api.js";
  import { router, go } from "../lib/router.svelte.js";
  import { session } from "../lib/session.svelte.js";
  import { blockTypes } from "../lib/blocks.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toastError } from "../lib/toast.svelte.js";
  let list = $state(null), seeding = $state(false);
  const category = router.query.get("category") || "";
  async function load() { list = await blockTypes.load(true); }
  load();
  const categories = $derived([...new Set((list || []).map((t) => t.category || "Other"))].sort());
  const rows = $derived((list || []).filter((t) => !category || (t.category || "Other") === category));
  async function seed() {
    seeding = true;
    try { const r = await post("/block_types/seed"); toast(r.count ? `Installed ${r.count} block types.` : "The starter pack is already installed."); load(); } catch (e) { toastError(e); }
    seeding = false;
  }
  async function remove(t) {
    if (!(await confirm({ title: `Delete the ${t.label} block type?`, message: "Only a block type no page or entry uses can be deleted; otherwise mark it deprecated.", confirmLabel: "Delete", destructive: true }))) return;
    try { await del(`/block_types/${t.slug}`); toast("Deleted."); load(); } catch (e) { toastError(e); }
  }
</script>

<PageHeader title="Block types" cli="librepublish list_block_types">
  {#snippet actions()}
    {#if session.can("block_types:write")}
      <Button loading={seeding} onclick={seed}>Install starter pack</Button>
      <Button icon="add" href="#/blocks/new">New block type</Button>
    {/if}
  {/snippet}
</PageHeader>
{#if categories.length > 1}<div><QuickFilter title="Category" param="category" options={[{ label: "Every category", value: "" }, ...categories.map((c) => ({ label: c, value: c }))]} /></div>{/if}
{#if list}
  <DataTable {rows} key="slug" onrowclick={(t) => go(`/blocks/${t.slug}`)} columns={[{ label: "Label" }, { label: "Slug" }, { label: "Category" }, { label: "Fields", num: true }, { label: "" }]}>
    {#snippet row(t)}
      <Cell>
        <a href="#/blocks/{t.slug}" class="font-medium text-foreground hover:underline">{t.label}</a>
        {#if t.description}<p class="max-w-md truncate text-xs text-muted-foreground">{t.description}</p>{/if}
        <RowActions>
          <RowAction href="#/blocks/{t.slug}">Edit</RowAction>
          {#if session.can("block_types:delete")}<RowAction danger onclick={() => remove(t)}>Delete</RowAction>{/if}
        </RowActions>
      </Cell>
      <Cell mono muted>{t.slug}</Cell>
      <Cell muted>{t.category || "—"}</Cell>
      <Cell num>{(t.fields || []).length}</Cell>
      <Cell>{#if t.deprecated}<Badge tone="danger">Deprecated</Badge>{:else if t.built_in}<Badge>Starter</Badge>{/if}</Cell>
    {/snippet}
    {#snippet empty()}
      <EmptyState icon="grid" title="No block types yet">Pages are built from blocks. The starter pack has 22: a hero, text, images, a list of entries, calls to action and more.
        {#snippet actions()}{#if session.can("block_types:write")}<Button variant="primary" loading={seeding} onclick={seed}>Install the starter pack</Button>{/if}{/snippet}
      </EmptyState>
    {/snippet}
  </DataTable>
{/if}
