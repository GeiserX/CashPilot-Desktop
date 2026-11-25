#!/usr/bin/env node
// Browser-free checks on the runtime page's first reading (src/render/runtimepage.ts).
//
//   1. EVERY READING REACHES THE CARD. A PC with no checklist (macOS, Linux) still
//      gets the fresh runtime in its card and state.
//   2. A FAILED READING SAYS SO. The guides it shows come with a note that no
//      check ran, and a way to check again; no checklist is mounted.
//   3. A PAGE THAT LEFT DRAWS NOTHING.
//   4. A CHECKLIST REPLACES THE DOCKER DESKTOP GUIDE CARD.
//
//   node scripts/runtimepage_check.mjs      # against ./.harness-build

import { loadRuntimePage, unknownRuntime, unknownRuntimeNotice, withRuntimeReading } from "../.harness-build/render/runtimepage.js";

import { renderDockerGuide } from "../.harness-build/render/dockerguide.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const guides = [{ id: "docker-desktop-windows", name: "Docker Desktop" }, { id: "podman", name: "Podman" }];
const fresh = { available: true, reason: "running", message: "Connected." };
const page = (current = true) => {
  const seen = { guides: null, holder: null, checklist: null };
  return {
    seen,
    deps: {
      current: () => current,
      showGuides: (g) => { seen.guides = g; },
      showHolder: (h) => { seen.holder = h; },
      readResume: async () => null,
      showChecklist: (r) => { seen.checklist = r; },
    },
  };
};

{
  const { seen, deps } = page();
  await loadRuntimePage(async () => ({ facts: { supported: false }, runtime: fresh, steps: null }), guides, deps);
  check("RULE 1: no checklist, every guide card", seen.guides?.length === 2 && seen.holder === "" && seen.checklist === null, JSON.stringify(seen));
}

{
  const { seen, deps } = page();
  await loadRuntimePage(async () => { throw new Error("PowerShell <did not answer>"); }, guides, deps);
  check("RULE 2: a failed reading says no check ran", /data-runtime-check-failed/.test(seen.holder || "") && /shown without that check/.test(seen.holder || ""), seen.holder);
  check("RULE 2: a failed reading offers Check again", /data-runtime-recheck/.test(seen.holder || ""), seen.holder);
  check("RULE 2: the error is escaped", !(seen.holder || "").includes("<did") && (seen.holder || "").includes("&lt;did not answer&gt;"), seen.holder);
  check("RULE 2: a failed reading mounts no checklist", seen.checklist === null);
  check("RULE 2: the guides still show", seen.guides?.length === 2);
}

{
  const { seen, deps } = page(false);
  await loadRuntimePage(async () => ({ facts: { supported: true }, runtime: fresh, steps: [{ id: "a1" }] }), guides, deps);
  check("RULE 3: a page that left draws nothing", seen.guides === null && seen.holder === null && seen.checklist === null, JSON.stringify(seen));
  const failed = page(false);
  await loadRuntimePage(async () => { throw new Error("x"); }, guides, failed.deps);
  check("RULE 3: a failure after the page left draws nothing", failed.seen.holder === null && failed.seen.guides === null);
}

{
  const { seen, deps } = page();
  const runtimeNotice = { runtime: true, level: "warning", title: "Docker setup needs you", message: "WSL is off." };
  const result = { facts: { supported: true }, runtime: fresh, runtimeNotice, steps: [{ id: "a1" }] };
  await loadRuntimePage(async () => result, guides, deps);
  check("RULE 4: a checklist is mounted with the reading", seen.checklist === result);
  check("RULE 4: the Docker Desktop guide card goes", seen.guides?.map((g) => g.id).join() === "podman", JSON.stringify(seen.guides));
}

{
  const oldAlert = { runtime: true, title: "Runtime offline" };
  const otherAlert = { title: "No services deployed" };
  const old = { runtime: { available: false, nativeAvailable: true }, notifications: [oldAlert, otherAlert] };
  const failed = withRuntimeReading(old, unknownRuntime, unknownRuntimeNotice);
  check("a failed refresh keeps native capability and unrelated alerts, and drops stale Docker status", failed.runtime.nativeAvailable && failed.runtime.reason === "" && failed.notifications[0]?.title === "Docker status unknown" && failed.notifications[1] === otherAlert && !failed.notifications.includes(oldAlert));
  const recovered = withRuntimeReading(failed, fresh, null);
  check("a runtime check clears only the runtime alert when Docker recovers", recovered.runtime === fresh && recovered.notifications.length === 1 && recovered.notifications[0] === otherAlert);
}

{
  const { deps } = page();
  const journal = { active: true, flow: "docker", step: "a3", restarted: "no", finish: "restart" };
  let html;
  deps.readResume = async () => journal;
  deps.showChecklist = (result, resume) => {
    html = renderDockerGuide({ ...result, resume, view: "list", waiting: null });
  };
  const result = {
    facts: { supported: true }, runtime: { available: false },
    steps: [{ id: "a3", title: "WSL", state: "needs-you", text: "WSL needs a restart.", action: {} }],
  };
  await loadRuntimePage(async () => result, guides, deps);
  check("reopening the checklist restores Restart to finish from the journal", html?.includes('data-resume="same-boot"') && html.includes("Restart to finish"));
  await loadRuntimePage(async () => ({ ...result, setupFinished: true }), guides, deps);
  check("a check that finished setup leaves the restart banner off", !html?.includes("data-resume"));
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} runtime page checks failed`);
  process.exit(1);
}
console.log(`runtimepage: ${checks} checks passed`);
