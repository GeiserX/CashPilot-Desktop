// The dashboard card "<server> can start and stop services on this computer", shown
// while this computer is joined to a CashPilot (worker_api.go). It says what the
// server may do, carries the switch, says whether the server can reach this computer,
// and shows the last command the server sent. On Windows it also offers the firewall
// rule, and says exactly what that rule opens before the user presses the button.
//
// Pure: takes state, returns HTML. main.ts wires the buttons.

import type { ServerCommand, ServerCommandsState } from "../wails";
import { escapeHtml, relativeTime } from "./format.js"; // .js so the emitted ESM resolves in Node

const ACTION_WORDS: Record<string, string> = {
  deploy: "Deploy",
  stop: "Stop",
  start: "Start",
  restart: "Restart",
  remove: "Remove",
};

/** One line about the last command: what, which service, when, and how it ended. */
export function lastCommandLine(last: ServerCommand | null | undefined, now: number = Date.now()): string {
  if (!last) return `<p class="pairing-line" data-server-last>No command received yet.</p>`;
  const what = ACTION_WORDS[last.action] || last.action;
  const when = relativeTime(last.at, now);
  const outcome = last.ok ? "done" : `refused: ${last.detail}`;
  return `<p class="pairing-line${last.ok ? "" : " warn"}" data-server-last>Last command: <strong>${escapeHtml(what)} ${escapeHtml(last.slug)}</strong>${
    when ? `, ${escapeHtml(when)}` : ""
  } (${escapeHtml(outcome)}).</p>`;
}

/** Whether the server can reach this computer now, in plain words. */
function reachLine(state: ServerCommandsState): string {
  if (!state.on) {
    return `<p class="pairing-line" data-server-reach>Off. ${escapeHtml(state.server)} can see what runs here, but cannot change it.</p>`;
  }
  if (state.serverTooOld) {
    return `<p class="pairing-line warn" data-server-reach>${escapeHtml(state.server || "Your CashPilot")} is older than CashPilot 3.0.0, so it cannot send commands to this computer. It only sees what runs here. Update it to 3.0.0 or newer.</p>`;
  }
  if (state.listening) {
    return `<p class="pairing-line" data-server-reach>It reaches this computer at <code>${escapeHtml(state.url)}</code>, with the key it gave this computer when it joined.</p>`;
  }
  return `<p class="pairing-line warn" data-server-reach>${escapeHtml(state.problem || "Your CashPilot cannot reach this computer right now.")}</p>`;
}

/** The Windows Firewall offer, only where a rule is needed and the switch is on. */
function firewallLine(state: ServerCommandsState): string {
  if (!state.firewallNeeded || !state.on) return "";
  if (state.firewallStatus === "unknown") {
    return `<p class="pairing-line warn" data-server-firewall>CashPilot could not read Windows Firewall. The rule status is unknown.</p>`;
  }
  if (state.firewallStatus === "removal-pending") {
    return `<p class="pairing-line warn" data-server-firewall>Remove the old rule in Settings under How this computer runs.</p>`;
  }
  if (state.firewallRule) {
    return `<p class="pairing-line" data-server-firewall>Windows Firewall lets it in: ${escapeHtml(state.opens)}. Switching to Run everything here removes this rule.</p>`;
  }
  const existing = state.firewallStatus === "different" ? "The rule in Windows does not match this installation. " : "";
  return `<p class="pairing-line warn" data-server-firewall>${existing}CashPilot does not listen until Windows Firewall lets it in, so Windows shows no prompt of its own for it. Allowing opens ${escapeHtml(state.opens)}. If Windows already blocks CashPilot (Windows makes such a rule when someone presses Cancel on its own firewall prompt), allowing removes that block too, because a block wins over an allow. Windows asks for an administrator first.</p>
    <div class="gate-actions"><button class="btn btn-ghost btn-sm" data-server-commands-firewall>Allow through Windows Firewall</button></div>`;
}

/** The card. */
export function renderServerCommands(state: ServerCommandsState, now: number = Date.now()): string {
  const server = escapeHtml(state.server || "Your CashPilot");
  return `
    <section class="card server-commands" aria-labelledby="server-commands-title" data-server-commands>
      <div class="card-header">
        <div>
          <span class="card-title" id="server-commands-title">${server} can start and stop services on this computer</span>
          <p class="muted compact-copy">It can deploy, stop, start, restart and remove the services of this computer's catalog, with the images CashPilot pins here. It cannot delete data that cannot be recovered, and it cannot change anything else on this computer.</p>
          ${reachLine(state)}
          ${firewallLine(state)}
          ${lastCommandLine(state.last, now)}
        </div>
        <label class="switch" title="Let ${server} start and stop services on this computer">
          <input type="checkbox" role="switch" data-server-commands-switch ${state.on ? "checked" : ""} aria-label="Let ${server} start and stop services on this computer" />
          <span class="switch-track"><span class="switch-thumb"></span></span>
        </label>
      </div>
    </section>
  `;
}
