#!/usr/bin/env node
// Browser-free checks on the controls that run a container on this computer.
//
// They read the one shared Docker status. While it is stopped or unknown:
//
//   1. THE CONTROL IS DISABLED: Import's Deploy, Run a copy through this proxy, a
//      proxied copy's Start, Dashboard Start (a service, a copy), Redeploy, and
//      Turn on isolation.
//   2. THE PAGE SAYS HOW TO GET DOCKER, in plain words, with a button to the
//      Runtime page: "Check Docker" when the status is unknown, "Set up Docker"
//      otherwise.
//   3. A NATIVE APP IS NEVER BLOCKED: its Start and Deploy stay as they were.
//   4. READY MEANS NO NOTE AND NOTHING DISABLED for Docker's sake.
//   5. ACTIVITY TELLS THE LAST SAMPLE FROM NOW: a sample that could not reach
//      Docker says so as history once Docker answers again.
//
//   node scripts/docker_gate_check.mjs      # against ./.harness-build

import { attentionItems, renderAttention } from "../.harness-build/render/attention.js";
import { renderServicesTable } from "../.harness-build/render/services.js";
import { renderImportSaved } from "../.harness-build/render/importer.js";
import { renderProxiesSection } from "../.harness-build/render/proxies.js";
import { renderActivityPage } from "../.harness-build/render/activity.js";
import { dockerBlocked, dockerBlockedNote } from "../.harness-build/render/runtime_state.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const stopped = { available: false, reason: "not-running", nativeAvailable: true, nativeServices: ["MystNodes"], message: "Docker Desktop is installed but not running." };
const missing = { ...stopped, reason: "not-installed", message: "Docker is not installed." };
const unknown = { ...stopped, reason: "unreachable", message: "CashPilot could not reach Docker." };
const ready = { ...stopped, available: true, reason: "running", message: "Docker's engine answered." };
const states = { stopped, missing, unknown, ready };

// The first <button> tag carrying every attribute in `has` and none in `not`, or "".
const button = (html, has, not = []) => (html.match(/<button\b[^>]*>/g) || [])
  .find((tag) => [].concat(has).every((a) => tag.includes(a)) && not.every((a) => !tag.includes(a))) || "";
const disabled = (tag) => /\sdisabled(\s|>|=)/.test(tag);
const runtimeLink = (html) => html.match(/<p[^>]*data-docker-blocked[^>]*>([\s\S]*?)<button[^>]*data-view="runtime"[^>]*>([^<]*)<\/button>/);

// The shared words.
check("ready blocks nothing", dockerBlocked(ready) === "" && dockerBlockedNote(ready) === "");
for (const [name, runtime] of Object.entries(states).filter(([name]) => name !== "ready")) {
  const note = runtimeLink(dockerBlockedNote(runtime));
  check(`${name}: the note links to the Runtime page`, !!note, dockerBlockedNote(runtime));
  check(`${name}: the note says to open Runtime`, /Open Runtime to/.test(note?.[1] || ""), note?.[1]);
  check(`${name}: the button says what it does`, note?.[2] === (name === "unknown" ? "Check Docker" : "Set up Docker"), note?.[2]);
}
check("not running says to start it", /not running\. Open Runtime to start it/.test(dockerBlocked(stopped)), dockerBlocked(stopped));
check("not installed says to set it up", /not set up.*Open Runtime to set it up/.test(dockerBlocked(missing)), dockerBlocked(missing));
check("unknown says to check it", /could not check Docker.*Open Runtime to check it again/.test(dockerBlocked(unknown)), dockerBlocked(unknown));

// Dashboard.
const services = [{ slug: "honeygain", name: "Honeygain" }, { slug: "mystnodes", name: "MystNodes" }];
const dep = (slug, runtime) => ({ slug, name: `cashpilot-${slug}`, image: `${slug}:1`, status: "exited", runtime, cpuPercent: 0, memoryMb: 0, customImage: "", instances: 1, containerId: "" });
const deployments = [dep("honeygain", "existing-docker"), dep("mystnodes", "native")];
const copies = { honeygain: [{ slug: "honeygain", name: "cashpilot-honeygain-2", status: "exited", instance: 2, cpuPercent: 0, memoryMb: 0, containerId: "", image: "honeygain:1" }] };
for (const [name, runtime] of Object.entries(states)) {
  const blocked = name !== "ready";
  const attention = renderAttention(attentionItems({
    runtime, services, deployments, copies, earnings: [],
    drift: { honeygain: ["missing NET_RAW"] },
    isolation: { mode: "legacy", setting: "auto", state: "reduced", outside: ["honeygain"] },
  }), 0, 3);
  const table = renderServicesTable({ services, deployments, copies, earnings: [], displayCurrency: "USD", runtime });
  const start = ['data-row-action="start"', 'data-slug="honeygain"'];
  for (const [what, html, has, not] of [
    ["attention Start", attention, start, ["data-copy"]],
    ["attention copy Start", attention, [...start, 'data-copy="2"']],
    ["Redeploy", attention, 'data-drift-action="redeploy"'],
    ["Turn on isolation", attention, "data-isolation-action"],
    ["table Start", table, start, ["data-copy"]],
    ["table copy Start", table, [...start, 'data-copy="2"']],
  ]) {
    const tag = button(html, has, not);
    check(`${name}: Dashboard ${what} is ${blocked ? "disabled" : "enabled"}`, !!tag && disabled(tag) === blocked, tag);
  }
  for (const html of [attention, table]) {
    const native = button(html, 'data-row-action="start" data-slug="mystnodes"');
    check(`${name}: native Start stays enabled`, !!native && !disabled(native), native);
  }
  const notes = (attention.match(/data-docker-blocked/g) || []).length;
  check(`${name}: each Docker row says how to get Docker`, notes === (blocked ? 4 : 0), `${notes} notes`);
  if (blocked) {
    const tag = button(table, start, ["data-copy"]);
    check(`${name}: the table's Start says why in its title`, tag.includes(`title="Start: ${dockerBlocked(runtime)}"`), tag);
  }
}

