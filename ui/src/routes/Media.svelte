<script>
  // The media library: a grid of files with filters (all, images,
  // documents, unused), search, uploading by button or by dropping files
  // anywhere on it, and a side sheet per file: preview, alt text, where
  // it's used.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import StatusLinks from "../lib/components/StatusLinks.svelte";
  import SearchBox from "../lib/components/SearchBox.svelte";
  import Button from "../lib/components/Button.svelte";
  import Icon from "../lib/components/Icon.svelte";
  import Dialog from "../lib/components/Dialog.svelte";
  import Field from "../lib/components/Field.svelte";
  import Input from "../lib/components/Input.svelte";
  import Textarea from "../lib/components/Textarea.svelte";
  import Badge from "../lib/components/Badge.svelte";
  import CopyButton from "../lib/components/CopyButton.svelte";
  import Pagination from "../lib/components/Pagination.svelte";
  import EmptyState from "../lib/components/EmptyState.svelte";
  import Spinner from "../lib/components/Spinner.svelte";
  import { get, post, patch, del } from "../lib/api.js";
  import { router } from "../lib/router.svelte.js";
  import { session } from "../lib/session.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toastError, undoable } from "../lib/toast.svelte.js";
  import { restorer } from "../lib/trash.js";
  import { bytes } from "../lib/format.js";

  const filter = router.query.get("show") || "", q = router.query.get("s") || "", page = Number(router.query.get("page") || 1);
  let list = $state(null), uploading = $state(0), dragging = $state(false), current = $state(null), open = $state(false), fileInput = $state();
  async function load() {
    const p = new URLSearchParams({ per: "60", page: String(page) });
    if (q) p.set("q", q);
    if (filter === "images") p.set("type", "image/");
    if (filter === "documents") p.set("type", "application/");
    if (filter === "unused") p.set("unused", "1");
    list = await get("/assets?" + p);
    const want = router.query.get("asset");
    if (want && !current) show({ id: want });
  }
  load().catch(toastError);
  async function upload(files) {
    if (!session.can("assets:write")) return;
    for (const f of files) {
      uploading++;
      const fd = new FormData(); fd.append("file", f);
      try { await post("/assets", fd); } catch (e) { toastError(e); }
      uploading--;
    }
    toast(`Uploaded ${files.length} ${files.length === 1 ? "file" : "files"}.`);
    load();
  }
  async function show(a) {
    try { current = (await get(`/assets/${a.id}`)).asset; open = true; } catch (e) { toastError(e); }
  }
  async function saveAsset() {
    try { await patch(`/assets/${current.id}`, { asset: { alt: current.alt, name: current.name, caption: current.caption, folder: current.folder } }); toast("Saved."); load(); } catch (e) { toastError(e); }
  }
  // A file nothing uses goes straight to the trash, with Undo; one in use
  // asks first, saying what would show nothing.
  async function remove() {
    const used = current.used_by.length;
    if (used && !(await confirm({ title: `Move ${current.filename} to the trash?`, message: `${used} ${used === 1 ? "record still uses it" : "records still use it"}: they'd show nothing where it was until it's restored.`, confirmLabel: "Move to trash", destructive: true }))) return;
    const { id, filename } = current;
    try { await del(`/assets/${id}`); open = false; undoable(`${filename} moved to the trash.`, restorer("asset", [id], load), { done: "Restored." }); load(); } catch (e) { toastError(e); }
  }
</script>

<svelte:window ondragover={(e) => { if (e.dataTransfer?.types.includes("Files")) { e.preventDefault(); dragging = true; } }} ondragleave={(e) => { if (!e.relatedTarget) dragging = false; }} ondrop={(e) => { e.preventDefault(); dragging = false; if (e.dataTransfer.files.length) upload([...e.dataTransfer.files]); }} />

