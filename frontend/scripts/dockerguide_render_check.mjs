#!/usr/bin/env node
// Browser-free checks on the Docker checklist (src/render/dockerguide.ts) and its
// wiring (src/render/dockerguide_wire.ts), against the HTML the real functions
// return.
//
//   1. EACH STATE IS DRAWN AS ITSELF. done, needs you, blocked, unknown and note each get
//      their own label, and no row that is not done is labelled Done.
//   2. A BLOCKED ROW names the step to finish first and offers nothing to click.
//   3. A CHANGE OPENS ITS CONFIRM SCREEN: the row's button names the change, and the
//      list itself never holds the button that runs it (dockersteps_render_check).
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
//  11. A NOTE IS ADVICE: it reads "Worth knowing", never Done or Needs you, and the
//      summary does not count it as a step to finish.
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
check("RULE 3: the admin action opens its confirm screen", /<button[^>]*data-guide-action="step" data-guide-op="lanmanserver-auto" data-guide-step="a12"/.test(byId.a12.html) && !/<button[^>]*disabled/.test(byId.a12.html), byId.a12.html);
check("RULE 3: the list never holds the button that runs a change", !/step-go|restart-go|signout-go/.test(list));

// RULE 4
check("RULE 4: the BIOS row opens the confirm screen", /data-guide-action="firmware" data-guide-step="a2"/.test(byId.a2.html), byId.a2.html);
check("RULE 4: the list itself has no restart button", !/firmware-go/.test(list));
check("RULE 4: the BIOS help text and Microsoft's guide are there", text(byId.a2.html).includes("Often F2 or Del.") && /data-guide-url="https:\/\/support.microsoft.com\/x"/.test(byId.a2.html), byId.a2.html);
const confirm = renderDockerGuide({ ...base, view: "firmware-confirm" });
check("RULE 4: the confirm screen is the confirm screen", confirm === `<section class="docker-guide">${firmwareConfirm()}</section>`);
check("RULE 4: it says the PC restarts now", text(confirm).includes("Your PC restarts now"), text(confirm));
check("RULE 4: it says open apps close and unsaved work can be lost", text(confirm).includes("Windows closes every open app. Save your work first: work you have not saved can be lost."), text(confirm));
check("RULE 4: it says CashPilot never changes BIOS settings", text(confirm).includes("CashPilot never changes BIOS settings itself."), text(confirm));
const restarting = renderDockerGuide({ ...base, view: "firmware-confirm", restarting: true });
check("RULE 4: while the restart is asked, Restart now is busy and disabled", /<button[^>]*data-guide-action="firmware-go"[^>]*disabled[^>]*aria-busy="true"[^>]*>Restarting…</.test(restarting), restarting);
check("RULE 4: while the restart is asked, Cancel waits too", /data-guide-action="back" disabled/.test(restarting), restarting);
check("RULE 4: it has the restart and a way out", /data-guide-action="firmware-go"/.test(confirm) && /data-guide-action="back"/.test(confirm), confirm);

// RULE 5
const failed = firmwareFailed({ started: false, reopens: true });
check("RULE 5: the fallback says nothing restarted", text(failed).includes("Nothing restarted."), text(failed));
check("RULE 5: the fallback offers Windows' Advanced startup, which needs no admin", text(failed).includes("Settings, System, Recovery") && text(failed).includes("Advanced startup") && text(failed).includes("UEFI Firmware Settings"), text(failed));
check("RULE 5: Advanced startup comes before the power-on keys", failed.indexOf("advanced-startup") < failed.indexOf("power-on-key") && failed.indexOf("advanced-startup") > 0, failed);
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
check("RULE 8: the running row tells whether the local engine answers", engineAnswers([{ id: "a6", state: "done" }]) && !engineAnswers(steps));

