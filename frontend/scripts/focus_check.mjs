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
//      not lost in the red of Close. The Windows buttons touch the window's top
//      and right edges, so their ring sits at least 2px inside the button.
//   5. WINDOWS GETS WINDOWS CONTROLS. On Windows and Linux the title bar shows
//      Minimise, Maximise and Close, in that order, at the top right, each at
//      least 44px wide and as tall as the bar; on macOS the dots stay. The
//      screens checked here are the ones main.ts draws (render/firstrun.ts).
//   6. THE SWITCH USES THE SAME RING. A switch's track gets the theme's focus
//      ring colour, not a text colour.
//
//   node scripts/focus_check.mjs      # against ./.harness-build and src/style.css

import { readFileSync } from "node:fs";
import { windowPlatform, withChrome } from "../.harness-build/render/chrome.js";
import { firstRunScreen } from "../.harness-build/render/firstrun.js";

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
// The first-run screens exactly as main.ts draws them, on each OS layout.
const PLATFORMS = ["mac", "windows"];
const screensOn = (platform) => ({
  welcome: firstRunScreen("welcome", { bar, path: "", platform }),
  mode: firstRunScreen("mode", { bar, path: "", platform }),
  join: firstRunScreen("join", { bar, path: "follower", platform }),
});

for (const platform of PLATFORMS) {
  const screens = screensOn(platform);
  // RULE 1
  {
    const stops = tabStops(screens.welcome);
    check(`RULE 1 (${platform}): Welcome's first Tab stop is Get started`, stops[0]?.attrs.id === "get-started", stops[0] ? describe(stops[0]) : "no Tab stop");
  }
  {
    const stops = tabStops(screens.mode);
    check(`RULE 1 (${platform}): the mode step's first Tab stop is the first mode card`, stops[0]?.attrs["data-mode-choice"] === "lead", stops[0] ? describe(stops[0]) : "no Tab stop");
  }
  {
    const stops = tabStops(screens.join);
    check(`RULE 1 (${platform}): the join step's first Tab stop is the paste field`, stops[0]?.tag === "textarea" && stops[0]?.attrs.name === "block", stops[0] ? describe(stops[0]) : "no Tab stop");
  }
  for (const [name, html] of Object.entries({ ...screens, "an app page": withChrome(`<main><button id="x">Refresh</button><a href="#y">Docs</a></main>`, platform) })) {
    const stops = tabStops(html);
    const firstControl = stops.findIndex(isWindowControl);
    const lastOwn = stops.map((t) => !isWindowControl(t)).lastIndexOf(true);
    check(`RULE 1 (${platform}): on ${name} the window controls come after the screen's own controls`, firstControl > lastOwn && lastOwn >= 0,
      stops.map(describe).join(" "));
  }

  // RULE 2
  for (const [name, html] of Object.entries(screens)) {
    const controls = tabStops(html).filter(isWindowControl);
    check(`RULE 2 (${platform}): on ${name} Close, Minimise and Maximise are all Tab stops`,
      ["close", "minimise", "maximise"].every((a) => controls.some((t) => t.attrs["data-window-action"] === a)),
      controls.map(describe).join(" "));
    check(`RULE 2 (${platform}): on ${name} every window control has a label`, controls.length === 3 && controls.every((t) => (t.attrs["aria-label"] || "").trim() !== ""),
      controls.map(describe).join(" "));
    const positive = tags(html).filter((t) => "tabindex" in t.attrs && Number(t.attrs.tabindex) > 0);
    check(`RULE 2 (${platform}): on ${name} no element has a positive tabindex`, positive.length === 0, positive.map(describe).join(" "));
  }
}

