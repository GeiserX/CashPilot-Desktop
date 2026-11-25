// The runtime card on first run's runtime step (after How it runs, and Join on the follower path).
//
// It must never call this machine ready when nothing useful runs. The native
// runtime is always there, but it runs only the few apps that ship a native
// build (Mysterium today), so without Docker the card says plainly that most
// apps need Docker and points at the Docker guide below it.
//
// On Windows the first reading of the PC can land after the card is first drawn.
// Until then the runtime's message is the generic one, so the card says it is
// checking instead: a PC with virtualization off must not first be told to open
// Docker Desktop.
//
// Pure: takes the runtime status, returns the card and whether first run may go on.

import type { RuntimeStatus } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { runtimeState } from "./runtime_state.js";

export interface RuntimeCard {
  /** "ok" only when Docker answers with Linux containers. */
  tone: "ok" | "warn";
  /** Whether "Open the dashboard" is enabled: something can run here. */
  canContinue: boolean;
  html: string;
}

type CardInput = Pick<RuntimeStatus, "available" | "message" | "context"> & {
  nativeServices?: string[] | null;
  reason?: string;
};

/** "A", "A and B", "A, B and C". */
function nameList(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

export function runtimeCard(runtime: CardInput, opts: { checking?: boolean } = {}): RuntimeCard {
  const native = runtime.nativeServices ?? [];
  const unknown = runtime.reason !== undefined && runtimeState(runtime).kind === "unknown";
  let tone: RuntimeCard["tone"] = "warn";
  let title: string;
  let body: string;
  if (!runtime.available && opts.checking) {
    title = "Checking this computer";
    body = "CashPilot is reading what this computer has, so it can say exactly what Docker needs.";
  } else if (runtime.available) {
    tone = "ok";
    title = "Runtime ready";
    body = escapeHtml(runtime.message);
  } else if (native.length > 0) {
    const only = native.length === 1 ? `only ${escapeHtml(native[0])} runs` : `only ${escapeHtml(nameList(native))} run`;
    // The title says most apps need Docker; the body says it once more, at most,
    // in the reason's own words, never in three sentences.
    const next = runtime.message ? escapeHtml(runtime.message) : "Set up Docker with the guide below, then click Check again.";
    title = unknown ? "Docker status unknown" : "Docker needed for most apps";
    body = `Without Docker, ${only} on this computer. ${next}`;
  } else {
    title = unknown ? "Docker status unknown" : "Runtime setup needed";
    body = escapeHtml(runtime.message);
  }
  const html = `<div class="runtime-card ${tone}">
            <strong>${title}</strong>
            <span>${body}</span>
            ${runtime.context ? `<small>Docker context: ${escapeHtml(runtime.context)}</small>` : ""}
          </div>`;
  return { tone, canContinue: runtime.available || (native.length > 0 && !opts.checking), html };
}