// RULE 9
const sameBoot = renderDockerGuide({ ...base, resume: { active: true, flow: "docker", step: "a2", restarted: "no", exeMoved: false, attempts: 1, fromRunOnce: false } });
check("RULE 9: same boot says Restart to finish", text(sameBoot).includes("Restart to finish") && text(sameBoot).includes("has not restarted"), text(sameBoot));
check("RULE 9: same boot offers the BIOS restart again, through the confirm screen", /data-resume="same-boot"[\s\S]*data-guide-action="firmware" data-guide-step="a2"/.test(sameBoot), sameBoot);
check("RULE 9: same boot is not an error", !/fail|error/i.test(text(sameBoot.match(/data-resume[\s\S]*?<\/div>\s*<\/div>/)?.[0] || "")));
const newBoot = renderDockerGuide({ ...base, resume: { active: true, flow: "docker", step: "a2", restarted: "yes", exeMoved: false, attempts: 1, fromRunOnce: true } });
check("RULE 9: a new boot says the checks ran again", text(newBoot).includes("Welcome back. The PC restarted, and CashPilot checked every step again."), text(newBoot));
check("RULE 9: the user can stop the guiding", /data-guide-action="cancel-setup"/.test(newBoot));
check("RULE 9: no resume, no banner", !/data-resume/.test(list));

// RULE 11
const noted = renderDockerGuide({ ...base, steps: [...steps, { id: "a10", title: "Other virtual machine software", state: "note", text: "VirtualBox is installed.", action: none }] });
const noteRow = rows(noted).find((r) => r.id === "a10");
check("RULE 11: a note reads Worth knowing", rowLabel(noteRow) === "Worth knowing", noteRow?.html);
check("RULE 11: a note is not a step to finish", text(noted).includes("1 of 6 steps done. 3 steps need you."), text(noted));
const allNotes = renderDockerGuide({ ...base, steps: [steps[0], { id: "a12", title: "Windows Server service", state: "note", text: "Docker runs, but the Server service is set to Manual.", action: none }] });
check("RULE 11: a note never makes the summary say a step needs you", text(allNotes).includes("1 of 1 steps done.") && !/need/.test(text(allNotes).match(/\d+ of \d+ steps done\.[^.]*\.?/)?.[0] || ""), text(allNotes));

// RULE 10
const hostile = renderDockerGuide({ ...base, steps: [{ id: "a1", title: "<b>t</b>", state: "needs-you", text: "<img src=x onerror=alert(1)>", action: { kind: "url", label: "<i>l</i>", url: '"><script>' } }] });
check("RULE 10: Go's text is escaped", !/<img|<script|<b>t|<i>l/.test(hostile) && hostile.includes("&lt;img"), hostile);

// The ready summary.
const localReady = {
  ...base,
  runtime: { ...runtimeDown, available: true, reason: "running" },
  steps: [
    { id: "a6", title: "Docker Desktop running", state: "done", text: "Docker's engine answers.", action: none },
    { id: "a9", title: "Linux containers", state: "done", text: "Docker runs Linux containers.", action: none },
  ],
};
check("Docker Desktop ready says so with a local Linux engine", text(renderDockerGuide(localReady)).includes("Docker Desktop is ready on this PC."));
const localUnknown = { ...localReady, steps: [{ ...localReady.steps[0], state: "unknown" }, localReady.steps[1]], runtime: { ...runtimeDown, reason: "unreachable" } };
check("An inconclusive local running row suppresses the ready summary even with a completed Linux engine row",
  !renderDockerGuide(localUnknown).includes("Docker Desktop is ready on this PC."));
check("Before the first reading it says it is checking", text(renderDockerGuide({ ...base, steps: null })).includes("Checking this PC…"));

const elsewhere = { ...base, runtime: { available: true, reason: "running", nativeServices: [] } };
check("a working engine elsewhere does not make the local checklist ready", text(renderDockerGuide(elsewhere)).includes("3 steps need you.") && !text(renderDockerGuide(elsewhere)).includes("is ready"));

