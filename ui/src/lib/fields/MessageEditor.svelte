<script>
  // An email message: rich text (TiptapEditor, kept as HTML) with its merge
  // tags in a sidebar on the right, to drag into the message (they drop as
  // text, where they land) or click in at the cursor. {{ tags }} in the
  // message show as chips.
  //   tags: [{ name, label, hint?, group? }], each group under its heading
  import TiptapEditor from "../components/TiptapEditor.svelte";
  let { value = $bindable(""), tags = [], id = undefined, invalid = false, placeholder = "" } = $props();
  let editor = $state();
  const text = (t) => `{{ ${t.name} }}`;
</script>

<div class="grid gap-3 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
  <TiptapEditor bind:this={editor} bind:value format="html" tools="email" mergeTags {id} {invalid} {placeholder} minHeight="min-h-56" />
  <aside class="flex flex-col gap-2 rounded-md border p-3 md:self-start" aria-label="Merge tags">
    <p class="text-sm font-medium">Insert a value</p>
    <p class="text-muted-foreground text-xs">Drag one into the message, or click to add it at the cursor.</p>
    <ul class="flex flex-wrap gap-1.5 md:flex-col">
      {#each tags as t, i (t.name)}
        {#if t.group && t.group !== tags[i - 1]?.group}<li class="text-muted-foreground mt-1 w-full text-[10px] font-semibold uppercase tracking-wide first:mt-0">{t.group}</li>{/if}
        <li>
          <button type="button" draggable="true" title={t.hint || text(t)}
            ondragstart={(e) => { e.dataTransfer.setData("application/x-lp-merge-tag", text(t)); e.dataTransfer.setData("text/plain", text(t)); e.dataTransfer.effectAllowed = "copy"; }}
            onclick={() => editor.insert(text(t) + " ")}
            class="merge-tag hover:bg-primary/15 flex w-full cursor-grab items-center justify-between gap-2 text-left active:cursor-grabbing">
            <span class="truncate">{t.label}</span>
            <span class="text-muted-foreground shrink-0 font-mono text-[10px]">{t.name}</span>
          </button>
        </li>
      {/each}
    </ul>
  </aside>
</div>
