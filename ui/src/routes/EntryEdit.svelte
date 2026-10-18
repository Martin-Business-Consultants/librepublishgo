<script>
  // An entry's editor: title, its collection's fields, the body, blocks when
  // the collection has them, SEO; the Publish box and attributes aside.
  import Field from "../lib/components/Field.svelte";
  import Input from "../lib/components/Input.svelte";
  import Combobox from "../lib/components/Combobox.svelte";
  import Postbox from "../lib/components/Postbox.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Spinner from "../lib/components/Spinner.svelte";
  import MarkdownEditor from "../lib/components/MarkdownEditor.svelte";
  import FieldsForm from "../lib/fields/FieldsForm.svelte";
  import BlocksEditor from "../lib/fields/BlocksEditor.svelte";
  import PublishBox from "../lib/fields/PublishBox.svelte";
  import PublishChecklist from "../lib/fields/PublishChecklist.svelte";
  import { prefs } from "../lib/prefs.svelte.js";
  import { flash } from "../lib/flash.js";
  import SeoFields from "../lib/fields/SeoFields.svelte";
  import EditorTabs from "../lib/components/EditorTabs.svelte";
  import EditingNotices from "../lib/components/EditingNotices.svelte";
  import { Editing } from "../lib/editing.svelte.js";
  import Button from "../lib/components/Button.svelte";
  import { get, post, patch, del } from "../lib/api.js";
  import { save, saveShortcut } from "../lib/save.js";
  import { session } from "../lib/session.svelte.js";
  import { site } from "../lib/site.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toastError, undoable } from "../lib/toast.svelte.js";
  import { restorer } from "../lib/trash.js";
  import { slugify } from "../lib/format.js";
  import { go } from "../lib/router.svelte.js";
  import { onDestroy } from "svelte";

  let { params } = $props();
  // svelte-ignore state_referenced_locally
  const colSlug = params.slug, slug = params.entry || null;
  let col = $state(null), entry = $state(null), errors = $state({}), saving = $state(false), slugTouched = $state(!!slug), baseline = $state("");
  let categories = $state([]), tags = $state([]), tab = $state("edit");
  let editing = $state(null);
  let checklist = $state(false), justPublished = $state(false), loadedStatus = "";
  const snapshot = () => JSON.stringify($state.snapshot(entry));
  const dirty = $derived(entry && baseline && snapshot() !== baseline);
  const base = `/collections/${colSlug}/entries`;

  async function load() {
    col = (await get(`/collections/${colSlug}`)).collection;
    if (slug) {
      const e = (await get(`${base}/${encodeURIComponent(slug)}`)).entry;
      entry = { ...e, category_entry_id: e.category?.id ?? null, tag_ids: (e.tags || []).map((t) => t.id), publish_at: null };
    } else entry = { title: "", slug: "", status: "draft", locale: "en", frontmatter: {}, seo: {}, blocks: [], body_markdown: "", category_entry_id: null, tag_ids: [], publish_at: null };
    const cols = site.collections.length ? site.collections : (await get("/collections")).collections;
    const pool = (id) => { const p = cols.find((x) => x.id === id); return p ? get(`/collections/${p.slug}/entries?per=100`).then((r) => r.entries.map((x) => ({ value: x.id, label: x.title }))) : []; };
    [categories, tags] = await Promise.all([pool(col.categories_collection_id), pool(col.tags_collection_id)]);
    baseline = snapshot();
    loadedStatus = entry.status;
    if (slug && flash.published === colSlug + "/" + slug) { flash.published = null; justPublished = true; }
    if (!editing) {
      editing = new Editing({ type: "entry", id: entry.id, key: slug ? String(entry.id) : "new:" + colSlug, snapshot, dirty: () => dirty, updatedAt: entry.updated_at });
      editing.start();
    }
  }
  load().catch(toastError);

  function requestSave(statusOverride) {
    if (statusOverride === "published" && loadedStatus !== "published" && prefs.get("publish_checks", true)) { checklist = true; return; }
    submit(statusOverride);
  }
  async function submit(statusOverride) {
    saving = true;
    const body = { entry: { title: entry.title, slug: entry.slug, locale: entry.locale, body_markdown: entry.body_markdown, frontmatter: entry.frontmatter, seo: entry.seo, blocks: entry.blocks, status: statusOverride || entry.status } };
    if (col.categories_collection_id) body.entry.category_entry_id = entry.category_entry_id || null;
    if (col.tags_collection_id) body.entry.tag_ids = entry.tag_ids;
    if (entry.publish_at) body.entry.publish_at = entry.publish_at;
    const r = await save(() => (slug ? patch(`${base}/${encodeURIComponent(slug)}`, body) : post(base, body)), "entry");
    saving = false; errors = r.errors || {};
    if (r.ok && !r.held && !r.unchanged) {
      baseline = "";
      await editing?.saved(r.res.entry.updated_at);
      if (loadedStatus !== "published" && r.res.entry.status === "published") flash.published = colSlug + "/" + r.res.entry.slug;
      if (r.res.entry.slug !== slug) { go(`/collections/${colSlug}/edit/${encodeURIComponent(r.res.entry.slug)}`); return; }
      await load();
    } else if (r.ok) { baseline = snapshot(); await editing?.saved(entry.updated_at); }
  }
  async function trash() {
    try {
      await del(`${base}/${encodeURIComponent(slug)}`); baseline = "";
      undoable(`“${entry.title}” moved to the trash.`, restorer("entry", [entry.id], () => go(`/collections/${colSlug}/edit/${encodeURIComponent(slug)}`)), { done: "Restored." });
      go(`/collections/${colSlug}`);
    } catch (e) { toastError(e); }
  }
  const stop = saveShortcut(() => submit(null));
  const warn = (e) => { if (dirty) { e.preventDefault(); e.returnValue = ""; } };
  addEventListener("beforeunload", warn);
  onDestroy(() => { stop(); removeEventListener("beforeunload", warn); editing?.stop(); });
  const url = $derived(entry ? `${site.siteURL.replace(/\/$/, "")}/${colSlug}/${entry.slug}` : "");
