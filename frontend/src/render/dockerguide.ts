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
import { ht, t } from "./i18n.js";
import { nameList } from "./runtime_state.js";

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

/** The row states this file draws; any other state draws as unknown. */
const STATES = new Set(["done", "needs-you", "blocked", "unknown", "note"]);

/** A row state in a few words, as plain text. */
export function stateLabel(state: string): string {
  switch (state) {
    case "done":
      return t("dockerguide.state.done");
    case "needs-you":
      return t("dockerguide.state.needsYou");
    case "blocked":
      return t("dockerguide.state.blocked");
    case "note":
      return t("dockerguide.state.note");
    default:
      return t("dockerguide.state.unknown");
  }
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
  return `<button class="${cls}" id="${escapeHtml(id)}" data-guide-check ${checking ? 'disabled aria-busy="true"' : ""}>${checking ? ht("dockerguide.checking") : ht("dockerguide.checkAgain")}</button>`;
}

/** The progress line while waiting for Docker's engine, and how the wait ended. */
export function waitLine(model: Pick<GuideModel, "waiting" | "waitResult" | "steps">): string {
  let text = "";
  let tone = "";
  if (model.waiting !== null) {
    text = t("dockerguide.wait.waiting", { seconds: Math.min(model.waiting, START_LIMIT_SECONDS), minutes: START_LIMIT_SECONDS / 60 });
  } else if (model.waitResult === "answered" || model.waitResult === "checklist-answered") {
    text = t("dockerguide.wait.connectedEarlier");
    if (engineAnswers(model.steps)) {
      text = model.waitResult === "answered"
        ? t("dockerguide.wait.answered")
        : t("dockerguide.wait.answeredDuringCheck");
      tone = "ok";
    }
  } else if (model.waitResult === "timeout") {
    text = t("dockerguide.wait.timeout", { minutes: START_LIMIT_SECONDS / 60 });
    tone = "warn";
  } else if (model.waitResult === "failed") {
    text = t("dockerguide.wait.startFailed");
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
      const page = a.url ? `<button class="btn btn-ghost btn-sm" data-guide-url="${escapeHtml(a.url)}">${ht("dockerguide.openDownloadPage")}</button>` : "";
      return `<button class="btn btn-secondary btn-sm" data-guide-action="step" data-guide-op="${escapeHtml(a.op)}" data-guide-step="${escapeHtml(step.id)}" ${busy ? "disabled" : ""}>${label}</button>${page}`;
    }
  }
  return "";
}

