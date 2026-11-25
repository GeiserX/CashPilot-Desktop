// The runtime card on first run's second step.
//
// It must never call this machine ready when nothing useful runs. The native
// runtime is always there, but it runs only the few apps that ship a native
// build (Mysterium today), so without Docker the card says plainly that most
// apps need Docker and points at the Docker guide below it.
//
// Pure: takes the runtime status, returns the card and whether first run may go on.

import type { RuntimeStatus } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

export interface RuntimeCard {
  /** "ok" only when Docker answers with Linux containers. */
  tone: "ok" | "warn";
  /** Whether "Open the dashboard" is enabled: something can run here. */
  canContinue: boolean;
  html: string;
}

type CardInput = Pick<RuntimeStatus, "available" | "message" | "context"> & {
  nativeServices?: string[] | null;
};

/** "A", "A and B", "A, B and C". */
function nameList(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

export function runtimeCard(runtime: CardInput): RuntimeCard {
  const native = runtime.nativeServices ?? [];
  let tone: RuntimeCard["tone"] = "warn";
  let title: string;
  let body: string;
  if (runtime.available) {
    tone = "ok";
    title = "Runtime ready";
    body = escapeHtml(runtime.message);
  } else if (native.length > 0) {
    const only = native.length === 1 ? `only ${escapeHtml(native[0])} runs` : `only ${escapeHtml(nameList(native))} run`;
    const next = runtime.message ? escapeHtml(runtime.message) : "Set up Docker with the guide below, then click Check again.";
    title = "Docker needed for most apps";
    body = `Without Docker, ${only} on this computer. Most apps in the catalog need Docker. ${next}`;
  } else {
    title = "Runtime setup needed";
    body = escapeHtml(runtime.message);
  }
  const html = `<div class="runtime-card ${tone}">
            <strong>${title}</strong>
            <span>${body}</span>
            ${runtime.context ? `<small>Docker context: ${escapeHtml(runtime.context)}</small>` : ""}
          </div>`;
  return { tone, canContinue: runtime.available || native.length > 0, html };
}
