<script>
  // A page's editor (pages/_form): the title, its fields and blocks, SEO;
  // down the side the Publish box and Page Attributes. ⌘S saves; leaving
  // with unsaved changes asks first.
  import Field from "../lib/components/Field.svelte";
  import Input from "../lib/components/Input.svelte";
  import Combobox from "../lib/components/Combobox.svelte";
  import Button from "../lib/components/Button.svelte";
  import Postbox from "../lib/components/Postbox.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Dialog from "../lib/components/Dialog.svelte";
  import Spinner from "../lib/components/Spinner.svelte";
  import Time from "../lib/components/Time.svelte";
  import CopyButton from "../lib/components/CopyButton.svelte";
  import FieldsForm from "../lib/fields/FieldsForm.svelte";
  import BlocksEditor from "../lib/fields/BlocksEditor.svelte";
  import SchemaEditor from "../lib/fields/SchemaEditor.svelte";
  import PublishBox from "../lib/fields/PublishBox.svelte";
  import PublishChecklist from "../lib/fields/PublishChecklist.svelte";
  import { prefs } from "../lib/prefs.svelte.js";
  import { flash } from "../lib/flash.js";
  import SeoFields from "../lib/fields/SeoFields.svelte";
  import EditorTabs from "../lib/components/EditorTabs.svelte";
  import EditingNotices from "../lib/components/EditingNotices.svelte";
  import { Editing } from "../lib/editing.svelte.js";
  import { get, post, patch, del, pagePath } from "../lib/api.js";
  import { save, saveShortcut } from "../lib/save.js";
  import { session } from "../lib/session.svelte.js";
  import { site } from "../lib/site.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toastError, undoable } from "../lib/toast.svelte.js";
  import { restorer } from "../lib/trash.js";
  import { slugify } from "../lib/format.js";
  import { go } from "../lib/router.svelte.js";
  import { onDestroy } from "svelte";

  let { params = {} } = $props();
  // svelte-ignore state_referenced_locally
  const path = params.rest || null;

  let page = $state(null), schema = $state([]), parents = $state([]);
  let errors = $state({}), saving = $state(false), slugTouched = $state(!!path), baseline = $state("");
  let editingFields = $state(false), draftSchema = $state([]), tab = $state("edit");
  // Who else has it open, and what's typed but not saved.
  let editing = $state(null);
  // The checks before a first publish, and the panel after it.
  let checklist = $state(false), justPublished = $state(false), loadedStatus = "";

  const snapshot = () => JSON.stringify($state.snapshot(page));
  const dirty = $derived(page && baseline && snapshot() !== baseline);

  async function load() {
    const all = (await get("/pages?per=100&sort=path")).pages;
    if (path) {
      const [p, s] = await Promise.all([get(`/pages/${pagePath(path)}`), get(`/pages/${pagePath(path)}/schema`).catch(() => ({ page: { schema: { fields: [] } } }))]);
      page = { ...p.page, blocks: p.page.blocks.map(({ resolved, ...b }) => b), publish_at: null };
      schema = s.page.schema?.fields || [];
    } else {
      page = { title: "", slug: "", status: "draft", locale: "en", parent_id: null, blocks: [], frontmatter: {}, seo: {}, publish_at: null };
    }
    parents = all.filter((p) => !page.path || (p.id !== page.id && !p.path.startsWith(page.path + "/")));
    baseline = snapshot();
    loadedStatus = page.status;
    if (path && flash.published === path) { flash.published = null; justPublished = true; }
    if (!editing) {
      editing = new Editing({ type: "page", id: page.id, key: path ? String(page.id) : "new", snapshot, dirty: () => dirty, updatedAt: page.updated_at });
      editing.start();
    }
  }
  load().catch(toastError);

  function requestSave(statusOverride) {
    if (statusOverride === "published" && loadedStatus !== "published" && prefs.get("publish_checks", true)) { checklist = true; return; }
    submit(statusOverride);
  }
  async function submit(statusOverride) {
    if (!page) return;
    saving = true;
    const body = { page: { title: page.title, slug: page.slug, parent_id: page.parent_id || null, locale: page.locale, blocks: page.blocks, frontmatter: page.frontmatter, seo: page.seo, status: statusOverride || page.status } };
    if (page.publish_at) body.page.publish_at = page.publish_at;
    if (!path) body.page.status = statusOverride === "published" ? "published" : page.status;
    const r = await save(() => (path ? patch(`/pages/${pagePath(path)}`, body) : post("/pages", body)), "page");
    saving = false;
    errors = r.errors || {};
    if (r.ok && !r.held && !r.unchanged) {
      baseline = "";
      await editing?.saved(r.res.page.updated_at);
      if (loadedStatus !== "published" && r.res.page.status === "published") flash.published = r.res.page.path;
      if (r.res.page.path !== path) { go(`/pages/edit/${encodeURIComponent(r.res.page.path)}`); return; }
      await load();
    } else if (r.ok) { baseline = snapshot(); await editing?.saved(page.updated_at); }
  }
  async function trash() {
    try {
      await del(`/pages/${pagePath(path)}`); baseline = "";
      const back = restorer("page", [page.id], () => go(`/pages/edit/${encodeURIComponent(path)}`));
      undoable(`“${page.title}” moved to the trash.`, back, { done: "Restored." });
      go("/pages");
    } catch (e) { toastError(e); }
  }
  async function saveSchema() {
    try {
      const r = await patch(`/pages/${pagePath(path)}/schema`, { fields: $state.snapshot(draftSchema).map(({ __new, ...f }) => f) });
      schema = r.page.schema.fields; editingFields = false; toast("Fields saved.");
    } catch (e) { toastError(e); }
  }

  const stop = saveShortcut(() => submit(null));
  const warn = (e) => { if (dirty) { e.preventDefault(); e.returnValue = ""; } };
  addEventListener("beforeunload", warn);
  onDestroy(() => { stop(); removeEventListener("beforeunload", warn); editing?.stop(); });
  // Where it lives on the site: the parent's path plus the slug; "home" is "/".
  const permalink = $derived.by(() => {
    if (!page) return "";
    const p = [parents.find((x) => x.id === page.parent_id)?.path, page.slug].filter(Boolean).join("/");
    return (site.siteURL ? site.siteURL.replace(/\/$/, "") : "") + (p === "home" ? "/" : "/" + p);
  });
