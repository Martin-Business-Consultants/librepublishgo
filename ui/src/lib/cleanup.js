// Cleans up a site's content through the CMS's API: prose fields (`text`,
// `markdown`) become `richtext`, the editor's own document; HTML typed into
// text becomes that document; image URL caches beside image fields go; alt
// text beside an image moves onto the image; old SEO keys become the
// standard ones. scripts/richtext.mjs runs it from a shell (with a token)
// against a running site; an import does the same in Go
// (internal/restore/content.go), with the same guesses.
//
// It's idempotent: a run that stopped halfway, or a second one, picks up
// what's left and changes nothing else. Values are converted by Tiptap
// itself (`convert`, which the caller builds from Tiptap's generateJSON and
// Markdown parser with the editor's extensions), so a document is what the
// editor would have saved.

export const TAG = /<\/?[A-Za-z][A-Za-z0-9]*(?:\s[^<>]*)?\/?>/;
/** An image's URL, as old content kept it beside (or instead of) the image itself. */
const IMAGE_URL = /(^|_)image_url$/;
const PROSE = new Set(["text", "markdown"]);
const SEO_ALIASES = { seo_title: "meta_title", seo_description: "meta_description", description: "meta_description", no_index: "noindex" };

const isDoc = (v) => v && typeof v === "object" && v.type === "doc";
const emptyDoc = (d) => !d.content?.some((n) => n.type !== "paragraph" || n.content?.length);

/** Plain text as paragraphs: a blank line between them, a line break within. */
function plainDoc(text) {
  const content = text.replace(/\r\n?/g, "\n").trim().split(/\n\s*\n/).map((para) => {
    const parts = [];
    para.split("\n").forEach((line, i) => {
      if (i) parts.push({ type: "hardBreak" });
      if (line) parts.push({ type: "text", text: line });
    });
    return { type: "paragraph", content: parts };
  });
  return { type: "doc", content };
}

/**
 * What a prose field should become, as a first guess the admin shows (and a
 * person can change): code for markup pasted on purpose (scripts, embeds),
 * plain text for addresses, alt text and the site's own description, rich
 * text for the rest.
 */
export function suggest(path, field) {
  const words = `${field.label ?? ""} ${field.help ?? ""}`;
  if (/<head>|<\/?body>|\bscripts?\b|embed code|pasted/i.test(words)) return "code";
  if (/(^|_)address(_|$)|(^|_)alt$/.test(field.name) || path === "general.description" || /\b(meta|seo)\b/i.test(words)) return "plain";
  return "richtext";
}

/**
 * Runs the clean-up. api(method, path, body) answers the CMS's JSON (throwing
 * on an error answer); convert is { html(s), markdown(s) → doc, toMarkdown(doc)
 * → s }. options: dryRun, choices (path → "richtext" | "plain" | "code"; a
 * prose field not listed gets suggest()'s), dropUrlCaches, seoAliases,
 * altToAssets. log(line) hears what it does. It answers what it planned or
 * did: { fields: [{ path, from, to, label }], notes, counts, orphans, left }.
 */
