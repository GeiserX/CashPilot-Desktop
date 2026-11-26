#!/usr/bin/env node
// Browser-free checks on the way back to Docker setup (src/render/runtime_state.ts).
//
// First run lets a person open the dashboard before Docker is set up. These rules
// keep the Docker setup one click away afterwards, on every runtime state:
//
//   1. THE DASHBOARD ASKS. While no Docker engine answers, "Needs attention" has a
//      row saying what is missing, with Set up Docker, and "Nothing needs you"
//      does not show. With only native apps it is a warning, not an outage.
//   2. SETTINGS SAYS THE REAL STATE. The Runtime card reads ready, not installed,
//      installed but not running, or unknown, with Set up Docker; the read-only
//      provider setting says the state next to it.
//   3. A DOCKER APP'S CARD SAYS IT CANNOT RUN. A report that blocks shows its
//      summary and Set up Docker; a report that does not block shows no button.
//   4. NO DEAD BUTTON. Every Set up Docker opens the runtime page, the same on
//      every OS; outside Windows that page shows the install guides.
//
// It drives the REAL functions and asserts on the HTML they return.
//
//   node scripts/runtime_way_back_check.mjs      # against ./.harness-build

import { attentionItems, renderAttention } from "../.harness-build/render/attention.js";
import { localDockerGap, renderPreflight } from "../.harness-build/render/preflight.js";
import { renderEnvSetting } from "../.harness-build/render/settings.js";
import { renderRuntimeSettings, runtimeProviderNote, runtimeState } from "../.harness-build/render/runtime_state.js";
import { loadRuntimePage } from "../.harness-build/render/runtimepage.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const SETUP = /<button[^>]*data-view="runtime"[^>]*>Set up Docker<\/button>/;
const services = [{ slug: "earnfm", name: "Earn.fm" }, { slug: "mysterium", name: "MystNodes" }];

// What a fresh Windows PC reports: no Docker, MystNodes native.
const states = {
  ready: { available: true, nativeAvailable: true, nativeServices: ["MystNodes"], reason: "running", message: "Connected to a Docker-compatible runtime." },
  notInstalled: { available: false, nativeAvailable: true, nativeServices: ["MystNodes"], reason: "not-installed", message: "Docker Desktop is not installed. Install it with the guide below, then click Check again." },
  notRunning: { available: false, nativeAvailable: true, nativeServices: ["MystNodes"], reason: "not-running", message: "Docker Desktop is installed, but its engine is not running." },
  firstStart: { available: false, nativeAvailable: true, nativeServices: ["MystNodes"], reason: "first-start-pending", message: "Docker Desktop is installed but has not been opened on this account yet." },
  wsl: { available: false, nativeAvailable: true, nativeServices: ["MystNodes"], reason: "wsl-missing", message: "Docker needs WSL." },
  unknown: { available: false, nativeAvailable: true, nativeServices: ["MystNodes"], reason: "", message: "" },
  // macOS or Linux with no runtime and no native app.
  nothing: { available: false, nativeAvailable: false, nativeServices: [], reason: "not-installed", message: "Choose one of the supported runtimes below." },
};

const dashboard = (runtime, deployments = []) => {
  const items = attentionItems({ runtime, services, deployments, earnings: [] });
  return { items, html: renderAttention(items, 0, deployments.length) };
};

