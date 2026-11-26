// How this computer runs: "Run everything here" (lead) or "Join my CashPilot"
// (follower). The first-run step, the join form, the dashboard banner while
// joined, and the Settings card that switches.
//
// Pure: takes state, returns HTML. main.ts wires the buttons. Nothing here ever
// puts the key back into the page: the form keeps what the user typed, and a
// result carries the server's address, never the key.

import type { JoinResult, LeaveResult, ModeState, PairingStatus } from "../wails";
import { escapeHtml, relativeTime } from "./format.js"; // .js so the emitted ESM resolves in Node

export type Mode = "lead" | "follower" | "";

export interface FirstRunStep {
  id: string;
  label: string;
}

/**
 * The first-run steps, as data, so a later step (the Secure VM's "desktop apps"
 * step after the runtime) is one more entry, not another template. The join step
 * shows only on the "Join my CashPilot" path.
 */
export function firstRunSteps(path: Mode): FirstRunStep[] {
  const steps: FirstRunStep[] = [
    { id: "welcome", label: "Welcome" },
    { id: "mode", label: "How it runs" },
  ];
  if (path === "follower") steps.push({ id: "join", label: "Join" });
  steps.push({ id: "runtime", label: "Runtime" });
  return steps;
}

/** The step bar: done steps before the active one, the active one, then the rest. */
export function renderGateSteps(steps: FirstRunStep[], activeId: string): string {
  const active = Math.max(0, steps.findIndex((s) => s.id === activeId));
  const items = steps
    .map((s, i) => {
      const cls = i === active ? "active" : i < active ? "completed" : "";
      return `<li class="gate-step ${cls}" data-step="${escapeHtml(s.id)}"><span class="wizard-step-number">${i + 1}</span>${escapeHtml(s.label)}</li>`;
    })
    .join("");
  return `<ol class="gate-steps" aria-label="Step ${active + 1} of ${steps.length}">${items}</ol>`;
}

/** First run, step 1. "Get started" is the screen's only control. */
export function renderWelcome(): string {
  return `
    <h1 class="gate-title">Welcome to CashPilot</h1>
    <p class="gate-lede">Let your spare resources earn in the background while CashPilot handles setup, monitoring, and payouts from one place.</p>
    <div class="gate-actions">
      <button class="btn btn-primary btn-lg" id="get-started">Get started</button>
    </div>
  `;
}

export const LEAD_TITLE = "Run everything here";
export const FOLLOWER_TITLE = "Join my CashPilot";

/** The two cards of the first-run choice. Nothing is pre-selected. */
export function renderModeChoice(): string {
  return `
    <h1 class="gate-title">How will this computer run?</h1>
    <p class="gate-lede">Pick one. You can switch later in Settings.</p>
    <div class="mode-choice">
      <button class="mode-card" data-mode-choice="lead">
        <strong>${LEAD_TITLE}</strong>
        <span>This computer collects your earnings, shows the dashboard and runs its earning apps. No CashPilot server needed.</span>
        <small>Pick this if you do not run CashPilot anywhere else.</small>
      </button>
      <button class="mode-card" data-mode-choice="follower">
        <strong>${FOLLOWER_TITLE}</strong>
        <span>This computer runs earning apps for a CashPilot server you already have. The server shows the earnings.</span>
        <small>Pick this if you already run the CashPilot dashboard on a server or another computer.</small>
      </button>
    </div>
    <p class="muted compact-copy">Not sure? Pick ${LEAD_TITLE}.</p>
  `;
}

/**
 * The join form: paste the "Add a box" block, or type the address and key. The
 * key field is a password field, and the form carries no value: a redraw never
 * prints the key. The outcome goes into #join-result (renderJoinOutcome) so the
 * user's paste stays where it is when a try fails.
 */
export function renderJoinForm(): string {
  return `
    <form class="join-form" id="join-form" autocomplete="off">
      <p class="gate-lede">On your CashPilot, open <strong>Fleet</strong>, then <strong>Add a box</strong>. Press <strong>Reveal API Key</strong>, then <strong>Copy</strong>, and paste it here.</p>
      <label class="join-field">
        <span>Paste from Add a box</span>
        <textarea name="block" rows="4" spellcheck="false" autocomplete="off" placeholder="CASHPILOT_UI_URL=http://192.168.1.10:8080&#10;CASHPILOT_API_KEY=..."></textarea>
      </label>
      <details class="join-typed">
        <summary>Type them instead</summary>
        <label class="join-field">
          <span>Address of your CashPilot</span>
          <input name="url" type="text" spellcheck="false" autocomplete="off" placeholder="http://192.168.1.10:8080" />
        </label>
        <label class="join-field">
          <span>Key (CASHPILOT_API_KEY)</span>
          <input name="key" type="password" spellcheck="false" autocomplete="off" />
        </label>
      </details>
      <div id="join-result" aria-live="polite"></div>
      <div class="gate-actions">
        <button type="submit" class="btn btn-primary btn-lg" id="join-connect">Connect</button>
        <button type="button" class="btn btn-ghost btn-lg" id="join-back">Back</button>
      </div>
    </form>
  `;
}

