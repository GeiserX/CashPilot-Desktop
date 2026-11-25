#!/usr/bin/env node
// Browser-free checks on keyboard focus: where Tab goes first on the first-run
// screens, and whether the stylesheet draws a ring a person can see.
//
//   1. THE MAIN ACTION FIRST. On the Welcome screen the first Tab stop is
//      "Get started". On every first-run screen, and on any screen, the window
//      controls come after the screen's own controls. When Close came first, a
//      click that redrew the screen left focus nowhere, and Tab then Enter quit
//      the app.
//   2. THE WINDOW CONTROLS STAY REACHABLE. Close, Minimise and Maximise keep a
//      label and are never taken out of the Tab order. No positive tabindex
//      anywhere: that would make the order differ from what the screen shows.
//   3. A RING ON EVERY CONTROL. Under the stylesheet's own cascade, a button, a
//      link, an input, a select, a textarea, a summary and each title bar button
//      gets, on :focus-visible, a solid outline at least 2px wide whose colour has
//      at least 3:1 contrast with every dark background of the theme (WCAG 2.2,
//      1.4.11 and 2.4.13). A rule that removes the outline on :focus without a
//      later :focus-visible rule giving it back fails here.
//   4. THE TITLE BAR RING CLEARS THE DOT. The 12px coloured dots get their ring
//      at least 2px away, against the title bar's own background, so the ring is
//      not lost in the red of Close.
//
//   node scripts/focus_check.mjs      # against ./.harness-build and src/style.css

import { readFileSync } from "node:fs";
import { gatePage, withChrome } from "../.harness-build/render/chrome.js";
import { firstRunSteps, renderGateSteps, renderJoinForm, renderModeChoice, renderWelcome } from "../.harness-build/render/mode.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

// ---------- Tab stops in rendered HTML ----------

