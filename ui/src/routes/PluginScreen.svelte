<script>
  // A plugin's admin screen, drawn from its manifest's description (data,
  // never code; see docs/plugins.md): a table (status links, filters, row
  // actions, paging), a form (sections in a main column and a sidebar, the
  // CMS's own field types plus the form builder), or a record's details.
  // The screen's query string is its parameters; they're passed to its
  // source route.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import DataTable from "../lib/components/DataTable.svelte";
  import Cell from "../lib/components/Cell.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Postbox from "../lib/components/Postbox.svelte";
  import Button from "../lib/components/Button.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import Time from "../lib/components/Time.svelte";
  import Spinner from "../lib/components/Spinner.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import StatusLinks from "../lib/components/StatusLinks.svelte";
  import QuickFilter from "../lib/components/QuickFilter.svelte";
  import RowActions from "../lib/components/RowActions.svelte";
  import RowAction from "../lib/components/RowAction.svelte";
  import CopyButton from "../lib/components/CopyButton.svelte";
  import Dialog from "../lib/components/Dialog.svelte";
  import FieldsForm from "../lib/fields/FieldsForm.svelte";
  import * as Tabs from "$lib/components/ui/tabs/index.js";
  import { get, post, ApiError } from "../lib/api.js";
  import { save, saveShortcut } from "../lib/save.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toasts, toastError } from "../lib/toast.svelte.js";
  import { router, href, withQuery } from "../lib/router.svelte.js";
  import { pluginAdmin } from "../lib/plugins.svelte.js";
  import { onDestroy } from "svelte";

  let { params } = $props();
  // svelte-ignore state_referenced_locally
  const key = params.key;
  const query = Object.fromEntries(router.query);
  let plugin = $state(null), screen = $state(null), data = $state(null), missing = $state(false);
  let errors = $state({}), saving = $state(false), tab = $state("");
  // Which of a form's tabs have errors: those holding a field the API
  // complained about.
  const tabHasErrors = (t) => (t.sections || []).some((sec) => (sec.fields || []).some((f) => Object.keys(errors).some((k) => k === f.name || k.startsWith(f.name + "[") || k.startsWith(f.name + "."))));

  const route = (path, q = {}) => {
    const s = new URLSearchParams(Object.entries(q).filter(([, v]) => v !== "" && v != null)).toString();
    return `/x/${key}${path}${s ? (path.includes("?") ? "&" : "?") + s : ""}`;
  };
  // A link or action's params, read from a row or record: {id: "slug"}.
  const paramsFrom = (map, rec) => Object.fromEntries(Object.entries(map || {}).map(([k, field]) => [k, rec?.[field]]));
  const screenHref = (id, p = {}) => href(`/plugins/${key}/${id}`, p);
  const byId = (id) => plugin?.screens.find((s) => s.id === id);

  async function load() {
    plugin = await pluginAdmin(key);
    screen = byId(params.screen);
    if (!screen) { missing = true; return; }
    data = await get(route(screen.source, query));
  }
  load().catch((e) => { if (e.status === 404 || e.status === 403) missing = true; toastError(e); });

  const record = $derived(screen?.form ? data?.[screen.form.record] : screen?.detail ? data?.[screen.detail.record] : null);
  const isNew = $derived(!!screen?.form && !query.id);
  const title = $derived(!screen ? "" : record && screen.title_key && record[screen.title_key] ? record[screen.title_key] : isNew && screen.new_title ? screen.new_title : screen.title);
  const parent = $derived(screen?.parent ? byId(screen.parent) : null);
  const canWrite = (path) => plugin?.writes.includes(path.split("?")[0]);

  async function runAction(a, rec) {
    if (a.confirm && !(await confirm({ title: a.label + "?", message: a.confirm, confirmLabel: a.label, destructive: !!a.danger }))) return;
    try {
      const r = await post(route(a.post, paramsFrom(a.params, rec)), a.body || {});
      toast(r?.message || "Done.");
      if (a.then) location.hash = screenHref(a.then).slice(1);
      else data = await get(route(screen.source, query));
    } catch (e) { toastError(e); }
  }

  async function submit() {
    if (!screen?.form || saving || !canWrite(screen.form.save)) return;
    saving = true;
    const r = await save(() => post(route(screen.form.save, isNew ? {} : { id: query.id }), { [screen.form.record]: $state.snapshot(record) }));
    saving = false;
    errors = r.errors || {};
    const bad = (screen.form.tabs || []).find(tabHasErrors);
    if (bad && !tabHasErrors(screen.form.tabs.find((t) => t.id === tab) || {})) tab = bad.id;
    if (r.ok) {
      const next = r.res?.redirect;
      if (next && (next.screen !== params.screen || JSON.stringify(next.params) !== JSON.stringify({ id: query.id }))) location.hash = screenHref(next.screen, next.params).slice(1);
      else if (r.res?.[screen.form.record]) data = { ...data, [screen.form.record]: r.res[screen.form.record] };
    }
  }
  // A section's action posts the record as it is in the editor, saved or
  // not ({"send": "record"}): Send test. Its errors show on the fields, and
  // an answer's result ({title, items}) opens to read: Test hook's.
  let running = $state(""), result = $state(null), resultOpen = $state(false);
  async function runSectionAction(a) {
    if (running || !canWrite(a.post)) return;
    if (a.confirm && !(await confirm({ title: a.label + "?", message: a.confirm, confirmLabel: a.label }))) return;
    running = a.post;
    try {
      const body = a.send === "record" ? { [screen.form.record]: $state.snapshot(record) } : a.body || {};
      const r = await post(route(a.post, isNew ? {} : { id: query.id }), body);
      errors = {};
      if (r?.result) { result = r.result; resultOpen = true; }
      else toast(r?.message || "Done.");
    } catch (e) {
      if (e instanceof ApiError && e.status === 422) { errors = e.errors || {}; toasts.show(e.body?.message && e.body.message !== "Validation failed" ? e.body.message : "Some fields need attention.", "danger"); }
      else toastError(e);
    } finally { running = ""; }
  }
  const stop = saveShortcut(submit);
  onDestroy(stop);

  const visible = (a, rec) => (!a.hide_if || !Object.entries(a.hide_if).every(([k, v]) => rec?.[k] === v)) && (!a.show_if || Object.entries(a.show_if).every(([k, v]) => rec?.[k] === v));
  const downloadHref = (a) => "/api" + route(a.download, Object.fromEntries((a.keep || []).map((k) => [k, query[k]])));
