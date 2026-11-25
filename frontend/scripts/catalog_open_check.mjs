#!/usr/bin/env node
// Browser-free checks on opening a service from the Catalog (src/render/catalog.ts
// and src/render/catalog_open.ts).
//
//   1. EVERY ROW OPENS ITS SERVICE. A click on a row, a service with only Visit
//      (a desktop app) included, opens that service's setup card.
//   2. THE KEYBOARD REACHES IT. The row's name is a real button, so Tab stops on
//      it and Enter or Space opens the service.
//   3. THE ROW'S OWN BUTTONS KEEP THEIR JOB. A click on Add, Manage, Sign up or
//      Visit does only what that button does, never also opens the card.
//   4. IT LOOKS CLICKABLE. The stylesheet gives the row a pointer cursor and the
//      name an underline on hover.
//
//   node scripts/catalog_open_check.mjs      # against ./.harness-build and src/style.css

import { readFileSync } from "node:fs";
import { catalogRow } from "../.harness-build/render/catalog.js";
import { catalogOpenTarget, wireCatalogRows } from "../.harness-build/render/catalog_open.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const wipter = {
  slug: "wipter", name: "Wipter", category: "depin", manualOnly: true,
  shortDescription: "Bandwidth sharing", description: "", platforms: ["windows", "macos"],
  website: "https://wipter.com", referral: { signupUrl: "https://wipter.com/register?ref=x" },
};
const docker = {
  slug: "honeygain", name: "Honeygain", category: "bandwidth", manualOnly: false,
  shortDescription: "Bandwidth sharing", description: "", platforms: ["linux"],
  website: "https://honeygain.com", referral: { signupUrl: "https://r.honeygain.me/x" },
};

// A tiny DOM for one rendered row: its elements, with closest() and attributes.
function parseRow(html) {
  const els = [];
  const stack = [];
  for (const m of html.matchAll(/<(\/?)([a-zA-Z][\w-]*)([^>]*)>/g)) {
    const [, close, tag, rest] = m;
    if (close) { while (stack.length && stack.pop().tag !== tag.toLowerCase()); continue; }
    const attrs = {};
    for (const a of rest.matchAll(/([^\s=\/]+)(?:="([^"]*)")?/g)) attrs[a[1]] = a[2] ?? "";
    const el = {
      tag: tag.toLowerCase(), attrs, parent: stack.at(-1) || null,
      hasAttribute(n) { return n in this.attrs; },
      closest(sel) {
        const want = sel.split(",").map((s) => s.trim());
        for (let e = this; e; e = e.parent) if (want.includes(e.tag)) return e;
        return null;
      },
    };
    els.push(el);
    if (!["input", "img", "br", "path", "rect"].includes(el.tag) && !/\/$/.test(rest.trim())) stack.push(el);
  }
  return els;
}

function rowParts(html) {
  const els = parseRow(html);
  const row = els.find((e) => e.tag === "tr");
  const dataset = {};
  for (const [k, v] of Object.entries(row?.attrs || {})) {
    if (k.startsWith("data-")) dataset[k.slice(5).replace(/-(\w)/g, (_, c) => c.toUpperCase())] = v;
  }
  return { els, row: row && { ...row, dataset }, buttons: els.filter((e) => e.tag === "button") };
}

// RULE 1 and RULE 3
for (const [name, service, deployment] of [["a Visit-only desktop app", wipter, undefined], ["a Docker service", docker, undefined], ["a deployed service", docker, { slug: "honeygain", status: "running" }]]) {
  const { els, row, buttons } = rowParts(catalogRow(service, deployment, false));
  check(`RULE 1: ${name}'s row names its service`, row?.dataset.catalogOpen === service.slug, JSON.stringify(row?.dataset));
  const cell = els.find((e) => e.tag === "td");
  check(`RULE 1: a click on ${name}'s row opens it`, catalogOpenTarget(row, cell) === service.slug);
  const sub = els.find((e) => e.attrs.class === "cell-sub");
  check(`RULE 1: a click on ${name}'s description opens it`, catalogOpenTarget(row, sub) === service.slug);
  const nameButton = buttons.find((b) => "data-catalog-open-name" in b.attrs);
  check(`RULE 2: ${name}'s name is a button`, !!nameButton && nameButton.attrs.type === "button" && !("tabindex" in nameButton.attrs) && !("disabled" in nameButton.attrs), JSON.stringify(nameButton?.attrs));
  check(`RULE 2: Enter on ${name}'s name opens it`, catalogOpenTarget(row, nameButton) === service.slug);
  const own = buttons.filter((b) => !("data-catalog-open-name" in b.attrs));
  check(`RULE 3: ${name}'s row has its own buttons`, own.length >= 1, String(own.length));
  for (const b of own) {
    check(`RULE 3: a click on ${name}'s ${b.attrs["data-service"] !== undefined ? "Add/Manage" : "link"} button does not also open the card`, catalogOpenTarget(row, b) === null, JSON.stringify(b.attrs));
  }
}

// The wiring main.ts runs: one listener per row, open(slug) on a click.
{
  const listeners = [];
  const fakeRow = { dataset: { catalogOpen: "wipter" }, addEventListener: (type, fn) => listeners.push([type, fn]) };
  const scope = { querySelectorAll: (sel) => (sel === "[data-catalog-open]" ? [fakeRow] : []) };
  const opened = [];
  wireCatalogRows(scope, (slug) => opened.push(slug));
  check("RULE 1: each row gets a click listener", listeners.length === 1 && listeners[0][0] === "click", JSON.stringify(listeners.map((l) => l[0])));
  const { els } = rowParts(catalogRow(wipter, undefined, false));
  listeners[0][1]({ target: els.find((e) => e.tag === "td") });
  listeners[0][1]({ target: els.find((e) => e.tag === "button" && "data-url" in e.attrs) });
  check("RULE 1/3: a row click opens the service, a Visit click does not", JSON.stringify(opened) === JSON.stringify(["wipter"]), JSON.stringify(opened));
}

// RULE 4
{
  const css = readFileSync(new URL("../src/style.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  check("RULE 4: a catalog row shows a pointer", /tr\.catalog-row\s*\{[^}]*cursor:\s*pointer/.test(css));
  check("RULE 4: the row's name is underlined on hover", /\.catalog-row:hover \.svc-open\s*\{[^}]*text-decoration:\s*underline/.test(css));
  check("RULE 4: the name button has no button chrome", /\.svc-open\s*\{[^}]*background:\s*none[^}]*border:\s*0/.test(css));
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} catalog open checks FAILED`);
  process.exit(1);
}
console.log(`catalog open checks: ${checks} passed`);
