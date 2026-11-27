// Fleet > Add Worker or Mobile: what is true now, then what to do.
//
// A fresh install does not accept other machines: Accept workers (hub mode) is off
// and the API listens on 127.0.0.1. The page used to offer a Docker worker snippet
// anyway, with this PC's LAN address in it, so a person who followed it set up a
// worker that could never connect. Now the snippets show only when another machine
// can report here, with the address the API really listens on. Otherwise the card
// says why not, and the one change that would let them in, with its risk.
//
// Pure: takes the FleetState the backend sent, returns HTML, touches no global.

import type { FleetState } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, raw, t } from "./i18n.js";

type ReachView = Pick<FleetState, "reach" | "uiUrl" | "localApiUrl" | "apiKey" | "apiListening">;

function stat(label: string, value: string): string {
  return `<div class="detail-stat"><span>${escapeHtml(label)}</span><strong>${escapeHtml(value)}</strong></div>`;
}

/** The reasons other machines cannot report here now, in plain words. */
export function fleetClosedReasons(reach: FleetState["reach"]): string[] {
  const reasons: string[] = [];
  if (reach.notListening) reasons.push(t("fleet_reach.reason.notListening"));
  if (reach.thisPcOnly) reasons.push(t("fleet_reach.reason.thisPcOnly", { listen: reach.listen || "127.0.0.1" }));
  if (reach.hubOff) reasons.push(t("fleet_reach.reason.hubOff"));
  return reasons;
}

/**
 * The connection details and the setup snippets (snippets: renderFleetSnippets'
 * HTML) when another machine can report here, or the closed notice without them.
 */
export function renderFleetConnect(fleet: ReachView, snippets: string): string {
  const reach = fleet.reach;
  const pending = reach.restartPending
    ? `<p class="muted compact-copy">${ht("fleet_reach.restartPending")}</p>`
    : "";
  if (reach.open) {
    return `
            <p class="muted">${ht("fleet_reach.open", { listen: reach.listen })}</p>
            <div class="connection-grid">
              ${stat(t("fleet_reach.stat.uiUrl"), fleet.uiUrl)}
              ${stat(t("fleet_reach.stat.localApi"), fleet.localApiUrl)}
              ${stat(t("fleet_reach.stat.apiKey"), fleet.apiKey ? t("fleet_reach.stat.generated") : t("fleet_reach.stat.missing"))}
              ${stat(t("fleet_reach.stat.listener"), fleet.apiListening ? t("fleet_reach.stat.listening") : t("fleet_reach.stat.offline"))}
            </div>
            ${pending}
            ${snippets}`;
  }
  const reasons = fleetClosedReasons(reach)
    .map((reason) => `<li>${escapeHtml(reason)}</li>`)
    .join("");
  const hub = reach.hubOff;
  const bind = reach.thisPcOnly || reach.notListening;
  const steps = hub && bind ? ht("fleet_reach.steps.both") : hub ? ht("fleet_reach.steps.hub") : bind ? ht("fleet_reach.steps.bind") : "";
  return `
            <div data-fleet-closed>
              <p><strong class="text-warn">${ht("fleet_reach.closed.title")}</strong> ${ht("fleet_reach.closed.body")}</p>
              <ul class="muted">${reasons}</ul>
              <p class="muted">${ht("fleet_reach.closed.howTo", { steps: raw(steps) })}</p>
              ${pending}
              <div class="connection-grid">
                ${stat(t("fleet_reach.stat.localApi"), fleet.localApiUrl)}
                ${stat(t("fleet_reach.stat.listensOn"), reach.listen || t("fleet_reach.stat.notRunning"))}
              </div>
              <button type="button" class="compact-btn" data-view="settings">${ht("fleet_reach.openSettings")}</button>
            </div>`;
}
