// The consented changes of the Docker checklist: the confirm screen each one
// opens, the progress line while it runs, and the screens that ask for the
// restart or the sign-out it needs. The changes themselves run in Go
// (docker_setup.go, internal/winvm/setupsteps.go).
//
// Rules this file holds (scripts/dockersteps_render_check.mjs):
//   - Every confirm screen says what will change, whether Windows shows a
//     permission prompt (and that it names Windows PowerShell), whether a
//     restart or sign-out follows, and how to undo the change.
//   - A restart or sign-out happens only from its own screen, which says open
//     apps close and unsaved work can be lost.
//   - The Docker install says CashPilot never accepts Docker's terms, and what
//     to pick in Docker's installer: the WSL 2 backend and Linux containers,
//     which the checklist's later steps expect.
//
// Pure: takes the model, returns HTML.

import type { SetupStepResult } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";

/** What one change's confirm screen says. */
export interface StepConsent {
  title: string;
  /** The running screen's heading: the title as a statement. */
  heading: string;
  /** What will change on this PC. */
  changes: string;
  /** Whether a Windows permission prompt shows, and what it names. */
  prompt: string;
  /** Whether a restart or sign-out follows. */
  after: string;
  /** How to undo the change. */
  undo: string;
  /** What to pick when the installer asks, when it asks anything. */
  choices?: string;
  /** The progress line's words while it runs. */
  running: string;
  /** The button that starts it. */
  go: string;
}

/** The prompt line of every change made with admin rights. */
export function elevatedPrompt(): string {
  return t("dockersteps.elevatedPrompt");
}

function restartAfter(): string {
  return t("dockersteps.restartAfter");
}

/** Every change's confirm screen, keyed by op, in the current language. */
export function consents(): Record<string, StepConsent> {
  const prompt = elevatedPrompt();
  return {
    "wsl-install": {
      title: t("dockersteps.wslInstall.title"),
      heading: t("dockersteps.wslInstall.heading"),
      changes: t("dockersteps.wslInstall.changes", { command: "wsl --install --no-distribution" }),
      prompt,
      after: restartAfter(),
      undo: t("dockersteps.wslInstall.undo"),
      running: t("dockersteps.wslInstall.running"),
      go: t("dockersteps.wslInstall.go"),
    },
    "wsl-update": {
      title: t("dockersteps.wslUpdate.title"),
      heading: t("dockersteps.wslUpdate.heading"),
      changes: t("dockersteps.wslUpdate.changes", { command: "wsl --update" }),
      prompt,
      after: t("dockersteps.wslUpdate.after"),
      undo: t("dockersteps.wslUpdate.undo"),
      running: t("dockersteps.wslUpdate.running"),
      go: t("dockersteps.wslUpdate.go"),
    },
    "docker-install": {
      title: t("dockersteps.dockerInstall.title"),
      heading: t("dockersteps.dockerInstall.heading"),
      changes: t("dockersteps.dockerInstall.changes"),
      prompt: t("dockersteps.dockerInstall.prompt"),
      after: t("dockersteps.dockerInstall.after"),
      undo: t("dockersteps.dockerInstall.undo"),
      choices: t("dockersteps.dockerInstall.choices"),
      running: t("dockersteps.dockerInstall.running"),
      go: t("dockersteps.dockerInstall.go"),
    },
    "docker-users": {
      title: t("dockersteps.dockerUsers.title"),
      heading: t("dockersteps.dockerUsers.heading"),
      changes: t("dockersteps.dockerUsers.changes"),
      prompt,
      after: t("dockersteps.dockerUsers.after"),
      undo: t("dockersteps.dockerUsers.undo"),
      running: t("dockersteps.dockerUsers.running"),
      go: t("dockersteps.dockerUsers.go"),
    },
    "linux-engine": {
      title: t("dockersteps.linuxEngine.title"),
      heading: t("dockersteps.linuxEngine.heading"),
      changes: t("dockersteps.linuxEngine.changes"),
      prompt: t("dockersteps.linuxEngine.prompt"),
      after: t("dockersteps.linuxEngine.after"),
      undo: t("dockersteps.linuxEngine.undo"),
      running: t("dockersteps.linuxEngine.running"),
      go: t("dockersteps.linuxEngine.go"),
    },
    "hypervisor-launch": {
      title: t("dockersteps.hypervisorLaunch.title"),
      heading: t("dockersteps.hypervisorLaunch.heading"),
      changes: t("dockersteps.hypervisorLaunch.changes"),
      prompt,
      after: t("dockersteps.hypervisorLaunch.after"),
      undo: t("dockersteps.hypervisorLaunch.undo", { command: "bcdedit /set hypervisorlaunchtype off" }),
      running: t("dockersteps.hypervisorLaunch.running"),
      go: t("dockersteps.hypervisorLaunch.go"),
    },
    "lanmanserver-auto": {
      title: t("dockersteps.lanmanserverAuto.title"),
      heading: t("dockersteps.lanmanserverAuto.heading"),
      changes: t("dockersteps.lanmanserverAuto.changes"),
      prompt,
      after: t("dockersteps.lanmanserverAuto.after"),
      undo: t("dockersteps.lanmanserverAuto.undo"),
      running: t("dockersteps.lanmanserverAuto.running"),
      go: t("dockersteps.lanmanserverAuto.go"),
    },
  };
}

