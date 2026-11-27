// How this computer runs: "Run everything here" (lead) or "Join my CashPilot"
// (follower). The first-run step, the join form, the dashboard banner while
// joined, and the Settings card that switches.
//
// Pure: takes state, returns HTML. main.ts wires the buttons. Nothing here ever
// puts the key back into the page: the form keeps what the user typed, and a
// result carries the server's address, never the key.

import type { JoinResult, LeaveResult, ModeState, PairingStatus } from "../wails";
import { escapeHtml, relativeTime } from "./format.js"; // .js so the emitted ESM resolves in Node
import { ht, t } from "./i18n.js";

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
    { id: "welcome", label: t("mode.step.welcome") },
    { id: "mode", label: t("mode.step.mode") },
  ];
  if (path === "follower") steps.push({ id: "join", label: t("mode.step.join") });
  steps.push({ id: "runtime", label: t("mode.step.runtime") });
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
  return `<ol class="gate-steps" aria-label="${ht("mode.stepOf", { n: active + 1, total: steps.length })}">${items}</ol>`;
}

/** First run, step 1. "Get started" is the screen's only control. */
export function renderWelcome(): string {
  return `
    <h1 class="gate-title">${ht("mode.welcome.title")}</h1>
    <p class="gate-lede">${ht("mode.welcome.lede")}</p>
    <div class="gate-actions">
      <button class="btn btn-primary btn-lg" id="get-started">${ht("mode.welcome.start")}</button>
    </div>
  `;
}

/** The name of the lead mode, as plain text. */
export function leadTitle(): string {
  return t("mode.leadTitle");
}

/** The name of the follower mode, as plain text. */
export function followerTitle(): string {
  return t("mode.followerTitle");
}

/** The two cards of the first-run choice. Nothing is pre-selected. */
export function renderModeChoice(): string {
  return `
    <h1 class="gate-title">${ht("mode.choice.title")}</h1>
    <p class="gate-lede">${ht("mode.choice.lede")}</p>
    <div class="mode-choice">
      <button class="mode-card" data-mode-choice="lead">
        <strong>${escapeHtml(leadTitle())}</strong>
        <span>${ht("mode.choice.lead.body")}</span>
        <small>${ht("mode.choice.lead.hint")}</small>
      </button>
      <button class="mode-card" data-mode-choice="follower">
        <strong>${escapeHtml(followerTitle())}</strong>
        <span>${ht("mode.choice.follower.body")}</span>
        <small>${ht("mode.choice.follower.hint")}</small>
      </button>
    </div>
    <p class="muted compact-copy">${ht("mode.choice.notSure", { lead: leadTitle() })}</p>
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
      <p class="gate-lede">${ht("mode.join.lede")}</p>
      <label class="join-field">
        <span>${ht("mode.join.paste")}</span>
        <textarea name="block" rows="4" spellcheck="false" autocomplete="off" placeholder="CASHPILOT_UI_URL=http://192.168.1.10:8080&#10;CASHPILOT_API_KEY=..."></textarea>
      </label>
      <details class="join-typed">
        <summary>${ht("mode.join.typeInstead")}</summary>
        <label class="join-field">
          <span>${ht("mode.join.address")}</span>
          <input name="url" type="text" spellcheck="false" autocomplete="off" placeholder="http://192.168.1.10:8080" />
        </label>
        <label class="join-field">
          <span>${ht("mode.join.key", { var: "CASHPILOT_API_KEY" })}</span>
          <input name="key" type="password" spellcheck="false" autocomplete="off" />
        </label>
      </details>
      <div id="join-result" aria-live="polite"></div>
      <div class="gate-actions">
        <button type="submit" class="btn btn-primary btn-lg" id="join-connect">${ht("mode.join.connect")}</button>
        <button type="button" class="btn btn-ghost btn-lg" id="join-back">${ht("mode.join.back")}</button>
      </div>
    </form>
  `;
}

/** What a join answered: why it failed, or what happens now that it worked. */
export function renderJoinOutcome(result: JoinResult): string {
  if (!result.ok) {
    return `
      <div class="join-outcome warn" role="alert" data-code="${escapeHtml(result.code)}">
        <strong>${ht("mode.outcome.notConnected")}</strong>
        <span>${escapeHtml(result.message)}</span>
        <small>${ht("mode.outcome.nothingSaved")}</small>
      </div>
    `;
  }
  const devices = result.hubDevices || [];
  const hub = result.hubWasOn
    ? `<li>${ht("mode.outcome.hubOff")}${
        devices.length ? ` ${ht("mode.outcome.pointTheseInstead", { devices: devices.join(", ") })}` : ""
      }</li>`
    : "";
  return `
    <div class="join-outcome ok" data-code="ok">
      <strong>${ht("mode.outcome.connectedTo", { host: result.serverHost })}</strong>
      <span>${ht("mode.outcome.appearsAs", { name: result.name })}</span>
      <ul class="mode-effects">
        <li>${ht("mode.outcome.serverReads")}</li>
        <li>${ht("mode.outcome.historyCopied")}</li>
        <li>${
          result.serverCommandsOn
            ? ht("mode.outcome.commandsOn")
            : ht("mode.outcome.commandsOff")
        }</li>
        <li>${ht("mode.outcome.addHere")}</li>
        ${hub}
      </ul>
      <div class="gate-actions"><button type="button" class="btn btn-primary btn-lg" id="join-continue">${ht("mode.continue")}</button></div>
    </div>
  `;
}