// The wiring, against a holder that only takes HTML.
const holder = () => ({ innerHTML: "", querySelectorAll: () => [], querySelector: () => null });
const deps = (over = {}, initialRuntime = runtimeDown) => {
  let now = 0;
  let runtime = initialRuntime;
  const calls = { firmware: [], hostCheck: 0, engineReady: 0, start: 0, cancel: 0, skip: 0 };
  const hostCheck = over.hostCheck ?? (async () => {
    calls.hostCheck++;
    return { facts: { supported: true }, runtime: runtimeDown, steps };
  });
  return {
    calls,
    deps: {
      startDocker: async () => { calls.start++; },
      dockerEngineReady: async () => { calls.engineReady++; return false; },
      openWindowsUpdate: async () => {},
      restartToFirmware: async (step) => { calls.firmware.push(step); return { started: false, reopens: true }; },
      cancelSetup: async () => { calls.cancel++; },
      runSetupStep: async () => { throw new Error("no change may run in these checks"); },
      restartNow: async () => { throw new Error("no restart in these checks"); },
      signOutNow: async () => { throw new Error("no sign-out in these checks"); },
      openUrl: () => {},
      skip: () => { calls.skip++; },
      sleep: async (ms) => { now += ms; },
      now: () => now,
      ...over,
      currentRuntime: () => runtime,
      hostCheck: async () => {
        try {
          const result = await hostCheck();
          runtime = result.runtime;
          return result;
        } catch (error) {
          runtime = { ...runtime, available: false, reason: "unreachable" };
          throw error;
        }
      },
    },
  };
};

{
  const { calls, deps: d } = deps();
  const g = new DockerGuide(holder(), d, { resume: null, result: { facts: { supported: true }, runtime: runtimeDown, steps } });
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
  const g = new DockerGuide(holder(), d, { resume: null });
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
  const g = new DockerGuide(holder(), d, { resume: null, result: { facts: {}, runtime: runtimeDown, steps } });
  await Promise.all([g.check(), g.check()]);
  check("RULE 7 (wiring): two clicks, one reading", reads === 1, String(reads));
}

{
  const reads = [];
  const remote = { available: true, reason: "running" };
  const { calls, deps: d } = deps({
    dockerEngineReady: async () => reads.push("pipe") === 2,
    hostCheck: async () => {
      reads.push("checklist");
      return { runtime: remote, steps: [{ id: "a6", state: "done" }] };
    },
  }, remote);
  const g = new DockerGuide(holder(), d, { resume: null, result: { facts: {}, runtime: remote, steps } });
  await g.startDocker();
  check("RULE 8 (wiring): the wait continues until this PC's engine answers", g.model.waitResult === "answered" && g.model.waiting === null && calls.start === 1, JSON.stringify(g.model));
  check("RULE 8 (wiring): only the local pipe is checked until the engine answers", reads.join() === "pipe,pipe,checklist", reads.join());
  check("RULE 8 (wiring): the checklist shows the reading that ended the wait", g.model.steps?.[0]?.id === "a6" && g.model.steps?.[0]?.state === "done");
}

for (const rejects of [false, true]) {
  const runningUnknown = { ...steps.find((step) => step.id === "a6"), state: "unknown" };
  const reading = { runtime: { ...runtimeDown, reason: "unreachable" }, steps: [runningUnknown] };
  const error = new Error("HostCheck unavailable");
  let reads = 0;
  let reported;
  const { deps: d } = deps({
    dockerEngineReady: async () => true,
    hostCheck: async () => {
      reads++;
      if (rejects && reads === 1) throw error;
      return reading;
    },
    failed: (err) => { reported = err; },
  });
  const h = holder();
  const g = new DockerGuide(h, d, { resume: null, result: { runtime: runtimeDown, steps } });
  await g.startDocker();
  const outcome = rejects ? "the checklist rejects" : "the running row is unknown";
  const message = text(waitLine(g.model));
  check(`RULE 8 (wiring): the first pipe answer ends the wait when ${outcome}`, d.now() === 3000 && reads === 1 && g.model.waiting === null);
  check(`RULE 8 (wiring): the pipe answer is shown as history when ${outcome}`,
    message === "CashPilot connected to Docker Desktop earlier." && !h.innerHTML.includes("docker-guide-wait ok"), message);
  if (rejects) {
    check("RULE 8 (wiring): a rejected checklist read is reported separately", reported === error && text(h.innerHTML).includes("could not refresh the Docker checklist"), text(h.innerHTML));
    await g.check();
    check("RULE 8 (wiring): a successful retry clears the checklist error", reads === 2 && !text(h.innerHTML).includes("could not refresh the Docker checklist"));
  } else {
    check("RULE 8 (wiring): an unknown checklist row keeps its own status", rowLabel(rows(h.innerHTML).find((row) => row.id === "a6")) === "Not known");
  }
}

