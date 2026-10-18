<script>
  // All pages (pages/index): status filters with counts, the locale filter,
  // bulk actions and search, the tree in a table with row actions.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import StatusLinks from "../lib/components/StatusLinks.svelte";
  import QuickFilter from "../lib/components/QuickFilter.svelte";
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
  import Icon from "../lib/components/Icon.svelte";
  import QuickEdit from "../lib/components/QuickEdit.svelte";
  import ViewOptions from "../lib/components/ViewOptions.svelte";
  import { listView } from "../lib/prefs.svelte.js";
  import { patch } from "../lib/api.js";
  import { save } from "../lib/save.js";
  import { get, post, del, pagePath } from "../lib/api.js";
  import { router } from "../lib/router.svelte.js";
  import { session } from "../lib/session.svelte.js";
  import { site } from "../lib/site.svelte.js";
  import { toast, toastError, undoable } from "../lib/toast.svelte.js";
  import { restorer } from "../lib/trash.js";

  let list = $state(null), counts = $state({}), locales = $state([]), selected = $state([]);
  const view = listView("pages");
  let quick = $state(null), quickErrors = $state({}), quickSaving = $state(false), parents = $state([]);
  const status = router.query.get("status") || "";
  const locale = router.query.get("locale") || "";
  const q = router.query.get("s") || "";
  const page = Number(router.query.get("page") || 1);
  const canBulk = session.can("pages:publish") || session.can("pages:delete");

  async function load() {
    const params = new URLSearchParams({ per: String(view.per), page: String(page), sort: q ? "updated" : "path" });
    if (status) params.set("status", status);
    if (locale) params.set("locale", locale);
    if (q) params.set("q", q);
    const [r, ...c] = await Promise.all([
      get("/pages?" + params),
      ...["", "draft", "published", "archived"].map((s) => get(`/pages?per=1${s ? "&status=" + s : ""}`).then((x) => [s, x.total])),
    ]);
    list = r; counts = Object.fromEntries(c);
    if (!locales.length) locales = [...new Set((await get("/pages?per=100")).pages.map((p) => p.locale))];
    selected = [];
  }
  load();

  // Trash and status changes are undone, not confirmed.
  async function bulk(action) {
    const rows = list.pages.filter((p) => selected.includes(p.path));
    try {
      if (action === "delete") {
        const r = await post("/pages/bulk_destroy", { slugs: selected });
        undoable(`${r.deleted} moved to the trash.`, restorer("page", rows.map((p) => p.id), load), { done: "Restored." });
      } else {
        const r = await post("/pages/bulk_update_status", { slugs: selected, status: action.replace("status-", "") });
        undoable(`${r.updated} set to ${r.status}.`, session.can("pages:publish") ? () => restoreStatuses(rows) : null, { done: "Statuses put back." });
      }
      load();
    } catch (e) { toastError(e); }
  }
  async function restoreStatuses(rows) {
    const by = Object.groupBy(rows, (p) => p.status);
    for (const [status, ps] of Object.entries(by)) await post("/pages/bulk_update_status", { slugs: ps.map((p) => p.path), status });
    load();
  }
  async function quickEdit(p) {
    quick = p.path; quickErrors = {};
    if (!parents.length) parents = (await get("/pages?per=100&sort=path")).pages;
  }
  async function quickSave(p, draft) {
    quickSaving = true;
    const body = { page: { title: draft.title, slug: draft.slug, locale: draft.locale, parent_id: draft.parent_id || null } };
    if (session.can("pages:publish")) body.page.status = draft.status;
    const r = await save(() => patch(`/pages/${pagePath(p.path)}`, body), "page");
    quickSaving = false; quickErrors = r.errors || {};
    if (r.ok) { quick = null; load(); }
  }
  async function trash(p) {
    try {
      await del(`/pages/${pagePath(p.path)}`);
      undoable(`“${p.title}” moved to the trash${list.pages.some((x) => x.path.startsWith(p.path + "/")) ? ", with its child pages" : ""}.`, restorer("page", [p.id], load), { done: "Restored." });
      load();
    } catch (e) { toastError(e); }
  }
  const statusOptions = $derived([{ label: "All", value: "", count: counts[""] }, ...["published", "draft", "archived"].filter((s) => counts[s]).map((s) => ({ label: s[0].toUpperCase() + s.slice(1), value: s, count: counts[s] }))]);
