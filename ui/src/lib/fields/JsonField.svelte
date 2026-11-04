<script>
  // A JSON value (an object, or a list of them) edited as indented text: the
  // SEO tab's JSON-LD. What's typed is written back only once it parses to
  // the kind of value the field holds, so a half-typed or broken value is
  // never saved; until then the box says what's wrong. Emptied, the value
  // is removed.
  import Textarea from "../components/Textarea.svelte";
  let { id, value, onchange, invalid = false } = $props();

  const show = (v) => (v == null ? "" : JSON.stringify(v, null, 2));
  // svelte-ignore state_referenced_locally
  let text = $state(show(value));
  let problem = $state("");
  // What was last written or shown, serialized: when the value becomes
  // something else (the record reloaded), the box shows it. Compared as
  // text, since the record keeps its own (proxied) copy of what was written.
  // svelte-ignore state_referenced_locally
  let shown = show(value);
  $effect(() => {
    const now = show(value);
    if (now !== shown) {
      shown = now;
      text = now;
      problem = "";
    }
  });

  function input(e) {
    text = e.currentTarget.value;
    if (!text.trim()) {
      problem = "";
      shown = "";
      onchange(null);
      return;
    }
    let parsed;
    try {
      parsed = JSON.parse(text);
    } catch (err) {
      problem = `Not valid JSON yet: ${err.message}. It isn't saved until it is.`;
      return;
    }
    if (parsed === null || typeof parsed !== "object" || (Array.isArray(parsed) && parsed.some((n) => n === null || typeof n !== "object" || Array.isArray(n)))) {
      problem = "JSON-LD is an object, or a list of objects. It isn't saved until it is.";
      return;
    }
    problem = "";
    shown = show(parsed);
    onchange(parsed);
  }
</script>

<Textarea {id} invalid={invalid || !!problem} mono rows={14} value={text} oninput={input} spellcheck="false" />
{#if problem}<p class="mt-1.5 text-xs text-destructive">{problem}</p>{/if}