// Import.
const preview = { rows: [{ slug: "honeygain", name: "Honeygain", fields: [] }, { slug: "mystnodes", name: "MystNodes", fields: [] }] };
const result = { saved: ["honeygain", "mystnodes"] };
for (const [name, runtime] of Object.entries(states)) {
  const blocked = name !== "ready";
  const waits = blocked ? { honeygain: dockerBlockedNote(runtime) } : {};
  const html = renderImportSaved(result, preview, [], {}, {}, waits);
  const deploy = button(html, 'data-import-deploy="honeygain"');
  const native = button(html, 'data-import-deploy="mystnodes"');
  check(`${name}: Import Deploy for a Docker app is ${blocked ? "disabled" : "enabled"}`, !!deploy && disabled(deploy) === blocked, deploy);
  check(`${name}: Import Deploy for a native app stays enabled`, !!native && !disabled(native), native);
  check(`${name}: Import ${blocked ? "says" : "does not say"} how to get Docker`, !!runtimeLink(html) === blocked, html);
}
check("a deployed import row shows no note", !renderImportSaved(result, preview, ["honeygain"], {}, {}, { honeygain: dockerBlockedNote(stopped) }).includes("data-docker-blocked"));

// Fleet > Proxies.
const proxiesState = {
  proxies: [{
    id: 1, name: "Local proxy", kind: "socks5", host: "192.0.2.1", port: 1080, exitNode: "", secrets: {}, lastTest: null,
    cap: { enabled: false, cappedAt: "", capReason: "", usedBytes: 0, capBytes: 0, thresholdPct: 90, resetDay: 0 },
    copies: [{ slug: "honeygain", name: "Honeygain", status: "exited", exitIp: "", exitCountry: "", stoppedByCap: false }],
  }],
  candidates: [{ slug: "honeygain", name: "Honeygain", refusal: "" }],
};
for (const [name, runtime] of Object.entries(states)) {
  const blocked = name !== "ready";
  const html = renderProxiesSection(proxiesState, null, undefined, runtime);
  const run = button(html, "data-proxy-run-deploy");
  const start = button(html, "data-proxy-copy-start");
  check(`${name}: Run a copy through this proxy is ${blocked ? "disabled" : "enabled"}`, !!run && disabled(run) === blocked, run);
  check(`${name}: a proxied copy's Start is ${blocked ? "disabled" : "enabled"}`, !!start && disabled(start) === blocked, start);
  check(`${name}: Proxies ${blocked ? "says" : "does not say"} how to get Docker`, !!runtimeLink(html) === blocked);
}
const capped = renderProxiesSection({ ...proxiesState, proxies: [{ ...proxiesState.proxies[0], cap: { ...proxiesState.proxies[0].cap, cappedAt: "2026-11-05" } }] }, null, undefined, stopped);
check("a capped copy keeps the cap's reason", /title="Reset the usage or raise the cap to start it"/.test(capped));

// Activity.
const view = (workerStatus) => ({
  enabled: true, days: 1, rawHours: 24, lastWindow: 0, round: { at: 1700000000, workerStatus, leftoverProbes: [] },
  networks: { present: false, ageSeconds: null, rows: null }, networksEnabled: true, containers: [], alerts: [],
});
const noRuntimeNow = renderActivityPage(view("no_runtime"), {}, stopped);
const noRuntimeThen = renderActivityPage(view("no_runtime"), {}, ready);
check("Docker away now: Activity says it cannot sample", /activity-chip-warn">cannot sample: Docker is not reachable/.test(noRuntimeNow), noRuntimeNow);
check("Docker back now: the failure is the last sample's", !/cannot sample/.test(noRuntimeThen) && /last sample could not reach Docker\. Docker is ready now/.test(noRuntimeThen), noRuntimeThen);
check("Docker back now: the line is not a warning", !/activity-chip-warn[^>]*>last sample/.test(noRuntimeThen));
check("no status given: Activity keeps the sample's words", /cannot sample: Docker is not reachable/.test(renderActivityPage(view("no_runtime"), {})));
check("a sample that ran says nothing about Docker", !/Docker/.test(renderActivityPage(view("ok"), {}, stopped).match(/activity-controls[\s\S]*?<\/div>/)?.[0] || ""));

if (failures) {
  console.error(`\n${failures} of ${checks} Docker gate checks failed`);
  process.exit(1);
}
console.log(`docker gate: ${checks} checks passed`);
