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
//   - The Docker install says CashPilot never accepts Docker's terms.
//
// Pure: takes the model, returns HTML.

import type { SetupStepResult } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** What one change's confirm screen says. */
export interface StepConsent {
  title: string;
  /** What will change on this PC. */
  changes: string;
  /** Whether a Windows permission prompt shows, and what it names. */
  prompt: string;
  /** Whether a restart or sign-out follows. */
  after: string;
  /** How to undo the change. */
  undo: string;
  /** The progress line's words while it runs. */
  running: string;
  /** The button that starts it. */
  go: string;
}

/** The prompt line of every change made with admin rights. */
export const ELEVATED_PROMPT =
  "Windows shows a permission prompt. It names Windows PowerShell, because CashPilot makes this change through Windows PowerShell. If your account is not an administrator, an administrator types their password there.";

const RESTART_AFTER = "Windows needs a restart to finish. CashPilot asks you first: nothing restarts until you click Restart now.";

export const CONSENT: Record<string, StepConsent> = {
  "wsl-install": {
    title: "Turn on WSL?",
    changes:
      "Windows turns on WSL, its built-in Linux layer, and the Virtual Machine Platform feature WSL needs. CashPilot runs wsl --install --no-distribution, so no Linux distribution such as Ubuntu is installed: Docker brings its own.",
    prompt: ELEVATED_PROMPT,
    after: RESTART_AFTER,
    undo: "To undo, open Settings, System, Optional features, More Windows features, turn off Windows Subsystem for Linux and Virtual Machine Platform, then restart.",
    running: "Windows is turning on WSL",
    go: "Turn on WSL",
  },
  "wsl-update": {
    title: "Update WSL?",
    changes: "Windows updates WSL to its newest version with wsl --update. Your Linux distributions and their files stay as they are.",
    prompt: ELEVATED_PROMPT,
    after: "Usually no restart is needed. If Windows asks for one, CashPilot asks you first. If Docker Desktop is open, close and reopen it afterwards.",
    undo: "CashPilot cannot roll WSL back. Older versions are on Microsoft's WSL releases page on GitHub.",
    running: "Windows is updating WSL",
    go: "Update WSL",
  },
  "docker-install": {
    title: "Install Docker Desktop?",
    changes:
      "A window opens where winget, Windows' app installer, downloads Docker Desktop from Docker and starts its installer. You answer winget's questions in that window. CashPilot never accepts Docker's terms for you: you read and accept them in Docker Desktop when it first opens.",
    prompt:
      "The Docker Desktop installer asks Windows for permission itself, so that prompt names Docker Desktop Installer, not CashPilot. If this PC has no winget, CashPilot opens Docker's download page instead.",
    after: "The installer may ask you to restart or sign out. CashPilot does not restart anything itself.",
    undo: "To undo, open Settings, Apps, Installed apps, find Docker Desktop and click Uninstall.",
    running: "Docker Desktop is installing in winget's window. Finish there; CashPilot waits",
    go: "Open the installer",
  },
  "docker-users": {
    title: "Add your account to docker-users?",
    changes:
      "Windows adds your account, and only yours, to the docker-users group, so Docker Desktop lets you use it. CashPilot names your account by its ID (SID), read before the permission prompt, so it is your account that joins even when an administrator types their password.",
    prompt: ELEVATED_PROMPT,
    after: "Windows applies a new group only after you sign out and back in; a restart works too. CashPilot asks you first: nothing signs you out until you click.",
    undo: "To undo, an administrator opens Computer Management, Local Users and Groups, Groups, docker-users, and removes your account.",
    running: "Windows is adding your account to docker-users",
    go: "Add my account",
  },
  "linux-engine": {
    title: "Switch Docker to Linux containers?",
    changes:
      "Docker Desktop switches from Windows containers to Linux containers. This changes all your Docker work, not only CashPilot's: Windows containers you run stop, and their images stay but do not show until you switch back.",
    prompt: "No Windows permission prompt: this is your own Docker Desktop setting.",
    after: "No restart. Docker's engine restarts, which takes a minute or two.",
    undo: "To switch back, right-click the Docker icon next to the clock and choose Switch to Windows containers.",
    running: "Docker Desktop is switching to Linux containers",
    go: "Switch to Linux containers",
  },
  "hypervisor-launch": {
    title: "Turn Windows' hypervisor back on?",
    changes:
      "CashPilot first tries to read Windows' boot option hypervisorlaunchtype. If the read works and it is already auto, nothing changes and no restart follows. Otherwise Windows sets it to auto (with bcdedit), so its hypervisor starts at boot, as WSL 2 and Docker need. Older VirtualBox, VMware or Android emulator versions may have turned it off. Once it is on, they run on top of it and may be slower, and very old versions may not start their machines: update them to their newest versions.",
    prompt: ELEVATED_PROMPT,
    after:
      "A restart is needed only if CashPilot changed the boot option. Then CashPilot asks you first: nothing restarts until you click Restart now. If the read showed it was already auto, no restart follows.",
    undo: "To undo, open PowerShell as administrator, run bcdedit /set hypervisorlaunchtype off, and restart. WSL 2 and Docker then stop working again.",
    running: "Windows is turning its hypervisor back on",
    go: "Turn it back on",
  },
  "lanmanserver-auto": {
    title: "Set the Server service to Automatic?",
    changes:
      "Windows sets its Server service (LanmanServer) to start automatically, and starts it now. Docker Desktop needs it. The same service shares this PC's files and printers with your network, if you turned sharing on.",
    prompt: ELEVATED_PROMPT,
    after: "No restart is needed.",
    undo: "To undo, open Services, find Server, and set its Startup type back to Manual or Disabled.",
    running: "Windows is setting the Server service to Automatic",
    go: "Set it to Automatic",
  },
};

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
export const WEB_DOWNLOAD_NOTE =
  "This time WSL downloads straight from Microsoft's GitHub page instead of the Microsoft Store (wsl --web-download).";

