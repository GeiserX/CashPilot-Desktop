// The Instances box on a service's setup card, and the dashboard rows of the copies a
// service runs beyond the first.
//
// The box is how many copies of the service this machine runs. It is locked at 1,
// with the reason under it, where a second copy would earn nothing or break the first
// (one device per IP, a host directory, a host port, a native program). Like the
// Image box, leaving it as it was sends null, which keeps the recorded count, so a
// redeploy from a card nobody touched cannot scale a service down.
//
// Pure: takes catalog data and recorded state, returns HTML strings or values.

import type { ScalingLimit, Service, ServiceCopy } from "../wails";
import { rowButton } from "./actions.js";
import { containerState } from "./attention.js";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/**
 * Render the Instances box for one service. `limit` is the backend's answer for this
 * machine (absent: no box at all, as on the web when it has none); `recorded` is how
 * many copies the deployment runs now (0 or absent when it is not deployed).
 */
export function instancesSection(service: Service, limit: ScalingLimit | null | undefined, recorded: number | null | undefined): string {
  if (service.manualOnly || !limit || !limit.max) return "";
  const current = Math.max(recorded || 0, 1);
  const locked = limit.max <= 1;
  return `
    <div class="instances-choice">
      <label>
        <span>Instances</span>
        <input data-wizard-instances="${escapeHtml(service.slug)}" data-initial="${current}" type="number" min="1" max="${limit.max}" step="1" value="${current}"${locked ? " disabled" : ""} />
      </label>
      ${instancesHint(service, limit)}
    </div>
  `;
}

/** What the Instances box means for this provider, under the box. The web app's words. */
export function instancesHint(service: Service, limit: ScalingLimit): string {
  const name = escapeHtml(service.name);
  if (limit.max <= 1) {
    return limit.reason
      ? `<p class="muted compact-copy instances-hint">${escapeHtml(limit.reason)} To run more, add machines on other connections.</p>`
      : "";
  }
  if (service.requirements?.devicesPerIp === 0) {
    return `<p class="muted compact-copy instances-hint">${name} allows several devices per IP, so each extra instance registers as its own device (<code>cashpilot-&lt;host&gt;-2</code>). They share this machine's connection: more instances add devices, not bandwidth.</p>`;
  }
  return `<p class="muted compact-copy instances-hint">${name} does not document how many devices it allows per IP. Extra instances share this machine's connection and may earn nothing.</p>`;
}

/**
 * The count to send with a deploy: null when the box is left as it was (the backend
 * then keeps the recorded count), or locked, or not a whole number; otherwise the
 * typed number. The backend checks the range again.
 */
export function instancesChoice(value: string, initial: string, disabled: boolean): number | null {
  if (disabled) return null;
  const typed = value.trim();
  if (typed === initial.trim() || !/^\d+$/.test(typed)) return null;
  return Number(typed);
}

/** The label of a deployment's first row: "#1" only when it has copies beside it. */
export function firstCopyLabel(copies: ServiceCopy[] | null | undefined): string {
  return copies && copies.length > 0 ? " #1" : "";
}

/**
 * The dashboard rows of copies 2..N, under the service's own row, each with its own
 * Logs and Stop or Start. Removing one copy is lowering the count on the card.
 */
export function copyRows(serviceName: string, copies: ServiceCopy[] | null | undefined): string {
  return (copies || []).map((copy) => {
    const n = copy.instance;
    const running = copy.status === "running";
    const state = containerState(copy.status);
    const cls = state === "ok" ? "running" : state === "fail" ? "stopped" : "unknown";
    // The balance is the service's, on its first row: a copy has no reading of its own.
    return `
      <tr class="copy-row instance-row">
        <td class="c-name">
          <span class="svc-name">${escapeHtml(serviceName)} #${n}</span>
          <div class="cell-sub cell-mono">${escapeHtml(copy.name)}</div>
        </td>
        <td><span class="badge badge-${cls}"><span class="status-dot ${cls}"></span>${escapeHtml(copy.status)}</span></td>
        <td class="c-num"><span class="muted">&mdash;</span></td>
        <td class="c-num"><span class="muted" title="A copy's balance is the service's, on its first row">&mdash;</span></td>
        <td class="c-num">${copy.cpuPercent.toFixed(1)}%</td>
        <td class="c-num">${copy.memoryMb.toFixed(0)} MB</td>
        <td class="c-pay"></td>
        <td class="c-act">
          <div class="action-btns">
            ${rowButton("logs", copy.slug, `Logs of copy ${n}`, n)}
            ${running ? rowButton("stop", copy.slug, `Stop copy ${n}`, n) : rowButton("start", copy.slug, `Start copy ${n}`, n)}
          </div>
        </td>
      </tr>
    `;
  }).join("");
}
