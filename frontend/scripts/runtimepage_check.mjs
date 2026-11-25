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

import { loadRuntimePage } from "../.harness-build/render/runtimepage.js";

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
  const seen = { runtime: [], guides: null, holder: null, checklist: null };
  return {
    seen,
    deps: {
      current: () => current,
      setRuntime: (r) => seen.runtime.push(r),
      showGuides: (g) => { seen.guides = g; },
      showHolder: (h) => { seen.holder = h; },
      showChecklist: (r) => { seen.checklist = r; },
    },
  };
};

{
  const { seen, deps } = page();
  await loadRuntimePage(async () => ({ facts: { supported: false }, runtime: fresh, steps: null }), guides, deps);
  check("RULE 1: a PC with no checklist gets the fresh runtime", seen.runtime.length === 1 && seen.runtime[0] === fresh, JSON.stringify(seen.runtime));
  check("RULE 1: no checklist, every guide card", seen.guides?.length === 2 && seen.holder === "" && seen.checklist === null, JSON.stringify(seen));
}

{
  const { seen, deps } = page();
  await loadRuntimePage(async () => { throw new Error("PowerShell <did not answer>"); }, guides, deps);
  check("RULE 2: a failed reading says no check ran", /data-runtime-check-failed/.test(seen.holder || "") && /shown without that check/.test(seen.holder || ""), seen.holder);
  check("RULE 2: a failed reading offers Check again", /data-runtime-recheck/.test(seen.holder || ""), seen.holder);
  check("RULE 2: the error is escaped", !(seen.holder || "").includes("<did") && (seen.holder || "").includes("&lt;did not answer&gt;"), seen.holder);
  check("RULE 2: no checklist and no runtime from a failed reading", seen.checklist === null && seen.runtime.length === 0);
  check("RULE 2: the guides still show", seen.guides?.length === 2);
}

{
  const { seen, deps } = page(false);
  await loadRuntimePage(async () => ({ facts: { supported: true }, runtime: fresh, steps: [{ id: "a1" }] }), guides, deps);
  check("RULE 3: a page that left draws nothing", seen.runtime.length === 0 && seen.guides === null && seen.holder === null && seen.checklist === null, JSON.stringify(seen));
  const failed = page(false);
  await loadRuntimePage(async () => { throw new Error("x"); }, guides, failed.deps);
  check("RULE 3: a failure after the page left draws nothing", failed.seen.holder === null && failed.seen.guides === null);
}

{
  const { seen, deps } = page();
  const result = { facts: { supported: true }, runtime: fresh, steps: [{ id: "a1" }] };
  await loadRuntimePage(async () => result, guides, deps);
  check("RULE 4: a checklist is mounted with the reading", seen.checklist === result && seen.runtime[0] === fresh);
  check("RULE 4: the Docker Desktop guide card goes", seen.guides?.map((g) => g.id).join() === "podman", JSON.stringify(seen.guides));
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} runtime page checks failed`);
  process.exit(1);
}
console.log(`runtimepage: ${checks} checks passed`);
