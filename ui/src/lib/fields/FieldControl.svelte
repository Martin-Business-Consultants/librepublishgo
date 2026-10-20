<script>
  import Field from "../components/Field.svelte";
  import Input from "../components/Input.svelte";
  import Textarea from "../components/Textarea.svelte";
  import Select from "../components/Select.svelte";
  import Switch from "../components/Switch.svelte";
  import TagInput from "../components/TagInput.svelte";
  import MarkdownEditor from "../components/MarkdownEditor.svelte";
  import TiptapEditor from "../components/TiptapEditor.svelte";
  import LinkField from "./LinkField.svelte";
  import AssetField from "./AssetField.svelte";
  import RecordField from "./RecordField.svelte";
  import RepeaterField from "./RepeaterField.svelte";
  import FieldsForm from "./FieldsForm.svelte";
  import BlocksEditor from "./BlocksEditor.svelte";
  import FormBuilder from "./FormBuilder.svelte";
  import MessageEditor from "./MessageEditor.svelte";

  let { field: f, data, path, errors = {} } = $props();
  const label = $derived(f.label || f.name.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase()));
  const error = $derived(errors[path]);
  // A select's choices can come from the record itself: the fields a
  // form builder holds, of some types ({builder: "fields", types: ["email"]}).
  // A message's merge tags: the builder's fields, then the plugin's own
  // ({builder: "fields", extra: [{name, label}]}), then any the record
  // carries ({record: "hook_tags", record_group}: what the form's hook has
  // added lately), each group under its heading.
  const mergeTags = $derived.by(() => {
    const m = f.merge_tags;
    if (!m) return [];
    const fromFields = (data[m.builder] || []).filter((x) => x.name && x.type !== "heading").map((x) => ({ name: x.name, label: x.label || x.name, group: "Answers" }));
    const taken = new Set([...fromFields.map((t) => t.name), ...(m.extra || []).map((t) => t.name)]);
    const fromRecord = (m.record ? data[m.record] || [] : []).filter((t) => t.name && !taken.has(t.name)).map((t) => ({ ...t, group: m.record_group || "More" }));
    return [...fromFields, ...(m.extra || []).map((t) => ({ ...t, group: "This form" })), ...fromRecord];
  });
  const options = $derived.by(() => {
    const from = f.options_from;
    if (!from) return f.options || [];
    return (data[from.builder] || []).filter((x) => x.name && (!from.types || from.types.includes(x.type))).map((x) => ({ value: x.name, label: x.label ? `${x.label} (${x.name})` : x.name }));
  });

  // The value, read and written through data[f.name]; blank means absent,
  // as the API reads it.
  function set(v) {
    if (v === "" || v == null || (Array.isArray(v) && !v.length && f.type !== "repeater" && f.type !== "blocks")) delete data[f.name];
    else data[f.name] = v;
  }
  // svelte-ignore state_referenced_locally
  if ((f.type === "group") && (typeof data[f.name] !== "object" || data[f.name] == null)) data[f.name] = {};
  // svelte-ignore state_referenced_locally
  if ((f.type === "repeater" || f.type === "blocks" || f.type === "form_builder") && !Array.isArray(data[f.name])) data[f.name] = [];

  const toLocal = (iso) => {
    const d = new Date(iso);
    if (!iso || isNaN(d)) return "";
    const p = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  };
</script>