</script>

<PageHeader title="Pages" cli="librepublish list_pages{status ? ' --status ' + status : ''}">
  {#snippet actions()}{#if session.can("pages:write")}<Button icon="add" href="#/pages/new">New page</Button>{/if}{/snippet}
</PageHeader>

<div class="flex flex-wrap items-center justify-between gap-3">
  <StatusLinks options={statusOptions} />
  {#if locales.length > 1}<QuickFilter title="Locale" param="locale" options={[{ label: "Any locale", value: "" }, ...locales.map((l) => ({ label: l, value: l }))]} />{/if}
</div>

<div class="flex flex-wrap items-center justify-between gap-2">
  {#if canBulk}
    <BulkActions noun="page" count={selected.length} onapply={bulk} actions={[
      ...(session.can("pages:delete") ? [{ value: "delete", label: "Move to trash" }] : []),
      ...(session.can("pages:publish") ? ["published", "draft", "archived"].map((s) => ({ value: "status-" + s, label: `Set to ${s}` })) : []),
    ]} />
  {:else}<span></span>{/if}
  <div class="flex items-center gap-2">
    <SearchBox placeholder="Search pages…" />
    <ViewOptions {view} columns={[{ key: "path", label: "Path" }, { key: "status", label: "Status" }, { key: "locale", label: "Locale" }, { key: "updated", label: "Updated" }]} onchange={(c) => { if (c.per) load(); }} />
  </div>
</div>

{#if list}
  <DataTable rows={list.pages} selectable={canBulk} bind:selected key="path" hidden={view.hidden} density={view.density} editing={quick}
    columns={[{ label: "Title" }, { key: "path", label: "Path" }, { key: "status", label: "Status" }, { key: "locale", label: "Locale" }, { key: "updated", label: "Updated" }]}
    emptyText={status || q || locale ? "No pages match." : "No pages yet. The first one is usually the home page."}>
    {#snippet row(p, show)}
      <Cell>
        <span class="inline-block" style="padding-left: {Math.min(q ? 0 : p.depth, 6) * 1.25}rem">
          {#if p.depth && !q}<span class="text-muted-foreground">↳ </span>{/if}
          <a href="#/pages/edit/{encodeURIComponent(p.path)}" class="font-medium text-foreground hover:underline">{p.title}</a>
          {#if p.path === "home"}<span class="text-muted-foreground text-sm font-medium"> — Home</span>{/if}
        </span>
        {#if p.locked_by}<p class="text-muted-foreground mt-1 flex items-center gap-1 text-xs"><Icon name="lock" size="xs" />{p.locked_by} is editing</p>{/if}
        <RowActions>
          {#if session.can("pages:write")}<RowAction href="#/pages/edit/{encodeURIComponent(p.path)}">Edit</RowAction>{/if}
          {#if session.can("pages:write") && !p.locked_by}<RowAction onclick={() => quickEdit(p)}>Quick Edit</RowAction>{/if}
          {#if site.siteURL && p.status === "published"}<RowAction href="{site.siteURL.replace(/\/$/, '')}/{p.path === 'home' ? '' : p.path}">View</RowAction>{/if}
          {#if session.can("pages:delete") && !p.locked_by}<RowAction danger onclick={() => trash(p)}>Trash</RowAction>{/if}
        </RowActions>
      </Cell>
      {#if show("path")}<Cell mono muted>/{p.path}</Cell>{/if}
      {#if show("status")}<Cell><Badge status={p.status} /></Cell>{/if}
      {#if show("locale")}<Cell muted>{p.locale}</Cell>{/if}
      {#if show("updated")}<Cell muted nowrap><Time value={p.updated_at} class="text-xs" /></Cell>{/if}
    {/snippet}
    {#snippet editor(p)}
      <QuickEdit record={p} canPublish={session.can("pages:publish")} errors={quickErrors} saving={quickSaving}
        parents={parents.filter((x) => x.id !== p.id && !x.path.startsWith(p.path + "/")).map((x) => ({ value: x.id, label: x.title, hint: "/" + x.path }))}
        onsave={(d) => quickSave(p, d)} oncancel={() => (quick = null)} />
    {/snippet}
  </DataTable>
  <Pagination {page} per={list.per} total={list.total} />
{/if}
