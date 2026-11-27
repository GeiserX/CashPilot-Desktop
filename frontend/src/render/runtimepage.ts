// The runtime page's first reading of this PC: what lands where once HostCheck
// answers, or fails. main.ts owns the elements; this file decides what goes in
// them, so the harness can drive it (scripts/runtimepage_check.mjs).
//
// Rules:
//   - The caller publishes the status from every reading, including failures.
//   - A reading that failed says so. The install guides it shows are not the
//     result of a check, and the page says that, with a way to check again.
//
// Pure apart from the deps it is given.

import type { AppState, HostCheckResult, InstallGuide, NotificationItem, RuntimeStatus, SetupResumeView } from "../wails";
import { ht, t } from "./i18n.js";

export interface RuntimePageDeps {
  /** The page is still on screen; a reading that lands after it left draws nothing. */
  current(): boolean;
  /** The install guide cards under the checklist. */
  showGuides(guides: InstallGuide[]): void;
  /** HTML for the checklist's slot; "" empties it. */
  showHolder(html: string): void;
  /** A setup step may have written the restart journal since the last visit. */
  readResume(): Promise<SetupResumeView>;
  /** This PC has a checklist: mount it with the reading and pending restart. */
  showChecklist(result: HostCheckResult, resume: SetupResumeView | null): void;
}

// One object each, because withRuntimeReading tells the unknown reading by
// identity. The words are getters, so they are read in the language current
// when the reading is drawn, never frozen at import.
export const unknownRuntime: RuntimeStatus = {
  available: false, nativeAvailable: false, nativeServices: null,
  reason: "", get message() { return t("runtimepage.unknown.message"); },
  kind: "", context: "", version: "", tools: null,
};

export const unknownRuntimeNotice: NotificationItem = {
  runtime: true, level: "warning",
  get title() { return t("runtimepage.unknown.title"); },
  get message() { return unknownRuntime.message; },
};

type RuntimeSnapshot = Pick<AppState, "runtime" | "notifications">;

/** Apply the runtime reading and its alert together, keeping unrelated alerts. */
export function withRuntimeReading(current: RuntimeSnapshot, runtime: RuntimeStatus, notice: NotificationItem | null): RuntimeSnapshot {
  const otherAlerts = (current.notifications || []).filter((item) => !item.runtime);
  const { nativeAvailable, nativeServices } = current.runtime;
  return {
    runtime: runtime === unknownRuntime ? { ...runtime, nativeAvailable, nativeServices } : runtime,
    notifications: notice ? [notice, ...otherAlerts] : otherAlerts,
  };
}

/** The checklist covers Docker Desktop, so its guide card goes when there is one. */
export function guidesBesideChecklist(guides: InstallGuide[], checklist: boolean): InstallGuide[] {
  return checklist ? guides.filter((g) => g.id !== "docker-desktop-windows") : guides;
}

/** The note in the checklist's slot when the reading failed. */
export function runtimeCheckFailed(error: unknown): string {
  return `<div class="docker-guide-wait warn" data-runtime-check-failed role="status">
      <p>${ht("runtimepage.checkFailed", { error: String(error) })}</p>
      <button class="btn btn-secondary btn-sm" data-runtime-recheck>${ht("runtimepage.checkAgain")}</button>
    </div>`;
}

/** Reads the PC once and puts the result on the page. */
export async function loadRuntimePage(check: () => Promise<HostCheckResult>, guides: InstallGuide[], deps: RuntimePageDeps): Promise<void> {
  let result: HostCheckResult;
  try {
    result = await check();
  } catch (error) {
    if (!deps.current()) return;
    deps.showHolder(runtimeCheckFailed(error));
    deps.showGuides(guides);
    return;
  }
  if (!deps.current()) return;
  const checklist = !!result.facts?.supported && (result.steps?.length ?? 0) > 0;
  deps.showGuides(guidesBesideChecklist(guides, checklist));
  if (!checklist) {
    deps.showHolder("");
    return;
  }
  const resume = result.setupFinished ? null : await deps.readResume().catch(() => null);
  if (deps.current()) deps.showChecklist(result, resume?.active ? resume : null);
}
