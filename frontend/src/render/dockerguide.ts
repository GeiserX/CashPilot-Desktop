// The Docker checklist on Windows: one row per thing Docker Desktop needs from this
// PC, each with its state, one plain sentence, and the next safe action. First
// run's runtime step and the app's runtime page show the same render.
//
// The rows come from Go (internal/winvm/dockerguide.go), derived from one reading
// of the PC. This file only draws them, plus the screens around them: the confirm
// before a restart into the BIOS, the fallback when that restart cannot happen,
// the honest "native apps only" exit, and (from render/dockersteps.ts) the
// confirm, progress, restart and sign-out screens of each change.
//
// Rules this file holds (scripts/dockerguide_render_check.mjs):
//   - A row is drawn in its own state, and a row that is not done never reads done.
//   - Nothing restarts without the confirm screen, which says open apps close.
//   - A change (admin or not) opens its confirm screen; it never runs from the list.
//
// Pure: takes the model, returns HTML.

import type { FirmwareRestart, GuideStep, HostCheckResult, RuntimeStatus, SetupResumeView, SetupStepResult } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { outcomeHtml, stepConfirm, stepRestart, stepRunning, stepSignOut } from "./dockersteps.js";

export type GuideView =
  | "list"
  | "firmware-confirm"
  | "firmware-failed"
  | "skip"
  | "step-confirm"
  | "step-running"
  | "step-restart"
  | "step-signout";

/** How long the checklist waits for Docker's engine after starting Docker Desktop. */
export const START_LIMIT_SECONDS = 120;

export interface GuideModel {
  /** Rows from the latest check. Null before a result or after a failed check. */
  steps: GuideStep[] | null;
  runtime: Pick<RuntimeStatus, "available" | "reason" | "nativeServices">;
  view: GuideView;
  /** A reading of the PC is running. */
  checking: boolean;
  /** The last checklist read failed. */
  checkFailed?: boolean;
  /** Seconds since Start Docker Desktop while waiting for its engine; null when not waiting. */
  waiting: number | null;
  /** How the last wait ended. A later checklist read can supersede its engine answer. */
  waitResult: "" | "answered" | "checklist-answered" | "timeout" | "failed";
  /** Set when the app opened on the checklist after a restart. */
  resume: SetupResumeView | null;
  /** What the last "Restart into BIOS" answered, when the PC did not restart. */
  firmware: FirmwareRestart | null;
  /** The step whose "Restart into BIOS" opened the confirm screen. */
  firmwareStep?: string;
  /** Restart now was clicked and Go has not answered yet. */
  restarting?: boolean;
  /** The change the step-* screens are about: its op, its row, and whether it uses --web-download. */
  stepOp?: string;
  stepId?: string;
  stepWeb?: boolean;
  /** Seconds since the change started, while it runs; null otherwise. */
  running?: number | null;
  /** How the last change ended. */
  result?: SetupStepResult | null;
  /** Per row, why its last change did not finish. */
  outcomes?: Record<string, SetupStepResult>;
  /** Restart now or Sign out now was clicked and Go has not answered yet. */
  finishing?: boolean;
}

export const STATE_LABEL: Record<string, string> = {
  done: "Done",
  "needs-you": "Needs you",
  blocked: "Waits for an earlier step",
  unknown: "Not known",
  note: "Worth knowing",
};