// RULE 1
for (const [name, runtime] of Object.entries(states)) {
  const { items, html } = dashboard(runtime);
  if (runtime.available) {
    check(`RULE 1 (${name}): Docker ready asks for nothing`, items.length === 0 && html.includes("Nothing needs you"), html);
    continue;
  }
  check(`RULE 1 (${name}): a row asks for Docker`, items.length === 1, JSON.stringify(items));
  check(`RULE 1 (${name}): the row has Set up Docker`, SETUP.test(html), html);
  check(`RULE 1 (${name}): "Nothing needs you" does not show`, !html.includes("Nothing needs you"), html);
}
const remote = { ...states.ready, setupTask: "Turn on WSL" };
const remoteDashboard = dashboard(remote);
check("a working engine with local setup left still asks for WSL", remoteDashboard.items[0]?.level === "warn" && remoteDashboard.html.includes("Turn on WSL") && SETUP.test(remoteDashboard.html) && !remoteDashboard.html.includes("Nothing needs you"), remoteDashboard.html);
check("Settings keeps Set up Docker for unfinished local setup with a working engine", SETUP.test(renderRuntimeSettings(remote)) && renderRuntimeSettings(remote).includes("Turn on WSL"));
const fresh = dashboard(states.notInstalled).items[0];
check("RULE 1: native only is a warning, not an outage", fresh?.level === "warn" && fresh.tag !== "Offline", JSON.stringify(fresh));
check("RULE 1: it says Docker is not set up, and what still runs", /Docker is not set up yet/.test(fresh?.why) && /only MystNodes can run here/.test(fresh?.why), fresh?.why);
check("native-only Dashboard explains when Docker is stopped", /engine is not running/.test(dashboard(states.notRunning).items[0]?.why || ""), dashboard(states.notRunning).items[0]?.why);
check("native-only Dashboard explains when WSL is missing", /needs WSL/.test(dashboard(states.wsl).items[0]?.why || ""), dashboard(states.wsl).items[0]?.why);
const unreachable = { ...states.unknown, reason: "unreachable", message: "CashPilot cannot reach the Docker engine that DOCKER_HOST names." };
const unreachableSettings = renderRuntimeSettings(unreachable);
check("an unreachable endpoint leaves install status Unknown", unreachableSettings.includes("Docker: Unknown") && unreachableSettings.includes("DOCKER_HOST") && !unreachableSettings.includes("Docker is installed but not running"), unreachableSettings);
const nothing = dashboard(states.nothing).items[0];
check("RULE 1: nothing able to run is an outage, with Set up Docker", nothing?.level === "fail" && nothing.tag === "Offline" && SETUP.test(nothing.actions), JSON.stringify(nothing));
const stopped = dashboard(states.notRunning, [{ slug: "earnfm", name: "cashpilot-earnfm", status: "exited", runtime: "docker" }]);
const down = stopped.items.find((i) => i.what === "Container runtime");
check("RULE 1: Docker gone under a container app is an outage, with Set up Docker", down?.level === "fail" && SETUP.test(down.actions), JSON.stringify(stopped.items));
check("RULE 1: the Docker row comes first", stopped.items[0]?.what === "Container runtime", JSON.stringify(stopped.items.map((i) => i.what)));

// RULE 2
const want = {
  ready: "Docker: Ready",
  notInstalled: "Docker: Docker is not installed",
  notRunning: "Docker: Docker is installed but not running",
  firstStart: "Docker: Docker is installed but not running",
  wsl: "Docker: Docker cannot run yet",
  unknown: "Docker: Unknown",
  nothing: "Docker: Docker is not installed",
};
for (const [name, runtime] of Object.entries(states)) {
  const card = renderRuntimeSettings(runtime);
  check(`RULE 2 (${name}): the card says ${want[name]}`, card.includes(want[name]), card);
  check(`RULE 2 (${name}): the card opens the runtime page`, /data-view="runtime"/.test(card), card);
  if (runtime.available) {
    check(`RULE 2 (${name}): ready offers no setup`, !SETUP.test(card), card);
  } else {
    check(`RULE 2 (${name}): not ready offers Set up Docker`, SETUP.test(card), card);
    check(`RULE 2 (${name}): the card never says ready`, !card.includes("Docker: Ready"), card);
  }
}
check("RULE 2: the card names what runs without Docker", renderRuntimeSettings(states.notInstalled).includes("only MystNodes can run here"));
const provider = { key: "CASHPILOT_RUNTIME_PROVIDER", label: "Runtime provider (chosen)", value: "existing-docker", source: "Read-only", secret: false, readOnly: true, help: "The kind of runtime CashPilot is set to use." };
const row = renderEnvSetting(provider, runtimeProviderNote(states.notInstalled));
check("RULE 2: the provider row says the state next to existing-docker", row.includes("existing-docker") && row.includes("Docker is not installed"), row);
check("RULE 2: the provider row with Docker ready says ready", renderEnvSetting(provider, runtimeProviderNote(states.ready)).includes("Docker now:</strong> Ready"));
check("RULE 2: the state words come from the reason code", runtimeState(states.firstStart).kind === "not-running" && runtimeState(states.unknown).kind === "unknown" && runtimeState(states.wsl).kind === "not-ready");

