// The way back to Docker setup, wherever the app shows that Docker is not ready.
//
// First run lets a person open the dashboard before Docker is set up, because a
// native app (MystNodes on Windows) runs without it. After that, the runtime page
// (the Docker checklist on Windows, the install guides on macOS and Linux) must
// stay one click away from every place that depends on Docker:
//
//   1. The Dashboard's "Needs attention" list has a row while no Docker engine
//      answers, so "Nothing needs you" never shows then. The row says what is
//      missing and has the Set up Docker button.
//   2. Settings has a Runtime card with the real state (ready, not installed,
//      installed but not running, unknown) and the same button. The read-only
//      "Runtime provider" setting gets the state next to it, so "existing-docker"
//      does not read as a Docker that exists.
//   3. The button always opens the runtime page (data-view="runtime"). That page
//      reads the computer again and shows the checklist on Windows, with its saved
//      progress, and the install guides elsewhere, so the button is never dead.
//
// The service card's half is the "Before you deploy" panel (render/preflight.ts),
// which shows this same button when the deploy cannot run here.
//
// Pure: takes the runtime status, returns HTML.

import type { RuntimeStatus } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

export type RuntimeInput = Pick<RuntimeStatus, "available" | "nativeAvailable" | "message"> &
  Partial<Pick<RuntimeStatus, "reason" | "nativeServices" | "setupTask">>;

export type RuntimeKind = "ready" | "not-installed" | "not-running" | "not-ready" | "unknown";

export interface RuntimeState {
  kind: RuntimeKind;
  /** A few words: "Ready", "Docker is not installed". */
  label: string;
}

/** The state in a few words, from the engine's reason code (internal/runtime/engine_state.go). */
export function runtimeState(runtime: Pick<RuntimeInput, "available" | "reason">): RuntimeState {
  if (runtime.available) return { kind: "ready", label: "Ready" };
  switch (runtime.reason || "") {
    case "not-installed":
      return { kind: "not-installed", label: "Docker is not installed" };
    case "not-running":
    case "first-start-pending":
      return { kind: "not-running", label: "Docker is installed but not running" };
    case "unreachable":
    case "":
      return { kind: "unknown", label: "Unknown whether Docker is installed" };
  }
  // windows-containers, access-denied, wsl-missing, wsl-outdated, virtualization-off
  return { kind: "not-ready", label: "Docker cannot run yet" };
}

/** "A", "A and B", "A, B and C". */
function nameList(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** The button that opens the runtime page. main.ts's view wiring reads data-view. */
export function setupDockerButton(ready = false, size = "btn-sm"): string {
  const label = ready ? "Show runtime" : "Set up Docker";
  return `<button class="btn ${ready ? "btn-ghost" : "btn-primary"} ${size}" data-view="runtime">${label}</button>`;
}

/** What still runs without Docker, in one sentence, or "" when nothing does. */
function withoutDocker(runtime: RuntimeInput): string {
  const native = runtime.nativeServices ?? [];
  if (!runtime.nativeAvailable || native.length === 0) return "";
  return native.length === 1
    ? `Without it only ${escapeHtml(native[0])} can run here.`
    : `Without it only ${escapeHtml(nameList(native))} can run here.`;
}

export interface RuntimeAttention {
  level: "fail" | "warn";
  tag: string;
  what: string;
  /** Already-escaped HTML. */
  why: string;
  /** Already-escaped HTML. */
  actions: string;
}

/**
 * The Dashboard row while no Docker engine answers, or null when one does.
 * `containers` is true when a container service is stored here, so Docker being
 * away stops something that ran.
 */
export function runtimeAttention(runtime: RuntimeInput, containers: boolean): RuntimeAttention | null {
  const actions = setupDockerButton();
  if (runtime.available) {
    return runtime.setupTask ? {
      level: "warn",
      tag: "Set up",
      what: "Docker on this computer",
      why: escapeHtml(runtime.setupTask),
      actions,
    } : null;
  }
  const state = runtimeState(runtime);
  if (runtime.reason !== undefined && state.kind === "unknown") {
    return {
      level: "warn",
      tag: "Unknown",
      what: "Docker",
      why: escapeHtml(runtime.message || "CashPilot could not check Docker."),
      actions,
    };
  }
  const message = escapeHtml(runtime.message || "Docker is not reachable.");
  if (!runtime.nativeAvailable) {
    return {
      level: "fail",
      tag: "Offline",
      what: "Runtime",
      why: `${escapeHtml(runtime.message || "Neither Docker nor the native runtime can run anything.")} Services cannot start until it is back.`,
      actions,
    };
  }
  if (containers) {
    // Native services still run, so this is not the whole app being down, but
    // every container service is.
    return {
      level: "fail",
      tag: "Offline",
      what: "Container runtime",
      why: `${message} Container services cannot start until it is back; native ones keep running.`,
      actions,
    };
  }
  // Nothing that ran has stopped, so this is not an outage: it is the setup a
  // person skipped on first run, and most apps wait for it.
  const why = state.kind === "not-installed" ? "Docker is not set up yet." : runtime.message || `${state.label}.`;
  return {
    level: "warn",
    tag: "Set up",
    what: "Docker",
    why: `${escapeHtml(why)} Most apps need Docker. ${withoutDocker(runtime)}`.trim(),
    actions,
  };
}

/** Settings' Runtime card: the state now, and the way to the runtime page. */
export function renderRuntimeSettings(runtime: RuntimeInput): string {
  const state = runtimeState(runtime);
  const ready = state.kind === "ready";
  const setupNeeded = !ready || !!runtime.setupTask;
  const tone = setupNeeded ? "warn" : "ok";
  const native = ready ? "" : withoutDocker(runtime);
  return `
    <section class="card" aria-labelledby="runtime-settings-title" data-runtime-settings="${state.kind}">
      <div class="card-header">
        <div>
          <span class="card-title" id="runtime-settings-title">Runtime</span>
          <p class="muted compact-copy">What runs the apps on this computer. Most apps run in Docker.</p>
        </div>
        ${setupDockerButton(!setupNeeded)}
      </div>
      <div class="runtime-card ${tone}">
        <strong>Docker: ${escapeHtml(state.label)}</strong>
        ${runtime.message ? `<span>${escapeHtml(runtime.message)}</span>` : ""}
        ${ready && runtime.setupTask ? `<small>${escapeHtml(runtime.setupTask)}</small>` : ""}
        ${native ? `<small>${native}</small>` : ""}
      </div>
    </section>
  `;
}

/** The line under the read-only "Runtime provider" setting: the chosen kind is not the state. */
export function runtimeProviderNote(runtime: RuntimeInput): string {
  const state = runtimeState(runtime);
  return `<small class="muted compact-copy" data-runtime-provider-state="${state.kind}"><strong>Docker now:</strong> ${escapeHtml(state.label)}. The value above is the kind CashPilot uses, not a sign that Docker is there.</small>`;
}
