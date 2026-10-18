<script>
  import PageHeader from "../lib/components/PageHeader.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Field from "../lib/components/Field.svelte";
  import Input from "../lib/components/Input.svelte";
  import Button from "../lib/components/Button.svelte";
  import SecretBox from "../lib/components/SecretBox.svelte";
  import CopyButton from "../lib/components/CopyButton.svelte";
  import Notice from "../lib/components/Notice.svelte";
  import PasswordStrength from "../lib/components/PasswordStrength.svelte";
  import { patch, post, del } from "../lib/api.js";
  import { save } from "../lib/save.js";
  import { session } from "../lib/session.svelte.js";
  import { confirm } from "../lib/confirm.svelte.js";
  import { toast, toastError } from "../lib/toast.svelte.js";
  const me = session.me.user;
  let name = $state(me.name), current = $state(""), next = $state(""), errors = $state({}), token = $state(null);
  let email = $state(me.email), pending = $state(me.pending_email), emailErrors = $state({});
  async function saveEmail() {
    const r = await save(() => patch(`/users/${me.id}`, { user: { email } }));
    emailErrors = r.errors || {};
    if (r.ok) { pending = r.res.user.pending_email; email = r.res.user.email; session.me.user.pending_email = pending; }
  }
  async function cancelEmail() {
    try { await del(`/users/${me.id}/pending_email`); pending = null; session.me.user.pending_email = null; toast("The change is cancelled."); } catch (e) { toastError(e); }
  }
  async function saveName() { const r = await save(() => patch(`/users/${me.id}`, { user: { name } })); if (r.ok) session.me.user.name = name; }
  async function savePassword() {
    const r = await save(() => patch(`/users/${me.id}`, { user: { current_password: current, password: next } }));
    errors = r.errors || {};
    if (r.ok) { current = ""; next = ""; }
  }
  // The commands for this site, its address filled in (and the token, once shown).
  const site = location.origin;
  const profile = location.hostname;
  const commands = $derived([
    { title: "1. Install it", note: "Once per computer (macOS and Linux; Windows: download librepublish-windows-amd64.exe from the releases).",
      command: "curl -fsSL https://raw.githubusercontent.com/Martin-Business-Consultants/librepublishgo/main/install.sh | sh" },
    { title: "2. Sign in to this site", note: "It opens this admin to approve the sign-in.", command: `librepublish login ${site}` },
    { title: "3. Check it all works, and see what it can do", command: "librepublish doctor\nlibrepublish tools" },
    { title: "Add it to Claude Code", note: "Through your sign-in on this computer. librepublish mcp setup prints the lines for Codex, Cursor and Claude Desktop.",
      command: `claude mcp add --scope user librepublish -- librepublish mcp --profile ${profile}` },
    { title: "Or connect an AI app straight to the site", note: token ? "With the API token above." : "With your API token: Rotate and show above, then it's filled in here.",
      command: `claude mcp add --transport http librepublish ${site}/mcp --header "Authorization: Bearer ${token || "<your API token>"}"` },
  ]);
  async function rotate() {
    if (!(await confirm({ title: "Rotate your API token?", message: "The old one stops working at once, on every machine that has it.", confirmLabel: "Rotate" }))) return;
    try { token = (await post("/api_tokens/rotate")).token.token; } catch (e) { toastError(e); }
  }
</script>

<PageHeader title="Your account" subtitle={me.email} />
<div class="grid items-start gap-6 lg:grid-cols-2">
  <Panel title="Profile">
    <div class="flex flex-col gap-5 p-6">
      <Field label="Name">{#snippet children(id)}<Input {id} bind:value={name} />{/snippet}</Field>
      <div class="flex justify-end"><Button variant="primary" onclick={saveName}>Save</Button></div>
    </div>
  </Panel>
  <Panel title="Email address">
    <div class="flex flex-col gap-5 p-6">
      <Field label="Email" help="A new address takes effect once you confirm it from the link we send there. Until then this one stays." error={emailErrors.email}>
        {#snippet children(id, invalid)}<Input {id} {invalid} type="email" bind:value={email} autocomplete="email" />{/snippet}
      </Field>
      {#if pending}
        <Notice tone="warning">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span>Waiting for you to confirm <strong>{pending.email}</strong>: check that inbox.</span>
            <Button size="sm" onclick={cancelEmail}>Cancel the change</Button>
          </div>
        </Notice>
      {/if}
      <div class="flex justify-end"><Button variant="primary" onclick={saveEmail} disabled={email.trim().toLowerCase() === me.email.toLowerCase()}>Change email</Button></div>
    </div>
  </Panel>
  <Panel title="Password">
    <div class="flex flex-col gap-5 p-6">
      <Field label="Current password" error={errors.current_password}>{#snippet children(id, invalid)}<Input {id} {invalid} type="password" bind:value={current} autocomplete="current-password" />{/snippet}</Field>
      <Field label="New password" help="At least 10 characters. Your other sessions are signed out." error={errors.password}>{#snippet children(id, invalid)}<Input {id} {invalid} type="password" bind:value={next} autocomplete="new-password" />{/snippet}</Field>
      <PasswordStrength password={next} />
      <div class="flex justify-end"><Button variant="primary" onclick={savePassword}>Change password</Button></div>
    </div>
  </Panel>
  <Panel title="API token">
    <div class="flex flex-col gap-4 p-6">
      <p class="text-sm text-muted-foreground">For <code class="rounded-sm bg-muted px-1 font-mono text-xs">librepublish</code> on a machine where <code class="rounded-sm bg-muted px-1 font-mono text-xs">librepublish login</code> can't open a browser, or an AI app connecting to <code class="rounded-sm bg-muted px-1 font-mono text-xs">/mcp</code>. It acts as you, with your role.</p>
      {#if token}<SecretBox value={token} note="Shown once." />{/if}
      <div><Button icon="refresh" onclick={rotate}>Rotate and show</Button></div>
    </div>
  </Panel>
  <Panel title="Command line and AI agents" class="lg:col-span-2">
    <div class="flex flex-col gap-5 p-6 text-sm">
      <p class="text-muted-foreground">The <code class="rounded-sm bg-muted px-1 font-mono text-xs">librepublish</code> program works on this site from your computer, and lets AI apps (Claude Code, Codex, Cursor…) work on it, as you and with your role.</p>
      {#each commands as c}
        <div class="flex flex-col gap-1.5">
          <p class="font-medium text-foreground">{c.title}</p>
          {#if c.note}<p class="text-muted-foreground">{c.note}</p>{/if}
          <div class="flex items-start gap-2">
            <pre class="min-w-0 flex-1 overflow-x-auto rounded-md bg-muted px-3 py-2 font-mono text-xs leading-relaxed">{c.command}</pre>
            <CopyButton text={c.command} />
          </div>
        </div>
      {/each}
    </div>
  </Panel>
</div>
