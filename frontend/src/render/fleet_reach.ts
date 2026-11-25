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

type ReachView = Pick<FleetState, "reach" | "uiUrl" | "localApiUrl" | "apiKey" | "apiListening">;

function stat(label: string, value: string): string {
  return `<div class="detail-stat"><span>${escapeHtml(label)}</span><strong>${escapeHtml(value)}</strong></div>`;
}

/** The reasons other machines cannot report here now, in plain words. */
export function fleetClosedReasons(reach: FleetState["reach"]): string[] {
  const reasons: string[] = [];
  if (reach.notListening) reasons.push("The fleet API is not running on this PC. Its port may be in use by another program.");
  if (reach.thisPcOnly) reasons.push(`The fleet API listens on ${reach.listen || "127.0.0.1"}, which only this PC can reach.`);
  if (reach.hubOff) reasons.push("Accept workers (hub mode) is off, so this PC turns away workers and phones that report to it.");
  return reasons;
}

/**
 * The connection details and the setup snippets (snippets: renderFleetSnippets'
 * HTML) when another machine can report here, or the closed notice without them.
 */
export function renderFleetConnect(fleet: ReachView, snippets: string): string {
  const reach = fleet.reach;
  const pending = reach.restartPending
    ? `<p class="muted compact-copy">You changed the fleet bind address or port in Settings. That takes effect after you restart CashPilot.</p>`
    : "";
  if (reach.open) {
    return `
            <p class="muted">Point CashPilot workers, mobile companions, or another machine on your LAN or tailnet at this desktop API. It listens on <code>${escapeHtml(reach.listen)}</code>, takes authenticated heartbeats and registers devices automatically.</p>
            <div class="connection-grid">
              ${stat("UI URL", fleet.uiUrl)}
              ${stat("Local API", fleet.localApiUrl)}
              ${stat("API Key", fleet.apiKey ? "Generated" : "Missing")}
              ${stat("Listener", fleet.apiListening ? "Listening" : "Offline")}
            </div>
            ${pending}
            ${snippets}`;
  }
  const reasons = fleetClosedReasons(reach)
    .map((reason) => `<li>${escapeHtml(reason)}</li>`)
    .join("");
  const steps: string[] = [];
  if (reach.hubOff) steps.push("set <strong>Accept workers (hub mode)</strong> to true");
  if (reach.thisPcOnly || reach.notListening) steps.push("set <strong>Fleet Bind Address</strong> to this PC's LAN or Tailscale address (or a free <strong>Fleet API Port</strong> if the port is in use)");
  return `
            <div data-fleet-closed>
              <p><strong class="text-warn">Other machines cannot connect to this PC now.</strong> A worker or phone set up to report here would not get through, so no setup snippet is shown.</p>
              <ul class="muted">${reasons}</ul>
              <p class="muted">To let them in: in Settings, ${steps.join(", then ")}, and restart CashPilot. Do this only on a home network or a tailnet you trust: any device that reaches this PC on that address can try the fleet API, and with metrics on it reads your earnings totals without a key. Most people do not need this: point workers and phones at a CashPilot server instead.</p>
              ${pending}
              <div class="connection-grid">
                ${stat("Local API", fleet.localApiUrl)}
                ${stat("Listens on", reach.listen || "Not running")}
              </div>
              <button type="button" class="compact-btn" data-view="settings">Open Settings</button>
            </div>`;
}