/** What a join answered: why it failed, or what happens now that it worked. */
export function renderJoinOutcome(result: JoinResult): string {
  if (!result.ok) {
    return `
      <div class="join-outcome warn" role="alert" data-code="${escapeHtml(result.code)}">
        <strong>Not connected</strong>
        <span>${escapeHtml(result.message)}</span>
        <small>Nothing was saved.</small>
      </div>
    `;
  }
  const devices = result.hubDevices || [];
  const hub = result.hubWasOn
    ? `<li>This computer no longer accepts workers and phones.${
        devices.length ? ` Point these at your CashPilot instead: ${devices.map(escapeHtml).join(", ")}.` : ""
      }</li>`
    : "";
  return `
    <div class="join-outcome ok" data-code="ok">
      <strong>Connected to ${escapeHtml(result.serverHost)}</strong>
      <span>This computer now appears in your CashPilot Fleet page as <strong>${escapeHtml(result.name)}</strong>.</span>
      <ul class="mode-effects">
        <li>Your CashPilot reads the earnings now. This computer stops logging in to your provider accounts.</li>
        <li>Your earnings history from this computer is copied to your CashPilot once.</li>
        <li>You still add and remove earning apps here, in this app.</li>
        ${hub}
      </ul>
      <div class="gate-actions"><button type="button" class="btn btn-primary btn-lg" id="join-continue">Continue</button></div>
    </div>
  `;
}

/** One line on the link: the last heartbeat, and the last error in plain words. */
export function pairingLine(pairing: PairingStatus, now: number = Date.now()): string {
  const beat = pairing.lastHeartbeat ? relativeTime(pairing.lastHeartbeat, now) : "";
  const last = beat ? `Last heartbeat ${escapeHtml(beat)}.` : "No heartbeat has reached it yet.";
  const error = pairing.lastError
    ? ` <span class="pairing-error" data-code="${escapeHtml(pairing.errorCode)}">Last try failed: ${escapeHtml(pairing.lastError)}</span>`
    : "";
  return `<p class="pairing-line ${pairing.lastError ? "warn" : "ok"}">${last}${error}</p>`;
}

/**
 * The dashboard's first card while joined. It says plainly where the earnings
 * are, and how the link is doing. The "Across your CashPilot account" card stays
 * below it.
 */
export function renderJoinedBanner(pairing: PairingStatus, now: number = Date.now()): string {
  return `
    <section class="card joined-banner" aria-labelledby="joined-title">
      <div class="card-header">
        <div>
          <span class="card-title" id="joined-title">Joined to your CashPilot</span>
          <p class="muted compact-copy">Your earnings are shown on your CashPilot at <code>${escapeHtml(pairing.serverUrl)}</code>. It logs in to your provider accounts; this computer runs the earning apps and reports what runs here. The figures below are what this computer collected before it joined.</p>
          ${pairingLine(pairing, now)}
        </div>
        <button class="btn btn-ghost btn-sm" data-url="${escapeHtml(pairing.serverUrl)}">Open your CashPilot</button>
      </div>
    </section>
  `;
}

/**
 * A computer that reported to a CashPilot before this version is a follower now,
 * but its "Accept workers" switch stays as it was, so the workers and phones that
 * report to it keep working until the user points them at the server. Joining
 * from the app turns the switch off; this line covers the upgrade.
 */
function hubStillOn(state: ModeState): string {
  if (!state.hubOn) return "";
  const devices = state.hubDevices || [];
  return `<p class="pairing-line warn" data-hub-still-on>This computer still accepts workers and phones${
    devices.length ? `: ${devices.map(escapeHtml).join(", ")}` : ""
  }. Point them at your CashPilot, then turn off <strong>Accept workers (hub mode)</strong> under Environment Variables.</p>`;
}