// RULE 3
const report = (overrides = {}) => ({
  slug: "earnfm",
  name: "Earn.fm",
  verdict: "looks_fine",
  summary: "Nothing stands out — Earn.fm should work normally here.",
  findings: [],
  notChecked: ["your connection speed"],
  machineArch: "",
  blocking: false,
  ...overrides,
});
const blocked = renderPreflight(report({ verdict: "will_earn_nothing", blocking: true, summary: "Docker is not set up on this PC yet, so Earn.fm cannot run here. Set up Docker shows how." }));
check("RULE 3: a blocked card says Docker is not set up", blocked.includes("Docker is not set up on this PC yet") && !blocked.includes("should work normally"), blocked);
check("RULE 3: a blocked card has Set up Docker", SETUP.test(blocked), blocked);
check("RULE 3: a blocked card says Deploy is refused", blocked.includes("Deploy is refused"), blocked);
check("RULE 3: a blocked card keeps what was not checked", blocked.includes("Not checked"), blocked);
const localOnly = report({ blocking: true, summary: "Docker is not set up on this PC yet." });
check("switching Where to a worker hides the local Docker refusal", renderPreflight(localOnly, 7) === "");
check("switching Where back to This computer shows the local refusal", SETUP.test(renderPreflight(localOnly, null)));
check("a worker gets no local-only preflight verdict", renderPreflight(report(), 7) === "");
const fine = renderPreflight(report());
check("RULE 3: a card that does not block has no button", !/<button/.test(fine), fine);
const nativeCard = renderPreflight(report({ slug: "mysterium", name: "MystNodes", summary: "Nothing stands out — MystNodes should work normally here." }));
check("RULE 3: a native app's card is untouched", !/<button/.test(nativeCard) && !nativeCard.includes("Docker"), nativeCard);
const dockerService = { name: "Earn.fm", docker: { image: "earnfm:1" } };
const unknownGap = localDockerGap(dockerService, states.unknown);
const unknownCard = renderPreflight(report(), null, unknownGap);
check("an unknown Docker reading replaces a clean local preflight", unknownCard.includes("Docker status is unknown") && SETUP.test(unknownCard) && !unknownCard.includes("should work normally"), unknownCard);
check("the unknown Docker reading still shows before the service findings arrive", SETUP.test(renderPreflight(null, null, unknownGap)));
check("a worker is unaffected by the local Docker reading", renderPreflight(report(), 7, unknownGap) === "");
check("Docker ready clears the local refusal", localDockerGap(dockerService, states.ready) === "");
check("a native app can run even when Docker is unknown", localDockerGap({ ...dockerService, name: "MystNodes" }, states.unknown) === "");
check("a custom Docker app cannot use the catalog app's native runtime", localDockerGap({ ...dockerService, name: "MystNodes", custom: true }, states.unknown) === unknownGap);

// RULE 4: outside Windows the runtime page reads the computer and shows the guides.
for (const [os, guides] of [["macOS", [{ id: "docker-desktop-macos" }, { id: "colima" }]], ["Linux", [{ id: "docker-engine" }, { id: "podman" }]]]) {
  const seen = { guides: null, holder: null, checklist: false };
  await loadRuntimePage(async () => ({ facts: { supported: false }, steps: [], runtime: states.nothing }), guides, {
    current: () => true,
    showGuides: (g) => { seen.guides = g; },
    showHolder: (html) => { seen.holder = html; },
    readResume: async () => null,
    showChecklist: () => { seen.checklist = true; },
  });
  check(`RULE 4 (${os}): Set up Docker lands on the install guides`, seen.guides?.length === guides.length && !seen.checklist, JSON.stringify(seen));
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} runtime way-back checks FAILED`);
  process.exit(1);
}
console.log(`runtime way-back: ${checks} checks passed`);
