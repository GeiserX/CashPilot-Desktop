#!/usr/bin/env node
// Browser-free checks on first run's runtime card, against the HTML the real
// render function returns.
//
//   1. NATIVE-ONLY IS NEVER "READY". With no Docker, a PC where only the native
//      runtime runs something (Mysterium on Windows) gets a warning card that
//      says most apps need Docker, names what does run, and points at the Docker
//      guide. The user may still go on.
//   2. NOTHING RUNS, NOTHING OPENS. A native runtime that runs no catalog app
//      does not enable "Open the dashboard".
//   3. EVERY DOCKER STATE SHOWS ITS OWN WORDS. Each reason's message reaches the
//      card, escaped, and no state but a running Linux engine is called ready.
//   4. DOCKER READY IS READY.
//   5. ONE SENTENCE ABOUT DOCKER. The native-only card says most apps need Docker
//      once (its title), then the reason in its own words, never three times.
//   6. CHECKING, NOT GENERIC. Until the first reading of the PC lands, the card
//      says it is checking: a PC with virtualization off must not first be told
//      to open Docker Desktop.
//
//   node scripts/onboarding_render_check.mjs      # against ./.harness-build

import { runtimeCard } from "../.harness-build/render/onboarding.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const decode = (s) => s.replace(/&quot;/g, '"').replace(/&#039;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
const text = (html) => decode(html.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim());
const title = (html) => (html.match(/<strong>([^<]*)<\/strong>/) || [])[1] || "";
const notInstalled = "Docker Desktop is not installed. Install it with the guide below, then click Check again.";

// RULE 1
const nativeOnly = runtimeCard({ available: false, nativeServices: ["Mysterium"], message: notInstalled, context: "" });
check("RULE 1: native-only is a warning", nativeOnly.tone === "warn" && /runtime-card warn/.test(nativeOnly.html), nativeOnly.html);
check("RULE 1: native-only never says ready", !/ready/i.test(text(nativeOnly.html)), text(nativeOnly.html));
check("RULE 1: the title says Docker is needed", title(nativeOnly.html) === "Docker needed for most apps", nativeOnly.html);
check("RULE 1: it names what runs without Docker", text(nativeOnly.html).includes("Without Docker, only Mysterium runs on this computer."), text(nativeOnly.html));
check("RULE 1: it says the reason", text(nativeOnly.html).includes(notInstalled), text(nativeOnly.html));
check("RULE 1: it points at the Docker guide", /guide below/.test(text(nativeOnly.html)), text(nativeOnly.html));
check("RULE 1: the user may still go on with the native app", nativeOnly.canContinue === true);
const two = runtimeCard({ available: false, nativeServices: ["Mysterium", "Other"], message: "", context: "" });
check("RULE 1: two native apps are listed, plural", text(two.html).includes("only Mysterium and Other run on this computer."), text(two.html));
check("RULE 1: with no message it still points at the Docker guide", text(two.html).includes("Set up Docker with the guide below"), text(two.html));
const hostile = runtimeCard({ available: false, nativeServices: ["<img src=x onerror=alert(1)>"], message: "", context: "" });
check("RULE 1: a service name is escaped", !hostile.html.includes("<img") && hostile.html.includes("&lt;img"), hostile.html);

// RULE 2
for (const native of [[], null, undefined]) {
  const none = runtimeCard({ available: false, nativeServices: native, message: notInstalled, context: "" });
  check(`RULE 2: native list ${JSON.stringify(native)} does not open the dashboard`, none.canContinue === false);
  check(`RULE 2: native list ${JSON.stringify(native)} says setup is needed`, title(none.html) === "Runtime setup needed" && none.tone === "warn", none.html);
}

