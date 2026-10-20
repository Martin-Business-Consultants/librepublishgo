<script>
  // `librepublish login` from a terminal: approve the code it shows.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Input from "../lib/components/Input.svelte";
  import Button from "../lib/components/Button.svelte";
  import Notice from "../lib/components/Notice.svelte";
  import { get, post } from "../lib/api.js";
  import { router, go } from "../lib/router.svelte.js";
  import { toast, toastError } from "../lib/toast.svelte.js";
  import { timeAgo, dateTime } from "../lib/format.js";
  let code = $state((router.query.get("code") || "").toUpperCase()), info = $state(null), error = $state("");
  async function look() {
    info = null; error = "";
    if (!code) return;
    try { info = await get(`/device/${encodeURIComponent(code.toUpperCase())}`); } catch (e) { error = e.message; }
  }
  // svelte-ignore state_referenced_locally
  if (code) look();
  async function decide(approve) {
    try { await post("/device/approve", { user_code: code.toUpperCase(), approve }); toast(approve ? "Approved: go back to your terminal." : "Denied."); go("/"); } catch (e) { toastError(e); }
  }
</script>

<PageHeader title="Connect a device" />
<div class="max-w-lg">
  <Panel>
    <div class="flex flex-col gap-5 p-6">
      <p class="text-sm text-muted-foreground">Approve the code your terminal shows. Only approve a code you started yourself.</p>
      <div class="flex gap-2"><Input bind:value={code} mono placeholder="ABCD-EFGH" class="max-w-48 text-center uppercase tracking-widest" onchange={look} /><Button onclick={look}>Look up</Button></div>
      {#if error}<Notice tone="danger">{error}</Notice>{/if}
      {#if info}
        <Notice tone="info">{info.purpose === "site" ? "A site build" : "librepublish"} on <strong class="font-semibold">{info.hostname || "a machine"}</strong> asks to act as you{info.purpose === "site" ? " and receive a read-only token for the site" : ""}.</Notice>
        <!-- The name is the machine's own word; where and when are the CMS's. -->
        <dl class="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 text-sm">
          <dt class="text-muted-foreground">Asked from</dt>
          <dd class="font-mono">{info.ip || "—"}</dd>
          <dt class="text-muted-foreground">Asked</dt>
          <dd title={dateTime(info.created_at)}>{timeAgo(info.created_at)}</dd>
        </dl>
        <p class="text-sm text-muted-foreground">If that isn't your computer, or you didn't just run <code class="rounded-sm bg-muted px-1 font-mono text-xs">librepublish login</code>, deny it.</p>
        <div class="flex justify-end gap-2"><Button variant="danger" onclick={() => decide(false)}>Deny</Button><Button variant="primary" onclick={() => decide(true)}>Approve</Button></div>
      {/if}
    </div>
  </Panel>
</div>