/** "A", "A and B", "A, B and C". */
function nameList(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/**
 * This PC's own Docker Desktop runs Linux containers, so a guided setup is
 * finished: Go cleared the journal on the same reading and says so
 * (host_check.go setupFinished), and the resume banner goes. An engine elsewhere,
 * or Windows containers, finishes nothing.
 */
export function setupFinished(result: Pick<HostCheckResult, "setupFinished">): boolean {
  return result.setupFinished === true;
}

/** Docker Desktop's own engine answers, in either container mode. */
export function engineAnswers(steps: GuideStep[] | null): boolean {
  return steps?.some((step) => step.id === "a6" && step.state === "done") ?? false;
}

/**
 * The "Check again" button: busy and disabled while a reading runs, so a second
 * click cannot start a second reading and the user sees that one is under way.
 */
export function checkAgainButton(id: string, checking: boolean, cls = "btn btn-ghost btn-lg"): string {
  return `<button class="${cls}" id="${escapeHtml(id)}" data-guide-check ${checking ? 'disabled aria-busy="true"' : ""}>${checking ? "Checking…" : "Check again"}</button>`;
}

/** The progress line while waiting for Docker's engine, and how the wait ended. */
export function waitLine(model: Pick<GuideModel, "waiting" | "waitResult" | "steps">): string {
  let text = "";
  let tone = "";
  if (model.waiting !== null) {
    text = `Waiting for Docker's engine to start: ${Math.min(model.waiting, START_LIMIT_SECONDS)} s of ${START_LIMIT_SECONDS / 60} min.`;
  } else if (model.waitResult === "answered" || model.waitResult === "checklist-answered") {
    text = "CashPilot connected to Docker Desktop earlier.";
    if (engineAnswers(model.steps)) {
      text = model.waitResult === "answered"
        ? "Docker's engine answered."
        : "Docker's engine answered during the checklist check.";
      tone = "ok";
    }
  } else if (model.waitResult === "timeout") {
    text = `Docker's engine did not answer within ${START_LIMIT_SECONDS / 60} minutes. Look at Docker Desktop's window: it may show its terms or a message. Then click Check again.`;
    tone = "warn";
  } else if (model.waitResult === "failed") {
    text = "CashPilot could not start Docker Desktop. Open it from the Start menu, then click Check again.";
    tone = "warn";
  }
  return `<p class="docker-guide-wait ${tone}" data-guide-wait aria-live="polite">${escapeHtml(text)}</p>`;
}

function actionHtml(step: GuideStep, model: GuideModel): string {
  const a = step.action;
  if (!a || !a.kind || step.state === "done" || step.state === "blocked") return "";
  const label = escapeHtml(a.label);
  const busy = model.waiting !== null || model.checking || model.running != null;
  switch (a.kind) {
    case "url":
      return `<button class="btn btn-secondary btn-sm" data-guide-url="${escapeHtml(a.url)}">${label}</button>`;
    case "windows-update":
    case "firmware":
      return `<button class="btn btn-secondary btn-sm" data-guide-action="${escapeHtml(a.kind)}" data-guide-step="${escapeHtml(step.id)}">${label}</button>`;
    case "start-docker":
    case "show-docker":
      return `<button class="btn btn-secondary btn-sm" data-guide-action="${escapeHtml(a.kind)}" ${busy ? "disabled" : ""}>${label}</button>`;
    case "elevated":
    case "run": {
      // The button opens the change's confirm screen; nothing runs from here.
      const page = a.url ? `<button class="btn btn-ghost btn-sm" data-guide-url="${escapeHtml(a.url)}">Open the download page</button>` : "";
      return `<button class="btn btn-secondary btn-sm" data-guide-action="step" data-guide-op="${escapeHtml(a.op)}" data-guide-step="${escapeHtml(step.id)}" ${busy ? "disabled" : ""}>${label}</button>${page}`;
    }
  }
  return "";
}

function stepHtml(step: GuideStep, index: number, all: GuideStep[], model: GuideModel): string {
  const state = STATE_LABEL[step.state] ? step.state : "unknown";
  let waits = "";
  if (state === "blocked" && step.blockedBy) {
    const at = all.findIndex((s) => s.id === step.blockedBy);
    const dep = all[at];
    waits = dep ? `<small class="docker-step-waits">First finish step ${at + 1}: ${escapeHtml(dep.title)}.</small>` : "";
  }
  const help = step.help
    ? `<details class="docker-step-help"><summary>Where to find it</summary><p>${escapeHtml(step.help)}</p>${
        step.helpUrl ? `<button class="text-link" data-guide-url="${escapeHtml(step.helpUrl)}">Microsoft's guide to turning on virtualization</button>` : ""
      }</details>`
    : "";
  return `<li class="docker-step" data-step="${escapeHtml(step.id)}" data-state="${state}">
      <span class="docker-step-number">${index + 1}</span>
      <div class="docker-step-body">
        <div class="docker-step-head"><strong>${escapeHtml(step.title)}</strong><span class="docker-step-state ${state}">${STATE_LABEL[state]}</span></div>
        <p>${escapeHtml(step.text)}</p>
        ${waits}
        ${state === "done" ? "" : outcomeHtml(model.outcomes?.[step.id], step.id)}
        ${help}
        <div class="docker-step-actions">${actionHtml(step, model)}</div>
      </div>
    </li>`;
}

function resumeBanner(resume: SetupResumeView | null): string {
  if (!resume || !resume.active) return "";
  const stop = `<button class="btn btn-ghost btn-sm" data-guide-action="cancel-setup">Stop guiding me after restarts</button>`;
  // The sign-in is proven by RunOnce, or by a token that already carries the
  // group (Go's SignedIn): then nothing asks for a sign-out again.
  const signInProven = resume.fromRunOnce || !!resume.signedIn;
  if (resume.finish === "signout" && resume.restarted === "no" && !signInProven) {
    return `<div class="docker-guide-resume warn" data-resume="sign-out">
        <strong>Sign out to finish</strong>
        <span>Your account joined the docker-users group. Windows applies it after you sign out and back in.</span>
        <div class="docker-step-actions"><button class="btn btn-secondary btn-sm" data-guide-action="finish-signout">Sign out…</button>${stop}</div>
      </div>`;
  }
  if (resume.restarted === "no" && !(resume.finish === "signout" && signInProven)) {
    const again = resume.step === "a2"
      ? `<button class="btn btn-secondary btn-sm" data-guide-action="firmware" data-guide-step="a2">Restart into BIOS</button>`
      : `<button class="btn btn-secondary btn-sm" data-guide-action="finish-restart">Restart…</button>`;
    return `<div class="docker-guide-resume warn" data-resume="same-boot">
        <strong>Restart to finish</strong>
        <span>This PC has not restarted since you chose to. Restart it to finish the step you started.</span>
        <div class="docker-step-actions">${again}${stop}</div>
      </div>`;
  }
  const signedIn = resume.finish === "signout" && resume.restarted === "no";
  const said = resume.restarted === "yes"
    ? "Welcome back. The PC restarted, and CashPilot checked every step again."
    : signedIn ? "Welcome back. You signed in again, and CashPilot checked every step again." : "CashPilot checked every step again.";
  return `<div class="docker-guide-resume ok" data-resume="${resume.restarted === "yes" ? "new-boot" : signedIn ? "signed-in" : "unknown"}">
      <span>${said}</span>
      <div class="docker-step-actions">${stop}</div>
    </div>`;
}

function summary(model: GuideModel): string {
  if (model.checkFailed) {
    return `<p class="docker-guide-summary">Docker Desktop's current status is unknown.</p>`;
  }
  if (engineAnswers(model.steps) && model.steps?.some((step) => step.id === "a9" && step.state === "done")) {
    return `<p class="docker-guide-summary ok">Docker Desktop is ready on this PC.</p>`;
  }
  if (!model.steps) {
    return `<p class="docker-guide-summary">Checking this PC…</p>`;
  }
  // A note is advice that blocks nothing, so it is not a step to finish.
  const steps = model.steps.filter((s) => s.state !== "note");
  const done = steps.filter((s) => s.state === "done").length;
  const needs = steps.filter((s) => s.state === "needs-you").length;
  const tail = needs === 0 ? "" : needs === 1 ? " One step needs you." : ` ${needs} steps need you.`;
  return `<p class="docker-guide-summary">${done} of ${steps.length} steps done.${tail}</p>`;
}

