<script>
  import PageHeader from "../lib/components/PageHeader.svelte";
  import DataTable from "../lib/components/DataTable.svelte";
  import Cell from "../lib/components/Cell.svelte";
  import Button from "../lib/components/Button.svelte";
  import Time from "../lib/components/Time.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import { get } from "../lib/api.js";
  import { session } from "../lib/session.svelte.js";
  import { go } from "../lib/router.svelte.js";
  let list = $state(null);
  get("/globals").then((r) => (list = r.globals));
</script>

<PageHeader title="Globals" cli="librepublish list_globals">
  {#snippet actions()}{#if session.can("globals:write")}<Button icon="add" href="#/globals/new">New global</Button>{/if}{/snippet}
</PageHeader>
{#if list}
  <DataTable rows={list} onrowclick={(g) => go(`/globals/${g.slug}`)} columns={[{ label: "Name" }, { label: "Slug" }, { label: "Description" }, { label: "Updated" }]}>
    {#snippet row(g)}
      <Cell><a href="#/globals/{g.slug}" class="font-medium text-foreground hover:underline">{g.name}</a></Cell>
      <Cell mono muted>{g.slug}</Cell>
      <Cell muted>{g.description || "—"}</Cell>
      <Cell muted nowrap><Time value={g.updated_at} class="text-xs" /></Cell>
    {/snippet}
    {#snippet empty()}
      <EmptyState icon="world" title="No globals yet">Navigation, the footer, contact details: what every page shares lives here.
        {#snippet actions()}{#if session.can("globals:write")}<Button variant="primary" href="#/globals/new">Add a global</Button>{/if}{/snippet}
      </EmptyState>
    {/snippet}
  </DataTable>
{/if}
