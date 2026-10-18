// End to end: a real Go core (`librepublish serve`, fresh data in a temp
// directory), seeded through its management API, and the example site built
// against it — then served, and pushed into every failure the integration
// promises to catch.
//
//   npm run e2e                                   # builds the Go binary from this repository (needs Go)
//   LIBREPUBLISH_BIN=/path/to/librepublish npm run e2e
//
// Needs `npm install` in example/ (done here when node_modules is missing)
// and `npm run build` in the package. Leaves nothing behind but
// example/dist, example/.astro and example/cms.lock.json (git-ignored).
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createHmac } from "node:crypto";
import { deflateSync } from "node:zlib";
import assert from "node:assert/strict";

const pkg = fileURLToPath(new URL("../../", import.meta.url));
const example = join(pkg, "example");
const repo = join(pkg, "../..");
const scratch = mkdtempSync(join(tmpdir(), "lp-e2e-"));
const children = [];
const cleanups = [];

function step(name) { console.log(`\n▸ ${name}`); }
function run(cmd, args, { cwd = example, env = {}, fail = false } = {}) {
  const result = spawnSync(cmd, args, { cwd, env: { ...process.env, ...env }, encoding: "utf8", maxBuffer: 64 << 20 });
  const out = `${result.stdout ?? ""}${result.stderr ?? ""}`;
  if (fail ? result.status === 0 : result.status !== 0) {
    console.error(out);
    throw new Error(`${cmd} ${args.join(" ")} ${fail ? "should have failed" : `exited ${result.status}`}`);
  }
  return out;
}
async function freePort() {
  return new Promise((resolve) => { const s = createServer().listen(0, "127.0.0.1", () => { const { port } = s.address(); s.close(() => resolve(port)); }); });
}
function start(cmd, args, options) {
  const child = spawn(cmd, args, { ...options, stdio: ["ignore", "pipe", "pipe"] });
  let log = "";
  child.stdout.on("data", (d) => { log += d; });
  child.stderr.on("data", (d) => { log += d; });
  children.push(child);
  return { child, log: () => log };
}
async function waitFor(url, headers = {}) {
  for (let i = 0; i < 100; i++) {
    try { if ((await fetch(url, { headers })).status < 500) return; } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`${url} never answered`);
}