export async function cleanUp({ api, convert, options = {}, log = () => {} }) {
  const { dryRun = false, choices = {}, dropUrlCaches = false, seoAliases = false, altToAssets = false } = options;
  const counts = { alts: 0, fields: 0, merged: 0, dropped: 0, values: 0, records: 0, bodies: 0, seo: 0 };
  const notes = [];
  const fields = [];
  const altUpdates = new Map();

  const listAll = async (path, key) => {
    const out = [];
    for (let page = 1; ; page++) {
      const answer = await api("GET", `${path}${path.includes("?") ? "&" : "?"}per=100&page=${page}`);
      out.push(...answer[key]);
      if (answer[key].length < 100) return out;
    }
  };

  const toDoc = (value, wasMarkdown) => {
    if (isDoc(value)) return value;
    if (typeof value !== "string" || !value.trim()) return null;
    const doc = TAG.test(value) ? convert.html(value) : wasMarkdown ? convert.markdown(value) : plainDoc(value);
    return emptyDoc(doc) ? null : doc;
  };

  const choiceFor = (path, f) => choices[path] ?? suggest(path, f);

  // The plan for one owner's fields: the new definitions, and for each old
  // field what becomes of its value. Fields nest through `of` (repeaters,
  // groups).
  function plan(defs, at) {
    const out = [];
    const moves = [];
    const names = new Set(defs.map((f) => f.name));
    for (const f of defs) {
      const path = `${at}.${f.name}`;
      const of = f.of ?? f.fields;
      if (dropUrlCaches && /_url$/.test(f.name) && ["string", "url"].includes(f.type)) {
        const id = defs.find((g) => g.name === f.name.replace(/_url$/, "_id") && g.type === "asset");
        if (id || IMAGE_URL.test(f.name)) {
          moves.push({ name: f.name, drop: true });
          counts.dropped++;
          fields.push({ path, label: f.label, from: f.type, to: "removed (the image is the image field)" });
          continue;
        }
      }
      if (altToAssets && /(^|_)alt$/.test(f.name) && ["string", "text"].includes(f.type)) {
        const assets = defs.filter((g) => g.type === "asset");
        const image = assets.find((g) => g.name === f.name.replace(/alt$/, "id")) ?? (assets.length === 1 ? assets[0] : null);
        if (image) {
          moves.push({ name: f.name, altFor: image.name });
          counts.fields++;
          fields.push({ path, label: f.label, from: f.type, to: "removed (alt text lives on the image)" });
          continue;
        }
      }
      if (PROSE.has(f.type)) {
        const to = choiceFor(path, f);
        if (to === "code") {
          out.push({ ...f, type: "code" });
          counts.fields++;
          fields.push({ path, label: f.label, from: f.type, to: "code", choice: to, field: f });
          continue;
        }
        if (to === "richtext") {
          out.push({ ...f, type: "richtext" });
          moves.push({ name: f.name, convert: f.type === "markdown" });
          counts.fields++;
          fields.push({ path, label: f.label, from: f.type, to: "richtext", choice: to, field: f });
          continue;
        }
        fields.push({ path, label: f.label, from: f.type, to: f.type, choice: to, field: f });
        out.push(f);
        continue;
      }
      if (f.type === "repeater" && of?.length === 1 && PROSE.has(of[0].type) && choiceFor(`${path}.${of[0].name}`, of[0]) === "richtext") {
        const name = names.has("body") ? f.name : "body";
        out.push({ name, label: "Text", type: "richtext", ...(f.required ? { required: true } : {}), ...(f.help ? { help: f.help } : {}) });
        moves.push({ name: f.name, mergeInto: name, sub: of[0].name, convert: of[0].type === "markdown" });
        counts.merged++;
        fields.push({ path: `${path}[].${of[0].name}`, label: f.label, from: `repeater of ${of[0].type}`, to: `one richtext field, ${at}.${name}` });
        continue;
      }
      // Already rich text (a run that stopped halfway, an earlier one): any
      // value that isn't a document yet is still converted, as Markdown.
      if (f.type === "richtext") {
        out.push(f);
        moves.push({ name: f.name, convert: true });
        continue;
      }
      if ((f.type === "repeater" || f.type === "group") && of?.length) {
        const inner = plan(of, path);
        out.push({ ...f, [f.of ? "of" : "fields"]: inner.fields });
        if (inner.moves.length) moves.push({ name: f.name, nested: inner.moves, repeater: f.type === "repeater" });
        continue;
      }
      out.push(f);
    }
    return { fields: out, moves };
  }

  // data with a plan's moves made; whether anything changed.
  function apply(data, moves) {
    if (!data || typeof data !== "object") return false;
    let changed = false;
    // A URL cache no schema declares any more, beside its asset id.
    if (dropUrlCaches) {
      for (const k of Object.keys(data)) {
        if (/_url$/.test(k) && (IMAGE_URL.test(k) || k.replace(/_url$/, "_id") in data) && !moves.some((m) => m.name === k)) {
          delete data[k];
          counts.dropped++;
          changed = true;
        }
      }
    }
    for (const m of moves) {
      if (!(m.name in data)) continue;
      const v = data[m.name];
      if (m.drop) {
        delete data[m.name];
        changed = true;
      } else if (m.altFor) {
        const id = data[m.altFor];
        if (typeof v === "string" && v.trim() && id && !altUpdates.has(String(id))) altUpdates.set(String(id), v.trim());
        delete data[m.name];
        changed = true;
      } else if (m.mergeInto) {
        if (!Array.isArray(v)) continue;
        const content = v.flatMap((item) => toDoc(item?.[m.sub], m.convert)?.content ?? []);
        delete data[m.name];
        if (content.length) data[m.mergeInto] = { type: "doc", content };
        counts.values++;
        changed = true;
      } else if (m.nested) {
        for (const item of m.repeater ? (Array.isArray(v) ? v : []) : [v]) changed = apply(item, m.nested) || changed;
      } else if (!isDoc(v)) {
        if (v === null || v === undefined) continue;
        const doc = toDoc(v, m.convert);
        if (doc) data[m.name] = doc;
        else delete data[m.name];
        counts.values++;
        changed = true;
      }
    }
    return changed;
  }

  function aliasSeo(seo) {
    if (!seoAliases || !seo || typeof seo !== "object") return false;
    let changed = false;
    for (const [old, std] of Object.entries(SEO_ALIASES)) {
      if (!(old in seo)) continue;
      let v = seo[old];
      if (std === "noindex") v = v === true || v === 1 || v === "1" || v === "true";
      if (seo[std] === undefined || seo[std] === null || seo[std] === "") seo[std] = v;
      delete seo[old];
      changed = true;
    }
    if (typeof seo.noindex === "number") {
      seo.noindex = seo.noindex === 1;
      changed = true;
    }
    if (changed) counts.seo++;
    return changed;
  }

  const blockTypes = (await api("GET", "/api/block_types")).block_types;
  const collections = (await api("GET", "/api/collections")).collections;
  const globals = await Promise.all((await api("GET", "/api/globals")).globals.map(async (g) => (await api("GET", `/api/globals/${g.slug}`)).global));
  const readPages = async () => Promise.all((await listAll("/api/pages?sort=path", "pages")).map(async (s) => (await api("GET", `/api/pages/${encodePath(s.path)}`)).page));
  const readEntries = async (c) => Promise.all((await listAll(`/api/collections/${c.slug}/entries`, "entries")).map(async (s) => (await api("GET", `/api/collections/${c.slug}/entries/${encodeURIComponent(s.slug)}`)).entry));

  // Before anything is written: an image URL with no image beside it would be
  // an image lost, so the run stops and says where.
  const orphans = [];
  if (dropUrlCaches) {
    const look = (v, at) => {
      if (Array.isArray(v)) return v.forEach((x, i) => look(x, `${at}[${i}]`));
      if (!v || typeof v !== "object" || isDoc(v)) return;
      for (const [k, x] of Object.entries(v)) {
        if (IMAGE_URL.test(k) && typeof x === "string" && x.trim() && !v[k.replace(/_url$/, "_id")]) orphans.push(`${at}.${k} = ${x}`);
        look(x, `${at}.${k}`);
      }
    };
    for (const p of await readPages()) look(p.blocks, `page ${p.path}`);
    for (const c of collections) for (const e of await readEntries(c)) look({ frontmatter: e.frontmatter, blocks: e.blocks }, `entry ${c.slug}/${e.slug}`);
    for (const g of globals) look(g.data, `global ${g.slug}`);
    if (orphans.length) return { fields, notes, counts, orphans, left: [] };
  }

  const blockPlans = {};
  const blockFields = {}; // each block type's fields after, for the check
  for (const bt of blockTypes) {
    const p = plan(bt.fields ?? [], bt.slug);
    blockPlans[bt.slug] = p.moves;
    blockFields[bt.slug] = p.fields;
    if (p.moves.length && !dryRun && JSON.stringify(p.fields) !== JSON.stringify(bt.fields ?? [])) {
      await api("PATCH", `/api/block_types/${bt.slug}`, { block_type: { fields: p.fields } });
      log(`block type ${bt.slug}: fields changed`);
    }
  }
  const collectionPlans = {};
  const collectionFields = {};
  for (const c of collections) {
    const defs = (await api("GET", `/api/collections/${c.slug}`)).collection.schema?.fields ?? [];
    const p = plan(defs, c.slug);
    collectionPlans[c.slug] = p.moves;
    collectionFields[c.slug] = p.fields;
    if (p.moves.length && !dryRun && JSON.stringify(p.fields) !== JSON.stringify(defs)) {
      await api("PATCH", `/api/collections/${c.slug}/schema`, { fields: p.fields });
      log(`collection ${c.slug}: fields changed`);
    }
  }
  // A global's schema and data change in one save: the CMS checks its data
  // against its schema whenever either changes.
  const globalPlans = {};
  for (const g of globals) globalPlans[g.slug] = plan(g.fields ?? [], g.slug);

  const applyBlocks = (blocks) => {
    let changed = false;
    for (const b of Array.isArray(blocks) ? blocks : []) {
      changed = apply(b.data, blockPlans[b.type] ?? []) || changed;
      for (const v of Object.values(b.data ?? {})) {
        if (Array.isArray(v) && v.some((x) => x?.type && x?.data)) changed = applyBlocks(v) || changed;
      }
    }
    return changed;
  };

  const save = async (path, kind, body, label) => {
    counts.records++;
    log(`${dryRun ? "would save" : "saved"} ${label}`);
    if (!dryRun) await api("PATCH", path, { [kind]: body });
  };

  for (const page of await readPages()) {
    const body = {};
    if (applyBlocks(page.blocks)) body.blocks = page.blocks;
    if (aliasSeo(page.seo)) body.seo = page.seo;
    if (Object.keys(body).length) await save(`/api/pages/${encodePath(page.path)}`, "page", { ...body, lock_version: page.lock_version }, `page ${page.path}`);
  }
  for (const c of collections) {
    for (const entry of await readEntries(c)) {
      const body = {};
      if (apply(entry.frontmatter, collectionPlans[c.slug] ?? [])) body.frontmatter = entry.frontmatter;
      if (applyBlocks(entry.blocks)) body.blocks = entry.blocks;
      if (typeof entry.body_markdown === "string" && TAG.test(entry.body_markdown)) {
        body.body_markdown = convert.toMarkdown(convert.html(entry.body_markdown));
        counts.bodies++;
      }
      if (aliasSeo(entry.seo)) body.seo = entry.seo;
      if (Object.keys(body).length) await save(`/api/collections/${c.slug}/entries/${encodeURIComponent(entry.slug)}`, "entry", { ...body, lock_version: entry.lock_version }, `entry ${c.slug}/${entry.slug}`);
    }
  }
  for (const g of globals) {
    const { fields: defs, moves } = globalPlans[g.slug];
    const data = g.data ?? {};
    const changed = apply(data, moves);
    if (!changed && JSON.stringify(defs) === JSON.stringify(g.fields ?? [])) continue;
    await save(`/api/globals/${g.slug}`, "global", { schema: { fields: defs }, data, lock_version: g.lock_version }, `global ${g.slug}`);
  }

  // The alt texts found go on their images, where an image has none yet; one
  // that already has its own keeps it.
  for (const [id, alt] of altUpdates) {
    let asset;
    try {
      asset = (await api("GET", `/api/assets/${id}`)).asset;
    } catch {
      notes.push(`asset ${id} isn't there: its alt text "${alt}" wasn't kept`);
      continue;
    }
    if ((asset.alt ?? "").trim()) {
      if (asset.alt.trim() !== alt) notes.push(`asset ${id} keeps its own alt ("${asset.alt}"), not "${alt}"`);
      continue;
    }
    counts.alts++;
    log(`${dryRun ? "would set" : "set"} asset ${id}'s alt: ${alt}`);
    if (!dryRun) await api("PATCH", `/api/assets/${id}`, { asset: { alt } });
  }

  // Checked again, from what the CMS now answers: HTML left in a text
  // field (code fields hold markup on purpose), or a URL cache left.
  const left = [];
  if (!dryRun) {
    const TEXT = new Set(["string", "text", "markdown"]);
    const caches = (v, at) => {
      if (!dropUrlCaches || !v || typeof v !== "object" || Array.isArray(v)) return;
      for (const k of Object.keys(v)) if (/_url$/.test(k) && (IMAGE_URL.test(k) || k.replace(/_url$/, "_id") in v)) left.push(`URL cache at ${at}.${k}`);
    };
    const scan = (data, defs, at) => {
      caches(data, at);
      for (const f of defs) {
        const v = data?.[f.name];
        if (v === undefined || v === null) continue;
        const of = f.of ?? f.fields;
        if (TEXT.has(f.type) && typeof v === "string" && TAG.test(v)) left.push(`HTML at ${at}.${f.name}`);
        else if (f.type === "repeater" && Array.isArray(v)) v.forEach((item, i) => scan(item, of ?? [], `${at}.${f.name}[${i}]`));
        else if (f.type === "group") scan(v, of ?? [], `${at}.${f.name}`);
        else if (f.type === "blocks") scanBlocks(v, `${at}.${f.name}`);
      }
    };
    const scanBlocks = (blocks, at) => (Array.isArray(blocks) ? blocks : []).forEach((b, i) => scan(b.data, blockFields[b.type] ?? [], `${at}[${i}] ${b.type}`));
    for (const p of await readPages()) scanBlocks(p.blocks, `page ${p.path}`);
    for (const c of collections) {
      for (const e of await readEntries(c)) {
        scan(e.frontmatter, collectionFields[c.slug] ?? [], `entry ${c.slug}/${e.slug}`);
        scanBlocks(e.blocks, `entry ${c.slug}/${e.slug}`);
        if (typeof e.body_markdown === "string" && TAG.test(e.body_markdown)) left.push(`HTML at entry ${c.slug}/${e.slug}.body_markdown`);
      }
    }
    for (const g of globals) scan((await api("GET", `/api/globals/${g.slug}`)).global.data, globalPlans[g.slug].fields, `global ${g.slug}`);
  }
  return { fields, notes, counts, orphans, left };
}

function encodePath(path) {
  return String(path).split("/").map(encodeURIComponent).join("/");
}