// RULE 5: the layout each OS gets, and the order of its controls.
{
  const ua = {
    windows: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
    mac: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)",
    linux: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15",
  };
  check("RULE 5: the Windows webview gets the Windows controls", windowPlatform(ua.windows) === "windows", windowPlatform(ua.windows));
  check("RULE 5: the macOS webview keeps the dots", windowPlatform(ua.mac) === "mac", windowPlatform(ua.mac));
  check("RULE 5: Linux gets the Windows layout", windowPlatform(ua.linux) === "windows", windowPlatform(ua.linux));
  const win = screensOn("windows").welcome;
  const winControls = tabStops(win).filter(isWindowControl);
  check("RULE 5: on Windows the controls are Minimise, Maximise, Close, in that order",
    winControls.map((t) => t.attrs["data-window-action"]).join(",") === "minimise,maximise,close", winControls.map(describe).join(" "));
  check("RULE 5: on Windows the controls are not the macOS dots",
    winControls.length === 3 && winControls.every((t) => (t.attrs.class || "").split(/\s+/).includes("win-ctl") && !(t.attrs.class || "").includes("traffic")),
    winControls.map(describe).join(" "));
  check("RULE 5: on Windows the controls sit in the right-hand group", /<div class="window-controls">/.test(win) && !/traffic-lights/.test(win), win.slice(-600));
  const macControls = tabStops(screensOn("mac").welcome).filter(isWindowControl);
  check("RULE 5: on macOS the controls are the dots: Close, Minimise, Maximise",
    macControls.map((t) => t.attrs["data-window-action"]).join(",") === "close,minimise,maximise" && macControls.every((t) => (t.attrs.class || "").includes("traffic")),
    macControls.map(describe).join(" "));
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
  { name: "the Windows Close button", tag: "button", classes: ["win-ctl", "close"], edge: true },
  { name: "the Windows Minimise button", tag: "button", classes: ["win-ctl", "minimise"], edge: true },
  { name: "the Windows Maximise button", tag: "button", classes: ["win-ctl", "maximise"], edge: true },
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
  if (el.edge) {
    check(`RULE 4: ${el.name}'s ring sits at least 2px inside the button`, px(o.offset) <= -2, shown);
  }
}

// RULE 5: the Windows buttons' hit area, from the stylesheet.
{
  const decls = Object.assign({}, ...rules.filter((r) => !r.media && r.selectors.includes(".win-ctl")).map((r) => r.decls));
  const width = px(resolve(decls.width || "0"));
  const height = px(resolve(decls.height || "0"));
  const bar = px(resolve("var(--titlebar-height)"));
  check("RULE 5: a Windows title bar button is at least 44px wide", width >= 44, JSON.stringify(decls));
  check("RULE 5: a Windows title bar button is as tall as the bar", height === bar && bar >= 32, `${height} vs ${bar}`);
  const group = Object.assign({}, ...rules.filter((r) => !r.media && r.selectors.includes(".window-controls")).map((r) => r.decls));
  check("RULE 5: the Windows controls sit at the top right", group.position === "absolute" && px(group.right ?? "x") === 0 && px(group.top ?? "x") === 0, JSON.stringify(group));
}

// RULE 6: a switch's track ring is the theme's focus ring.
{
  const sel = ".switch input:focus-visible + .switch-track";
  const hit = rules.filter((r) => !r.media && r.selectors.includes(sel)).at(-1);
  const outline = hit ? resolve(hit.decls.outline || "") : "";
  const color = outline.split(/\s+/).find((tok) => tok.startsWith("#")) || "";
  check("RULE 6: the switch has a focus rule", !!hit, sel);
  check("RULE 6: the switch's ring is solid and at least 2px", /\bsolid\b/.test(outline) && px(outline.split(/\s+/).find((t) => /px$/.test(t)) || "0") >= 2, outline);
  check("RULE 6: the switch's ring has the focus ring colour", color.toLowerCase() === resolve("var(--focus-ring)").toLowerCase(), `${color} vs ${resolve("var(--focus-ring)")}`);
}

if (failures > 0) {
  console.error(`\nfocus: ${failures} of ${checks} checks failed`);
  process.exit(1);
}
console.log(`focus: ${checks} checks passed`);