{#if f.type === "boolean"}
  <div class="flex flex-col gap-1">
    <Switch checked={!!data[f.name]} onchange={(on) => (data[f.name] = on)} {label} description={f.help} />
    {#if error}<p class="text-xs text-destructive">{error.join(", ")}</p>{/if}
  </div>
{:else if f.type === "group"}
  <fieldset class="rounded-lg border border-border px-4 pb-4 pt-2">
    <legend class="px-1 text-sm font-semibold text-foreground">{label}{#if f.required}<span class="text-destructive"> *</span>{/if}</legend>
    {#if f.help}<p class="mb-3 text-xs text-muted-foreground">{f.help}</p>{/if}
    <FieldsForm fields={f.of || []} data={data[f.name]} {path} {errors} gap="gap-4" />
  </fieldset>
{:else if f.type === "repeater"}
  <Field {label} required={f.required} help={f.help} {error}>
    {#snippet children()}<RepeaterField field={f} items={data[f.name]} {path} {errors} />{/snippet}
  </Field>
{:else if f.type === "form_builder"}
  <div class="flex flex-col gap-1.5">
    {#if f.label}<span class="text-sm font-medium">{label}</span>{/if}
    <FormBuilder fields={data[f.name]} palette={f.palette || []} {errors} path={path} />
    {#if error}<p class="text-xs text-destructive">{error.join(", ")}</p>{/if}
  </div>
{:else if f.type === "blocks"}
  <Field {label} required={f.required} help={f.help} {error}>
    {#snippet children()}<BlocksEditor blocks={data[f.name]} allowed={f.allowed_types} {path} {errors} />{/snippet}
  </Field>
{:else}
  <Field {label} required={f.required} help={f.help} {error}>
    {#snippet children(id, invalid)}
      {#if f.type === "text"}
        <Textarea {id} {invalid} rows={3} value={data[f.name] ?? ""} oninput={(e) => set(e.currentTarget.value)} />
      {:else if f.type === "richtext"}
        <TiptapEditor {id} {invalid} format="json" bind:value={() => data[f.name] ?? null, (v) => set(v)} />
      {:else if f.type === "markdown"}
        <MarkdownEditor {id} {invalid} bind:value={() => data[f.name] ?? "", (v) => set(v)} />
      {:else if f.type === "code"}
        <Textarea {id} {invalid} mono rows={6} value={data[f.name] ?? ""} oninput={(e) => set(e.currentTarget.value)} spellcheck="false" />
      {:else if f.type === "integer"}
        <Input {id} {invalid} type="number" step="1" class="max-w-48" value={data[f.name] ?? ""} oninput={(e) => set(e.currentTarget.value === "" ? null : parseInt(e.currentTarget.value, 10))} />
      {:else if f.type === "number"}
        <Input {id} {invalid} type="number" step="any" class="max-w-48" value={data[f.name] ?? ""} oninput={(e) => set(e.currentTarget.value === "" ? null : Number(e.currentTarget.value))} />
      {:else if f.type === "select"}
        <Select {id} {invalid} class="max-w-80" placeholder="—" {options} value={data[f.name] ?? ""} onchange={set} />
      {:else if f.type === "rich_text"}
        <MessageEditor {id} {invalid} tags={mergeTags} bind:value={() => data[f.name] ?? "", (v) => set(v)} />
      {:else if f.type === "password"}
        <Input {id} {invalid} type="password" autocomplete="new-password" value={data[f.name] ?? ""} oninput={(e) => set(e.currentTarget.value)} />
      {:else if f.type === "url"}
        <Input {id} {invalid} icon="link" placeholder="https://… or /path" value={data[f.name] ?? ""} oninput={(e) => set(e.currentTarget.value)} />
      {:else if f.type === "datetime"}
        <Input {id} {invalid} type="datetime-local" class="max-w-64" value={toLocal(data[f.name])} oninput={(e) => set(e.currentTarget.value ? new Date(e.currentTarget.value).toISOString() : null)} />
      {:else if f.type === "string_list"}
        <TagInput {id} {invalid} bind:value={() => data[f.name] ?? [], (v) => set(v)} />
      {:else if f.type === "link"}
        <LinkField value={data[f.name]} onchange={set} {invalid} />
      {:else if f.type === "asset"}
        <AssetField value={data[f.name]} onchange={set} />
      {:else if f.type === "record_ref" || f.type === "record_refs"}
        <RecordField {id} collection={f.of_collection} multiple={f.type === "record_refs"} value={data[f.name]} onchange={set} {invalid} />
      {:else}
        <Input {id} {invalid} value={data[f.name] ?? ""} oninput={(e) => set(e.currentTarget.value)} />
      {/if}
    {/snippet}
  </Field>
{/if}
