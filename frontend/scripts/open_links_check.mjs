#!/usr/bin/env node
// Browser-free checks on external links (src/render/open_links.ts).
//
//   1. ONE CLICK OPENS ONE URL. A page wires its links in wireShellNav and used to
//      wire them again in its own render function, so Visit and Sign up on the
//      Catalog opened the browser twice. Wiring the same page twice must still
//      leave one listener per button.
//   2. THE LINK IS THE BUTTON'S OWN. The URL opened is the button's data-url,
//      unchanged, so a referral link stays exactly as the catalog wrote it.
//   3. A BUTTON WITH NO URL OPENS NOTHING.
//
//   node scripts/open_links_check.mjs      # against ./.harness-build

import { catalogRow } from "../.harness-build/render/catalog.js";
import { wireOpenLinks } from "../.harness-build/render/open_links.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

// A fake button: its dataset and the click listeners added to it.
function fakeButton(url) {
  const listeners = [];
  return {
    dataset: url === undefined ? {} : { url },
    addEventListener: (type, fn) => listeners.push([type, fn]),
    click() { for (const [type, fn] of listeners) if (type === "click") fn({ target: this }); },
  };
}

function fakeScope(buttons) {
  return { querySelectorAll: (sel) => (sel === "[data-url]" ? buttons : []) };
}

// The Catalog row's own Visit and Sign up URLs, as render/catalog.ts writes them.
const wipter = {
  slug: "wipter", name: "Wipter", category: "depin", manualOnly: true,
  shortDescription: "Bandwidth sharing", description: "", platforms: ["windows", "macos"],
  website: "https://wipter.com", referral: { signupUrl: "https://wipter.com/register?ref=x" },
};
const honeygain = {
  slug: "honeygain", name: "Honeygain", category: "bandwidth", manualOnly: false,
  shortDescription: "Bandwidth sharing", description: "", platforms: ["linux"],
  website: "https://honeygain.com", referral: { signupUrl: "https://r.honeygain.me/x" },
};
const rowUrls = (service) => [...catalogRow(service, undefined, false).matchAll(/data-url="([^"]*)"/g)].map((m) => m[1].replace(/&amp;/g, "&"));
const visit = rowUrls(wipter);
const signup = rowUrls(honeygain);
check("the Catalog renders a Visit link for a desktop app", visit.length === 1, JSON.stringify(visit));
check("the Catalog renders a Sign up link for a Docker service", signup.length === 1, JSON.stringify(signup));
check("RULE 2: Visit is the referral link", visit[0] === wipter.referral.signupUrl, JSON.stringify(visit));
check("RULE 2: Sign up is the referral link", signup[0] === honeygain.referral.signupUrl, JSON.stringify(signup));

// RULE 1 and RULE 2: wired once by wireShellNav, then again by the page.
for (const [name, url] of [["Visit", visit[0]], ["Sign up", signup[0]]]) {
  const button = fakeButton(url);
  const scope = fakeScope([button]);
  const opened = [];
  wireOpenLinks(scope, (u) => opened.push(u));
  wireOpenLinks(scope, (u) => opened.push(u));
  button.click();
  check(`RULE 1: one click on ${name} opens one URL`, opened.length === 1, JSON.stringify(opened));
  check(`RULE 2: ${name} opens its own URL unchanged`, opened[0] === url, JSON.stringify(opened));
}

// A redraw makes new buttons: each new one is wired, once.
{
  const opened = [];
  const first = fakeButton("https://example.com/a");
  wireOpenLinks(fakeScope([first]), (u) => opened.push(u));
  const second = fakeButton("https://example.com/b");
  wireOpenLinks(fakeScope([first, second]), (u) => opened.push(u));
  first.click();
  second.click();
  check("RULE 1: after a redraw every button opens once", JSON.stringify(opened) === JSON.stringify(["https://example.com/a", "https://example.com/b"]), JSON.stringify(opened));
}

// RULE 3
{
  const opened = [];
  const button = fakeButton(undefined);
  wireOpenLinks(fakeScope([button]), (u) => opened.push(u));
  button.click();
  check("RULE 3: a button with no URL opens nothing", opened.length === 0, JSON.stringify(opened));
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} open link checks FAILED`);
  process.exit(1);
}
console.log(`open link checks: ${checks} passed`);