/** The skip screen's words: what still runs without Docker, honestly. */
export function skipText(native: string[] | null | undefined): string {
  const names = native ?? [];
  if (names.length === 0) {
    return "Without Docker, no app in the catalog runs on this computer.";
  }
  const runs = names.length === 1 ? `only ${escapeHtml(names[0])} runs` : `only ${escapeHtml(nameList(names))} run`;
  return `Without Docker, ${runs} on this computer. Every other app in the catalog needs Docker and stays unavailable until it runs.`;
}

/** The confirm screen before a restart into the BIOS. busy: Restart now was clicked. */
export function firmwareConfirm(busy = false): string {
  return `
    <div class="docker-guide-confirm" data-guide-view="firmware-confirm" role="alertdialog" aria-labelledby="fw-title">
      <h2 id="fw-title">Restart into the BIOS now?</h2>
      <ul class="mode-effects">
        <li>Your PC restarts now, straight into its firmware settings (BIOS or UEFI).</li>
        <li>Windows closes every open app. Save your work first: work you have not saved can be lost.</li>
        <li>In the BIOS, turn on Intel Virtualization Technology, VT-x, AMD-V or SVM Mode, then save and exit (often F10).</li>
        <li>When you sign in to Windows again, CashPilot opens this checklist and checks every step again.</li>
      </ul>
      <p class="muted compact-copy">CashPilot never changes BIOS settings itself. You make the change in the BIOS.</p>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="firmware-go" ${busy ? 'disabled aria-busy="true"' : ""}>${busy ? "Restarting…" : "Restart now"}</button>
        <button class="btn btn-ghost" data-guide-action="back" ${busy ? "disabled" : ""}>Cancel</button>
      </div>
    </div>`;
}