/** The confirm screen before a change. busy: Go ahead was clicked. */
export function stepConfirm(op: string, web: boolean, busy = false): string {
  const c = CONSENT[op];
  if (!c) return "";
  const retry = web ? `<li data-consent="web-download">${escapeHtml(WEB_DOWNLOAD_NOTE)}</li>` : "";
  return `
    <div class="docker-guide-confirm" data-guide-view="step-confirm" data-op="${escapeHtml(op)}" role="alertdialog" aria-labelledby="step-title">
      <h2 id="step-title">${escapeHtml(c.title)}</h2>
      <ul class="mode-effects">
        <li data-consent="changes">${escapeHtml(c.changes)}</li>
        ${retry}
        <li data-consent="prompt">${escapeHtml(c.prompt)}</li>
        <li data-consent="after">${escapeHtml(c.after)}</li>
        <li data-consent="undo">${escapeHtml(c.undo)}</li>
      </ul>
      <p class="muted compact-copy">It stops by itself if it takes longer than ${setupMinutes(op)} minutes.</p>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="step-go" ${busy ? 'disabled aria-busy="true"' : ""}>${escapeHtml(c.go)}</button>
        <button class="btn btn-ghost" data-guide-action="back" ${busy ? "disabled" : ""}>Cancel</button>
      </div>
    </div>`;
}

/** The progress line while a change runs. */
export function runLine(op: string, seconds: number): string {
  const c = CONSENT[op];
  const what = c ? c.running : "CashPilot is making the change";
  return `<p class="docker-guide-wait" data-guide-run aria-live="polite">${escapeHtml(`${what}: ${seconds} s so far. CashPilot stops waiting after ${setupMinutes(op)} minutes.`)}</p>`;
}

