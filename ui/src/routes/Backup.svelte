<script>
  // Download a backup: the database and every uploaded file. Importing one
  // is its own page (Import.svelte).
  import PageHeader from "../lib/components/PageHeader.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Button from "../lib/components/Button.svelte";
  import Checkbox from "../lib/components/Checkbox.svelte";
  import { session } from "../lib/session.svelte.js";

  const system = $derived(!!session.me?.system);
  let withKey = $state(false);
</script>

<PageHeader title="Backup" cli="librepublish api_request --method GET --path /api/tools/backup > backup.tar.gz" />
<div class="flex max-w-3xl flex-col gap-6">
  <Panel title="Download a backup">
    <div class="flex flex-col gap-4 p-6 text-sm text-foreground/80">
      <p>A backup is a <code class="rounded-sm bg-muted px-1 font-mono text-xs">.tar.gz</code> of a consistent copy of the database and every uploaded file: the whole site. Import it on the <a href="#/import" class="text-primary underline-offset-4 hover:underline">Import</a> page, here or on another install, or with <code class="rounded-sm bg-muted px-1 font-mono text-xs">librepublish import</code>.</p>
      {#if system}
        <Checkbox bind:checked={withKey} label="Include the secret key"
          description="The key that seals token plaintexts, webhook secrets and the SMTP password, so an import elsewhere can read them. Whoever has the file has those secrets too: keep it as you would a password." />
      {/if}
      <div><Button variant="primary" href={withKey ? "/api/tools/backup?include_secret_key=1" : "/api/tools/backup"} icon="download">Download a backup</Button></div>
    </div>
  </Panel>
</div>