function consentFor(op: string): StepConsent | undefined {
  return consents()[op];
}

/** How long Go waits for each change, in minutes (winvm.SetupTimeout). */
export const SETUP_MINUTES: Record<string, number> = {
  "wsl-install": 15,
  "wsl-update": 15,
  "docker-install": 60,
  "linux-engine": 3,
};

export function setupMinutes(op: string): number {
  return SETUP_MINUTES[op] ?? 2;
}

/** The words of the direct-download retry on a WSL change. */
export function webDownloadNote(): string {
  return t("dockersteps.webDownloadNote", { command: "wsl --web-download" });
}

/** The confirm screen before a change. busy: Go ahead was clicked. */
export function stepConfirm(op: string, web: boolean, busy = false): string {
  const c = consentFor(op);
  if (!c) return "";
  const retry = web ? `<li data-consent="web-download">${escapeHtml(webDownloadNote())}</li>` : "";
  return `
    <div class="docker-guide-confirm" data-guide-view="step-confirm" data-op="${escapeHtml(op)}" role="alertdialog" aria-labelledby="step-title">
      <h2 id="step-title">${escapeHtml(c.title)}</h2>
      <ul class="mode-effects">
        <li data-consent="changes">${escapeHtml(c.changes)}</li>
        ${c.choices ? `<li data-consent="choices">${escapeHtml(c.choices)}</li>` : ""}
        ${retry}
        <li data-consent="prompt">${escapeHtml(c.prompt)}</li>
        <li data-consent="after">${escapeHtml(c.after)}</li>
        <li data-consent="undo">${escapeHtml(c.undo)}</li>
      </ul>
      <p class="muted compact-copy">${ht("dockersteps.stopsAfter", { minutes: setupMinutes(op) })}</p>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="step-go" ${busy ? 'disabled aria-busy="true"' : ""}>${escapeHtml(c.go)}</button>
        <button class="btn btn-ghost" data-guide-action="back" ${busy ? "disabled" : ""}>${ht("dockersteps.cancel")}</button>
      </div>
    </div>`;
}

/** The progress line while a change runs. */
export function runLine(op: string, seconds: number): string {
  const c = consentFor(op);
  const what = c ? c.running : t("dockersteps.makingChangeRunning");
  return `<p class="docker-guide-wait" data-guide-run aria-live="polite">${escapeHtml(t("dockersteps.runLine", { what, seconds, minutes: setupMinutes(op) }))}</p>`;
}

