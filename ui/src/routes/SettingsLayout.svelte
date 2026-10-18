<script>
  // Settings' frame (settings.css): the sections down the side, the section
  // beside them.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import { router } from "../lib/router.svelte.js";
  let { title, children } = $props();
  const nav = [
    ["Site", [["General", "/settings"], ["Brand", "/settings/brand"], ["AI", "/settings/ai"], ["Site repository", "/settings/github"]]],
    ["Publishing", [["Deploy", "/settings/deploy"], ["Email", "/settings/email"]]],
    ["Access", [["Service tokens", "/settings/tokens"]]],
    ["System", [["Updates", "/settings/updates"]]],
  ];
</script>

<PageHeader title="Settings" icon="settings-outline" />
<div class="grid items-start gap-6 lg:grid-cols-[13rem_minmax(0,1fr)]">
  <nav class="flex flex-col gap-4 lg:sticky lg:top-16" aria-label="Settings">
    {#each nav as [group, links]}
      <div class="flex flex-col gap-1">
        <span class="px-2 text-xs font-medium text-muted-foreground">{group}</span>
        {#each links as [label, path]}
          <a href="#{path}" class="block rounded-md px-2 py-1 text-sm {router.path === path ? 'bg-primary/5 font-medium text-primary' : 'text-foreground/80 hover:bg-muted hover:text-foreground'}" aria-current={router.path === path ? "page" : undefined}>{label}</a>
        {/each}
      </div>
    {/each}
  </nav>
  <div class="flex min-w-0 flex-col gap-6">
    <h2 class="text-xl font-semibold tracking-tight text-foreground">{title}</h2>
    {@render children()}
  </div>
</div>