{
  const { calls, deps: d } = deps();
  const g = new DockerGuide(holder(), d, { resume: null, result: { facts: {}, runtime: runtimeDown, steps } });
  const wait = g.startDocker();
  await g.check();
  await wait;
  check("RULE 8 (wiring): no answer in two minutes is a timeout", g.model.waitResult === "timeout" && d.now() === START_LIMIT_SECONDS * 1000, `${g.model.waitResult} after ${d.now()} ms`);
  check("RULE 8 (wiring): the local pipe is polled up to the deadline", calls.engineReady === START_LIMIT_SECONDS / 3 - 1, `${calls.engineReady} pipe checks`);
  check("RULE 8 (wiring): the full checklist is read once after a timeout", calls.hostCheck === 1, `${calls.hostCheck} HostCheck calls`);
}

for (const retryFails of [true, false]) {
  const confirmation = "Docker's engine answered during the checklist check.";
  const readySummary = "Docker Desktop is ready on this PC.";
  const running = { id: "a6", title: "Docker Desktop running", state: "done", text: "Docker's engine answers.", action: none };
  const linux = { id: "a9", title: "Linux containers", state: "done", text: "Docker runs Linux containers.", action: none };
  const stopped = { ...running, state: "needs-you", text: "Docker Desktop is not running.", action: { kind: "start-docker", label: "Start Docker Desktop" } };
  const runtimeUp = { ...runtimeDown, available: true, reason: "running" };
  const error = new Error("HostCheck unavailable");
  let reads = 0;
  let reported;
  const { deps: d } = deps({
    hostCheck: async () => {
      if (++reads === 1) return { runtime: runtimeUp, steps: [running, linux] };
      if (retryFails) throw error;
      return { runtime: runtimeDown, steps: [stopped] };
    },
    failed: (err) => { reported = err; },
  });
  const h = holder();
  const g = new DockerGuide(h, d, { resume: null, result: { runtime: runtimeDown, steps } });
  await g.startDocker();
  const beforeRetry = text(h.innerHTML);
  check("RULE 8 (wiring): the deadline checklist can confirm readiness",
    d.now() === START_LIMIT_SECONDS * 1000 && beforeRetry.includes(confirmation) && beforeRetry.includes(readySummary), beforeRetry);
  await g.check();
  const message = text(h.innerHTML);
  const runningRow = rows(h.innerHTML).find((row) => row.id === "a6");
  const currentReadiness = message.includes(readySummary) || h.innerHTML.includes('class="docker-guide-wait ok"') || runningRow?.state === "done";
  if (retryFails) {
    check("RULE 8 (wiring): after readiness, a failed retry shows unknown status and an earlier connection",
      reads === 2 && reported === error && message.includes("Docker Desktop's current status is unknown.") &&
      message.includes("CashPilot connected to Docker Desktop earlier.") && message.includes("could not refresh the Docker checklist") &&
      !message.includes(confirmation) && !message.includes("may show its terms") && !currentReadiness, message);
  } else {
    check("RULE 8 (wiring): after readiness, a retry that finds Docker stopped clears the confirmation",
      reads === 2 && runningRow?.state === "needs-you" && rowLabel(runningRow) === "Needs you" &&
      message.includes("Docker Desktop is not running.") && !message.includes(confirmation) &&
      !message.includes("could not refresh the Docker checklist") && !currentReadiness, message);
  }
}

{
  const { deps: d } = deps({ startDocker: async () => { throw new Error("not installed"); } });
  const g = new DockerGuide(holder(), d, { resume: null, result: { facts: {}, runtime: runtimeDown, steps } });
  await g.startDocker();
  check("RULE 8 (wiring): a start that failed says so and waits for nothing", g.model.waitResult === "failed" && g.model.waiting === null);
}

