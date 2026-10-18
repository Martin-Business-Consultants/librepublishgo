<script>
  // Your account: profile, security, and the command line and AI agents, in
  // tabs (?tab= so a tab can be linked to), with who you are beside them.
  import PageHeader from "../lib/components/PageHeader.svelte";
  import Tabs from "../lib/components/Tabs.svelte";
  import Icon from "../lib/components/Icon.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Field from "../lib/components/Field.svelte";
  import Input from "../lib/components/Input.svelte";
  import Button from "../lib/components/Button.svelte";
  import SecretBox from "../lib/components/SecretBox.svelte";
  import CopyButton from "../lib/components/CopyButton.svelte";
  import Notice from "../lib/components/Notice.svelte";
  import PasswordStrength from "../lib/components/PasswordStrength.svelte";
  import { patch, post, del } from "../lib/api.js";
  import { router } from "../lib/router.svelte.js";
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
    { title: "Or connect an AI app straight to the site", note: token ? "With your API token, filled in from below." : "With your API token: press Rotate and show below, and it's filled in here.",
      command: `claude mcp add --transport http librepublish ${site}/mcp --header "Authorization: Bearer ${token || "<your API token>"}"` },
  ]);
  async function rotate() {
    if (!(await confirm({ title: "Rotate your API token?", message: "The old one stops working at once, on every machine that has it.", confirmLabel: "Rotate" }))) return;
    try { token = (await post("/api_tokens/rotate")).token.token; } catch (e) { toastError(e); }
  }

  const TABS = [{ value: "profile", label: "Profile" }, { value: "security", label: "Security" }, { value: "agents", label: "API & agents" }];
  let tab = $state(TABS.some((t) => t.value === router.query.get("tab")) ? router.query.get("tab") : "profile");
  $effect(() => {
    const want = tab === "profile" ? "#/account" : `#/account?tab=${tab}`;
    if (location.hash !== want) history.replaceState(null, "", want);
  });
  const initials = $derived((name || me.email).split(/[\s@.]+/).filter(Boolean).slice(0, 2).map((w) => w[0].toUpperCase()).join(""));
  async function signOut() {
    await del("/session").catch(() => {});
    session.signedOut();
    location.hash = "#/";
  }
</script>

<PageHeader title="Your account" subtitle={me.email} />
<Tabs tabs={TABS} bind:value={tab} class="mb-6" />
<div class="flex flex-col gap-6 lg:flex-row lg:items-start">
  <div class="flex min-w-0 flex-1 flex-col gap-6">
    {#if tab === "profile"}
      <Panel title="Profile">
        <div class="flex flex-col gap-5 p-6">
          <Field label="Name">{#snippet children(id)}<Input {id} bind:value={name} autocomplete="name" />{/snippet}</Field>
          <div class="flex justify-end"><Button variant="primary" onclick={saveName} disabled={name.trim() === me.name}>Save</Button></div>
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
    {:else if tab === "security"}
      <Panel title="Password">
        <div class="flex flex-col gap-5 p-6">
          <Field label="Current password" error={errors.current_password}>{#snippet children(id, invalid)}<Input {id} {invalid} type="password" bind:value={current} autocomplete="current-password" />{/snippet}</Field>
          <Field label="New password" help="At least 10 characters. Your other sessions are signed out." error={errors.password}>{#snippet children(id, invalid)}<Input {id} {invalid} type="password" bind:value={next} autocomplete="new-password" />{/snippet}</Field>
          <PasswordStrength password={next} />
          <div class="flex justify-end"><Button variant="primary" onclick={savePassword} disabled={!current || !next}>Change password</Button></div>
        </div>
      </Panel>
    {:else}
      <Panel title="Command line and AI agents">
        <div class="flex flex-col gap-5 p-6 text-sm">
          <p class="text-muted-foreground">The <code class="rounded-sm bg-muted px-1 font-mono text-xs">librepublish</code> program works on this site from your computer, and lets AI apps (Claude Code, Codex, Cursor…) work on it, as you and with your role.</p>
          {#each commands as c}
            <div class="flex flex-col gap-1.5">
              <p class="font-medium text-foreground">{c.title}</p>
              {#if c.note}<p class="text-muted-foreground">{c.note}</p>{/if}
              <div class="flex items-start gap-2">
                <pre class="min-w-0 flex-1 whitespace-pre-wrap break-all rounded-md bg-muted px-3 py-2 font-mono text-xs leading-relaxed">{c.command}</pre>
                <CopyButton text={c.command} />
              </div>
            </div>
          {/each}
        </div>
      </Panel>
      <Panel title="API token">
        <div class="flex flex-col gap-4 p-6 text-sm">
          <p class="text-muted-foreground">For <code class="rounded-sm bg-muted px-1 font-mono text-xs">librepublish</code> on a machine where <code class="rounded-sm bg-muted px-1 font-mono text-xs">librepublish login</code> can't open a browser, or an AI app connecting straight to <code class="rounded-sm bg-muted px-1 font-mono text-xs">/mcp</code>. It acts as you, with your role. Rotating it stops the old one at once, everywhere.</p>
          {#if token}<SecretBox value={token} note="Shown once: it's filled into the last command above until you leave this page." />{/if}
          <div><Button icon="refresh" onclick={rotate}>Rotate and show</Button></div>
        </div>
      </Panel>
    {/if}
  </div>

  <aside class="flex min-w-0 shrink-0 flex-col gap-4 lg:sticky lg:top-16 lg:w-80">
    <Panel>
      <div class="flex flex-col items-center gap-3 p-6 text-center">
        <span class="flex size-16 items-center justify-center rounded-full bg-primary text-xl font-semibold text-primary-foreground" aria-hidden="true">{initials}</span>
        <div class="min-w-0">
          <p class="truncate font-semibold text-foreground">{name || me.email}</p>
          <p class="truncate text-sm text-muted-foreground">{me.email}</p>
        </div>
      </div>
      <dl class="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2 border-t px-6 py-4 text-sm">
        <dt class="text-muted-foreground">Role</dt>
        <dd class="text-right font-medium">{session.me.role || "—"}</dd>
        <dt class="text-muted-foreground">Site</dt>
        <dd class="truncate text-right">{session.site || location.host}</dd>
        <dt class="text-muted-foreground">Version</dt>
        <dd class="text-right">{session.version}</dd>
      </dl>
      <div class="border-t p-4"><Button class="w-full" icon="logout" onclick={signOut}>Sign out</Button></div>
    </Panel>
    {#if tab !== "agents"}
      <Panel>
        <button type="button" onclick={() => (tab = "agents")} class="flex w-full items-start gap-3 p-4 text-left text-sm hover:bg-muted/50">
          <Icon name="code" class="mt-0.5 text-muted-foreground" />
          <span><span class="block font-medium text-foreground">Work with AI agents</span><span class="text-muted-foreground">Sign in from your computer, and add this site to Claude Code, Codex or Cursor.</span></span>
        </button>
      </Panel>
    {/if}
  </aside>
</div>