/** The screen while a change runs. */
export function stepRunning(op: string, seconds: number, elevated: boolean): string {
  const c = consentFor(op);
  const prompt = elevated
    ? `<p>${ht("dockersteps.runningPrompt")}</p>`
    : "";
  return `
    <div class="docker-guide-confirm" data-guide-view="step-running" data-op="${escapeHtml(op)}" aria-busy="true">
      <h2>${escapeHtml(c ? c.heading : t("dockersteps.makingChange"))}</h2>
      ${prompt}
      ${runLine(op, seconds)}
    </div>`;
}

function unsaved(): string {
  return ht("dockersteps.unsaved");
}

function reopenLine(result: Pick<SetupStepResult, "reopens"> | null): string {
  return result?.reopens
    ? t("dockersteps.reopens")
    : t("dockersteps.reopenYourself");
}

/** The screen that asks for the restart a change needs. busy: Restart now was clicked. */
export function stepRestart(result: SetupStepResult | null, busy = false): string {
  const said = result?.text ? `<p>${escapeHtml(result.text)}</p>` : `<p>${ht("dockersteps.restart.waits")}</p>`;
  return `
    <div class="docker-guide-confirm" data-guide-view="step-restart" role="alertdialog" aria-labelledby="restart-title">
      <h2 id="restart-title">${ht("dockersteps.restart.title")}</h2>
      ${said}
      <ul class="mode-effects">
        <li>${ht("dockersteps.restart.now")} ${unsaved()}</li>
        <li>${escapeHtml(reopenLine(result))}</li>
      </ul>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="restart-go" ${busy ? 'disabled aria-busy="true"' : ""}>${busy ? ht("dockersteps.restarting") : ht("dockersteps.restartNow")}</button>
        <button class="btn btn-ghost" data-guide-action="back" ${busy ? "disabled" : ""}>${ht("dockersteps.later")}</button>
      </div>
    </div>`;
}

/** The screen that asks for the sign-out the docker-users change needs. */
export function stepSignOut(result: SetupStepResult | null, busy = false): string {
  const said = result?.text ? `<p>${escapeHtml(result.text)}</p>` : `<p>${ht("dockersteps.signout.joined")}</p>`;
  return `
    <div class="docker-guide-confirm" data-guide-view="step-signout" role="alertdialog" aria-labelledby="signout-title">
      <h2 id="signout-title">${ht("dockersteps.signout.title")}</h2>
      ${said}
      <ul class="mode-effects">
        <li>${ht("dockersteps.signout.now")} ${unsaved()}</li>
        <li>${escapeHtml(reopenLine(result))} ${ht("dockersteps.signout.checksGroup")}</li>
        <li>${ht("dockersteps.signout.restartInstead")}</li>
      </ul>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="signout-go" ${busy ? 'disabled aria-busy="true"' : ""}>${busy ? ht("dockersteps.signingOut") : ht("dockersteps.signOutNow")}</button>
        <button class="btn btn-secondary" data-guide-action="restart-go" ${busy ? "disabled" : ""}>${ht("dockersteps.restartInsteadButton")}</button>
        <button class="btn btn-ghost" data-guide-action="back" ${busy ? "disabled" : ""}>${ht("dockersteps.later")}</button>
      </div>
    </div>`;
}

/** Why the last try of a change did not finish, on its row, with the retry it offers. */
export function outcomeHtml(result: SetupStepResult | undefined, step: string): string {
  if (!result) return "";
  const code = result.code && !result.text.includes(result.code) ? ` ${t("dockersteps.code", { code: result.code })}` : "";
  const retry =
    result.retry === "web-download"
      ? `<button class="btn btn-secondary btn-sm" data-guide-action="step" data-guide-op="${escapeHtml(result.op)}" data-guide-step="${escapeHtml(step)}" data-guide-web="1">${ht("dockersteps.retryDirect")}</button>`
      : "";
  return `<p class="docker-step-reason ${result.state === "policy" ? "warn" : ""}" data-outcome="${escapeHtml(result.state)}">${escapeHtml(result.text + code)}</p>${retry}`;
}
