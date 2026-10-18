<script>
  import Notice from "../lib/components/Notice.svelte";
  // A settings section: a form over one settings key, with Save.
  import SettingsLayout from "./SettingsLayout.svelte";
  import Panel from "../lib/components/Panel.svelte";
  import Button from "../lib/components/Button.svelte";
  import Spinner from "../lib/components/Spinner.svelte";
  import FieldsForm from "../lib/fields/FieldsForm.svelte";
  import Input from "../lib/components/Input.svelte";
  import Field from "../lib/components/Field.svelte";
  import { get, patch, post } from "../lib/api.js";
  import { toast, toastError } from "../lib/toast.svelte.js";
  import { save } from "../lib/save.js";
  import { session } from "../lib/session.svelte.js";
  import { site } from "../lib/site.svelte.js";

  let { params = {} } = $props();
  const SECTIONS = {
    general: { title: "General", key: "general", panels: [
      ["The site", [
        { name: "title", label: "Site name", type: "string" },
        { name: "site_base_url", label: "Public site URL", type: "url", help: "Where the site lives (https://example.com). Webhooks, the sitemap and \"View site\" use it." },
        { name: "cms_url", label: "CMS address", type: "url", help: "This CMS's own address (https://cms.example.com): the URLs of images and files it serves start with it. APP_URL, when the server sets it, wins." },
        { name: "default_locale", label: "Default locale", type: "string", help: "Pages in other locales are served under a prefix: /fr/…" },
      ]],
      ["How the site is served", [
        { name: "serving", label: "Delivery API", type: "select", options: [{ value: "api_only", label: "Everything live" }, { value: "website_api", label: "Only what's chosen" }], help: "Everything live is on /api/v1, or only the collections marked \"Available over the API\" (and pages and globals when switched on below)." },
        { name: "api_pages", label: "Serve pages and globals (website_api)", type: "boolean" },
        { name: "cors_origins", label: "Allowed origins", type: "text", help: "Browsers on these origins may call the delivery API and post forms. One per line." },
      ]],
      ["Contact details", [
        { name: "phone", label: "Phone", type: "string" }, { name: "email", label: "Email", type: "string" },
        { name: "address_line1", label: "Address", type: "string" },
        { name: "city", label: "City", type: "string" }, { name: "state", label: "State or region", type: "string" }, { name: "zip", label: "Postal code", type: "string" },
      ]],
    ] },
    // Stored under "brand", where agents read it (get_setting brand, the
    // manifest's brand); the visual identity is Settings › Brand.
    ai: { title: "AI writing brief", key: "brand", intro: "The brief AI agents read before writing copy (it comes with get_manifest): keep it short and specific. Nothing in the CMS renders it.", panels: [
      ["Writing brief", [
        { name: "brand_voice", label: "Voice", type: "text", help: "How the site sounds: plain, warm, technical…" },
        { name: "audience", label: "Audience", type: "text", help: "Who reads it, and what they come for." },
        { name: "key_facts", label: "Facts that must stay accurate", type: "text", help: "Prices, hours, names, claims." },
        { name: "style_notes", label: "House style", type: "text", help: "Spelling, capitalisation, words to use or avoid." },
      ]],
    ] },
    email: { title: "Email", key: "email", intro: "The server the CMS sends email through (form notifications, confirmations), and who it comes from unless a form says otherwise.", panels: [
      ["Sent from", [
        { name: "from_name", label: "From name", type: "string", help: "What people see as the sender, e.g. Acme." },
        { name: "from_email", label: "From address", type: "string", help: "An address your mail server may send as (SPF/DKIM), e.g. hello@acme.com." },
        { name: "reply_to", label: "Reply-to address", type: "string", help: "Where replies go. Blank: the from address." },
      ]],
      ["SMTP server", [
        { name: "smtp_host", label: "Host", type: "string", help: "smtp.postmarkapp.com, email-smtp.eu-west-1.amazonaws.com, smtp.gmail.com…" },
        { name: "smtp_port", label: "Port", type: "integer", help: "587 for STARTTLS, 465 for TLS. Blank: chosen from the security." },
        { name: "smtp_security", label: "Security", type: "select", options: [{ value: "starttls", label: "STARTTLS (587)" }, { value: "tls", label: "TLS (465)" }, { value: "none", label: "None (25, not recommended)" }] },
        { name: "smtp_username", label: "Username", type: "string" },
        { name: "smtp_password", label: "Password", type: "password", help: "Stored encrypted. Leave as it is to keep it." },
      ]],
    ] },
    github: { title: "Site repository", key: "github", intro: "The frontend this CMS publishes, so agents working on the site itself find it.", panels: [
      ["Repository", [{ name: "frontend_github_repo", label: "Frontend repository", type: "string", help: "owner/name, not a URL." }]],
    ] },
  };
  // svelte-ignore state_referenced_locally
  const section = SECTIONS[params.section || "general"] || SECTIONS.general;
  let data = $state(null), errors = $state({}), saving = $state(false), testTo = $state(session.me?.user?.email || ""), testing = $state(false);
  async function sendTest() {
    testing = true;
    try { const r = await post("/settings/email/test", { to: testTo }); toast(`Sent a test email to ${r.to}.`); } catch (e) { toastError(e); }
    testing = false;
  }
  get(`/settings/${section.key}`).then((r) => (data = r.setting.data || {}));
  async function submit() {
    saving = true;
    const r = await save(() => patch(`/settings/${section.key}`, { setting: { data: $state.snapshot(data) } }));
    saving = false; errors = r.errors || {};
    if (r.ok && section.key === "general") { session.site = data.title; site.refresh(); }
  }
</script>

<SettingsLayout title={section.title}>
  {#if section.intro}<p class="-mt-4 text-sm text-muted-foreground">{section.intro}</p>{/if}
  {#if !data}
    <div class="flex justify-center py-16"><Spinner /></div>
  {:else}
    {#if section.key === "general" && !data.cms_url && session.can("settings:write")}
      <Notice tone="warning">
        The CMS address isn't set, so image and file URLs may start with localhost.
        <button type="button" class="font-medium underline underline-offset-4" onclick={() => (data.cms_url = location.origin)}>Use {location.origin}</button>, the address you're on, then save.
      </Notice>
    {/if}
    {#each section.panels as [title, fields]}
      <Panel {title}><div class="p-6"><FieldsForm {fields} {data} {errors} /></div></Panel>
    {/each}
    {#if session.can("settings:write")}<div class="flex justify-end"><Button variant="primary" loading={saving} onclick={submit}>Save changes</Button></div>{/if}
    {#if section.key === "email" && session.can("settings:write")}
      <Panel title="Send a test">
        <div class="flex flex-col gap-3 p-6 sm:flex-row sm:items-end">
          <Field label="To" class="flex-1">{#snippet children(id)}<Input {id} type="email" bind:value={testTo} />{/snippet}</Field>
          <Button icon="send" loading={testing} onclick={sendTest}>Send test email</Button>
        </div>
        {#snippet footer()}Save first: the test uses the saved settings.{/snippet}
      </Panel>
    {/if}
  {/if}
</SettingsLayout>
