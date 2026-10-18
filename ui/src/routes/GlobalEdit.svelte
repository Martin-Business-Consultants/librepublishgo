<script>
  // A global: its data through the form its fields draw, saved as a whole.
  // Globals are always live, so without globals:publish a save is proposed
  // for review. Its fields are edited in a side sheet.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Postbox from "../lib/components/Postbox.svelte";
  import Field from "../lib/components/Field.svelte";
  import Input from "../lib/components/Input.svelte";
  import Button from "../lib/components/Button.svelte";
  import Dialog from "../lib/components/Dialog.svelte";
  import Notice from "../lib/components/Notice.svelte";
  import Spinner from "../lib/components/Spinner.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import FieldsForm from "../lib/fields/FieldsForm.svelte";
  import SchemaEditor from "../lib/fields/SchemaEditor.svelte";
  import { get, post, patch, del } from "../lib/api.js";
  import { save, saveShortcut } from "../lib/save.js";
  import { session } from "../lib/session.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toastError, undoable } from "../lib/toast.svelte.js";
  import { restorer } from "../lib/trash.js";
  import { slugify } from "../lib/format.js";
  import { go } from "../lib/router.svelte.js";
  import { onDestroy } from "svelte";
  import EditingNotices from "../lib/components/EditingNotices.svelte";
  import { Editing } from "../lib/editing.svelte.js";

  let { params } = $props();
  // svelte-ignore state_referenced_locally
  const slug = params.slug === "new" ? null : params.slug;
  let editing = $state(null);
  let g = $state(null), fields = $state([]), errors = $state({}), saving = $state(false), editingFields = $state(!slug), draft = $state([]);
  async function load() {
    if (slug) {
      const r = (await get(`/globals/${slug}`)).global; g = r; fields = r.fields || [];
      // Globals are saved whole, on purpose: locked, but not autosaved.
      if (!editing) { editing = new Editing({ type: "global", id: r.id, snapshot: () => "", dirty: () => false, updatedAt: r.updated_at }); editing.start(); }
    }
    else g = { name: "", slug: "", description: "", data: {} };
    draft = JSON.parse(JSON.stringify(fields));
  }
  load().catch(toastError);
  async function submit(withFields = false) {
    saving = true;
    const body = { global: { name: g.name, slug: g.slug, description: g.description, data: g.data } };
    if (withFields || !slug) body.global.schema = { fields: $state.snapshot(draft).map(({ __new, ...f }) => f) };
    const r = await save(() => (slug ? patch(`/globals/${slug}`, body) : post("/globals", body)), "global");
    saving = false; errors = r.errors || {};
    if (r.ok && !r.held) { editingFields = false; if (r.res.global && r.res.global.slug !== slug) go(`/globals/${r.res.global.slug}`); else load(); }
  }
  async function remove() {
    try {
      await del(`/globals/${slug}`);
      undoable(`${g.name} moved to the trash: the site finds nothing there until it's restored.`, restorer("global", [g.id], () => go(`/globals/${slug}`)), { done: "Restored." });
      go("/globals");
    } catch (e) { toastError(e); }
  }
  const stop = saveShortcut(() => submit(false));
  onDestroy(() => { stop(); editing?.stop(); });
</script>

{#if !g}
  <div class="flex justify-center py-16"><Spinner /></div>
{:else}
  <PageHeader title={slug ? g.name : "New global"} icon="world" cli={slug ? `librepublish get_global --slug ${slug}` : ""}>
    {#snippet actions()}
      {#if slug && session.can("globals:write")}<Button icon="sliders" onclick={() => { draft = JSON.parse(JSON.stringify(fields)); editingFields = true; }}>Edit fields</Button>{/if}
      {#if slug}<Button variant="primary" loading={saving} onclick={() => submit(false)}>{session.can("globals:publish") ? "Update" : "Propose changes"}</Button>{/if}
    {/snippet}
  </PageHeader>
  {#if editing}<EditingNotices {editing} noun="global" back="#/globals" onrestore={() => {}} />{/if}
  {#if slug && !session.can("globals:publish")}
    <Notice tone="warning">Globals are live as soon as they change, and your role can't publish: saving sends your changes to review.</Notice>
  {/if}
  <div class="flex flex-col gap-6 lg:flex-row lg:items-start">
    <div class="flex min-w-0 flex-1 flex-col gap-6">
      {#if slug}
        <Panel title="Content">
          <div class="p-5">
            {#if fields.length}<FieldsForm {fields} data={g.data} path="data" {errors} />
            {:else}<EmptyState icon="sliders" title="No fields yet">Define what this global holds, and its form appears here.
              {#snippet actions()}<Button onclick={() => (editingFields = true)}>Add fields</Button>{/snippet}</EmptyState>{/if}
          </div>
        </Panel>
      {:else}
        <Panel title="Fields"><div class="p-5"><SchemaEditor fields={draft} {errors} path="schema.fields" /></div></Panel>
      {/if}
    </div>
    <aside class="flex min-w-0 shrink-0 flex-col gap-4 lg:w-80">
      <Postbox key="global-details" title="Details">
        <Field label="Name" required error={errors.name}>{#snippet children(id, invalid)}<Input {id} {invalid} bind:value={g.name} oninput={() => { if (!slug) g.slug = slugify(g.name).replace(/-/g, "_"); }} />{/snippet}</Field>
        <Field label="Slug" help="What the site's code reads it by; renaming it breaks that." error={errors.slug}>{#snippet children(id, invalid)}<Input {id} {invalid} mono bind:value={g.slug} />{/snippet}</Field>
        <Field label="Description" error={errors.description}>{#snippet children(id)}<Input {id} bind:value={g.description} />{/snippet}</Field>
        {#if !slug}<Button variant="primary" loading={saving} onclick={() => submit(true)}>Create global</Button>{/if}
      </Postbox>
      {#if slug && session.can("globals:delete")}<div><Button variant="danger" size="sm" icon="trash" onclick={remove}>Move to trash</Button></div>{/if}
    </aside>
  </div>
  {#if slug}
    <Dialog bind:open={editingFields} title="{g.name}: fields" size="lg" sheet>
      <SchemaEditor fields={draft} {errors} path="schema.fields" />
      {#snippet actions()}<Button onclick={() => (editingFields = false)}>Cancel</Button><Button variant="primary" loading={saving} onclick={() => submit(true)}>Save fields</Button>{/snippet}
    </Dialog>
  {/if}
{/if}
