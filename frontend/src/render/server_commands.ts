// The dashboard card "<server> can start and stop services on this computer", shown
// while this computer is joined to a CashPilot (worker_api.go). It says what the
// server may do, carries the switch, says whether the server can reach this computer,
// and shows the last command the server sent. On Windows it also offers the firewall
// rule, and says exactly what that rule opens before the user presses the button.
//
// Pure: takes state, returns HTML. main.ts wires the buttons.

import type { ServerCommand, ServerCommandsState } from "../wails";
import { escapeHtml, relativeTime } from "./format.js"; // .js so the emitted ESM resolves in Node
import { ht, t } from "./i18n.js";

/** A command's verb, or "" for one this file does not know. */
function actionWord(action: string): string {
  switch (action) {
    case "deploy":
      return t("server_commands.action.deploy");
    case "stop":
      return t("server_commands.action.stop");
    case "start":
      return t("server_commands.action.start");
    case "restart":
      return t("server_commands.action.restart");
    case "remove":
      return t("server_commands.action.remove");
    default:
      return "";
  }
}

/** One line about the last command: what, which service, when, and how it ended. */
export function lastCommandLine(last: ServerCommand | null | undefined, now: number = Date.now()): string {
  if (!last) return `<p class="pairing-line" data-server-last>${ht("server_commands.noCommand")}</p>`;
  const what = actionWord(last.action) || last.action;
  const when = relativeTime(last.at, now);
  const outcome = last.ok ? t("server_commands.done") : t("server_commands.refused", { detail: last.detail });
  const line = when
    ? ht("server_commands.lastWhen", { what, slug: last.slug, when, outcome })
    : ht("server_commands.last", { what, slug: last.slug, outcome });
  return `<p class="pairing-line${last.ok ? "" : " warn"}" data-server-last>${line}</p>`;
}

/** Whether the server can reach this computer now, in plain words. */
function reachLine(state: ServerCommandsState): string {
  if (!state.on) {
    return `<p class="pairing-line" data-server-reach>${ht("server_commands.off", { server: state.server })}</p>`;
  }
  if (state.serverTooOld) {
    return `<p class="pairing-line warn" data-server-reach>${ht("server_commands.tooOld", { server: state.server || t("server_commands.yourCashPilot") })}</p>`;
  }
  if (state.listening) {
    return `<p class="pairing-line" data-server-reach>${ht("server_commands.reaches", { url: state.url ?? "" })}</p>`;
  }
  return `<p class="pairing-line warn" data-server-reach>${escapeHtml(state.problem || t("server_commands.cannotReach"))}</p>`;
}

/** The Windows Firewall offer, only where a rule is needed and the switch is on. */
function firewallLine(state: ServerCommandsState): string {
  if (!state.firewallNeeded || !state.on) return "";
  if (state.firewallStatus === "unknown") {
    return `<p class="pairing-line warn" data-server-firewall>${ht("server_commands.firewall.unknown")}</p>`;
  }
  if (state.firewallStatus === "removal-pending") {
    return `<p class="pairing-line warn" data-server-firewall>${ht("server_commands.firewall.removalPending")}</p>`;
  }
  if (state.firewallRule) {
    return `<p class="pairing-line" data-server-firewall>${ht("server_commands.firewall.rule", { opens: state.opens })}</p>`;
  }
  const existing = state.firewallStatus === "different" ? `${ht("server_commands.firewall.different")} ` : "";
  return `<p class="pairing-line warn" data-server-firewall>${existing}${ht("server_commands.firewall.offer", { opens: state.opens })}</p>
    <div class="gate-actions"><button class="btn btn-ghost btn-sm" data-server-commands-firewall>${ht("server_commands.firewall.allow")}</button></div>`;
}

/** The card. */
export function renderServerCommands(state: ServerCommandsState, now: number = Date.now()): string {
  const server = state.server || t("server_commands.yourCashPilot");
  return `
    <section class="card server-commands" aria-labelledby="server-commands-title" data-server-commands>
      <div class="card-header">
        <div>
          <span class="card-title" id="server-commands-title">${ht("server_commands.title", { server })}</span>
          <p class="muted compact-copy">${ht("server_commands.help")}</p>
          ${reachLine(state)}
          ${firewallLine(state)}
          ${lastCommandLine(state.last, now)}
        </div>
        <label class="switch" title="${ht("server_commands.switch", { server })}">
          <input type="checkbox" role="switch" data-server-commands-switch ${state.on ? "checked" : ""} aria-label="${ht("server_commands.switch", { server })}" />
          <span class="switch-track"><span class="switch-thumb"></span></span>
        </label>
      </div>
    </section>
  `;
}
