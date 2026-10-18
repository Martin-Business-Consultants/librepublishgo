<script>
  // A collection's entries: status filters, bulk actions, search, the table.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import StatusLinks from "../lib/components/StatusLinks.svelte";
  import SearchBox from "../lib/components/SearchBox.svelte";
  import BulkActions from "../lib/components/BulkActions.svelte";
  import DataTable from "../lib/components/DataTable.svelte";
  import Cell from "../lib/components/Cell.svelte";
  import RowActions from "../lib/components/RowActions.svelte";
  import RowAction from "../lib/components/RowAction.svelte";
  import Pagination from "../lib/components/Pagination.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import Button from "../lib/components/Button.svelte";
  import Time from "../lib/components/Time.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import Icon from "../lib/components/Icon.svelte";
  import QuickEdit from "../lib/components/QuickEdit.svelte";
  import ViewOptions from "../lib/components/ViewOptions.svelte";
  import { listView } from "../lib/prefs.svelte.js";
  import { patch } from "../lib/api.js";
  import { save } from "../lib/save.js";
  import { get, post, del } from "../lib/api.js";
  import { router } from "../lib/router.svelte.js";
  import { session } from "../lib/session.svelte.js";
  import { toast, toastError, undoable } from "../lib/toast.svelte.js";
  import { restorer } from "../lib/trash.js";

  let { params } = $props();
  // svelte-ignore state_referenced_locally
  const slug = params.slug;
  let col = $state(null), list = $state(null), counts = $state({}), selected = $state([]);
  const view = listView("entries");
  let quick = $state(null), quickErrors = $state({}), quickSaving = $state(false);
  const status = router.query.get("status") || "", q = router.query.get("s") || "", page = Number(router.query.get("page") || 1);
  const canBulk = session.can("entries:publish") || session.can("entries:delete");
  async function load() {
    const p = new URLSearchParams({ per: String(view.per), page: String(page) });
    if (status) p.set("status", status);
    if (q) p.set("q", q);
    const [c, r, ...n] = await Promise.all([
      col ? Promise.resolve({ collection: col }) : get(`/collections/${slug}`),
      get(`/collections/${slug}/entries?${p}`),
      ...["", "draft", "published", "archived"].map((s) => get(`/collections/${slug}/entries?per=1${s ? "&status=" + s : ""}`).then((x) => [s, x.total])),
    ]);
    col = c.collection; list = r; counts = Object.fromEntries(n); selected = [];
  }
  load().catch(toastError);
  // Trash and status changes are undone, not confirmed.
  async function bulk(action) {
    const rows = list.entries.filter((e) => selected.includes(e.slug));
    try {
      if (action === "delete") {
        const r = await post(`/collections/${slug}/entries/bulk_destroy`, { slugs: selected });
        undoable(`${r.deleted} moved to the trash.`, restorer("entry", rows.map((e) => e.id), load), { done: "Restored." });
      } else {
        const r = await post(`/collections/${slug}/entries/bulk_update_status`, { slugs: selected, status: action.replace("status-", "") });
        undoable(`${r.updated} set to ${r.status}.`, session.can("entries:publish") ? () => restoreStatuses(rows) : null, { done: "Statuses put back." });
      }
      load();
    } catch (e) { toastError(e); }
  }
  async function restoreStatuses(rows) {
    const by = Object.groupBy(rows, (e) => e.status);
    for (const [status, es] of Object.entries(by)) await post(`/collections/${slug}/entries/bulk_update_status`, { slugs: es.map((e) => e.slug), status });
    load();
  }
  async function quickSave(e, draft) {
    quickSaving = true;
    const body = { entry: { title: draft.title, slug: draft.slug, locale: draft.locale } };
    if (session.can("entries:publish")) body.entry.status = draft.status;
    const r = await save(() => patch(`/collections/${slug}/entries/${encodeURIComponent(e.slug)}`, body), "entry");
    quickSaving = false; quickErrors = r.errors || {};
    if (r.ok) { quick = null; load(); }
  }
  async function trash(e) {
    try {
      await del(`/collections/${slug}/entries/${encodeURIComponent(e.slug)}`);
      undoable(`“${e.title}” moved to the trash.`, restorer("entry", [e.id], load), { done: "Restored." });
      load();
    } catch (err) { toastError(err); }
  }
  const statusOptions = $derived([{ label: "All", value: "", count: counts[""] }, ...["published", "draft", "archived"].filter((s) => counts[s]).map((s) => ({ label: s[0].toUpperCase() + s.slice(1), value: s, count: counts[s] }))]);
