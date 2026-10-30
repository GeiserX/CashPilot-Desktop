#!/usr/bin/env node
// Browser-free checks on the Instances box on a service's setup card and the
// dashboard rows of its extra copies.
//
//   1. A SERVICE THAT CANNOT SCALE HAS THE BOX LOCKED AT 1, WITH THE REASON UNDER IT.
//      The reason is the backend's (one device per IP, a host directory, a port, a
//      native program), so the user is told why, not just stopped.
//   2. A SERVICE THAT CAN SCALE HAS THE BOX OPEN UP TO ITS CEILING, STARTING AT THE
//      RECORDED COUNT, AND THE HINT SAYS WHAT EXTRA COPIES ARE: devices, not
//      bandwidth, where the provider documents several per IP; a warning where it
//      documents nothing.
//   3. LEFT ALONE OR LOCKED, THE BOX SENDS null. The backend reads null as "keep the
//      recorded count", so a redeploy from a card nobody touched cannot scale down.
//   4. THE BOX IS NOT A CREDENTIAL. readWizardForm() collects data-wizard-env inputs.
//   5. NO ANSWER FROM THE BACKEND, OR A MANUAL SERVICE, MEANS NO BOX.
//   6. EACH COPY GETS ITS OWN DASHBOARD ROW, "#N", WITH LOGS AND STOP OR START
//      ADDRESSED TO THAT COPY; THE FIRST ROW IS "#1" ONLY WHEN COPIES EXIST.
//
//   node scripts/instances_check.mjs      # against ./.harness-build

import { copyRows, firstCopyLabel, instancesChoice, instancesSection } from "../.harness-build/render/instances.js";
import { renderWizardServiceSetup } from "../.harness-build/render/wizard.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function box(html) {
  const tag = (html.match(/<input\b[^>]*\bdata-wizard-instances="[^"]*"[^>]*>/) || [])[0] || "";
  const attr = (name) => (tag.match(new RegExp(`\\b${name}="([^"]*)"`)) || [])[1];
  return { tag, value: attr("value"), initial: attr("data-initial"), max: attr("max"), disabled: /\bdisabled\b/.test(tag), env: attr("data-wizard-env") };
}

function service(slug, name, devicesPerIp) {
  return {
    name,
    slug,
    category: "bandwidth",
    status: "active",
    website: "https://example.test",
    description: name,
    shortDescription: name,
    referral: { signupUrl: "", code: "", program: null, bonus: { referrer: "", referee: "" } },
    docker: { image: `example/${slug}:1.0`, env: [], ports: [], volumes: [] },
    requirements: { residentialIp: true, devicesPerIp },
    cashout: { method: "redirect", dashboardUrl: "", minAmount: 0, currency: "USD", notes: "" },
    manualOnly: false,
  };
}

const earnfm = service("earnfm", "EarnFM", 0);
const honeygain = service("honeygain", "Honeygain", 1);
const mystery = service("mystery", "Mystery", null);
const lockedReason = "Honeygain allows one device per IP address, so a second copy on one machine earns nothing.";

// RULE 1
const lockedHtml = instancesSection(honeygain, { max: 1, reason: lockedReason }, 1);
const locked = box(lockedHtml);
check("CONTROL: the section renders an Instances box", locked.tag !== "", lockedHtml);
check("RULE 1: a service that cannot scale has the box disabled at 1", locked.disabled && locked.value === "1" && locked.max === "1", locked.tag);
check("RULE 1: the reason is under the box", lockedHtml.includes(lockedReason) && lockedHtml.includes("add machines on other connections"), lockedHtml);

// RULE 2
const openHtml = instancesSection(earnfm, { max: 10, reason: "" }, 3);
const open = box(openHtml);
check("RULE 2: a service that can scale has the box enabled up to its ceiling", !open.disabled && open.max === "10", open.tag);
check("RULE 2: the box starts at the recorded count", open.value === "3" && open.initial === "3", open.tag);
check("RULE 2: a fresh service starts at 1", box(instancesSection(earnfm, { max: 10, reason: "" }, 0)).value === "1");
check("RULE 2: several devices per IP says devices, not bandwidth", openHtml.includes("more instances add devices, not bandwidth"), openHtml);
const unknownHtml = instancesSection(mystery, { max: 10, reason: "" }, 1);
check("RULE 2: an undocumented limit warns the copies may earn nothing", unknownHtml.includes("does not document") && unknownHtml.includes("may earn nothing"), unknownHtml);

// RULE 3
check("RULE 3: left as it was, the box sends null", instancesChoice("3", "3", false) === null);
check("RULE 3: a locked box sends null", instancesChoice("4", "1", true) === null);
check("RULE 3: a changed box sends the number", instancesChoice("5", "3", false) === 5);
check("RULE 3: lowering to 1 is sent, not dropped", instancesChoice("1", "3", false) === 1);
check("RULE 3: something that is not a whole number sends null", instancesChoice("2.5", "1", false) === null && instancesChoice("", "1", false) === null);

// RULE 4
const card = renderWizardServiceSetup(earnfm, null, "box", null, "", { max: 10, reason: "" }, 2);
check("RULE 4: the wizard card carries the box", box(card).value === "2", card);
check("RULE 4: the box is not a credential", box(card).env === undefined, box(card).tag);

// RULE 5
check("RULE 5: no backend answer, no box", instancesSection(earnfm, undefined, 1) === "");
check("RULE 5: a manual service has no box", instancesSection({ ...earnfm, manualOnly: true }, { max: 10, reason: "" }, 1) === "");
check("RULE 5: a card rendered without scaling has no box", !renderWizardServiceSetup(earnfm, null, "box", null, "").includes("data-wizard-instances"));

// RULE 6
const copies = [
  { slug: "earnfm", containerId: "b", name: "cashpilot-earnfm-2", image: "i", status: "running", cpuPercent: 1, memoryMb: 20, instance: 2 },
  { slug: "earnfm", containerId: "c", name: "cashpilot-earnfm-3", image: "i", status: "exited", cpuPercent: 0, memoryMb: 0, instance: 3 },
];
const rows = copyRows("EarnFM", copies);
check("RULE 6: one row per copy, labelled #N", (rows.match(/<tr\b/g) || []).length === 2 && rows.includes("EarnFM #2") && rows.includes("EarnFM #3"), rows);
check("RULE 6: a running copy offers Stop for that copy", /data-row-action="stop" data-slug="earnfm" data-copy="2"/.test(rows), rows);
check("RULE 6: a stopped copy offers Start for that copy", /data-row-action="start" data-slug="earnfm" data-copy="3"/.test(rows), rows);
check("RULE 6: every copy has its own Logs", /data-row-action="logs" data-slug="earnfm" data-copy="2"/.test(rows) && /data-row-action="logs" data-slug="earnfm" data-copy="3"/.test(rows), rows);
check("RULE 6: a stopped copy is drawn stopped, a restarting one in between", rows.includes("badge-stopped") && copyRows("EarnFM", [{ ...copies[0], status: "restarting" }]).includes("badge-unknown"), rows);
check("RULE 6: no copy row offers Remove (lowering the count does that)", !rows.includes('data-row-action="remove"'), rows);
check("RULE 6: the first row is #1 only beside copies", firstCopyLabel(copies) === " #1" && firstCopyLabel([]) === "" && firstCopyLabel(null) === "");
check("RULE 6: no copies, no rows", copyRows("EarnFM", null) === "");

console.log(`${checks - failures}/${checks} instances checks passed`);
if (failures > 0) process.exit(1);
