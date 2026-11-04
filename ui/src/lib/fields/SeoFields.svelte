<script>
  // A record's SEO (the Rails admin's SeoFields): the search result as it
  // would look, then the fields behind it, with lengths that fit.
  import FieldsForm from "./FieldsForm.svelte";
  let { seo, errors = {}, title = "", url = "" } = $props();
  const fields = [
    { name: "meta_title", label: "Meta title", type: "string", help: "Shown as the result's title. Defaults to the page title." },
    { name: "meta_description", label: "Meta description", type: "text", help: "A concise summary that search engines and previews show under the title." },
    { name: "canonical_url", label: "Canonical URL", type: "url" },
    { name: "focus_keyword", label: "Focus keyword", type: "string", help: "The search the page is meant to be found for." },
    { name: "og_image_id", label: "Social share image", type: "asset" },
    { name: "og_title", label: "Share title override", type: "string", help: "The title a shared link shows. Defaults to the meta title." },
    { name: "og_description", label: "Share description override", type: "text", help: "The description a shared link shows. Defaults to the meta description." },
    { name: "og_type", label: "Open Graph type", type: "string", help: "website, article… Defaults to website." },
    { name: "twitter_card", label: "Twitter card", type: "select", options: [{ value: "summary_large_image", label: "Large image" }, { value: "summary", label: "Summary" }] },
    { name: "noindex", label: "Hide from search engines", type: "boolean" },
    { name: "nofollow", label: "Ask search engines not to follow links", type: "boolean" },
    { name: "exclude_from_sitemap", label: "Leave out of the sitemap", type: "boolean" },
    { name: "schema_type", label: "schema.org type", type: "string", help: "WebPage, AboutPage, ContactPage, Service…" },
    { name: "json_ld", label: "JSON-LD", type: "json", help: "The page's structured data, as the site outputs it: one node or a list of nodes, each with an @type." },
  ];
  const t = $derived(seo.meta_title || title || "Untitled");
  const d = $derived(seo.meta_description || "");
</script>

<div class="flex flex-col gap-5">
  <div class="rounded-lg border border-border bg-muted/50 p-4">
    <p class="truncate text-xs text-muted-foreground">{url || "/"}</p>
    <p class="truncate text-lg leading-6 text-primary">{t}</p>
    <p class="line-clamp-2 text-sm text-muted-foreground">{d || "No meta description: search engines will pick some text from the page."}</p>
    <p class="mt-2 flex gap-3 text-xs {t.length > 60 ? 'text-amber-700 dark:text-amber-400' : 'text-muted-foreground'}">Title {t.length}/60 <span class={d.length > 160 ? "text-amber-700 dark:text-amber-400" : "text-muted-foreground"}>Description {d.length}/160</span></p>
  </div>
  <FieldsForm {fields} data={seo} path="seo" {errors} />
</div>