// Every opening tag, in document order, with its attributes.
function tags(html) {
  const out = [];
  const re = /<([a-zA-Z][a-zA-Z0-9-]*)((?:\s+[^\s=>\/]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+))?)*)\s*\/?>/g;
  for (const m of html.matchAll(re)) {
    const attrs = {};
    for (const a of m[2].matchAll(/([^\s=>\/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g)) {
      attrs[a[1].toLowerCase()] = a[2] ?? a[3] ?? a[4] ?? "";
    }
    out.push({ tag: m[1].toLowerCase(), attrs });
  }
  return out;
}

// The elements Tab stops on, in the order it visits them (no positive tabindex
// is allowed, so document order is Tab order).
function tabStops(html) {
  return tags(html).filter(({ tag, attrs }) => {
    if ("tabindex" in attrs) return Number(attrs.tabindex) >= 0;
    if ("disabled" in attrs && ["button", "input", "select", "textarea"].includes(tag)) return false;
    if (tag === "input") return attrs.type !== "hidden";
    if (tag === "a") return "href" in attrs;
    return ["button", "select", "textarea", "summary"].includes(tag);
  });
}

const isWindowControl = (t) => "data-window-action" in t.attrs;
const describe = (t) => `<${t.tag}${Object.entries(t.attrs).map(([k, v]) => ` ${k}="${v}"`).join("")}>`;

// The brand bar main.ts draws on the first-run screens: a logo and a name, no control.
const bar = `<header class="gate-bar"><span class="topbar-brand"><svg aria-hidden="true"></svg><span>CashPilot</span></span></header>`;
const screens = {
  welcome: gatePage(bar, `${renderGateSteps(firstRunSteps(""), "welcome")}${renderWelcome()}`),
  mode: gatePage(bar, `${renderGateSteps(firstRunSteps(""), "mode")}${renderModeChoice()}`, { wide: true }),
  join: gatePage(bar, `${renderGateSteps(firstRunSteps("follower"), "join")}<h1 class="gate-title">Join my CashPilot</h1>${renderJoinForm()}`, { wide: true }),
};

// RULE 1
{
  const stops = tabStops(screens.welcome);
  check("RULE 1: Welcome's first Tab stop is Get started", stops[0]?.attrs.id === "get-started", stops[0] ? describe(stops[0]) : "no Tab stop");
}
{
  const stops = tabStops(screens.mode);
  check("RULE 1: the mode step's first Tab stop is the first mode card", stops[0]?.attrs["data-mode-choice"] === "lead", stops[0] ? describe(stops[0]) : "no Tab stop");
}
{
  const stops = tabStops(screens.join);
  check("RULE 1: the join step's first Tab stop is the paste field", stops[0]?.tag === "textarea" && stops[0]?.attrs.name === "block", stops[0] ? describe(stops[0]) : "no Tab stop");
}
for (const [name, html] of Object.entries({ ...screens, "an app page": withChrome(`<main><button id="x">Refresh</button><a href="#y">Docs</a></main>`) })) {
  const stops = tabStops(html);
  const firstControl = stops.findIndex(isWindowControl);
  const lastOwn = stops.map((t) => !isWindowControl(t)).lastIndexOf(true);
  check(`RULE 1: on ${name} the window controls come after the screen's own controls`, firstControl > lastOwn && lastOwn >= 0,
    stops.map(describe).join(" "));
}

// RULE 2
for (const [name, html] of Object.entries(screens)) {
  const controls = tabStops(html).filter(isWindowControl);
  check(`RULE 2: on ${name} Close, Minimise and Maximise are all Tab stops`,
    ["close", "minimise", "maximise"].every((a) => controls.some((t) => t.attrs["data-window-action"] === a)),
    controls.map(describe).join(" "));
  check(`RULE 2: on ${name} every window control has a label`, controls.length === 3 && controls.every((t) => (t.attrs["aria-label"] || "").trim() !== ""),
    controls.map(describe).join(" "));
  const positive = tags(html).filter((t) => "tabindex" in t.attrs && Number(t.attrs.tabindex) > 0);
  check(`RULE 2: on ${name} no element has a positive tabindex`, positive.length === 0, positive.map(describe).join(" "));
}

// ---------- The stylesheet's cascade for :focus-visible ----------

const css = readFileSync(new URL("../src/style.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// Rules in source order. A rule inside an @media block keeps its condition, and
// the checks below ignore it (none of them is about a media condition).
function parseRules(text, media = null, out = []) {
  let i = 0;
  while (i < text.length) {
    const open = text.indexOf("{", i);
    if (open < 0) break;
    const prelude = text.slice(i, open).trim();
    let depth = 1;
    let j = open + 1;
    while (j < text.length && depth > 0) {
      if (text[j] === "{") depth++;
      else if (text[j] === "}") depth--;
      j++;
    }
    const body = text.slice(open + 1, j - 1);
    if (prelude.startsWith("@")) {
      parseRules(body, prelude, out);
    } else {
      const decls = {};
      for (const d of body.split(";")) {
        const k = d.indexOf(":");
        if (k > 0) decls[d.slice(0, k).trim().toLowerCase()] = d.slice(k + 1).trim().replace(/\s*!important$/, "");
      }
      out.push({ selectors: prelude.split(",").map((s) => s.trim()).filter(Boolean), decls, media, order: out.length });
    }
    i = j;
  }
  return out;
}

const rules = parseRules(css);
const vars = Object.assign({}, ...rules.filter((r) => r.selectors.includes(":root") && !r.media).map((r) => r.decls));
function resolve(value, depth = 0) {
  if (depth > 8) return value;
  return value.replace(/var\((--[\w-]+)(?:\s*,\s*([^)]*))?\)/g, (_, name, fallback) => resolve(vars[name] ?? fallback ?? "", depth + 1));
}

// Does one simple compound selector (no combinator) match this element while it
// has keyboard focus? Pseudo-classes other than :focus and :focus-visible never
// match: the element is focused, not hovered, disabled or checked.
function matches(selector, el) {
  if (/[\s>+~]/.test(selector.replace(/\[[^\]]*\]/g, "").replace(/\([^)]*\)/g, ""))) return null;
  const m = selector.match(/^([a-zA-Z*][\w-]*)?((?:\.[\w-]+|:[\w-]+(?:\([^)]*\))?|::[\w-]+|\[[^\]]*\])*)$/);
  if (!m) return null;
  const tag = m[1];
  if (tag && tag !== "*" && tag !== el.tag) return null;
  const parts = m[2].match(/\.[\w-]+|::?[\w-]+(?:\([^)]*\))?|\[[^\]]*\]/g) || [];
  let classes = 0;
  for (const p of parts) {
    if (p.startsWith("::")) return null; // a pseudo-element is another box
    if (p.startsWith(".")) {
      if (!el.classes.includes(p.slice(1))) return null;
      classes++;
    } else if (p === ":focus" || p === ":focus-visible") {
      classes++;
    } else {
      return null;
    }
  }
  return [0, classes, tag && tag !== "*" ? 1 : 0];
}