try {
  step("the Go core");
  let bin = process.env.LIBREPUBLISH_BIN;
  if (!bin) {
    bin = join(scratch, "librepublish");
    run("go", ["build", "-o", bin, "./cmd/librepublish"], { cwd: repo });
  }
  const data = join(scratch, "data");
  // What the CLI prints on stdout (a token is its last line); its notes go to stderr.
  const cli = (...args) => {
    const result = spawnSync(bin, args, { cwd: scratch, env: { ...process.env, DATA_DIR: data }, encoding: "utf8" });
    if (result.status !== 0) throw new Error(`librepublish ${args.join(" ")}: ${result.stderr}`);
    return result.stdout.trim().split("\n").at(-1).trim();
  };
  cli("seed-blocks");
  const token = cli("token", "create", "--name", "SITE");
  const admin = cli("token", "create", "--name", "E2E", "--role", "Administrator");
  const port = await freePort();
  const cms = `http://127.0.0.1:${port}`;
  const server = start(bin, ["serve", "--data", data, "--addr", `127.0.0.1:${port}`, "--url", cms], { cwd: scratch });
  await waitFor(`${cms}/api/v1/site`, { Authorization: `Bearer ${token}` });

  const api = async (method, path, body, { expect = [200, 201] } = {}) => {
    const init = { method, headers: { Authorization: `Bearer ${admin}` } };
    if (body instanceof FormData) init.body = body;
    else if (body) { init.body = JSON.stringify(body); init.headers["Content-Type"] = "application/json"; }
    const response = await fetch(`${cms}${path}`, init);
    const text = await response.text();
    if (!expect.includes(response.status)) throw new Error(`${method} ${path}: ${response.status} ${text}`);
    return text ? JSON.parse(text) : null;
  };

  step("seeding it through the management API");
  await api("PATCH", "/api/settings/general", { setting: { site_base_url: "https://site.test" } });
  const upload = new FormData();
  upload.append("file", new Blob([png(1600, 800)], { type: "image/png" }), "hero.png");
  upload.append("alt", "A gradient");
  const { asset } = await api("POST", "/api/assets", upload);
  const image = String(asset.id);
  await api("PATCH", "/api/settings/branding", { setting: { data: { logo_id: image, primary_color: "#B45309", font: "Lora", border_radius: "large", top_bar: "dark" } } });
  await api("POST", "/api/collections", { collection: { slug: "posts", name: "Posts", api_enabled: true, schema: [
    { name: "summary", label: "Summary", type: "text", required: true },
    { name: "cover", label: "Cover", type: "asset" },
  ] } });
  await api("POST", "/api/globals", { global: { slug: "nav", name: "Navigation",
    schema: [{ name: "items", label: "Items", type: "repeater", of: [{ name: "label", label: "Label", type: "string", required: true }, { name: "link", label: "Link", type: "link" }] }],
    data: { items: [{ label: "Home", link: { kind: "page", value: "home" } }, { label: "About", link: { kind: "page", value: "about" } }] } } });
  await api("POST", "/api/pages", { page: { path: "home", slug: "home", title: "Home", status: "published", seo: { meta_description: "Welcome", og_image_id: image }, blocks: [
    { type: "hero", data: { heading: "Hello from the CMS", subheading: "Built by Astro", image_id: image, ctas: [{ label: "About", url: { kind: "page", value: "about" }, style: "primary" }] } },
    { type: "text", data: { body: "Some **markdown**." } },
    { type: "collection_list", data: { collection_slug: "posts", heading: "Latest", layout: "grid", filter_status: "published", sort_by: "published_at", sort_dir: "desc" } },
  ] } });
  await api("POST", "/api/pages", { page: { path: "about", slug: "about", title: "About", status: "published", seo: { meta_description: "About us" }, blocks: [
    { type: "columns", data: { column_count: "2", items: [{ type: "heading", data: { level: "2", text: "Inside" } }, { type: "image", data: { asset_id: image, alt: "g" } }] } },
  ] } });
  await api("PATCH", "/api/pages/about/schema", { schema: [{ name: "tagline", label: "Tagline", type: "string", required: true }] });
  await api("PATCH", "/api/pages/about", { page: { frontmatter: { tagline: "Who we are" } } });
  await api("POST", "/api/collections/posts/entries", { entry: { slug: "hello-world", title: "Hello world", status: "published", frontmatter: { summary: "First post", cover: image }, body_markdown: "# Hi" } });
  await api("POST", "/api/x/forms/form", { form: { name: "Contact", slug: "contact", status: "active", store_submissions: true, honeypot: true, min_seconds: 3, captcha: "none", submit_label: "Send it", success_message: "Thanks!", admin_enabled: false, fields: [
    { type: "heading", text: "About you" },
    { type: "text", name: "name", label: "Name", required: true, width: "half", max_length: 80 },
    { type: "email", name: "email", label: "Email", required: true, width: "half" },
    { type: "tel", name: "phone", label: "Phone" },
    { type: "url", name: "website", label: "Website" },
    { type: "number", name: "budget", label: "Budget", min: 100, max: 10000 },
    { type: "select", name: "topic", label: "Topic", options: ["Sales", "Support"] },
    { type: "radio", name: "size", label: "Team size", options: ["1-10", "11-50"] },
    { type: "checkboxes", name: "services", label: "Services", options: ["Design", "Build"] },
    { type: "date", name: "start", label: "Start date" },
    { type: "file", name: "brief", label: "Brief", accept: ".pdf,image/*", max_mb: 2 },
    { type: "hidden", name: "source", default: "website" },
    { type: "textarea", name: "message", label: "Message", required: true, rows: 6, help: "Tell us more" },
    { type: "checkbox", name: "consent", label: "Consent", text: "I agree to be contacted", required: true },
  ] } });

  const env = { CMS_BASE_URL: cms, CMS_API_TOKEN: token, CMS_WEBHOOK_SECRET: "e2e-secret", LIBREPUBLISH_ACCEPT_SCHEMA: "" };
  if (!existsSync(join(example, "node_modules"))) run("npm", ["install"], { env });
  const lock = join(example, "cms.lock.json");
  rmSync(lock, { force: true });
  for (const dir of ["dist", ".astro", "node_modules/.astro"]) rmSync(join(example, dir), { recursive: true, force: true });

  step("no lock: the build stops");
  assert.match(run("npx", ["astro", "build"], { env, fail: true }), /There's no cms\.lock\.json/);

  step("librepublish-astro sync, then astro check");
  assert.match(run("npx", ["librepublish-astro", "sync"], { env }), /Wrote cms\.lock\.json: sha256:/);
  const locked = JSON.parse(readFileSync(lock, "utf8"));
  assert.match(locked.schema_digest, /^sha256:[0-9a-f]{64}$/);
  assert.ok(locked.parts["blocks/hero"] && locked.parts["collections/posts"] && locked.parts["pages/about"]);
  assert.ok(!locked.parts["blocks/pattern"], "patterns are expanded, not a block type");
  assert.equal(typeof locked.cms_version, "string");
  assert.ok(locked.forms.contact?.startsWith("sha256:"), "the contact form's digest");
  assert.match(run("npx", ["librepublish-astro", "check"], { env }), /matches/);
  assert.match(run("npx", ["astro", "check"], { env }), /0 errors/);

  for (const mode of ["hybrid", "SSR=1"]) {
    step(`astro build (${mode})`);
    rmSync(join(example, "dist"), { recursive: true, force: true });
    // Astro colors its log on CI too: matched without the escape codes.
    const log = run("npx", ["astro", "build"], { env: { ...env, SSR: mode === "SSR=1" ? "1" : "" } }).replace(/\x1B\[[0-9;]*m/g, "");
    const out = join(example, "dist/client");
    const read = (path) => readFileSync(join(out, path), "utf8");
    assert.match(log, /Reported the build to the CMS \((hybrid|server)\)/);
    assert.match(log, /librepublish-pages\] (loaded|updated)/);
    assert.match(read("index.html"), /Hello from the CMS/);
    assert.match(read("index.html"), /srcset="[^"]*\/w640\/hero\.png 640w/, "<Image> uses the CMS's renditions");
    assert.match(read("about/index.html"), /Who we are/, "a page's own fields");
    assert.match(read("about/index.html"), /data-block="columns"[\s\S]*data-block="heading"/, "nested blocks");
    assert.match(read("posts/hello-world/index.html"), /First post/);
    assert.match(read("index.html"), /data-block="text"><p>Some <strong>markdown<\/strong>\.<\/p>/, "<RichText> renders a markdown field");
    assert.match(read("posts/hello-world/index.html"), /<article><h1>Hi<\/h1>/, "<RichText> renders body_markdown");
    assert.match(read("index.html"), /<style>:root\{--brand-primary:#b45309;--brand-on-primary:#ffffff;--brand-font:"Lora", Georgia, "Times New Roman", serif;--brand-radius:0\.875rem\}<\/style>/, "<BrandStyles> draws Settings › Brand");
    assert.match(read("index.html"), /<nav><img src="[^"]*\/hero\.png"/, "the logo");
    assert.match(read("sitemap.xml"), /<loc>https:\/\/site\.test\/about<\/loc>/);
    assert.match(read("robots.txt"), /Sitemap: /);
    assert.match(read("_redirects"), /# From the CMS/);
    const types = readFileSync(join(example, ".astro/integrations/librepublish/cms.d.ts"), "utf8");
    assert.match(types, /declare module "librepublish:types"/);
    assert.ok(types.includes(locked.schema_digest));
    assert.ok(!existsSync(join(out, "contact/index.html")), "the form page isn't prerendered");

    step(`serving it (${mode})`);
    const sitePort = await freePort();
    const site = start("node", ["dist/server/entry.mjs"], { cwd: example, env: { ...process.env, ...env, HOST: "127.0.0.1", PORT: String(sitePort) } });
    const base = `http://127.0.0.1:${sitePort}`;
    await waitFor(`${base}/contact`);
    const html = await (await fetch(`${base}/contact`)).text();
    const { form } = await (await fetch(`${cms}/api/x/forms/definition?form=contact`)).json();
    assert.match(html, /data-cms-form="contact"/);
    assert.ok(html.includes(`action="${form.action.replace(/&/g, "&amp;")}"`), "posts straight to the CMS");
    for (const field of form.fields) {
      if (field.type === "heading") assert.ok(html.includes(field.text), "the heading");
      else assert.ok(html.includes(`name="${field.name}"`), `field ${field.name} (${field.type})`);
    }
    assert.match(html, /name="_hp"/);
    assert.match(html, /name="_ts" value="\d+"/);
    assert.match(html, /enctype="multipart\/form-data"/);

    // Edit the form in the CMS: the next request shows it, no build.
    await api("POST", "/api/x/forms/form?id=contact", { form: { ...form, admin_enabled: false, fields: [...form.fields.map(({ rules, ...field }) => field), { type: "text", name: "company", label: "Company" }] } });
    assert.match(await (await fetch(`${base}/contact`)).text(), /name="company"/, "the live definition, not a stale one");
    await api("POST", "/api/x/forms/form?id=contact", { form: { ...form, admin_enabled: false, fields: form.fields.map(({ rules, ...field }) => field) } });

    // The purge webhook takes only a signed body.
    const purge = JSON.stringify({ tags: ["page:about"], reason: "e2e" });
    const signed = `sha256=${createHmac("sha256", "e2e-secret").update(purge).digest("hex")}`;
    assert.equal((await fetch(`${base}/_cms/webhook`, { method: "POST", body: purge, headers: { "X-CMS-Signature": "sha256=00" } })).status, 401);
    const answer = await (await fetch(`${base}/_cms/webhook`, { method: "POST", body: purge, headers: { "X-CMS-Signature": signed } })).json();
    assert.deepEqual(answer.tags, ["page:about"]);

    if (mode === "SSR=1") {
      const live = await fetch(`${base}/live/about`);
      assert.equal(live.status, 200);
      assert.match(await live.text(), /About/);
      assert.match(live.headers.get("cache-tag") ?? "", /page:about/);
    }
    site.child.kill();
  }

  step("a form on a prerendered page stops the build");
  const misuse = join(example, "src/pages/prerendered-form.astro");
  writeFileSync(misuse, '---\nimport Form from "@librepublish/astro/components/Form.astro";\nexport const prerender = true;\n---\n<Form slug="contact" />\n');
  cleanups.push(() => rmSync(misuse, { force: true }));
  assert.match(run("npx", ["astro", "build"], { env, fail: true }), /<Form slug="contact"> is on a prerendered page/);
  unlinkSync(misuse);

  step("the CMS's schema moves: build, astro check and sync stop");
  await api("POST", "/api/block_types", { block_type: { slug: "promo", label: "Promo", fields: [{ name: "headline", label: "Headline", type: "string", required: true }] } });
  for (const args of [["astro", "build"], ["astro", "sync"], ["astro", "check"]]) {
    const out = run("npx", args, { env, fail: true });
    assert.match(out, /schema has changed since cms\.lock\.json/, args.join(" "));
    assert.match(out, /\+ blocks\/promo/);
    assert.match(out, /npx librepublish-astro sync/);
  }
  assert.match(run("npx", ["librepublish-astro", "check"], { env, fail: true }), /\+ blocks\/promo/);

  step("LIBREPUBLISH_ACCEPT_SCHEMA=1 takes it; astro check then wants a component for it");
  rmSync(join(example, "dist"), { recursive: true, force: true });
  assert.match(run("npx", ["astro", "build"], { env: { ...env, LIBREPUBLISH_ACCEPT_SCHEMA: "1" } }), /rewrote cms\.lock\.json/);
  assert.notEqual(JSON.parse(readFileSync(lock, "utf8")).schema_digest, locked.schema_digest);
  assert.match(run("npx", ["astro", "check"], { env, fail: true }), /Property 'promo' is missing/);

  step("content using a block type with no component stops the build");
  await api("PATCH", "/api/pages/home", { page: { blocks: [{ type: "promo", data: { headline: "Sale" } }] } });
  assert.match(run("npx", ["astro", "build"], { env, fail: true }), /no component for block type "promo"/);

  step("content outside the schema stops the build");
  await api("PATCH", "/api/collections/posts/schema", { schema: [
    { name: "summary", label: "Summary", type: "text", required: true },
    { name: "cover", label: "Cover", type: "asset" },
    { name: "author", label: "Author", type: "string", required: true },
  ] });
  const outside = run("npx", ["astro", "build"], { env: { ...env, LIBREPUBLISH_ACCEPT_SCHEMA: "1" }, fail: true });
  assert.match(outside, /hello-world/);
  assert.match(outside, /author/);

  void server;
  console.log("\n✓ end to end");
} finally {
  for (const cleanup of cleanups) cleanup();
  for (const child of children) child.kill();
  rmSync(scratch, { recursive: true, force: true });
}

/** A w×h RGB PNG, a horizontal gradient. */
function png(w, h) {
  const row = Buffer.alloc(1 + w * 3);
  for (let x = 0; x < w; x++) row.set([Math.floor((x * 255) / w), 100, 200], 1 + x * 3);
  const raw = Buffer.concat(Array.from({ length: h }, () => row));
  const chunk = (type, body) => {
    const len = Buffer.alloc(4); len.writeUInt32BE(body.length);
    const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(Buffer.concat([Buffer.from(type), body])));
    return Buffer.concat([len, Buffer.from(type), body, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr.set([8, 2, 0, 0, 0], 8);
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}
function crc32(buf) {
  let c = ~0;
  for (const byte of buf) { c ^= byte; for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1)); }
  return ~c >>> 0;
}
