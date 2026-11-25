// The copies a CashPilot Docker worker runs, with their buttons, under the worker on
// the Fleet page (lead_workers.go).
//
// A lead Desktop stops, starts, restarts and removes a copy on a worker, as the web
// UI does from a box's row. Only Remove asks first, with the web UI's wording; the
// worker keeps the copy's data.
//
// Pure: takes the worker and its copies, returns HTML strings or values.

import type { WorkerCommandsView, WorkerCopy } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

function copyName(copy: WorkerCopy): string {
  return copy.instance > 1 ? `${copy.slug} (copy ${copy.instance})` : copy.slug;
}

function button(command: string, label: string, workerId: number, copy: WorkerCopy, disabled: boolean, kind = "secondary"): string {
  return `<button class="${kind} compact-btn" data-worker-copy="${command}" data-worker-id="${workerId}" data-slug="${escapeHtml(copy.slug)}" data-instance="${copy.instance || 1}"${disabled ? " disabled" : ""}>${label}</button>`;
}

/**
 * Render a worker's copies, or "" when it reports none. With `view.reason` set the
 * worker takes no commands now: the reason shows and every button is off.
 */
export function workerCopiesSection(workerId: number, view: WorkerCommandsView | null | undefined): string {
  const copies = view?.copies || [];
  if (copies.length === 0) return view?.reason ? `<p class="muted compact-copy worker-copy-reason">${escapeHtml(view.reason)}</p>` : "";
  const off = Boolean(view?.reason);
  const rows = copies.map((copy) => {
    const running = copy.status === "running";
    const status = `<span class="badge ${running ? "success" : "warn"}">${escapeHtml(copy.status || "unknown")}</span>`;
    if (!copy.managed) {
      return `
      <div class="worker-copy">
        <span>${escapeHtml(copyName(copy))}</span> ${status}
        <span class="muted compact-copy">Found by its image: CashPilot did not deploy it, so it takes no commands here.</span>
      </div>`;
    }
    const actions = running
      ? button("stop", "Stop", workerId, copy, off) + button("restart", "Restart", workerId, copy, off)
      : button("start", "Start", workerId, copy, off);
    return `
      <div class="worker-copy">
        <span>${escapeHtml(copyName(copy))}</span> ${status}
        <span class="device-actions">${actions}${button("remove", "Remove", workerId, copy, off, "danger")}</span>
      </div>`;
  }).join("");
  return `
    <div class="worker-copies">
      ${off ? `<p class="muted compact-copy worker-copy-reason">${escapeHtml(view?.reason || "")}</p>` : ""}
      ${rows}
    </div>
  `;
}

/**
 * The question to put before a command, or null for none. Only Remove asks, as on
 * the web UI. Removing copy 1 removes the service on that worker, its other copies
 * with it; the data stays for a later deploy.
 */
export function copyCommandQuestion(command: string, slug: string, instance: number, workerName: string, copies: WorkerCopy[]): string | null {
  if (command !== "remove") return null;
  if (instance > 1) return `Remove copy ${instance} of ${slug} from ${workerName}? This will stop and delete the container. Its data is kept.`;
  const others = copies.filter((c) => c.slug === slug && c.managed && c.instance > 1).length;
  const more = others > 0 ? ` Its ${others === 1 ? "other copy goes" : `${others} other copies go`} too.` : "";
  return `Remove ${slug} from ${workerName}? This will stop and delete the container.${more} Its data is kept for a later deploy.`;
}
