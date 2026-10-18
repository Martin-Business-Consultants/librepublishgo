<script>
  // Settings › Updates: which version this is, the newest release, and the
  // button to update to it, or, when there's none to press, why.
  import SettingsLayout from "./SettingsLayout.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Button from "../lib/components/Button.svelte";
  import Notice from "../lib/components/Notice.svelte";
  import Spinner from "../lib/components/Spinner.svelte";
  import { get } from "../lib/api.js";
  import { session } from "../lib/session.svelte.js";
  import { toastError } from "../lib/toast.svelte.js";
  import { dateTime } from "../lib/format.js";
  import { updateTo } from "../lib/update.js";

  let status = $state(null), checking = $state(false), updating = $state(""), denied = $state(false);
  async function load(check = false) {
    checking = true;
    try {
      status = await get(`/tools/update${check ? "?check=1" : ""}`);
    } catch (e) {
      if (e.status === 403) denied = true;
      else toastError(e);
    }
    checking = false;
  }
  load();

  async function update() {
    try {
      await updateTo(status.latest.version, (msg) => (updating = msg));
    } catch (e) {
      updating = "";
      toastError(e);
    }
  }
</script>

<SettingsLayout title="Updates">
  {#if denied}
    <Notice tone="info">Only the Administrator role can see and run updates.</Notice>
  {:else if !status}
    <div class="flex justify-center py-16"><Spinner /></div>
  {:else}
    <Panel title="Version">
      <div class="flex flex-col gap-4 p-6 text-sm">
        <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2">
          <dt class="text-muted-foreground">This site runs</dt>
          <dd class="font-medium">Libre Publish {status.running}</dd>
          <dt class="text-muted-foreground">The newest release</dt>
          <dd>
            {#if status.latest}
              <a href={status.latest.url} target="_blank" rel="noopener noreferrer" class="font-medium underline underline-offset-4">{status.latest.version}</a>
              <span class="text-muted-foreground">, {dateTime(status.latest.published_at)}</span>
            {:else}
              <span class="text-muted-foreground">Couldn't ask: {status.check_error}</span>
            {/if}
          </dd>
          <dt class="text-muted-foreground">Checked</dt>
          <dd class="text-muted-foreground">{dateTime(status.checked_at)}</dd>
        </dl>

        {#if status.failed}
          <Notice tone="warning">The update to <strong>{status.failed.version}</strong> didn't come up ({dateTime(status.failed.at)}), so the site went back to this version. Nothing was lost: a backup from just before it is in the data directory's <code class="font-mono text-xs">backups/</code>, and the logs say why it stopped.</Notice>
        {/if}

        {#if updating}
          <Notice tone="info">{updating}</Notice>
        {:else if !status.release_build}
          <Notice tone="info">This is a development build (<code class="font-mono text-xs">{status.running}</code>), not a release, so it doesn't update itself: it runs whatever was built. Releases update from here.</Notice>
        {:else if status.newer && status.can_update}
          <p>Updating takes a backup into the data directory first, then restarts the site on <strong>{status.latest.version}</strong>: a few seconds offline. If the new version doesn't come up, the site goes back to this one.</p>
        {:else if status.newer}
          <Notice tone="info">This install can't update itself from here. Run <code class="font-mono text-xs">librepublish update</code> where it runs, or deploy {status.latest.version} from your host.</Notice>
        {:else if status.latest}
          <Notice tone="success">This is the newest release.</Notice>
        {/if}

        <div class="flex flex-wrap gap-2">
          {#if status.release_build && status.newer && status.can_update && !updating}
            <Button variant="primary" icon="download" onclick={update}>Update to {status.latest.version}</Button>
          {/if}
          <Button icon="refresh" loading={checking} disabled={!!updating} onclick={() => load(true)}>Check now</Button>
        </div>
      </div>
    </Panel>
    {#if status.in_container}
      <Panel title="On a container host">
        <p class="p-6 text-sm text-muted-foreground">An update made here lives in the site's own volume, and every start runs the newer of it and the image the host deployed. Deploying a newer image from the host takes over from it.</p>
      </Panel>
    {/if}
  {/if}
</SettingsLayout>
