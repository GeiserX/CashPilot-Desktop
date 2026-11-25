#!/usr/bin/env node
// Browser-free checks on the Docker checklist (src/render/dockerguide.ts) and its
// wiring (src/render/dockerguide_wire.ts), against the HTML the real functions
// return.
//
//   1. EACH STATE IS DRAWN AS ITSELF. done, needs you, blocked and unknown each get
//      their own label, and no row that is not done is labelled Done.
//   2. A BLOCKED ROW names the step to finish first and offers nothing to click.
//   3. AN ADMIN ACTION IS "COMING NEXT": a disabled button, and the way to do it by
//      hand.
//   4. NOTHING RESTARTS WITHOUT THE CONFIRM SCREEN. The list has no restart button;
//      the confirm screen says open apps close and unsaved work can be lost, and
//      that CashPilot never changes BIOS settings. The wiring ignores a restart
//      that does not come from that screen.
//   5. THE LEGACY-BIOS FALLBACK says how to get into the BIOS by hand.
//   6. THE SKIP PATH is honest: only the native apps run, and it is offered only
//      when one does.
//   7. CHECK AGAIN IS BUSY while a reading runs, and a second click starts nothing.
//   8. THE WAIT FOR DOCKER'S ENGINE shows its progress and ends at two minutes.
//   9. RESUME AFTER A RESTART: same boot says "Restart to finish"; a new boot says
//      the checks ran again.
//  10. TEXT FROM GO IS ESCAPED.
//
//   node scripts/dockerguide_render_check.mjs      # against ./.harness-build

