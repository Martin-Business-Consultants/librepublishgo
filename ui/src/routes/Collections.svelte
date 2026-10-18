<script>
  import PageHeader from "../lib/components/PageHeader.svelte";
  import DataTable from "../lib/components/DataTable.svelte";
  import Cell from "../lib/components/Cell.svelte";
  import RowActions from "../lib/components/RowActions.svelte";
  import RowAction from "../lib/components/RowAction.svelte";
  import Button from "../lib/components/Button.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import { get } from "../lib/api.js";
  import { session } from "../lib/session.svelte.js";
  import { go } from "../lib/router.svelte.js";
  let cols = $state(null);
  get("/collections").then((r) => (cols = r.collections));
</script>

<PageHeader title="Collections" cli="librepublish list_collections">
  {#snippet actions()}{#if session.can("collections:write")}<Button icon="add" href="#/collections/new">New collection</Button>{/if}{/snippet}
</PageHeader>

{#if cols}
  <DataTable rows={cols} onrowclick={(c) => go(`/collections/${c.slug}`)} columns={[{ label: "Name" }, { label: "Slug" }, { label: "Fields" }, { label: "Entries", num: true }, { label: "API" }]}>
    {#snippet row(c)}
      <Cell>
        <a href="#/collections/{c.slug}" class="font-medium text-foreground hover:underline">{c.name}</a>
        <RowActions>
          <RowAction href="#/collections/{c.slug}">Entries</RowAction>
          {#if session.can("entries:write")}<RowAction href="#/collections/{c.slug}/new">Add entry</RowAction>{/if}
          {#if session.can("collections:write")}<RowAction href="#/collections/{c.slug}/settings">Settings</RowAction>{/if}
        </RowActions>
      </Cell>
      <Cell mono muted>{c.slug}</Cell>
      <Cell muted>{(c.schema?.fields || []).map((f) => f.label || f.name).join(", ") || "—"}</Cell>
      <Cell num>{c.entry_count}</Cell>
      <Cell>{#if c.api_enabled}<Badge tone="info">On</Badge>{:else}<span class="text-muted-foreground">—</span>{/if}</Cell>
    {/snippet}
    {#snippet empty()}
      <EmptyState icon="stack" title="No collections yet">
        A collection is a set of entries of one shape: posts, team members, products, reviews.
        {#snippet actions()}{#if session.can("collections:write")}<Button variant="primary" href="#/collections/new">Add a collection</Button>{/if}{/snippet}
      </EmptyState>
    {/snippet}
  </DataTable>
{/if}