/** The fallback when Windows could not restart into the BIOS. */
export function firmwareFailed(result: FirmwareRestart | null): string {
  const reopen = result?.reopens
    ? "When you sign in again, CashPilot opens this checklist by itself."
    : "When you sign in again, open CashPilot: it picks up this checklist where you left it.";
  return `
    <div class="docker-guide-confirm" data-guide-view="firmware-failed" role="alert">
      <h2>This PC could not restart into its BIOS by itself</h2>
      <p>It may have an older BIOS, or Windows did not allow it. Nothing restarted.</p>
      <p data-fw-fallback="advanced-startup">On a UEFI PC, Windows can open the BIOS for you. Save your work, then open Settings, System, Recovery, and click Restart now next to Advanced startup. On the blue screen, choose Troubleshoot, Advanced options, UEFI Firmware Settings, then Restart.</p>
      <p data-fw-fallback="power-on-key">If that option is not there, restart the PC yourself and press the BIOS key as soon as it powers on: often F2 or Del, F10 or Esc on HP, F1 on some Lenovo PCs. ${reopen}</p>
      <div class="gate-actions"><button class="btn btn-ghost" data-guide-action="back">Back to the checklist</button></div>
    </div>`;
}

/** The skip screen. */
export function skipScreen(native: string[] | null | undefined): string {
  return `
    <div class="docker-guide-confirm" data-guide-view="skip">
      <h2>Use native apps only?</h2>
      <p>${skipText(native)}</p>
      <p class="muted compact-copy">You can come back to this checklist at any time: click the runtime status at the top of the app.</p>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="skip-go">Use native apps only</button>
        <button class="btn btn-ghost" data-guide-action="back">Back to the checklist</button>
      </div>
    </div>`;
}

/** The whole checklist, in the screen the model is on. */
export function renderDockerGuide(model: GuideModel): string {
  if (model.view === "firmware-confirm") return `<section class="docker-guide">${firmwareConfirm(!!model.restarting)}</section>`;
  if (model.view === "firmware-failed") return `<section class="docker-guide">${firmwareFailed(model.firmware)}</section>`;
  if (model.view === "step-confirm" && model.stepOp) return `<section class="docker-guide">${stepConfirm(model.stepOp, !!model.stepWeb)}</section>`;
  if (model.view === "step-running" && model.stepOp) {
    const elevated = (model.steps ?? []).find((s) => s.id === model.stepId)?.action?.kind === "elevated";
    return `<section class="docker-guide">${stepRunning(model.stepOp, model.running ?? 0, elevated)}</section>`;
  }
  if (model.view === "step-restart") return `<section class="docker-guide">${stepRestart(model.result ?? null, !!model.finishing)}</section>`;
  if (model.view === "step-signout") return `<section class="docker-guide">${stepSignOut(model.result ?? null, !!model.finishing)}</section>`;
  const native = model.runtime.nativeServices ?? [];
  if (model.view === "skip" && native.length > 0) return `<section class="docker-guide">${skipScreen(native)}</section>`;
  const steps = model.steps ?? [];
  const skip = native.length > 0 && !model.runtime.available
    ? `<button class="btn btn-ghost btn-lg" data-guide-action="skip">Skip, use native apps only</button>`
    : "";
  return `
    <section class="docker-guide" data-guide-view="list" aria-labelledby="docker-guide-title">
      <h2 id="docker-guide-title">Set up Docker on this PC</h2>
      <p class="muted compact-copy">CashPilot checks each thing Docker needs. It changes nothing by itself: every action waits for your click.</p>
      ${resumeBanner(model.resume)}
      ${summary(model)}
      <ol class="docker-steps">${steps.map((s, i) => stepHtml(s, i, steps, model)).join("")}</ol>
      ${waitLine(model)}
      ${model.checkFailed ? '<p class="docker-guide-wait warn" role="status">CashPilot could not refresh the Docker checklist. Click Check again to retry.</p>' : ""}
      <div class="gate-actions">
        ${checkAgainButton("docker-guide-check", model.checking || model.waiting !== null)}
        ${skip}
      </div>
    </section>`;
}