import {
  START_LIMIT_SECONDS,
  checkAgainButton,
  engineAnswers,
  firmwareConfirm,
  firmwareFailed,
  renderDockerGuide,
  skipText,
  waitLine,
} from "../.harness-build/render/dockerguide.js";
import { DockerGuide } from "../.harness-build/render/dockerguide_wire.js";

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
const rows = (html) => [...html.matchAll(/<li class="docker-step" data-step="([^"]*)" data-state="([^"]*)">([\s\S]*?)<\/li>/g)].map((m) => ({ id: m[1], state: m[2], html: m[3] }));
const rowLabel = (row) => (row.html.match(/class="docker-step-state [^"]*">([^<]*)</) || [])[1] || "";

const none = { kind: "", label: "" };
const steps = [
  { id: "a1", title: "Windows version", state: "done", text: "This PC runs Windows 11 (build 26100), which Docker supports.", action: none },
  {
    id: "a2", title: "Virtualization in the BIOS", state: "needs-you", text: "Virtualization is turned off in this PC's BIOS (UEFI), and Docker needs it.",
    action: { kind: "firmware", label: "Restart into BIOS" }, help: "Often F2 or Del.", helpUrl: "https://support.microsoft.com/x",
  },
  { id: "a3", title: "WSL 2 backend: Windows features", state: "unknown", text: "CashPilot could not read whether the Windows features WSL 2 needs are on.", action: none },
  { id: "a5", title: "Docker Desktop installed", state: "needs-you", text: "Docker Desktop is not installed.", action: { kind: "url", label: "Open the Docker download page", url: "https://www.docker.com/products/docker-desktop/" } },
  { id: "a6", title: "Docker Desktop running", state: "blocked", blockedBy: "a5", text: "Docker Desktop is not running.", action: none },
  {
    id: "a12", title: "Windows Server service", state: "needs-you", text: "The Windows Server service (LanmanServer) is turned off.",
    action: { kind: "elevated", label: "Set it to Automatic", op: "lanmanserver-auto", manual: "Open PowerShell as administrator and run Set-Service LanmanServer -StartupType Automatic." },
  },
];
const runtimeDown = { available: false, reason: "virtualization-off", nativeServices: ["Mysterium"] };
const base = { steps, runtime: runtimeDown, view: "list", checking: false, waiting: null, waitResult: "", resume: null, firmware: null };
const list = renderDockerGuide(base);
const byId = Object.fromEntries(rows(list).map((r) => [r.id, r]));

// RULE 1
check("RULE 1: one row per step, in order", rows(list).map((r) => r.id).join(" ") === "a1 a2 a3 a5 a6 a12", rows(list).map((r) => r.id).join(" "));
for (const [id, label] of [["a1", "Done"], ["a2", "Needs you"], ["a3", "Not known"], ["a6", "Waits for an earlier step"]]) {
  check(`RULE 1: ${id} reads ${label}`, rowLabel(byId[id]) === label, byId[id]?.html);
}
for (const r of rows(list)) {
  if (r.state !== "done") check(`RULE 1: ${r.id} (${r.state}) is never labelled Done`, rowLabel(r) !== "Done" && !/>Done</.test(r.html), r.html);
}
check("RULE 1: an unknown row offers no action", !/data-guide-(action|url)/.test(byId.a3.html), byId.a3.html);
check("RULE 1: an unknown state from Go reads Not known", rowLabel(rows(renderDockerGuide({ ...base, steps: [{ ...steps[0], state: "weird" }] }))[0]) === "Not known");
check("RULE 1: the summary counts what is done", text(list).includes("1 of 6 steps done. 3 steps need you."), text(list));

// RULE 2
check("RULE 2: a blocked row names the step to finish first", text(byId.a6.html).includes("First finish step 4: Docker Desktop installed."), text(byId.a6.html));
check("RULE 2: a blocked row offers nothing to click", !/<button/.test(byId.a6.html), byId.a6.html);
const blockedWithAction = rows(renderDockerGuide({ ...base, steps: [{ ...steps[4], action: { kind: "start-docker", label: "Start Docker Desktop" } }] }))[0];
check("RULE 2: even an action Go left on a blocked row is not drawn", !/data-guide-action/.test(blockedWithAction.html), blockedWithAction.html);

// RULE 3
check("RULE 3: the admin action is a disabled button", /<button[^>]*disabled[^>]*data-guide-elevated="lanmanserver-auto"/.test(byId.a12.html), byId.a12.html);
check("RULE 3: it says it comes in the next version", text(byId.a12.html).includes("Coming in the next version"), text(byId.a12.html));
check("RULE 3: it says how to do it by hand", text(byId.a12.html).includes("Set-Service LanmanServer -StartupType Automatic"), text(byId.a12.html));

// RULE 4
check("RULE 4: the BIOS row opens the confirm screen", /data-guide-action="firmware" data-guide-step="a2"/.test(byId.a2.html), byId.a2.html);
check("RULE 4: the list itself has no restart button", !/firmware-go/.test(list));
check("RULE 4: the BIOS help text and Microsoft's guide are there", text(byId.a2.html).includes("Often F2 or Del.") && /data-guide-url="https:\/\/support.microsoft.com\/x"/.test(byId.a2.html), byId.a2.html);
const confirm = renderDockerGuide({ ...base, view: "firmware-confirm" });
check("RULE 4: the confirm screen is the confirm screen", confirm === `<section class="docker-guide">${firmwareConfirm()}</section>`);
check("RULE 4: it says the PC restarts now", text(confirm).includes("Your PC restarts now"), text(confirm));
check("RULE 4: it says open apps close and unsaved work can be lost", text(confirm).includes("Windows closes every open app. Save your work first: work you have not saved can be lost."), text(confirm));
check("RULE 4: it says CashPilot never changes BIOS settings", text(confirm).includes("CashPilot never changes BIOS settings itself."), text(confirm));
check("RULE 4: it has the restart and a way out", /data-guide-action="firmware-go"/.test(confirm) && /data-guide-action="back"/.test(confirm), confirm);

// RULE 5
const failed = firmwareFailed({ started: false, reopens: true });
check("RULE 5: the fallback says nothing restarted", text(failed).includes("Nothing restarted."), text(failed));
check("RULE 5: the fallback names the BIOS keys", /F2 or Del/.test(text(failed)) && /F10 or Esc on HP/.test(text(failed)), text(failed));
check("RULE 5: armed RunOnce: CashPilot opens by itself", text(failed).includes("opens this checklist by itself"), text(failed));
check("RULE 5: no RunOnce: open CashPilot by hand", text(firmwareFailed({ started: false, reopens: false })).includes("open CashPilot"), firmwareFailed(null));
check("RULE 5: the failed view is drawn from the model", renderDockerGuide({ ...base, view: "firmware-failed", firmware: { started: false, reopens: false } }).includes('data-guide-view="firmware-failed"'));

// RULE 6
check("RULE 6: skip is offered when a native app runs", /data-guide-action="skip"/.test(list), list);
const skip = renderDockerGuide({ ...base, view: "skip" });
check("RULE 6: the skip screen names what still runs", text(skip).includes("Without Docker, only Mysterium runs on this computer. Every other app in the catalog needs Docker and stays unavailable until it runs."), text(skip));
check("RULE 6: the skip screen has its confirm and a way back", /data-guide-action="skip-go"/.test(skip) && /data-guide-action="back"/.test(skip), skip);
const noNative = renderDockerGuide({ ...base, runtime: { ...runtimeDown, nativeServices: [] } });
check("RULE 6: no native app, no skip", !/data-guide-action="skip"/.test(noNative), noNative);
check("RULE 6: no native app, the skip view is not drawn", !renderDockerGuide({ ...base, view: "skip", runtime: { ...runtimeDown, nativeServices: null } }).includes("skip-go"));
check("RULE 6: two native apps are listed", skipText(["Mysterium", "Other"]).includes("only Mysterium and Other run"), skipText(["Mysterium", "Other"]));
check("RULE 6: Docker ready, no skip", !/data-guide-action="skip"/.test(renderDockerGuide({ ...base, runtime: { available: true, reason: "running", nativeServices: ["Mysterium"] } })));

// RULE 7
const busy = checkAgainButton("x", true);
check("RULE 7: Check again is disabled while checking", /disabled/.test(busy) && /aria-busy="true"/.test(busy) && text(busy) === "Checking…", busy);
const idle = checkAgainButton("x", false);
check("RULE 7: Check again is enabled when idle", !/disabled/.test(idle) && text(idle) === "Check again", idle);
check("RULE 7: the checklist's own Check again is busy while checking", /<button[^>]*id="docker-guide-check"[^>]*disabled/.test(renderDockerGuide({ ...base, checking: true })));
check("RULE 7: Start Docker Desktop waits while checking", /data-guide-action="start-docker" disabled/.test(renderDockerGuide({ ...base, checking: true, steps: [{ ...steps[4], state: "needs-you", action: { kind: "start-docker", label: "Start Docker Desktop" } }] })));

// RULE 8
check("RULE 8: the wait line counts seconds of two minutes", text(waitLine({ waiting: 35, waitResult: "" })) === "Waiting for Docker's engine to start: 35 s of 2 min.", waitLine({ waiting: 35, waitResult: "" }));
check("RULE 8: the wait line never counts past the limit", text(waitLine({ waiting: 500, waitResult: "" })).includes(`${START_LIMIT_SECONDS} s of 2 min`));
check("RULE 8: a timeout points at Docker's window", text(waitLine({ waiting: null, waitResult: "timeout" })).includes("did not answer within 2 minutes"));
check("RULE 8: a failed start says so", text(waitLine({ waiting: null, waitResult: "failed" })).includes("could not start Docker Desktop"));
check("RULE 8: nothing to say, nothing shown", text(waitLine({ waiting: null, waitResult: "" })) === "");
check("RULE 8: Windows containers mode counts as the engine answering", engineAnswers({ available: false, reason: "windows-containers" }) && !engineAnswers({ available: false, reason: "not-running" }));

// RULE 9
const sameBoot = renderDockerGuide({ ...base, resume: { active: true, flow: "docker", step: "a2", restarted: "no", exeMoved: false, attempts: 1, fromRunOnce: false } });
check("RULE 9: same boot says Restart to finish", text(sameBoot).includes("Restart to finish") && text(sameBoot).includes("has not restarted"), text(sameBoot));
check("RULE 9: same boot offers the BIOS restart again, through the confirm screen", /data-resume="same-boot"[\s\S]*data-guide-action="firmware" data-guide-step="a2"/.test(sameBoot), sameBoot);
check("RULE 9: same boot is not an error", !/fail|error/i.test(text(sameBoot.match(/data-resume[\s\S]*?<\/div>\s*<\/div>/)?.[0] || "")));
const newBoot = renderDockerGuide({ ...base, resume: { active: true, flow: "docker", step: "a2", restarted: "yes", exeMoved: false, attempts: 1, fromRunOnce: true } });
check("RULE 9: a new boot says the checks ran again", text(newBoot).includes("Welcome back. The PC restarted, and CashPilot checked every step again."), text(newBoot));
check("RULE 9: the user can stop the guiding", /data-guide-action="cancel-setup"/.test(newBoot));
check("RULE 9: no resume, no banner", !/data-resume/.test(list));

// RULE 10
const hostile = renderDockerGuide({ ...base, steps: [{ id: "a1", title: "<b>t</b>", state: "needs-you", text: "<img src=x onerror=alert(1)>", action: { kind: "url", label: "<i>l</i>", url: '"><script>' } }] });
check("RULE 10: Go's text is escaped", !/<img|<script|<b>t|<i>l/.test(hostile) && hostile.includes("&lt;img"), hostile);

// The ready summary.
check("Docker ready says so", text(renderDockerGuide({ ...base, runtime: { available: true, reason: "running", nativeServices: [] } })).includes("Docker is ready."));
check("Before the first reading it says it is checking", text(renderDockerGuide({ ...base, steps: null })).includes("Checking this PC…"));

// The wiring, against a holder that only takes HTML.
const holder = () => ({ innerHTML: "", querySelectorAll: () => [], querySelector: () => null });
const deps = (over = {}) => {
  const calls = { firmware: [], hostCheck: 0, checkRuntime: 0, start: 0, cancel: 0, skip: 0 };
  return {
    calls,
    deps: {
      hostCheck: async () => { calls.hostCheck++; return { facts: { supported: true }, runtime: runtimeDown, steps }; },
      checkRuntime: async () => { calls.checkRuntime++; return { available: false, reason: "not-running" }; },
      startDocker: async () => { calls.start++; },
      openWindowsUpdate: async () => {},
      restartToFirmware: async (step) => { calls.firmware.push(step); return { started: false, reopens: true }; },
      cancelSetup: async () => { calls.cancel++; },
      openUrl: () => {},
      skip: () => { calls.skip++; },
      sleep: async () => {},
      ...over,
    },
  };
};

{
  const { calls, deps: d } = deps();
  const g = new DockerGuide(holder(), d, { runtime: runtimeDown, resume: null, result: { facts: { supported: true }, runtime: runtimeDown, steps } });
  await g.act("firmware-go", "a2");
  check("RULE 4 (wiring): a restart from the list is ignored", calls.firmware.length === 0, JSON.stringify(calls.firmware));
  await g.act("firmware", "a2");
  check("RULE 4 (wiring): Restart into BIOS opens the confirm screen", g.model.view === "firmware-confirm");
  await g.act("firmware-go", "");
  check("RULE 4 (wiring): Restart now restarts, for the step that asked", calls.firmware.join() === "a2", JSON.stringify(calls.firmware));
  check("RULE 5 (wiring): a restart Windows refused shows the fallback", g.model.view === "firmware-failed" && g.model.firmware?.reopens === true, g.model.view);
  await g.act("skip-go", "");
  check("RULE 6 (wiring): skip-go acts only from the skip screen", calls.skip === 0);
  await g.act("skip", "");
  await g.act("skip-go", "");
  check("RULE 6 (wiring): skipping ends the guided setup, then leaves", calls.cancel === 1 && calls.skip === 1, JSON.stringify(calls));
}

{
  let release;
  let started = 0;
  const { deps: d } = deps({ hostCheck: () => { started++; return new Promise((r) => { release = r; }); } });
  const g = new DockerGuide(holder(), d, { runtime: runtimeDown, resume: null });
  check("RULE 7 (wiring): opening with no reading starts one, busy", g.model.checking === true && started === 1);
  void g.check();
  check("RULE 7 (wiring): Check again during a reading starts nothing", started === 1, String(started));
  release({ facts: { supported: true }, runtime: runtimeDown, steps });
  await new Promise((r) => setTimeout(r, 0));
  check("RULE 7 (wiring): the reading lands and the button is free", g.model.checking === false && g.model.steps?.length === steps.length);
}

{
  let reads = 0;
  const { deps: d } = deps({ hostCheck: async () => { reads++; return { facts: { supported: true }, runtime: runtimeDown, steps }; } });
  const g = new DockerGuide(holder(), d, { runtime: runtimeDown, resume: null, result: { facts: {}, runtime: runtimeDown, steps } });
  await Promise.all([g.check(), g.check()]);
  check("RULE 7 (wiring): two clicks, one reading", reads === 1, String(reads));
}

{
  let asked = 0;
  const { calls, deps: d } = deps({ checkRuntime: async () => (++asked >= 2 ? { available: true, reason: "running" } : { available: false, reason: "not-running" }) });
  const g = new DockerGuide(holder(), d, { runtime: runtimeDown, resume: null, result: { facts: {}, runtime: runtimeDown, steps } });
  await g.startDocker();
  check("RULE 8 (wiring): the engine answering ends the wait as ready", g.model.waitResult === "ready" && g.model.waiting === null && calls.start === 1, JSON.stringify(g.model));
  check("RULE 8 (wiring): every row is read again after the wait", calls.hostCheck === 1);
}

{
  const { calls, deps: d } = deps();
  const g = new DockerGuide(holder(), d, { runtime: runtimeDown, resume: null, result: { facts: {}, runtime: runtimeDown, steps } });
  await g.startDocker();
  check("RULE 8 (wiring): no answer in two minutes is a timeout", g.model.waitResult === "timeout" && calls.checkRuntime === START_LIMIT_SECONDS / 3, `${g.model.waitResult} after ${calls.checkRuntime} asks`);
}

{
  const { deps: d } = deps({ startDocker: async () => { throw new Error("not installed"); } });
  const g = new DockerGuide(holder(), d, { runtime: runtimeDown, resume: null, result: { facts: {}, runtime: runtimeDown, steps } });
  await g.startDocker();
  check("RULE 8 (wiring): a start that failed says so and waits for nothing", g.model.waitResult === "failed" && g.model.waiting === null);
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} Docker checklist checks failed`);
  process.exit(1);
}
console.log(`dockerguide: ${checks} checks passed`);
