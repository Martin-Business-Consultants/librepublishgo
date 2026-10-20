<script>
  // The visual form builder (a plugin's "form_builder" field): the field
  // types down the left, to drag (or click) onto the form; the form as it
  // will look, its fields dragged to reorder and clicked to edit; and the
  // chosen field's settings on the right. The types come from the plugin
  // (palette: [{type, label, icon, input, props}]), so the builder knows
  // nothing about any one plugin.
  import Sortable from "sortablejs";
  import Icon from "../components/Icon.svelte";
  import Button from "../components/Button.svelte";
  import Input from "../components/Input.svelte";
  import Textarea from "../components/Textarea.svelte";
  import Switch from "../components/Switch.svelte";
  import Select from "../components/Select.svelte";
  import Checkbox from "../components/Checkbox.svelte";
  import Field from "../components/Field.svelte";
  import DragHandle from "../components/DragHandle.svelte";
  import * as ToggleGroup from "$lib/components/ui/toggle-group/index.js";
  import { move } from "../sortable.js";

  let { fields, palette = [], errors = {}, path = "fields" } = $props();

  const kind = (t) => palette.find((p) => p.type === t) || { type: t, label: t, input: "text", props: [] };
  const uid = () => "f" + Math.random().toString(36).slice(2, 8);
  // svelte-ignore state_referenced_locally
  for (const f of fields) {
    if (!f.id) f.id = uid();
    // A saved field's name is what the site posts: it doesn't follow the
    // label any more.
    f.__named = true;
  }
  // svelte-ignore state_referenced_locally
  let selected = $state(fields[0]?.id ?? null);
  const current = $derived(fields.find((f) => f.id === selected));
  const nameOf = (label) => label.toLowerCase().normalize("NFKD").replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "").replace(/^(\d)/, "f_$1");

  function add(type, at = null) {
    const k = kind(type);
    const f = { id: uid(), type, label: k.layout ? "" : k.label, name: "", required: false };
    if (!k.layout) f.name = uniqueName(nameOf(k.label));
    if (type === "heading") f.text = "A heading";
    if (type === "checkbox") f.text = "I agree to be contacted";
    if ((k.props || []).includes("options")) f.options = ["Option 1", "Option 2"];
    const i = at == null ? (current ? fields.indexOf(current) + 1 : fields.length) : at;
    fields.splice(i, 0, f);
    selected = f.id;
  }
  function uniqueName(base, self = null) {
    let n = base || "field", i = 2;
    while (fields.some((f) => f !== self && f.name === n)) n = `${base || "field"}_${i++}`;
    return n;
  }
  function duplicate(f) {
    const copy = { ...$state.snapshot(f), id: uid() };
    if (copy.name) copy.name = uniqueName(copy.name);
    fields.splice(fields.indexOf(f) + 1, 0, copy);
    selected = copy.id;
  }
  function remove(f) {
    const i = fields.indexOf(f);
    fields.splice(i, 1);
    selected = fields[Math.min(i, fields.length - 1)]?.id ?? null;
  }
  // A new field's name follows its label until someone edits the name.
  function setLabel(f, label) {
    f.label = label;
    if (!f.__named) f.name = uniqueName(nameOf(label), f);
  }
  const errorsAt = (i) => Object.entries(errors).filter(([k]) => k.startsWith(`${path}[${i}]`)).flatMap(([, v]) => v);
  const errorAt = (i, key) => errors[`${path}[${i}].${key}`];

  // Dragging a type from the palette is plain HTML drag and drop: the form
  // doesn't move while it's dragged, so where the pointer is says where it
  // goes, in reading order (the form is a grid of full- and half-width
  // fields), and a line shows it. Fields dragged within the form are
  // Sortable's, which shows them moving.
  const DND = "application/x-lp-field-type";
  let dropAt = $state(null);
  let grid = $state();
  function slotAt(x, y) {
    const cards = [...grid.children].filter((c) => c.matches("[data-sortable-item]"));
    return cards.filter((c) => {
      const r = c.getBoundingClientRect();
      return y > r.bottom || (y >= r.top && x > r.left + r.width / 2);
    }).length;
  }
  function over(e) {
    if (!e.dataTransfer?.types.includes(DND)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
    dropAt = slotAt(e.clientX, e.clientY);
  }
  function drop(e) {
    if (!e.dataTransfer?.types.includes(DND)) return;
    e.preventDefault();
    add(e.dataTransfer.getData(DND), slotAt(e.clientX, e.clientY));
    dropAt = null;
  }
  function leave(e) { if (!e.currentTarget.contains(e.relatedTarget)) dropAt = null; }
  function canvas(el) {
    const s = Sortable.create(el, {
      draggable: ">[data-sortable-item]",
      handle: "[data-drag-handle]",
      animation: 150,
      ghostClass: "opacity-40",
      onEnd({ item, oldDraggableIndex: from, newDraggableIndex: to }) {
        if (from === to) return;
        const others = [...el.children].filter((c) => c !== item && c.matches("[data-sortable-item]"));
        el.insertBefore(item, others[from] ?? others[others.length - 1]?.nextSibling ?? null);
        move(fields, from, to);
      },
    });
    return () => s.destroy();
  }
  const has = (f, prop) => (kind(f.type).props || []).includes(prop);

  // "Show only when": an earlier field's answer is one of some values (a
  // consent checkbox's is "yes"). Fields above it that take an answer can
  // control it.
  const controlsFor = (i) => fields.slice(0, i).filter((x) => !kind(x.type).layout && x.name);
  function setShowIf(f, field) {
    if (!field) { delete f.show_if; return; }
    const ctl = fields.find((x) => x.name === field);
    const keep = f.show_if?.field === field ? f.show_if.values : [];
    f.show_if = { field, values: ctl?.type === "checkbox" ? ["yes"] : keep };
  }
  function toggleShowValue(f, value, on) {
    const values = (f.show_if.values || []).filter((v) => v !== value);
    f.show_if = { ...f.show_if, values: on ? [...values, value] : values };
  }
  const showIfText = (f) => {
    const ctl = fields.find((x) => x.name === f.show_if?.field);
    const what = ctl?.type === "checkbox" ? "is ticked" : `is ${(f.show_if?.values || []).join(" or ") || "…"}`;
    return `Shown when ${ctl?.label || f.show_if?.field} ${what}`;
  };
</script>

<div class="grid gap-4 md:grid-cols-[10rem_minmax(0,1fr)] xl:grid-cols-[11rem_minmax(0,1fr)_18rem]">
  <!-- The palette -->
  <div class="flex flex-col gap-1">
    <p class="text-muted-foreground px-1 text-xs font-medium">Add a field</p>
    <div class="grid grid-cols-2 gap-1 lg:grid-cols-1">
      {#each palette as p (p.type)}
        <button type="button" data-type={p.type} onclick={() => add(p.type)} draggable="true"
          ondragstart={(e) => { e.dataTransfer.setData(DND, p.type); e.dataTransfer.effectAllowed = "copy"; }} ondragend={() => (dropAt = null)}
          class="hover:bg-muted hover:border-ring/50 flex cursor-grab items-center gap-2 rounded-md border bg-background px-2 py-1.5 text-left text-sm active:cursor-grabbing">
          <Icon name={p.icon} class="text-muted-foreground" /><span class="truncate">{p.label}</span>
        </button>
      {/each}
    </div>
  </div>

  <!-- The form, as it will look -->
  <div class="bg-muted/40 flex min-h-64 flex-col rounded-lg border border-dashed p-3 {dropAt != null ? 'border-ring bg-muted/70' : ''}"
    role="list" ondragover={over} ondrop={drop} ondragleave={leave}>
    <div class="grid grid-cols-2 content-start gap-2" bind:this={grid} {@attach canvas}>
      {#each fields as f, i (f.id)}
        {@const k = kind(f.type)}
        {@const bad = errorsAt(i)}
        <div data-sortable-item
          class="group bg-card relative flex gap-2 rounded-md border p-2.5 text-left transition-colors {dropAt === i ? 'shadow-[0_-4px_0_-1px_var(--ring)]' : ''} {dropAt === fields.length && i === fields.length - 1 ? 'shadow-[0_4px_0_-1px_var(--ring)]' : ''} {f.width === 'half' ? 'col-span-2 sm:col-span-1' : 'col-span-2'} {selected === f.id ? 'border-ring ring-ring/30 ring-2' : 'hover:border-ring/50'} {bad.length ? 'border-destructive/60' : ''}"
          role="button" tabindex="0" onclick={() => (selected = f.id)} onkeydown={(e) => { if (e.key === "Enter" && e.target === e.currentTarget) selected = f.id; }}>
          <DragHandle onmove={(d) => move(fields, i, i + d)} />
          <div class="pointer-events-none flex min-w-0 flex-1 flex-col gap-1.5">
            {#if k.input === "heading"}
              <p class="text-base font-semibold">{f.text || "Heading"}</p>
            {:else if k.input === "hidden"}
              <p class="text-muted-foreground flex items-center gap-1.5 text-xs"><Icon name="eye-off" size="xs" />Hidden: <span class="font-mono">{f.name}</span>{#if f.default} = {f.default}{/if}</p>
            {:else}
              {#if k.input !== "checkbox"}
                <p class="text-sm font-medium">{f.label || "Untitled"}{#if f.required}<span class="text-destructive"> *</span>{/if}</p>
              {/if}
              {#if k.input === "textarea"}
                <div class="border-input bg-background text-muted-foreground h-16 rounded-md border px-2.5 py-1.5 text-sm">{f.placeholder || ""}</div>
              {:else if k.input === "select"}
                <div class="border-input bg-background text-muted-foreground flex h-9 items-center justify-between rounded-md border px-2.5 text-sm">{f.placeholder || f.default || "Choose…"}<Icon name="caret-down" /></div>
              {:else if k.input === "radio" || k.input === "checkboxes"}
                <div class="flex flex-col gap-1">
                  {#each (f.options || []).slice(0, 4) as o}
                    <span class="flex items-center gap-2 text-sm"><span class="border-input size-3.5 border {k.input === 'radio' ? 'rounded-full' : 'rounded-[3px]'}"></span>{o}</span>
                  {/each}
                  {#if (f.options || []).length > 4}<span class="text-muted-foreground text-xs">+{f.options.length - 4} more</span>{/if}
                </div>
              {:else if k.input === "checkbox"}
                <span class="flex items-center gap-2 text-sm"><span class="border-input size-3.5 rounded-[3px] border"></span>{f.text || f.label}{#if f.required}<span class="text-destructive">*</span>{/if}</span>
              {:else if k.input === "file"}
                <div class="border-input bg-background text-muted-foreground flex h-9 items-center gap-2 rounded-md border border-dashed px-2.5 text-sm"><Icon name="upload" />Choose a file{#if f.accept}<span class="text-xs">({f.accept})</span>{/if}</div>
              {:else}
                <div class="border-input bg-background text-muted-foreground flex h-9 items-center rounded-md border px-2.5 text-sm">{f.placeholder || ""}</div>
              {/if}
              {#if f.help}<p class="text-muted-foreground text-xs">{f.help}</p>{/if}
            {/if}
            {#if f.show_if?.field}<p class="text-muted-foreground flex items-center gap-1 text-xs"><Icon name="eye-off" size="xs" />{showIfText(f)}</p>{/if}
            {#if bad.length}<p class="text-destructive text-xs">{bad.join(", ")}</p>{/if}
          </div>
          <span class="text-muted-foreground absolute right-1.5 top-1.5 hidden items-center gap-0.5 group-hover:flex {selected === f.id ? '!flex' : ''}">
            <Button variant="plain" size="sm" iconOnly icon="copy-paste" title="Duplicate" onclick={(e) => { e.stopPropagation(); duplicate(f); }} />
            <Button variant="plain" size="sm" iconOnly icon="trash" title="Remove" class="hover:bg-destructive/10 hover:text-destructive" onclick={(e) => { e.stopPropagation(); remove(f); }} />
          </span>
        </div>
      {/each}
    </div>
    {#if !fields.length}
      <p class="text-muted-foreground pointer-events-none py-12 text-center text-sm">Drag a field here, or click one on the left.</p>
    {/if}
    <div class="min-h-10 flex-1" aria-hidden="true"></div>
  </div>

  <!-- The chosen field's settings -->
  <div class="flex flex-col gap-4 rounded-lg border p-4 md:col-span-2 xl:sticky xl:top-16 xl:col-span-1 xl:self-start">
    {#if current}
      {@const i = fields.indexOf(current)}
      {@const k = kind(current.type)}
      <p class="flex items-center gap-2 text-sm font-semibold"><Icon name={k.icon} class="text-muted-foreground" />{k.label}</p>
      {#if !k.layout}
        {#if k.input !== "hidden"}
          <Field label="Label" error={errorAt(i, "label")}>{#snippet children(id, invalid)}<Input {id} {invalid} value={current.label || ""} oninput={(e) => setLabel(current, e.currentTarget.value)} />{/snippet}</Field>
        {/if}
        <Field label="Name" help={"What the site posts it as, and its {{merge tag}}."} error={errorAt(i, "name")}>{#snippet children(id, invalid)}<Input {id} {invalid} mono bind:value={current.name} oninput={() => (current.__named = true)} />{/snippet}</Field>
        {#if k.input !== "hidden"}
          <Switch bind:checked={current.required} label="Required" />
          <div class="flex flex-col gap-1.5">
            <span class="text-sm font-medium">Width</span>
            <ToggleGroup.Root type="single" variant="outline" size="sm" value={current.width === "half" ? "half" : "full"} onValueChange={(v) => { if (v) current.width = v === "half" ? "half" : ""; }}>
              <ToggleGroup.Item value="full">Full</ToggleGroup.Item>
              <ToggleGroup.Item value="half">Half</ToggleGroup.Item>
            </ToggleGroup.Root>
          </div>
        {/if}
      {/if}
      {#if has(current, "text")}
        <Field label={k.input === "heading" ? "Text" : "Checkbox text"}>{#snippet children(id)}<Textarea {id} rows={2} bind:value={current.text} />{/snippet}</Field>
      {/if}
      {#if has(current, "options")}
        <Field label="Choices" help="One per line." error={errorAt(i, "options")}>
          {#snippet children(id, invalid)}<Textarea {id} {invalid} rows={4} value={(current.options || []).join("\n")} oninput={(e) => (current.options = e.currentTarget.value.split("\n"))} />{/snippet}
        </Field>
      {/if}
      {#if has(current, "placeholder")}
        <Field label="Placeholder">{#snippet children(id)}<Input {id} bind:value={() => current.placeholder || "", (v) => (current.placeholder = v)} />{/snippet}</Field>
      {/if}
      {#if has(current, "default")}
        <Field label={k.input === "hidden" ? "Value" : "Default"}>{#snippet children(id)}<Input {id} bind:value={() => current.default || "", (v) => (current.default = v)} />{/snippet}</Field>
      {/if}
      {#if has(current, "rows")}
        <Field label="Rows">{#snippet children(id)}<Input {id} type="number" min="2" max="20" value={current.rows || ""} oninput={(e) => (current.rows = parseInt(e.currentTarget.value, 10) || undefined)} />{/snippet}</Field>
      {/if}
      {#if has(current, "min") || has(current, "max")}
        <div class="grid grid-cols-2 gap-2">
          <Field label="Min">{#snippet children(id)}<Input {id} type="number" value={current.min ?? ""} oninput={(e) => (current.min = e.currentTarget.value === "" ? undefined : Number(e.currentTarget.value))} />{/snippet}</Field>
          <Field label="Max">{#snippet children(id)}<Input {id} type="number" value={current.max ?? ""} oninput={(e) => (current.max = e.currentTarget.value === "" ? undefined : Number(e.currentTarget.value))} />{/snippet}</Field>
        </div>
      {/if}
      {#if has(current, "max_length")}
        <Field label="Most characters" help="Blank: 500, or 10,000 for a paragraph.">{#snippet children(id)}<Input {id} type="number" min="1" value={current.max_length || ""} oninput={(e) => (current.max_length = parseInt(e.currentTarget.value, 10) || undefined)} />{/snippet}</Field>
      {/if}
      {#if has(current, "accept")}
        <Field label="Allowed types" help=".pdf,.docx or image/*. Blank: any.">{#snippet children(id)}<Input {id} bind:value={() => current.accept || "", (v) => (current.accept = v)} placeholder=".pdf,image/*" />{/snippet}</Field>
      {/if}
      {#if has(current, "max_mb")}
        <Field label="Largest file (MB)" help="Up to 10.">{#snippet children(id)}<Input {id} type="number" min="1" max="10" value={current.max_mb || ""} oninput={(e) => (current.max_mb = Number(e.currentTarget.value) || undefined)} placeholder="5" />{/snippet}</Field>
      {/if}
      {#if has(current, "show_if")}
        {@const controls = controlsFor(i)}
        <Field label="Show only when" help="Blank: always. A field that's hidden isn't asked for, or required." error={errorAt(i, "show_if.field")}>
          {#snippet children(id, invalid)}
            <Select {id} {invalid} placeholder="Always shown" options={controls.map((x) => ({ value: x.name, label: x.label || x.name }))}
              value={current.show_if?.field ?? ""} onchange={(v) => setShowIf(current, v)} />
          {/snippet}
        </Field>
        {#if current.show_if?.field}
          {@const ctl = fields.find((x) => x.name === current.show_if.field)}
          {#if ctl?.type === "checkbox"}
            <p class="text-muted-foreground -mt-2 text-xs">…is ticked.</p>
          {:else if (ctl?.options || []).length}
            <Field label="…is one of" error={errorAt(i, "show_if.values")}>
              {#snippet children()}
                <div class="flex flex-col gap-1.5">
                  {#each ctl.options.filter((o) => o.trim()) as o}
                    <Checkbox label={o} checked={(current.show_if.values || []).includes(o)} onchange={(on) => toggleShowValue(current, o, on)} />
                  {/each}
                </div>
              {/snippet}
            </Field>
          {:else}
            <Field label="…is one of" help="One answer per line." error={errorAt(i, "show_if.values")}>
              {#snippet children(id, invalid)}<Textarea {id} {invalid} rows={2} value={(current.show_if.values || []).join("\n")} oninput={(e) => (current.show_if = { ...current.show_if, values: e.currentTarget.value.split("\n") })} />{/snippet}
            </Field>
          {/if}
        {/if}
      {/if}
      {#if !k.layout && k.input !== "hidden"}
        <Field label="Help text">{#snippet children(id)}<Input {id} bind:value={() => current.help || "", (v) => (current.help = v)} />{/snippet}</Field>
      {/if}
    {:else}
      <p class="text-muted-foreground text-sm">Choose a field to edit it.</p>
    {/if}
  </div>
</div>