<PageHeader title="Media" cli="librepublish list_assets">
  {#snippet actions()}
    {#if session.can("assets:write")}
      <input type="file" multiple class="hidden" bind:this={fileInput} onchange={(e) => { upload([...e.currentTarget.files]); e.currentTarget.value = ""; }} />
      <Button icon="add" loading={uploading > 0} onclick={() => fileInput.click()}>Upload files</Button>
    {/if}
  {/snippet}
</PageHeader>

<div class="flex flex-wrap items-center justify-between gap-3">
  <StatusLinks param="show" options={[{ label: "All", value: "", count: filter ? null : list?.pagination.total }, { label: "Images", value: "images" }, { label: "Documents", value: "documents" }, { label: "Unused", value: "unused" }]} />
  <SearchBox placeholder="Search files…" />
</div>

{#if dragging}
  <div class="pointer-events-none fixed inset-0 z-40 flex items-center justify-center bg-primary/5 text-lg font-semibold text-primary backdrop-blur-sm">Drop to upload</div>
{/if}

{#if !list}
  <div class="flex justify-center py-16"><Spinner /></div>
{:else if !list.assets.length}
  <div class="rounded-xl border border-dashed border-input bg-card">
    <EmptyState icon="picture-add" title={q || filter ? "Nothing matches" : "The library is empty"}>{q || filter ? "Try another filter." : "Drop files anywhere on this page, or upload them."}</EmptyState>
  </div>
{:else}
  <ul class="grid grid-cols-2 gap-x-3 gap-y-4 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-6">
    {#each list.assets as a (a.id)}
      <li class="min-w-0">
        <button type="button" class="group flex w-full flex-col gap-1.5 text-left" onclick={() => show(a)}>
          <span class="relative flex aspect-square w-full items-center justify-center overflow-hidden rounded-lg border border-border bg-muted/50 text-muted-foreground group-hover:border-ring group-focus-visible:border-ring">
            {#if a.thumb_url}<img src={a.thumb_url} alt={a.alt || ""} loading="lazy" class="size-full object-cover" />
            {:else}<span class="flex flex-col items-center gap-1"><Icon name="document" size="xl" /><span class="text-[10px] font-semibold uppercase">{a.filename.split(".").pop()}</span></span>{/if}
            {#if !a.usage_count}<span class="absolute left-1.5 top-1.5"><Badge tone="warning">Unused</Badge></span>{/if}
          </span>
          <span class="truncate px-0.5 text-xs font-medium text-foreground/80">{a.filename}</span>
        </button>
      </li>
    {/each}
  </ul>
  <Pagination {page} per={list.pagination.per} total={list.pagination.total} />
{/if}

<Dialog bind:open title={current?.filename || ""} sheet size="lg">
  {#if current}
    <div class="flex items-center justify-center overflow-hidden rounded-lg border border-border bg-[repeating-conic-gradient(var(--muted)_0_25%,var(--background)_0_50%)] bg-[length:16px_16px]">
      {#if current.thumb_url}<img src={current.variants?.w1280 || current.url} alt={current.alt || ""} class="max-h-80 object-contain" />{:else}<Icon name="document" size="xl" class="my-12 text-muted-foreground" />{/if}
    </div>
    <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
      <dt class="text-muted-foreground">Type</dt><dd>{current.content_type}</dd>
      <dt class="text-muted-foreground">Size</dt><dd>{bytes(current.byte_size)}{current.width ? ` · ${current.width} × ${current.height}` : ""}</dd>
      <dt class="text-muted-foreground">Address</dt><dd class="flex items-center gap-1"><span class="truncate font-mono text-xs">{current.url}</span><CopyButton text={current.url} /></dd>
    </dl>
    <div class="flex flex-col gap-4">
      {#if current.content_type?.startsWith("image/")}
        <Field label="Alt text" help="What the image shows, for people who can't see it.">{#snippet children(id)}<Textarea {id} rows={2} bind:value={current.alt} />{/snippet}</Field>
      {/if}
      <Field label="Name">{#snippet children(id)}<Input {id} bind:value={current.name} />{/snippet}</Field>
      <Field label="Caption">{#snippet children(id)}<Input {id} bind:value={current.caption} />{/snippet}</Field>
      <Field label="Folder">{#snippet children(id)}<Input {id} mono bind:value={current.folder} />{/snippet}</Field>
    </div>
    <section class="flex flex-col gap-2">
      <h3 class="text-sm font-semibold text-foreground">Used by</h3>
      {#if current.used_by.length}
        <ul class="divide-y divide-border rounded-lg border border-border">
          {#each current.used_by as u}
            <li class="flex items-center justify-between gap-3 px-3 py-2 text-sm">
              <a class="min-w-0 truncate font-medium text-foreground hover:underline" href={u.type === "Page" ? `#/pages/edit/${encodeURIComponent(u.path)}` : u.type === "Global" ? `#/globals/${u.path}` : u.type === "Setting" ? `#/settings/${u.path}` : `#/collections/${u.path.split("/")[0]}/edit/${u.path.split("/")[1]}`}>{u.label}</a>
              <span class="shrink-0 text-xs text-muted-foreground">{u.type === "Setting" ? u.where : `${u.type === "CollectionEntry" ? "Entry" : u.type} · ${u.where}`}</span>
            </li>
          {/each}
        </ul>
      {:else}<p class="text-sm text-muted-foreground">Nothing uses this file.</p>{/if}
    </section>
  {/if}
  {#snippet actions()}
    {#if session.can("assets:delete")}<Button variant="danger" icon="trash" onclick={remove} class="mr-auto">Delete</Button>{/if}
    <Button onclick={() => (open = false)}>Close</Button>
    {#if session.can("assets:write")}<Button variant="primary" onclick={saveAsset}>Save</Button>{/if}
  {/snippet}
</Dialog>
