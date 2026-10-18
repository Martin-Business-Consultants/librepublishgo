#!/usr/bin/env node
// Tools › Clean up content, from a shell: the same clean-up (src/lib/cleanup.js)
// through the CMS's API with an Admin token, for when the admin isn't the
// way in. Prose fields become rich text (the editor's own document, made by
// Tiptap with the editor's extensions), and with the flags image URL caches,
// alt text beside images and old SEO keys are dealt with. Idempotent.
//
//   CMS_BASE_URL=http://localhost:8080 CMS_ADMIN_TOKEN=… node scripts/richtext.mjs [--dry-run]
//     [--keep hours_location.address --keep …]   fields that stay plain text (owner.field.path)
//     [--code scripts.items.head --code …]       fields that hold markup on purpose: become `code`
//     [--rich general.description --rich …]      fields that become rich text whatever the guess
//     [--drop-url-caches]                        drop every image_url field and key (the image is its
//                                                asset field), and other <x>_url caches beside an <x>_id
//                                                asset field; refused if one holds the only copy of an image
//     [--seo-aliases]                            seo_title → meta_title and kin, on pages and entries
//     [--alt-to-assets]                          an alt field beside an image field goes: its text moves
//                                                to the image's own alt (where the image has none)
//
// A prose field not named gets the admin's first guess (cleanup.js suggest):
// code for pasted scripts, plain text for addresses, alt text and the
// site's description, rich text for the rest.
import { parseArgs } from "node:util";
import { generateJSON } from "@tiptap/html/server";
import { MarkdownManager } from "@tiptap/markdown";
import { richTextExtensions } from "../src/lib/richtext-extensions.js";
import { cleanUp } from "../src/lib/cleanup.js";

const { values: args } = parseArgs({
  options: {
    "dry-run": { type: "boolean", default: false },
    keep: { type: "string", multiple: true, default: [] },
    code: { type: "string", multiple: true, default: [] },
    rich: { type: "string", multiple: true, default: [] },
    "drop-url-caches": { type: "boolean", default: false },
    "seo-aliases": { type: "boolean", default: false },
    "alt-to-assets": { type: "boolean", default: false },
  },
});
const BASE = (process.env.CMS_BASE_URL || "").replace(/\/+$/, "");
const TOKEN = process.env.CMS_ADMIN_TOKEN;
if (!BASE || !TOKEN) {
  console.error("CMS_BASE_URL and CMS_ADMIN_TOKEN (an Admin-role token) must be set");
  process.exit(2);
}

async function api(method, path, body) {
  const headers = { Authorization: `Bearer ${TOKEN}`, Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path} → ${res.status}: ${text.slice(0, 600)}`);
  return text ? JSON.parse(text) : null;
}

const extensions = richTextExtensions();
const markdown = new MarkdownManager({ extensions });
const convert = { html: (s) => generateJSON(s, extensions), markdown: (s) => markdown.parse(s), toMarkdown: (d) => markdown.serialize(d) };

const choices = {};
for (const p of args.keep) choices[p] = "plain";
for (const p of args.code) choices[p] = "code";
for (const p of args.rich) choices[p] = "richtext";

const dryRun = args["dry-run"];
const r = await cleanUp({
  api, convert, log: (line) => console.log(line),
  options: { dryRun, choices, dropUrlCaches: args["drop-url-caches"], seoAliases: args["seo-aliases"], altToAssets: args["alt-to-assets"] },
});
if (r.orphans.length) {
  console.error(`These image URLs have no image beside them; pick each image in the media library first:\n  ${r.orphans.join("\n  ")}`);
  process.exit(1);
}
for (const f of r.fields) if (f.from !== f.to) console.log(`  ${f.path}: ${f.from} → ${f.to}`);
for (const n of r.notes) console.log(`  ${n}`);
const c = r.counts;
console.log(`${dryRun ? "Dry run: " : ""}${c.fields} fields changed, ${c.merged} repeaters merged, ${c.dropped} URL caches dropped; ` +
  `${c.values} values converted, ${c.bodies} entry bodies' HTML → Markdown, ${c.seo} SEO aliased, ${c.alts} alt texts onto images; ${c.records} records saved.`);
if (r.left.length) {
  console.error(`Still left:\n  ${r.left.join("\n  ")}`);
  process.exit(1);
}
if (!dryRun) console.log("Checked: no HTML in text, no URL caches.");