</script>

{#if !entry}
  <div class="flex justify-center py-16"><Spinner /></div>
{:else}
  <EditorTabs bind:tab cli={slug ? `librepublish get_entry --slug ${colSlug} --entry ${slug}` : ""} seoWarning={!entry.seo?.meta_description}>
    {#snippet actions()}
      {#if dirty}<span class="text-xs font-medium text-amber-700 dark:text-amber-400">Unsaved changes · ⌘S{#if editing?.autosavedAt} · autosaved{/if}</span>{/if}
      <Button href="#/collections/{colSlug}">All {col.name.toLowerCase()}</Button>
      {#if slug && session.can("entries:write")}<Button href="#/collections/{colSlug}/new" icon="add">Add New</Button>{/if}
    {/snippet}
  </EditorTabs>
  {#if editing}<EditingNotices {editing} noun="entry" back="#/collections/{colSlug}" onrestore={(data) => (entry = { ...entry, ...data })} />{/if}
  <div class="flex flex-col gap-6 lg:flex-row lg:items-start">
    <div class="flex min-w-0 flex-1 flex-col gap-6">
      <div>
        <label class="sr-only" for="entry-title">Title</label>
        <!-- svelte-ignore a11y_autofocus -->
        <input id="entry-title" bind:value={entry.title} placeholder="Add title" autofocus={!slug} oninput={() => { if (!slugTouched) entry.slug = slugify(entry.title); }}
          class="w-full border-0 border-b border-input bg-transparent pb-3 text-3xl font-semibold leading-9 tracking-tight text-foreground outline-none placeholder:text-muted-foreground/60 focus:border-ring" />
        {#if errors.title}<p class="mt-1 text-xs text-destructive">Title {errors.title.join(", ")}</p>{/if}
      </div>
      {#if tab === "edit"}
        {#if (col.schema?.fields || []).length}
          <Postbox key="entry-fields-{colSlug}" title="Fields"><FieldsForm fields={col.schema.fields} data={entry.frontmatter} path="frontmatter" {errors} /></Postbox>
        {/if}
        <Panel title="Body">
          <div class="p-5"><MarkdownEditor bind:value={entry.body_markdown} rows={12} placeholder="Write in Markdown…" /></div>
        </Panel>
        {#if col.enable_blocks}
          <Panel title="Blocks"><div class="p-3 sm:p-5"><BlocksEditor blocks={entry.blocks} {errors} /></div></Panel>
        {/if}
      {:else}
        <Panel title="SEO"><div class="p-5"><SeoFields seo={entry.seo} {errors} title={entry.title} {url} /></div></Panel>
      {/if}
    </div>
    <aside class="flex min-w-0 shrink-0 flex-col gap-4 lg:sticky lg:top-16 lg:w-80">
      <PublishBox record={entry} isNew={!slug} canPublish={session.can("entries:publish")} {saving} onsave={requestSave} ontrash={slug && session.can("entries:delete") ? trash : null} />
      <Postbox key="entry-attributes" title="Attributes">
        <Field label="Slug" required error={errors.slug}>{#snippet children(id, invalid)}<Input {id} {invalid} mono bind:value={entry.slug} oninput={() => (slugTouched = true)} />{/snippet}</Field>
        <Field label="Locale" error={errors.locale}>{#snippet children(id, invalid)}<Input {id} {invalid} class="max-w-24" bind:value={entry.locale} />{/snippet}</Field>
      </Postbox>
      {#if col.categories_collection_id || col.tags_collection_id}
        <Postbox key="entry-taxonomy" title="Categories & Tags">
          {#if col.categories_collection_id}<Field label="Category" error={errors.category_entry_id}>{#snippet children(id)}<Combobox {id} placeholder="No category" options={categories} bind:value={entry.category_entry_id} />{/snippet}</Field>{/if}
          {#if col.tags_collection_id}<Field label="Tags" error={errors.tag_ids}>{#snippet children(id)}<Combobox {id} multiple placeholder="Add tags…" options={tags} bind:value={entry.tag_ids} />{/snippet}</Field>{/if}
        </Postbox>
      {/if}
    </aside>
  </div>
  <PublishChecklist bind:open={checklist} bind:published={justPublished} record={entry} noun="entry" {url} addHref="#/collections/{colSlug}/new"
    hasContent={!!entry.body_markdown?.trim() || entry.blocks.length > 0 || Object.keys(entry.frontmatter || {}).length > 0} onpublish={() => submit("published")} />
{/if}