/** The screen while a change runs. */
export function stepRunning(op: string, seconds: number, elevated: boolean): string {
  const c = CONSENT[op];
  const prompt = elevated
    ? `<p>If Windows shows a permission prompt naming Windows PowerShell, it is this change: choose Yes to go ahead, or No to stop.</p>`
    : "";
  return `
    <div class="docker-guide-confirm" data-guide-view="step-running" data-op="${escapeHtml(op)}" aria-busy="true">
      <h2>${escapeHtml(c ? c.title.replace(/\?$/, "") : "Making the change")}</h2>
      ${prompt}
      ${runLine(op, seconds)}
    </div>`;
}

const UNSAVED = "Windows closes every open app. Save your work first: work you have not saved can be lost.";

function reopenLine(result: Pick<SetupStepResult, "reopens"> | null): string {
  return result?.reopens
    ? "When you sign in again, CashPilot opens this checklist by itself and checks every step again."
    : "When you sign in again, open CashPilot: it picks up this checklist where you left it.";
}

/** The screen that asks for the restart a change needs. busy: Restart now was clicked. */
export function stepRestart(result: SetupStepResult | null, busy = false): string {
  const said = result?.text ? `<p>${escapeHtml(result.text)}</p>` : `<p>A change you made waits for a restart.</p>`;
  return `
    <div class="docker-guide-confirm" data-guide-view="step-restart" role="alertdialog" aria-labelledby="restart-title">
      <h2 id="restart-title">Restart to finish</h2>
      ${said}
      <ul class="mode-effects">
        <li>Restart now restarts your PC at once. ${UNSAVED}</li>
        <li>${escapeHtml(reopenLine(result))}</li>
      </ul>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="restart-go" ${busy ? 'disabled aria-busy="true"' : ""}>${busy ? "Restarting…" : "Restart now"}</button>
        <button class="btn btn-ghost" data-guide-action="back" ${busy ? "disabled" : ""}>Later</button>
      </div>
    </div>`;
}

/** The screen that asks for the sign-out the docker-users change needs. */
export function stepSignOut(result: SetupStepResult | null, busy = false): string {
  const said = result?.text ? `<p>${escapeHtml(result.text)}</p>` : `<p>Your account joined the docker-users group, and Windows applies it after you sign out and back in.</p>`;
  return `
    <div class="docker-guide-confirm" data-guide-view="step-signout" role="alertdialog" aria-labelledby="signout-title">
      <h2 id="signout-title">Sign out and back in to finish</h2>
      ${said}
      <ul class="mode-effects">
        <li>Sign out now signs you out of Windows at once. ${UNSAVED}</li>
        <li>${escapeHtml(reopenLine(result))} It then checks that Windows gave your account the group.</li>
        <li>Restart instead does the same, through a restart.</li>
      </ul>
      <div class="gate-actions">
        <button class="btn btn-primary" data-guide-action="signout-go" ${busy ? 'disabled aria-busy="true"' : ""}>${busy ? "Signing out…" : "Sign out now"}</button>
        <button class="btn btn-secondary" data-guide-action="restart-go" ${busy ? "disabled" : ""}>Restart instead</button>
        <button class="btn btn-ghost" data-guide-action="back" ${busy ? "disabled" : ""}>Later</button>
      </div>
    </div>`;
}

/** Why the last try of a change did not finish, on its row, with the retry it offers. */
export function outcomeHtml(result: SetupStepResult | undefined, step: string): string {
  if (!result) return "";
  const code = result.code && !result.text.includes(result.code) ? ` (code ${result.code})` : "";
  const retry =
    result.retry === "web-download"
      ? `<button class="btn btn-secondary btn-sm" data-guide-action="step" data-guide-op="${escapeHtml(result.op)}" data-guide-step="${escapeHtml(step)}" data-guide-web="1">Try again with a direct download</button>`
      : "";
  return `<p class="docker-step-reason ${result.state === "policy" ? "warn" : ""}" data-outcome="${escapeHtml(result.state)}">${escapeHtml(result.text + code)}</p>${retry}`;
}