const cmp = (a, b) => a[0] - b[0] || a[1] - b[1] || a[2] - b[2];

// The outline the element ends with on :focus-visible, after the cascade.
function focusOutline(el) {
  const hits = [];
  for (const r of rules) {
    if (r.media) continue;
    for (const s of r.selectors) {
      const spec = matches(s, el);
      if (spec) hits.push({ spec, order: r.order, decls: r.decls });
    }
  }
  hits.sort((a, b) => cmp(a.spec, b.spec) || a.order - b.order);
  const out = { width: "medium", style: "none", color: "currentcolor", offset: "0" };
  for (const h of hits) {
    for (const [k, v] of Object.entries(h.decls)) {
      const val = resolve(v);
      if (k === "outline") {
        out.width = "medium"; out.style = "none"; out.color = "currentcolor";
        if (/^(none|0)$/.test(val)) continue;
        for (const tok of val.split(/\s+(?![^(]*\))/)) {
          if (/^(solid|dashed|dotted|double|groove|ridge|inset|outset|auto|none)$/.test(tok)) out.style = tok;
          else if (/^(-?[\d.]+(px|em|rem)?|thin|medium|thick)$/.test(tok)) out.width = tok;
          else out.color = tok;
        }
      } else if (k === "outline-width") out.width = val;
      else if (k === "outline-style") out.style = val;
      else if (k === "outline-color") out.color = val;
      else if (k === "outline-offset") out.offset = val;
    }
  }
  return out;
}

function rgb(color) {
  const hex = color.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i);
  if (!hex) return null;
  const h = hex[1].length === 3 ? hex[1].replace(/./g, "$&$&") : hex[1];
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
}
function luminance([r, g, b]) {
  const f = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
}
function contrast(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}
const px = (v) => (v === "thin" ? 1 : v === "medium" ? 3 : v === "thick" ? 5 : parseFloat(v));

// The dark surfaces a ring can sit on: the page, the bars, the cards, the inputs
// and the title bar.
const backgrounds = ["--bg-primary", "--bg-secondary", "--bg-tertiary", "--bg-hover", "--bg-input"].map((v) => [v, rgb(resolve(`var(${v})`))]);
check("RULE 3: the theme's backgrounds resolve to colours", backgrounds.every(([, c]) => c), JSON.stringify(backgrounds));

const targets = [
  { name: "a button", tag: "button", classes: [] },
  { name: "the primary button", tag: "button", classes: ["btn", "btn-primary", "btn-lg"] },
  { name: "a mode card", tag: "button", classes: ["mode-card"] },
  { name: "a link", tag: "a", classes: [] },
  { name: "an input", tag: "input", classes: [] },
  { name: "a select", tag: "select", classes: [] },
  { name: "a textarea", tag: "textarea", classes: [] },
  { name: "a summary", tag: "summary", classes: [] },
  { name: "the Close button", tag: "button", classes: ["traffic", "close"], titlebar: true },
  { name: "the Minimise button", tag: "button", classes: ["traffic", "minimise"], titlebar: true },
  { name: "the Maximise button", tag: "button", classes: ["traffic", "maximise"], titlebar: true },
];

// RULE 3 and RULE 4
for (const el of targets) {
  const o = focusOutline(el);
  const shown = JSON.stringify(o);
  check(`RULE 3: ${el.name} has a solid focus ring`, o.style === "solid", shown);
  check(`RULE 3: ${el.name}'s focus ring is at least 2px wide`, px(o.width) >= 2, shown);
  const color = rgb(o.color);
  check(`RULE 3: ${el.name}'s focus ring has a set colour`, !!color, shown);
  if (color) {
    for (const [name, bg] of backgrounds) {
      if (!bg) continue;
      const ratio = contrast(color, bg);
      check(`RULE 3: ${el.name}'s focus ring has 3:1 contrast on ${name}`, ratio >= 3, `${o.color} on ${name}: ${ratio.toFixed(2)}:1`);
    }
  }
  if (el.titlebar) {
    check(`RULE 4: ${el.name}'s ring sits at least 2px off the dot`, px(o.offset) >= 2, shown);
  }
}

if (failures > 0) {
  console.error(`\nfocus: ${failures} of ${checks} checks failed`);
  process.exit(1);
}
console.log(`focus: ${checks} checks passed`);