/** The screen the Settings card "How this computer runs" shows. */
export type ModeCardView = "summary" | "confirm-join" | "join" | "confirm-lead" | "left";

/**
 * The Settings card. Each switch goes through a confirm screen that says what
 * happens before anything changes.
 */
export function renderModeCard(state: ModeState, view: ModeCardView, left: LeaveResult | null = null, now: number = Date.now()): string {
  const following = state.mode === "follower";
  const server = state.pairing?.serverUrl || "";
  let body = "";
  if (view === "confirm-join") {
    const devices = state.hubDevices || [];
    body = `
      <p><strong>Join my CashPilot instead?</strong> Here is what happens:</p>
      <ul class="mode-effects">
        <li>Earning apps on this computer keep running. Nothing is redeployed.</li>
        <li>Your earnings history here is copied to your CashPilot once. The copy here stays.</li>
        <li>This computer stops collecting earnings. Your CashPilot collects them. Your saved provider logins stay here, encrypted.</li>
        ${state.hubOn ? `<li>This computer stops accepting workers and phones.${devices.length ? ` Point these at your CashPilot: ${devices.map(escapeHtml).join(", ")}.` : ""}</li>` : ""}
      </ul>
      <div class="gate-actions">
        <button class="btn btn-primary" data-mode-action="join">Continue</button>
        <button class="btn btn-ghost" data-mode-action="summary">Cancel</button>
      </div>`;
  } else if (view === "join") {
    body = renderJoinForm();
  } else if (view === "confirm-lead") {
    body = `
      <p><strong>Run everything here instead?</strong> Here is what happens:</p>
      <ul class="mode-effects">
        <li>Earning apps on this computer keep running.</li>
        <li>This computer stops reporting to <code>${escapeHtml(server)}</code> and forgets its keys.</li>
        <li>Your CashPilot can no longer start and stop services on this computer.</li>
        <li>This computer starts collecting earnings again, with the provider logins saved here.</li>
        <li>Your CashPilot keeps a row for <strong>${escapeHtml(state.name)}</strong>. Remove it on its Fleet page.</li>
        <li>The days while joined stay on your CashPilot. They are not copied back here.</li>
      </ul>
      <div class="gate-actions">
        <button class="btn btn-primary" data-mode-action="leave">Switch to ${LEAD_TITLE}</button>
        <button class="btn btn-ghost" data-mode-action="summary">Cancel</button>
      </div>`;
  } else if (view === "left" && left) {
    body = `
      <div class="join-outcome ${left.firewallError ? "warn" : "ok"}">
        <strong>${left.firewallError ? "Firewall cleanup is pending" : "This computer runs everything here again"}</strong>
        <span>Now remove <strong>${escapeHtml(left.name)}</strong> on the Fleet page of your CashPilot at <code>${escapeHtml(left.serverUrl)}</code>.</span>
      </div>
      ${left.firewallError ? `<p class="pairing-line warn" data-left-firewall>${escapeHtml(left.firewallError)} You can delete it in Windows Defender Firewall.</p>` : ""}
      <div class="gate-actions"><button class="btn btn-ghost" data-mode-action="summary">OK</button></div>`;
  } else if (following) {
    body = `
      <p><strong>${FOLLOWER_TITLE}.</strong> This computer runs earning apps for your CashPilot at <code>${escapeHtml(server)}</code>, which collects and shows the earnings.</p>
      ${state.pairing ? pairingLine(state.pairing, now) : ""}
      ${hubStillOn(state)}
      <div class="gate-actions"><button class="btn btn-ghost" data-mode-action="confirm-lead">Switch to ${LEAD_TITLE}</button></div>`;
  } else {
    body = `
      <p><strong>${LEAD_TITLE}.</strong> This computer collects your earnings, shows the dashboard and runs its earning apps.</p>
      <div class="gate-actions"><button class="btn btn-ghost" data-mode-action="confirm-join">Switch to ${FOLLOWER_TITLE}</button></div>`;
  }
  const cleanup = state.firewallCleanup ? `
    <p class="pairing-line warn" data-firewall-cleanup>${escapeHtml(state.firewallCleanup)}</p>
    <div class="gate-actions"><button class="btn btn-ghost" data-mode-action="firewall-cleanup">Retry firewall removal</button></div>` : "";
  return `
    <section class="card mode-settings" id="mode-card" data-view="${escapeHtml(view)}">
      <div class="card-header"><div><span class="card-title">How this computer runs</span></div></div>
      <div class="mode-card-body">${body}${cleanup}</div>
    </section>
  `;
}