</script>

{#if !page}
  <div class="flex justify-center py-16"><Spinner /></div>
{:else}
  <EditorTabs bind:tab cli={path ? `librepublish get_page --path ${path}` : ""} seoWarning={!page.seo?.meta_description}>
    {#snippet actions()}
      {#if dirty}<span class="text-xs font-medium text-amber-700 dark:text-amber-400">Unsaved changes · ⌘S{#if editing?.autosavedAt} · autosaved{/if}</span>{/if}
      {#if path && session.can("pages:write")}
        <Button href="#/pages/new" icon="add">Add New</Button>
        <Button onclick={() => { draftSchema = JSON.parse(JSON.stringify(schema)); editingFields = true; }}>Fields</Button>
      {/if}
    {/snippet}
  </EditorTabs>
  {#if editing}<EditingNotices {editing} noun="page" back="#/pages" onrestore={(data) => (page = { ...page, ...data })} />{/if}
  <div class="flex flex-col gap-6 lg:flex-row lg:items-start">
    <div class="flex min-w-0 flex-1 flex-col gap-6">
      <div>
        <label class="sr-only" for="page-title">Title</label>
        <!-- svelte-ignore a11y_autofocus -->
        <input id="page-title" bind:value={page.title} placeholder="Add title" autofocus={!path}
          oninput={() => { if (!slugTouched) page.slug = slugify(page.title); }}
          class="w-full border-0 border-b border-input bg-transparent pb-3 text-3xl font-semibold leading-9 tracking-tight text-foreground outline-none placeholder:text-muted-foreground/60 focus:border-ring {errors.title ? 'border-destructive' : ''}" />
        {#if errors.title}<p class="mt-1 text-xs text-destructive">Title {errors.title.join(", ")}</p>{/if}
      </div>

      {#if tab === "edit"}
        {#if schema.length}
          <Postbox key="page-fields" title="Fields"><FieldsForm fields={schema} data={page.frontmatter} path="frontmatter" {errors} /></Postbox>
        {/if}
        <Panel title="Blocks">
          <div class="p-3 sm:p-5"><BlocksEditor blocks={page.blocks} {errors} /></div>
          {#snippet footer()}
            {#if path}Last edited <Time value={page.updated_at} />{:else}Not saved yet{/if}
          {/snippet}
        </Panel>
      {:else}
        <Panel title="SEO"><div class="p-5"><SeoFields seo={page.seo} {errors} title={page.title} url={permalink} /></div></Panel>
      {/if}
    </div>

    <aside class="flex min-w-0 shrink-0 flex-col gap-4 lg:sticky lg:top-16 lg:w-80">
      <PublishBox record={page} isNew={!path} canPublish={session.can("pages:publish")} {saving} onsave={requestSave} ontrash={path && session.can("pages:delete") ? trash : null} />
      <Postbox key="page-attributes" title="Page Attributes">
        <Field label="Slug" required help="Lowercase letters, digits and dashes." error={errors.slug}>
          {#snippet children(id, invalid)}<Input {id} {invalid} mono bind:value={page.slug} oninput={() => (slugTouched = true)} />{/snippet}
        </Field>
        {#if page.slug}
          <div class="flex flex-col gap-1.5">
            <span class="text-sm font-semibold text-foreground">Permalink</span>
            {#if site.siteURL}<a href={permalink} target="_blank" rel="noopener" class="truncate text-sm font-medium text-primary underline decoration-primary/40 underline-offset-2 hover:text-primary">{permalink}</a>
            {:else}<span class="font-mono text-xs text-muted-foreground">{permalink}</span>{/if}
          </div>
        {/if}
        <Field label="Parent" help="Nests this page under another; its URL becomes the parent's path plus its slug." error={errors.parent_id}>
          {#snippet children(id)}
            <Combobox {id} placeholder="No parent (top level)" options={parents.map((p) => ({ value: p.id, label: p.title, hint: "/" + p.path }))} bind:value={page.parent_id} />
          {/snippet}
        </Field>
        <Field label="Locale" error={errors.locale}>{#snippet children(id, invalid)}<Input {id} {invalid} bind:value={page.locale} />{/snippet}</Field>
        {#if path}
          <div class="flex items-center justify-between gap-2 rounded-md border border-border bg-muted/50 px-3 py-2">
            <span class="min-w-0"><span class="block text-xs text-muted-foreground">Reference</span><span class="block truncate font-mono text-xs text-foreground">/{page.path}</span></span>
            <CopyButton text={"/" + page.path} label="" />
          </div>
        {/if}
      </Postbox>
    </aside>
  </div>

  <PublishChecklist bind:open={checklist} bind:published={justPublished} record={page} noun="page" url={permalink} addHref="#/pages/new"
    hasContent={page.blocks.length > 0 || Object.keys(page.frontmatter || {}).length > 0} onpublish={() => submit("published")} />
  <Dialog bind:open={editingFields} title="This page's fields" size="lg" sheet>
    <p class="text-sm text-muted-foreground">Fields beyond the blocks, filled in above them and handed to the site as the page's <code class="rounded-sm bg-muted px-1 font-mono text-xs">frontmatter</code>.</p>
    <SchemaEditor fields={draftSchema} />
    {#snippet actions()}<Button onclick={() => (editingFields = false)}>Cancel</Button><Button variant="primary" onclick={saveSchema}>Save fields</Button>{/snippet}
  </Dialog>
{/if}
