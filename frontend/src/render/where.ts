// The Where box on a service's setup card (lead_workers.go).
//
// A lead Desktop that accepts workers deploys to this computer or to one of the
// CashPilot Docker workers that report to it, as the web UI does. The box lists the
// workers that can take a deploy now; a worker sent a deploy gets the values on the
// card with it and keeps none of them.
//
// Pure: takes the catalog entry and the workers, returns HTML strings or values.

import type { Service, WorkerDeployResult, WorkerTarget } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { seedFileVar } from "./wallet.js";

/**
 * Render the Where box for one service, or "" when there is nothing to choose: no
 * workers report here, or this service cannot run on a worker. A service that keeps
 * its wallet seed in a file gets a note that it runs on this computer, and no pick.
 *
 * `draft` is the owner's pick from before a redraw (a worker id, null for this
 * computer, undefined for none). A redraw must keep it, or a worker picked for the
 * deploy would fall back to this computer unasked.
 */
export function whereChoiceSection(service: Service, targets: WorkerTarget[] | null | undefined, draft?: number | null): string {
  if (service.manualOnly || service.custom || (!draft && !targets?.length)) return "";
  const slug = escapeHtml(service.slug);
  if (seedFileVar(service)) {
    // The seed phrase is the wallet, and it never travels to a worker (workerapi.BuildDeploySpec):
    // the node runs where its wallet file is made, so the card's wallet choice applies here.
    return `<p class="muted compact-copy where-hint" data-where-local="${slug}">Where: this computer. ${escapeHtml(service.name)} keeps its wallet seed phrase in a file on the machine that runs it, so it runs only on this computer for now.</p>`;
  }
  const workers = targets || [];
  const ready = workers.filter((t) => t.online && !t.reason);
  const unavailable = draft != null && !ready.some((t) => t.id === draft);
  const picked = unavailable ? workers.find((t) => t.id === draft) : undefined;
  if (ready.length === 0 && !unavailable) {
    return `<p class="muted compact-copy where-hint" data-where-none="${slug}">Where: this computer. No worker is online.</p>`;
  }
  const options = [
    `<option value="">This computer</option>`,
    unavailable ? `<option value="${draft}" selected disabled>${picked ? `Worker ${escapeHtml(picked.name)}` : "Selected worker"} (unavailable)</option>` : "",
    ...ready.map((t) => {
      const runs = (t.services || []).includes(service.slug) ? " (runs it now)" : "";
      return `<option value="${t.id}"${draft === t.id ? " selected" : ""}>Worker ${escapeHtml(t.name)}${runs}</option>`;
    }),
  ].join("");
  const waiting = workers.filter((t) => t.online && t.reason);
  return `
    <div class="where-choice">
      <label>
        <span>Where</span>
        <select data-wizard-where="${slug}">${options}</select>
      </label>
      <p class="muted compact-copy where-hint">A worker gets the values above with this deploy and keeps none of them. It runs one copy, from its own IP.</p>
      ${unavailable ? `<p class="muted compact-copy where-hint">${escapeHtml(picked?.reason || "The selected worker is unavailable.")} Choose a target before deploying.</p>` : ""}
      ${waiting.map((t) => `<p class="muted compact-copy where-hint">${escapeHtml(t.reason)}</p>`).join("")}
    </div>
  `;
}

/** The worker id to deploy to, or null for this computer. */
export function whereChoice(value: string | null | undefined): number | null {
  const id = Number.parseInt(String(value ?? ""), 10);
  return Number.isInteger(id) && id > 0 ? id : null;
}

/** What a deploy to a worker did, or why it did nothing. */
export type WorkerDeployOutcome =
  | { kind: "refused"; message: string }
  | { kind: "cancelled" }
  | { kind: "deployed"; result: WorkerDeployResult };

/** The calls a deploy to a worker makes; main.ts passes the Wails bindings and window.confirm. */
export interface WorkerDeployCalls {
  confirm(message: string): boolean;
  /** One deploy, with the owner's yes so far. */
  deploy(yes: { confirmed: boolean; allowSharedIdentity: boolean; replaceExisting: boolean }): Promise<WorkerDeployResult>;
}

/**
 * Deploy from the setup card to a worker. The worker runs one copy from its own IP,
 * so a route through a proxy or more than one copy is refused, not dropped. A
 * question the backend asks first (a device id that runs on another box, another
 * image on a service that keeps a wallet or identity) is put to the owner with the
 * web UI's wording, and only a yes sends the deploy again. A declined deploy sends
 * nothing and saves nothing.
 */
export async function runWorkerDeploy(
  slug: string,
  worker: WorkerTarget,
  proxyId: number | null,
  instances: number | null,
  calls: WorkerDeployCalls,
): Promise<WorkerDeployOutcome> {
  if (!worker.online || worker.reason) {
    return { kind: "refused", message: `${worker.reason || "The selected worker is unavailable."} Choose a target before deploying.` };
  }
  if (proxyId !== null || (instances !== null && instances > 1)) {
    return {
      kind: "refused",
      message: "A deploy to a worker runs one copy, from the worker's own IP. Set Route to Direct and Instances to 1, or choose This computer. Nothing was deployed.",
    };
  }
  const yes = { confirmed: false, allowSharedIdentity: false, replaceExisting: false };
  // Each question is asked at most once.
  for (let round = 0; round < 4; round++) {
    const result = await calls.deploy({ ...yes });
    if (result.ask === "replace_existing" && !yes.replaceExisting) {
      const message = result.message || `Redeploy ${slug} on ${worker.name}? The existing container will be replaced, but its volumes are kept.`;
      if (!calls.confirm(message)) return { kind: "cancelled" };
      yes.replaceExisting = true;
      continue;
    }
    if (result.ask === "identity_in_use" && !yes.allowSharedIdentity) {
      if (!calls.confirm(`${result.message}\n\nDeploy anyway?`)) return { kind: "cancelled" };
      yes.allowSharedIdentity = true;
      continue;
    }
    if (result.ask === "step_up_required" && !yes.confirmed) {
      if (!calls.confirm(`${result.message}\n\nDeploy it anyway?`)) return { kind: "cancelled" };
      yes.confirmed = true;
      continue;
    }
    if (result.ask) return { kind: "refused", message: `${result.message} Nothing was deployed.` };
    return { kind: "deployed", result };
  }
  return { kind: "refused", message: "The deploy kept asking the same question. Nothing was deployed." };
}

/** What the setup card says after a deploy to a worker. */
export function workerDeployText(result: Partial<WorkerDeployResult>): string {
  const notes = (result.notes || []).map((n) => `Kept: ${n}.`);
  return [`Deployed to ${result.worker || "the worker"}. The Fleet page shows it after the worker's next check-in.`, ...notes, result.credentialWarning].filter(Boolean).join("\n\n");
}