</script>

<PageHeader title={col?.name || "…"} icon="newspaper" cli="librepublish list_entries --slug {slug}">
  {#snippet actions()}
    {#if session.can("collections:write")}<Button href="#/collections/{slug}/settings">Schema</Button>{/if}
    {#if session.can("entries:write")}<Button icon="add" href="#/collections/{slug}/new">New entry</Button>{/if}
  {/snippet}
</PageHeader>
<StatusLinks options={statusOptions} />
<div class="flex flex-wrap items-center justify-between gap-2">
  {#if canBulk}
    <BulkActions noun="entry" count={selected.length} onapply={bulk} actions={[
      ...(session.can("entries:delete") ? [{ value: "delete", label: "Move to trash" }] : []),
      ...(session.can("entries:publish") ? ["published", "draft", "archived"].map((s) => ({ value: "status-" + s, label: `Set to ${s}` })) : []),
    ]} />
  {:else}<span></span>{/if}
  <div class="flex items-center gap-2">
    <SearchBox placeholder="Search entries…" />
    <ViewOptions {view} columns={[{ key: "slug", label: "Slug" }, { key: "category", label: "Category" }, { key: "status", label: "Status" }, { key: "locale", label: "Locale" }, { key: "updated", label: "Updated" }]} onchange={(c) => { if (c.per) load(); }} />
  </div>
</div>
{#if list}
  <DataTable rows={list.entries} key="slug" selectable={canBulk} bind:selected hidden={view.hidden} density={view.density} editing={quick}
    columns={[{ label: "Title" }, { key: "slug", label: "Slug" }, { key: "category", label: "Category" }, { key: "status", label: "Status" }, { key: "locale", label: "Locale" }, { key: "updated", label: "Updated" }]}>
    {#snippet row(e, show)}
      <Cell>
        <a href="#/collections/{slug}/edit/{encodeURIComponent(e.slug)}" class="font-medium text-foreground hover:underline">{e.title}</a>
        {#if e.locked_by}<p class="text-muted-foreground mt-1 flex items-center gap-1 text-xs"><Icon name="lock" size="xs" />{e.locked_by} is editing</p>{/if}
        <RowActions>
          <RowAction href="#/collections/{slug}/edit/{encodeURIComponent(e.slug)}">Edit</RowAction>
          {#if session.can("entries:write") && !e.locked_by}<RowAction onclick={() => { quick = e.slug; quickErrors = {}; }}>Quick Edit</RowAction>{/if}
          {#if session.can("entries:delete") && !e.locked_by}<RowAction danger onclick={() => trash(e)}>Trash</RowAction>{/if}
        </RowActions>
      </Cell>
      {#if show("slug")}<Cell mono muted>{e.slug}</Cell>{/if}
      {#if show("category")}<Cell muted>{e.category?.title || "—"}</Cell>{/if}
      {#if show("status")}<Cell><Badge status={e.status} /></Cell>{/if}
      {#if show("locale")}<Cell muted>{e.locale}</Cell>{/if}
      {#if show("updated")}<Cell muted nowrap><Time value={e.updated_at} class="text-xs" /></Cell>{/if}
    {/snippet}
    {#snippet editor(e)}
      <QuickEdit record={e} canPublish={session.can("entries:publish")} errors={quickErrors} saving={quickSaving} onsave={(d) => quickSave(e, d)} oncancel={() => (quick = null)} />
    {/snippet}
    {#snippet empty()}
      {#if status || q}No entries match.
      {:else}
        <EmptyState icon="newspaper" title="No entries yet">
          {#snippet actions()}{#if session.can("entries:write")}<Button variant="primary" href="#/collections/{slug}/new">Add the first</Button>{/if}{/snippet}
        </EmptyState>
      {/if}
    {/snippet}
  </DataTable>
  <Pagination {page} per={list.per} total={list.total} />
{/if}