{
  // A double click on Start Docker Desktop starts it once, and a failed start
  // frees the button again.
  let opened;
  const { calls, deps: d } = deps({ startDocker: () => { calls.start++; return new Promise((r, reject) => { opened = { r, reject }; }); } });
  const g = new DockerGuide(holder(), d, { resume: null, result: { facts: {}, runtime: runtimeDown, steps } });
  const first = g.startDocker();
  const second = g.startDocker();
  check("RULE 8 (wiring): a double click on Start Docker Desktop starts it once", calls.start === 1, String(calls.start));
  check("RULE 8 (wiring): the button is busy while Windows starts Docker Desktop", g.model.waiting !== null);
  opened.reject(new Error("not installed"));
  await Promise.all([first, second]);
  check("RULE 8 (wiring): a failed start frees the button", g.model.waiting === null && g.model.waitResult === "failed", JSON.stringify(g.model));
  void g.startDocker();
  check("RULE 8 (wiring): after a failed start, a new click starts again", calls.start === 2, String(calls.start));
  opened.reject(new Error("still not installed"));
  await new Promise((r) => setTimeout(r, 0));
}

{
  // A double click on Restart now asks Windows once.
  let answer;
  const { calls, deps: d } = deps({ restartToFirmware: (step) => { calls.firmware.push(step); return new Promise((r) => { answer = r; }); } });
  const g = new DockerGuide(holder(), d, { resume: null, result: { facts: {}, runtime: runtimeDown, steps } });
  await g.act("firmware", "a2");
  const first = g.act("firmware-go", "");
  const second = g.act("firmware-go", "");
  check("RULE 4 (wiring): a double click on Restart now restarts once", calls.firmware.length === 1, JSON.stringify(calls.firmware));
  check("RULE 4 (wiring): Restart now is busy until Go answers", g.model.restarting === true);
  answer({ started: false, reopens: false });
  await Promise.all([first, second]);
  check("RULE 4 (wiring): a refused restart frees the button", g.model.restarting === false && g.model.view === "firmware-failed", JSON.stringify(g.model));
  await g.act("back", "");
  await g.act("firmware", "a2");
  const third = g.act("firmware-go", "");
  check("RULE 4 (wiring): after a refusal, a new click asks again", calls.firmware.length === 2, JSON.stringify(calls.firmware));
  answer({ started: true, reopens: true });
  await third;
  check("RULE 4 (wiring): a restart Windows started keeps the button busy", g.model.restarting === true);
}

{
  // Docker came up: the setup is finished, so no "Restart to finish" stays.
  const sameBootResume = { active: true, flow: "docker", step: "a2", restarted: "no", exeMoved: false, attempts: 1, fromRunOnce: false };
  const running = { available: true, reason: "running", nativeServices: [] };
  // up: an engine answers in Linux mode; local: Go found it is this PC's own
  // Docker Desktop and ended the setup (host_check.go setupFinished).
  let up = false;
  let local = false;
  const { deps: d } = deps({ hostCheck: async () => ({ facts: { supported: true }, runtime: up ? running : runtimeDown, steps, setupFinished: up && local }) });
  const g = new DockerGuide(holder(), d, { resume: sameBootResume, result: { facts: {}, runtime: runtimeDown, steps, setupFinished: false } });
  await g.check();
  check("RULE 9 (wiring): Docker still down keeps the banner", g.model.resume === sameBootResume);
  up = true;
  await g.check();
  check("RULE 9 (wiring): an engine elsewhere answering keeps the banner, as Go keeps the journal", g.model.resume === sameBootResume);
  local = true;
  await g.check();
  check("RULE 9 (wiring): a reading that finds this PC's Docker running drops the banner", g.model.resume === null && !/data-resume/.test(renderDockerGuide(g.model)));
  const later = new DockerGuide(holder(), { ...d, currentRuntime: () => running }, { resume: sameBootResume, result: { facts: {}, runtime: running, steps, setupFinished: true } });
  check("RULE 9 (wiring): a guide opened with this PC's Docker running draws no banner", later.model.resume === null);
  const remote = new DockerGuide(holder(), { ...d, currentRuntime: () => running }, { resume: sameBootResume, result: { facts: {}, runtime: running, steps, setupFinished: false } });
  check("RULE 9 (wiring): a guide opened with an engine elsewhere answering keeps the banner", remote.model.resume === sameBootResume);
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} Docker checklist checks failed`);
  process.exit(1);
}
console.log(`dockerguide: ${checks} checks passed`);
