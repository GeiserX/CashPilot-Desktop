#!/usr/bin/env node
// Browser-free checks on the top bar's tabs, its Add service button and the
// catalog row's action, against the HTML the real render functions return.
//
//   1. ADD SERVICE IS A PRIMARY BUTTON, NOT A TAB. The tabs are Dashboard, Fleet,
//      Activity, Catalog and Settings; none of them opens the wizard.
//   2. THE BUTTON OPENS THE WIZARD and keeps an aria-label, because on a narrow
//      window only "+" shows. Its visible label sits in its own span so the CSS
//      can hide it.
//   3. ON THE WIZARD THE BUTTON IS THE CURRENT PAGE, and no tab claims to be.
//   4. A CATALOG ROW OFFERS "Add" FOR A SERVICE NOT RUNNING HERE, "Manage" for one
//      that is, and both carry data-service, which main.ts turns into
//      openWizard(slug) at the setup step.
//
//   node scripts/topbar_render_check.mjs      # against ./.harness-build

import { renderAddServiceButton, renderTopnav } from "../.harness-build/render/topbar.js";
import { catalogRow } from "../.harness-build/render/catalog.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const views = (html) => [...html.matchAll(/data-view="([^"]*)"/g)].map((m) => m[1]);
const current = (html) => [...html.matchAll(/<button\b[^>]*aria-current="page"[^>]*>/g)].map((m) => m[0]);

// RULE 1
const nav = renderTopnav("dashboard");
check("RULE 1: the tabs are Dashboard, Fleet, Activity, Catalog, Settings, in that order",
  JSON.stringify(views(nav)) === JSON.stringify(["dashboard", "fleet", "activity", "catalog", "settings"]), nav);
check("RULE 1: no tab opens the wizard", !views(nav).includes("wizard") && !/Add services?/.test(nav), nav);

// RULE 2
const button = renderAddServiceButton("dashboard");
check("RULE 2: the button opens the wizard", views(button).join() === "wizard", button);
check("RULE 2: it is a primary button", /class="btn btn-primary btn-sm topbar-add"/.test(button), button);
check("RULE 2: it is labelled \"Add service\" for screen readers", /aria-label="Add service"/.test(button), button);
check("RULE 2: the visible label is in .topbar-add-label, so a narrow window shows only \"+\"",
  /<span aria-hidden="true">\+<\/span><span class="topbar-add-label" aria-hidden="true"> Add service<\/span>/.test(button), button);
check("RULE 2: off the wizard it is not the current page", current(button).length === 0, button);

// RULE 3
const onWizard = renderAddServiceButton("wizard");
check("RULE 3: on the wizard the button is the current page",
  current(onWizard).length === 1 && /topbar-add active/.test(onWizard), onWizard);
check("RULE 3: on the wizard no tab is the current page", current(renderTopnav("wizard")).length === 0, renderTopnav("wizard"));
check("CONTROL: on Catalog its tab is the current page",
  current(renderTopnav("catalog")).length === 1 && /data-view="catalog" aria-current="page"/.test(renderTopnav("catalog")), renderTopnav("catalog"));

// RULE 4
const honeygain = {
  name: "Honeygain",
  slug: "honeygain",
  category: "bandwidth",
  status: "active",
  website: "https://honeygain.com",
  description: "Bandwidth sharing",
  shortDescription: "Bandwidth sharing",
  platforms: ["docker"],
  referral: { signupUrl: "https://join.honeygain.com/X", code: "", program: null, bonus: { referrer: "", referee: "" } },
  docker: { image: "honeygain/honeygain@sha256:abc", env: [], ports: [], volumes: [] },
  manualOnly: false,
};
const actions = (html) => [...html.matchAll(/<button\b[^>]*data-service="([^"]*)"[^>]*>([^<]*)<\/button>/g)].map((m) => [m[1], m[2]]);
const fresh = catalogRow(honeygain, undefined, false);
check("RULE 4: a service not running here offers Add, opening the wizard on it",
  JSON.stringify(actions(fresh)) === JSON.stringify([["honeygain", "Add"]]), fresh);
check("RULE 4: the row no longer says Deploy", !/>Deploy</.test(fresh), fresh);
const running = catalogRow(honeygain, { slug: "honeygain", status: "running" }, false);
check("RULE 4: a running service keeps Manage", JSON.stringify(actions(running)) === JSON.stringify([["honeygain", "Manage"]]), running);
const manual = catalogRow({ ...honeygain, manualOnly: true }, undefined, false);
check("CONTROL: a manual-only service has no Add, only Visit", actions(manual).length === 0 && />Visit</.test(manual), manual);

if (failures) {
  console.error(`\n${failures} of ${checks} top bar checks failed.`);
  process.exit(1);
}
console.log(`Top bar: ${checks} checks passed. Add service is a primary button with an aria-label, no tab opens the wizard, catalog rows say Add.`);