/** One line on the link: the last heartbeat, and the last error in plain words. */
export function pairingLine(pairing: PairingStatus, now: number = Date.now()): string {
  const beat = pairing.lastHeartbeat ? relativeTime(pairing.lastHeartbeat, now) : "";
  // The last success is kept in memory only, so after a restart a failing link
  // may have had heartbeats before: say "since", not "yet".
  const last = beat
    ? ht("mode.pairing.lastHeartbeat", { when: beat })
    : pairing.lastError
      ? ht("mode.pairing.noneSinceStart")
      : ht("mode.pairing.noneYet");
  const error = pairing.lastError
    ? ` <span class="pairing-error" data-code="${escapeHtml(pairing.errorCode)}">${ht("mode.pairing.lastTryFailed", { error: pairing.lastError })}</span>`
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
          <span class="card-title" id="joined-title">${ht("mode.joined.title")}</span>
          <p class="muted compact-copy">${ht("mode.joined.body", { url: pairing.serverUrl })}</p>
          ${pairingLine(pairing, now)}
        </div>
        <button class="btn btn-ghost btn-sm" data-url="${escapeHtml(pairing.serverUrl)}">${ht("mode.joined.open")}</button>
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
  const text = devices.length
    ? ht("mode.hubStillOn.devices", { devices: devices.join(", ") })
    : ht("mode.hubStillOn.plain");
  return `<p class="pairing-line warn" data-hub-still-on>${text}</p>`;
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
      <p>${ht("mode.confirmJoin.title")}</p>
      <ul class="mode-effects">
        <li>${ht("mode.confirmJoin.keepRunning")}</li>
        <li>${ht("mode.confirmJoin.historyCopied")}</li>
        <li>${ht("mode.confirmJoin.stopsCollecting")}</li>
        ${state.hubOn ? `<li>${ht("mode.confirmJoin.hubOff")}${devices.length ? ` ${ht("mode.confirmJoin.pointThese", { devices: devices.join(", ") })}` : ""}</li>` : ""}
      </ul>
      <div class="gate-actions">
        <button class="btn btn-primary" data-mode-action="join">${ht("mode.continue")}</button>
        <button class="btn btn-ghost" data-mode-action="summary">${ht("mode.cancel")}</button>
      </div>`;
  } else if (view === "join") {
    body = renderJoinForm();
  } else if (view === "confirm-lead") {
    body = `
      <p>${ht("mode.confirmLead.title")}</p>
      <ul class="mode-effects">
        <li>${ht("mode.confirmLead.keepRunning")}</li>
        <li>${ht("mode.confirmLead.stopsReporting", { server })}</li>
        <li>${ht("mode.confirmLead.noCommands")}</li>
        <li>${ht("mode.confirmLead.startsCollecting")}</li>
        <li>${ht("mode.confirmLead.keepsRow", { name: state.name })}</li>
        <li>${ht("mode.confirmLead.daysStay")}</li>
      </ul>
      <div class="gate-actions">
        <button class="btn btn-primary" data-mode-action="leave">${ht("mode.switchTo", { title: leadTitle() })}</button>
        <button class="btn btn-ghost" data-mode-action="summary">${ht("mode.cancel")}</button>
      </div>`;
  } else if (view === "left" && left) {
    body = `
      <div class="join-outcome ${left.firewallError ? "warn" : "ok"}">
        <strong>${left.firewallError ? ht("mode.left.firewallPending") : ht("mode.left.runsHereAgain")}</strong>
        <span>${ht("mode.left.removeRow", { name: left.name, server: left.serverUrl })}</span>
      </div>
      ${left.firewallError ? `<p class="pairing-line warn" data-left-firewall>${escapeHtml(left.firewallError)} ${ht("mode.left.deleteInFirewall")}</p>` : ""}
      <div class="gate-actions"><button class="btn btn-ghost" data-mode-action="summary">${ht("mode.ok")}</button></div>`;
  } else if (following) {
    body = `
      <p>${ht("mode.summary.follower", { title: followerTitle(), server })}</p>
      ${state.pairing ? pairingLine(state.pairing, now) : ""}
      ${hubStillOn(state)}
      <div class="gate-actions"><button class="btn btn-ghost" data-mode-action="confirm-lead">${ht("mode.switchTo", { title: leadTitle() })}</button></div>`;
  } else {
    body = `
      <p>${ht("mode.summary.lead", { title: leadTitle() })}</p>
      <div class="gate-actions"><button class="btn btn-ghost" data-mode-action="confirm-join">${ht("mode.switchTo", { title: followerTitle() })}</button></div>`;
  }
  const cleanup = state.firewallCleanup ? `
    <p class="pairing-line warn" data-firewall-cleanup>${escapeHtml(state.firewallCleanup)}</p>
    <div class="gate-actions"><button class="btn btn-ghost" data-mode-action="firewall-cleanup">${ht("mode.retryFirewall")}</button></div>` : "";
  return `
    <section class="card mode-settings" id="mode-card" data-view="${escapeHtml(view)}">
      <div class="card-header"><div><span class="card-title">${ht("mode.card.title")}</span></div></div>
      <div class="mode-card-body">${body}${cleanup}</div>
    </section>
  `;
}