</script>

{#snippet itemValue(it)}
  {#if it.badge}<Badge status={it.value} />
  {:else if it.time}<Time value={it.value} />
  {:else if it.screen}<a class="text-primary hover:underline" href={screenHref(it.screen, it.params)}>{it.value}</a>
  {:else if it.href && it.value}<a class="text-primary break-all hover:underline" href={it.href} target={it.href.startsWith("mailto:") || it.href.startsWith("#") ? undefined : "_blank"} rel="noreferrer">{it.value}</a>
  {:else if it.tone}<span class={it.tone === "danger" ? "text-destructive" : "text-emerald-700 dark:text-emerald-400"}>{it.value}</span>
  {:else if it.multiline}<p class="whitespace-pre-wrap break-words">{it.value || "—"}</p>
  {:else}<span class="break-words">{it.value || "—"}</span>{/if}
{/snippet}

{#snippet sectionActions(sec)}
  {#each (sec.actions || []).filter((a) => canWrite(a.post)) as a}
    <div class="mt-6 flex flex-wrap items-center gap-3 border-t pt-4">
      <Button icon={a.icon} loading={running === a.post} onclick={() => runSectionAction(a)}>{a.label}</Button>
      {#if a.help}<p class="text-muted-foreground max-w-prose flex-1 text-xs">{a.help}</p>{/if}
    </div>
  {/each}
{/snippet}

{#snippet items(list, side = false)}
  <dl class="flex flex-col {side ? 'gap-3' : 'divide-y'}">
    {#each list || [] as it}
      <div class={side ? "flex flex-col gap-0.5" : "grid gap-1 py-3 first:pt-0 last:pb-0 sm:grid-cols-[12rem_minmax(0,1fr)] sm:gap-4"}>
        <dt class="text-muted-foreground text-xs font-medium {side ? '' : 'sm:pt-0.5 sm:text-sm sm:font-normal'}">{it.label}</dt>
        <dd class="flex min-w-0 items-start gap-1 text-sm">{@render itemValue(it)}{#if it.copy}<CopyButton text={it.value} label="" />{/if}</dd>
      </div>
    {/each}
  </dl>
{/snippet}

<Dialog bind:open={resultOpen} title={result?.title || ""} size="lg">
  {#if result}{@render items(result.items)}{/if}
  {#snippet actions()}<Button variant="primary" onclick={() => (resultOpen = false)}>Done</Button>{/snippet}
</Dialog>

{#if missing}
  <EmptyState icon="package" title="This screen isn't here">The plugin may be off, or your role can't open it. <a class="text-primary hover:underline" href="#/plugins">Plugins</a></EmptyState>
{:else if !screen || !data}
  <div class="flex justify-center py-16"><Spinner /></div>
{:else}
  {#if parent}<a href={screenHref(parent.id)} class="text-muted-foreground hover:text-foreground -mb-2 text-sm">← {parent.title}</a>{/if}
  <PageHeader {title} icon={plugin.menu?.icon || "package"}>
    {#snippet actions()}
      {#each screen.actions || [] as a}
        {#if a.screen}<Button variant={a.primary ? "primary" : "default"} icon={a.icon} href={screenHref(a.screen, a.params)}>{a.label}</Button>
        {:else if a.download}<Button icon={a.icon} href={downloadHref(a)} download>{a.label}</Button>
        {:else if a.post && canWrite(a.post)}<Button icon={a.icon} onclick={() => runAction(a, record)}>{a.label}</Button>{/if}
      {/each}
      {#if screen.form && canWrite(screen.form.save)}<Button variant="primary" loading={saving} onclick={submit}>{isNew ? "Create" : "Save changes"}</Button>{/if}
      {#if screen.detail}
        {#each (screen.detail.actions || []).filter((a) => visible(a, record) && canWrite(a.post)) as a}
          <Button variant={a.danger ? "danger" : "default"} onclick={() => runAction(a, record)}>{a.label}</Button>
        {/each}
      {/if}
    {/snippet}
  </PageHeader>

  {#if screen.table}
    {@const t = screen.table}
    {@const rows = data[t.rows] || []}
    {#if t.status_links || t.filters}
      <div class="flex flex-wrap items-center justify-between gap-3">
        {#if t.status_links}<StatusLinks param={t.status_links.param} options={data[t.status_links.options] || []} />{:else}<span></span>{/if}
        <div class="flex flex-wrap gap-2">
          {#each t.filters || [] as f}<QuickFilter title={f.label} param={f.param} options={data[f.options] || []} />{/each}
        </div>
      </div>
    {/if}
    <DataTable {rows} key={t.key || "id"} columns={t.columns.map((c) => ({ label: c.label, num: c.num }))}>
      {#snippet row(r)}
        {#each t.columns as c, ci}
          <Cell num={c.num} muted={ci > 0 && !c.badge}>
            {#if c.badge}<Badge status={r[c.key]} />
            {:else if c.time}<Time value={r[c.key]} />
            {:else if c.link}<a class="{ci === 0 ? 'text-foreground font-medium' : 'text-primary'} hover:underline" href={screenHref(c.link.screen, paramsFrom(c.link.params, r))}>{r[c.key] ?? ""}</a>
            {:else}{r[c.key] ?? ""}{/if}
            {#if ci === 0 && c.sub && r[c.sub]}<p class="text-muted-foreground mt-0.5 line-clamp-1 text-xs">{r[c.sub]}</p>{/if}
            {#if ci === 0 && t.row_actions}
              <RowActions>
                {#each t.row_actions as a}
                  {#if a.screen}<RowAction href={screenHref(a.screen, paramsFrom(a.params, r))}>{a.label}</RowAction>
                  {:else if a.post && canWrite(a.post)}<RowAction danger={a.danger} onclick={() => runAction(a, r)}>{a.label}</RowAction>{/if}
                {/each}
              </RowActions>
            {/if}
          </Cell>
        {/each}
      {/snippet}
      {#snippet empty()}
        <EmptyState icon={plugin.menu?.icon || "package"} title={t.empty?.title || "Nothing here yet"}>{t.empty?.text || ""}</EmptyState>
      {/snippet}
    </DataTable>
    {#if t.cursor && (data[t.cursor.next] || query[t.cursor.param])}
      <nav class="flex items-center justify-end gap-2" aria-label="Pages">
        {#if query[t.cursor.param]}<Button size="sm" href={withQuery({ [t.cursor.param]: null })}>Newest</Button>{/if}
        {#if data[t.cursor.next]}<Button size="sm" icon="arrow-right" href={withQuery({ [t.cursor.param]: data[t.cursor.next] })}>Older</Button>{/if}
      </nav>
    {/if}
  {:else if screen.form && record && screen.form.tabs}
    {@const fm = screen.form}
    <Tabs.Root value={tab || fm.tabs[0].id} onValueChange={(v) => (tab = v)} class="gap-4">
      <div class="overflow-x-auto">
        <Tabs.List>
          {#each fm.tabs as t (t.id)}
            <Tabs.Trigger value={t.id}>{t.title}{#if tabHasErrors(t)}<span class="bg-destructive size-1.5 rounded-full" title="Something to fix"></span>{/if}</Tabs.Trigger>
          {/each}
        </Tabs.List>
      </div>
      {#each fm.tabs as t (t.id)}
        <Tabs.Content value={t.id} class="flex flex-col gap-6">
          {#each t.sections as sec}
            {#if sec.readonly}
              {#if record[sec.readonly]}<Panel title={sec.title}><div class="p-6">{@render items(record[sec.readonly])}</div></Panel>{/if}
            {:else if sec.title}
              <Panel title={sec.title}><div class="p-6"><FieldsForm fields={sec.fields} data={record} {errors} />{@render sectionActions(sec)}</div></Panel>
            {:else}
              <Panel><div class="p-6"><FieldsForm fields={sec.fields} data={record} {errors} />{@render sectionActions(sec)}</div></Panel>
            {/if}
          {/each}
        </Tabs.Content>
      {/each}
    </Tabs.Root>
  {:else if screen.form && record}
    {@const fm = screen.form}
    {#each fm.top || [] as sec}
      <Panel title={sec.title}><div class="p-6"><FieldsForm fields={sec.fields} data={record} {errors} />{@render sectionActions(sec)}</div></Panel>
    {/each}
    <div class="flex flex-col gap-6 lg:flex-row lg:items-start">
      <div class="flex min-w-0 flex-1 flex-col gap-6">
        {#each fm.main || [] as sec}
          <Panel title={sec.title}><div class="p-6"><FieldsForm fields={sec.fields} data={record} {errors} />{@render sectionActions(sec)}</div></Panel>
        {/each}
      </div>
      <aside class="flex min-w-0 shrink-0 flex-col gap-4 lg:sticky lg:top-16 lg:w-80">
        {#each fm.side || [] as sec}
          {#if sec.readonly}
            {#if record[sec.readonly]}<Postbox key="{key}-{screen.id}-{sec.key}" title={sec.title}>{@render items(record[sec.readonly], true)}</Postbox>{/if}
          {:else}
            <Postbox key="{key}-{screen.id}-{sec.key}" title={sec.title}><FieldsForm fields={sec.fields} data={record} {errors} gap="gap-4" />{@render sectionActions(sec)}</Postbox>
          {/if}
        {/each}
      </aside>
    </div>
  {:else if screen.detail && record}
    {@const d = screen.detail}
    <div class="flex flex-col gap-6 lg:flex-row lg:items-start">
      <div class="flex min-w-0 flex-1 flex-col gap-6">
        {#each d.main || [] as sec}<Panel title={sec.title}><div class="p-6">{@render items(record[sec.items])}</div></Panel>{/each}
      </div>
      <aside class="flex min-w-0 shrink-0 flex-col gap-4 lg:w-80">
        {#each d.side || [] as sec}<Postbox key="{key}-{screen.id}-{sec.title}" title={sec.title}>{@render items(record[sec.items], true)}</Postbox>{/each}
      </aside>
    </div>
  {/if}
{/if}