function stepHtml(step: GuideStep, index: number, all: GuideStep[], model: GuideModel): string {
  const state = STATES.has(step.state) ? step.state : "unknown";
  let waits = "";
  if (state === "blocked" && step.blockedBy) {
    const at = all.findIndex((s) => s.id === step.blockedBy);
    const dep = all[at];
    waits = dep ? `<small class="docker-step-waits">${ht("dockerguide.firstFinishStep", { n: at + 1, title: dep.title })}</small>` : "";
  }
  const help = step.help
    ? `<details class="docker-step-help"><summary>${ht("dockerguide.whereToFind")}</summary><p>${escapeHtml(step.help)}</p>${
        step.helpUrl ? `<button class="text-link" data-guide-url="${escapeHtml(step.helpUrl)}">${ht("dockerguide.microsoftGuide")}</button>` : ""
      }</details>`
    : "";
  return `<li class="docker-step" data-step="${escapeHtml(step.id)}" data-state="${state}">
      <span class="docker-step-number">${index + 1}</span>
      <div class="docker-step-body">
        <div class="docker-step-head"><strong>${escapeHtml(step.title)}</strong><span class="docker-step-state ${state}">${escapeHtml(stateLabel(state))}</span></div>
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
  const stop = `<button class="btn btn-ghost btn-sm" data-guide-action="cancel-setup">${ht("dockerguide.resume.stop")}</button>`;
  // The sign-in is proven by RunOnce, or by a token that already carries the
  // group (Go's SignedIn): then nothing asks for a sign-out again.
  const signInProven = resume.fromRunOnce || !!resume.signedIn;
  if (resume.finish === "signout" && resume.restarted === "no" && !signInProven) {
    return `<div class="docker-guide-resume warn" data-resume="sign-out">
        <strong>${ht("dockerguide.resume.signOutTitle")}</strong>
        <span>${ht("dockerguide.resume.signOutBody")}</span>
        <div class="docker-step-actions"><button class="btn btn-secondary btn-sm" data-guide-action="finish-signout">${ht("dockerguide.resume.signOut")}</button>${stop}</div>
      </div>`;
  }
  if (resume.restarted === "no" && !(resume.finish === "signout" && signInProven)) {
    const again = resume.step === "a2"
      ? `<button class="btn btn-secondary btn-sm" data-guide-action="firmware" data-guide-step="a2">${ht("dockerguide.resume.restartIntoBios")}</button>`
      : `<button class="btn btn-secondary btn-sm" data-guide-action="finish-restart">${ht("dockerguide.resume.restart")}</button>`;
    return `<div class="docker-guide-resume warn" data-resume="same-boot">
        <strong>${ht("dockerguide.resume.restartTitle")}</strong>
        <span>${ht("dockerguide.resume.restartBody")}</span>
        <div class="docker-step-actions">${again}${stop}</div>
      </div>`;
  }
  const signedIn = resume.finish === "signout" && resume.restarted === "no";
  const said = resume.restarted === "yes"
    ? ht("dockerguide.resume.restarted")
    : signedIn ? ht("dockerguide.resume.signedIn") : ht("dockerguide.resume.checked");
  return `<div class="docker-guide-resume ok" data-resume="${resume.restarted === "yes" ? "new-boot" : signedIn ? "signed-in" : "unknown"}">
      <span>${said}</span>
      <div class="docker-step-actions">${stop}</div>
    </div>`;
}

function summary(model: GuideModel): string {
  if (model.checkFailed) {
    return `<p class="docker-guide-summary">${ht("dockerguide.summary.unknown")}</p>`;
  }
  if (engineAnswers(model.steps) && model.steps?.some((step) => step.id === "a9" && step.state === "done")) {
    return `<p class="docker-guide-summary ok">${ht("dockerguide.summary.ready")}</p>`;
  }
  if (!model.steps) {
    return `<p class="docker-guide-summary">${ht("dockerguide.summary.checking")}</p>`;
  }
  // A note is advice that blocks nothing, so it is not a step to finish.
  const steps = model.steps.filter((s) => s.state !== "note");
  const done = steps.filter((s) => s.state === "done").length;
  const needs = steps.filter((s) => s.state === "needs-you").length;
  const tail = needs === 0 ? "" : ` ${ht("dockerguide.summary.needs", { n: needs })}`;
  return `<p class="docker-guide-summary">${ht("dockerguide.summary.done", { done, total: steps.length })}${tail}</p>`;
}

/** The skip screen's words: what still runs without Docker, honestly. */
export function skipText(native: string[] | null | undefined): string {
  const names = native ?? [];
  if (names.length === 0) {
    return ht("dockerguide.skip.none");
  }
  return ht("dockerguide.skip.some", { n: names.length, names: nameList(names) });
}

/** The confirm screen before a restart into the BIOS. busy: Restart now was clicked. */
export function firmwareConfirm(busy = false): string {
  return `
    <div class="docker-guide-confirm" data-guide-view="firmware-confirm" role="alertdialog" aria-labelledby="fw-title">
      <h2 id="fw-title">${ht("dockerguide.firmware.title")}</h2>
      <ul class="mode-effects">
        <li>${ht("dockerguide.firmware.restarts")}</li>
        <li>${ht("dockerguide.firmware.unsaved")}</li>
        <li>${ht("dockerguide.firmware.whatToTurnOn")}</li>
        <li>${ht("dockerguide.firmware.reopens")}</li>
      </ul>
      <p class="muted compact-copy">${ht("dockerguide.firmware.neverChanges")}</p>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="firmware-go" ${busy ? 'disabled aria-busy="true"' : ""}>${busy ? ht("dockerguide.restarting") : ht("dockerguide.restartNow")}</button>
        <button class="btn btn-ghost" data-guide-action="back" ${busy ? "disabled" : ""}>${ht("dockerguide.cancel")}</button>
      </div>
    </div>`;
}

/** The fallback when Windows could not restart into the BIOS. */
export function firmwareFailed(result: FirmwareRestart | null): string {
  const reopen = result?.reopens
    ? ht("dockerguide.firmwareFailed.reopens")
    : ht("dockerguide.firmwareFailed.reopenYourself");
  return `
    <div class="docker-guide-confirm" data-guide-view="firmware-failed" role="alert">
      <h2>${ht("dockerguide.firmwareFailed.title")}</h2>
      <p>${ht("dockerguide.firmwareFailed.why")}</p>
      <p data-fw-fallback="advanced-startup">${ht("dockerguide.firmwareFailed.advancedStartup")}</p>
      <p data-fw-fallback="power-on-key">${ht("dockerguide.firmwareFailed.powerOnKey")} ${reopen}</p>
      <div class="gate-actions"><button class="btn btn-ghost" data-guide-action="back">${ht("dockerguide.backToChecklist")}</button></div>
    </div>`;
}

/** The skip screen. */
export function skipScreen(native: string[] | null | undefined): string {
  return `
    <div class="docker-guide-confirm" data-guide-view="skip">
      <h2>${ht("dockerguide.skip.title")}</h2>
      <p>${skipText(native)}</p>
      <p class="muted compact-copy">${ht("dockerguide.skip.comeBack")}</p>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="skip-go">${ht("dockerguide.skip.go")}</button>
        <button class="btn btn-ghost" data-guide-action="back">${ht("dockerguide.backToChecklist")}</button>
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
    ? `<button class="btn btn-ghost btn-lg" data-guide-action="skip">${ht("dockerguide.skip.button")}</button>`
    : "";
  return `
    <section class="docker-guide" data-guide-view="list" aria-labelledby="docker-guide-title">
      <h2 id="docker-guide-title">${ht("dockerguide.title")}</h2>
      <p class="muted compact-copy">${ht("dockerguide.intro")}</p>
      ${resumeBanner(model.resume)}
      ${summary(model)}
      <ol class="docker-steps">${steps.map((s, i) => stepHtml(s, i, steps, model)).join("")}</ol>
      ${waitLine(model)}
      ${model.checkFailed ? `<p class="docker-guide-wait warn" role="status">${ht("dockerguide.refreshFailed")}</p>` : ""}
      <div class="gate-actions">
        ${checkAgainButton("docker-guide-check", model.checking || model.waiting !== null)}
        ${skip}
      </div>
    </section>`;
}