// RULE 3
const reasons = {
  "not-installed": notInstalled,
  "not-running": "Docker Desktop is installed, but its engine is not running. Open Docker Desktop, wait until it says the engine is running, then click Check again.",
  "windows-containers": "Docker Desktop is set to Windows containers. The apps CashPilot runs are Linux containers, so none of them can start there. Right-click the Docker icon next to the clock, choose \"Switch to Linux containers\", then click Check again.",
  "access-denied": "Docker is running, but your Windows account may not use it. This happens when another account installed Docker Desktop. An administrator must add your account to the docker-users group, then you sign out and back in.",
  "wsl-missing": "Docker needs WSL, the Linux layer built into Windows, and it is not installed. Open PowerShell as administrator, run wsl --install --no-distribution, then restart the PC.",
  "wsl-outdated": "WSL on this PC is version 2.0.14.0, and Docker needs 2.1.5 or newer. Run wsl --update, then open Docker Desktop again.",
  "virtualization-off": "Virtualization is turned off in this PC's firmware (BIOS or UEFI), and Docker needs it.",
  "first-start-pending": "Docker Desktop is installed but has not been opened on this account yet. Open it and accept Docker's terms.",
};
for (const [reason, message] of Object.entries(reasons)) {
  for (const native of [["Mysterium"], []]) {
    const card = runtimeCard({ available: false, nativeServices: native, message, context: "", reason });
    const shown = text(card.html);
    check(`RULE 3: ${reason} (native ${native.length}) shows its message`, shown.includes(message), shown);
    check(`RULE 3: ${reason} (native ${native.length}) is not called ready`, card.tone === "warn" && !/\bready\b/i.test(title(card.html)), card.html);
  }
}
const escaped = runtimeCard({ available: false, nativeServices: [], message: "<b>x</b>", context: "" });
check("RULE 3: the message is escaped", escaped.html.includes("&lt;b&gt;x&lt;/b&gt;"), escaped.html);

// RULE 4
const ready = runtimeCard({ available: true, nativeServices: ["Mysterium"], message: "Connected to a Docker-compatible runtime.", context: "desktop-linux" });
check("RULE 4: Docker ready is ok", ready.tone === "ok" && title(ready.html) === "Runtime ready" && ready.canContinue, ready.html);
check("RULE 4: the Docker context is shown", text(ready.html).includes("Docker context: desktop-linux"), ready.html);

// RULE 5
const dockerNeed = /most apps|need(?:s)? Docker|run inside it/gi;
for (const message of [notInstalled, "", "Docker Desktop is installed, but its engine is not running. Open Docker Desktop, wait until it says the engine is running, then click Check again."]) {
  const card = runtimeCard({ available: false, nativeServices: ["Mysterium"], message, context: "" });
  const n = (text(card.html).match(dockerNeed) || []).length;
  check(`RULE 5: one sentence about needing Docker (message ${JSON.stringify(message.slice(0, 30))})`, n === 1, `${n}: ${text(card.html)}`);
}

// RULE 6
const generic = "Docker Desktop is installed, but its engine is not running. Open Docker Desktop, wait until it says the engine is running, then click Check again.";
const checking = runtimeCard({ available: false, nativeServices: ["Mysterium"], message: generic, context: "" }, { checking: true });
check("RULE 6: while checking, the card says so", title(checking.html) === "Checking this computer", checking.html);
check("RULE 6: while checking, no generic Docker advice", !text(checking.html).includes("Open Docker Desktop"), text(checking.html));
check("RULE 6: while checking, the card is not ready", checking.tone === "warn" && !/\bready\b/i.test(text(checking.html)));
check("RULE 6: while checking, the dashboard waits", checking.canContinue === false);
const readyWhileChecking = runtimeCard({ available: true, nativeServices: [], message: "Connected to a Docker-compatible runtime.", context: "" }, { checking: true });
check("RULE 6: Docker ready needs no reading", title(readyWhileChecking.html) === "Runtime ready" && readyWhileChecking.canContinue);

if (failures > 0) {
  console.error(`\n${failures} of ${checks} onboarding checks failed`);
  process.exit(1);
}
console.log(`onboarding: ${checks} checks passed`);
