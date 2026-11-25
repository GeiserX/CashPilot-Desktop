// The runtime page's first reading of this PC: what lands where once HostCheck
// answers, or fails. main.ts owns the elements; this file decides what goes in
// them, so the harness can drive it (scripts/runtimepage_check.mjs).
//
// Rules:
//   - Every reading is fresh, so the runtime card and the app's state take its
//     runtime, whether or not the PC has a checklist.
//   - A reading that failed says so. The install guides it shows are not the
//     result of a check, and the page says that, with a way to check again.
//
// Pure apart from the deps it is given.

import type { HostCheckResult, InstallGuide, RuntimeStatus } from "../wails";
import { escapeHtml } from "./format.js";

export interface RuntimePageDeps {
  /** The page is still on screen; a reading that lands after it left draws nothing. */
  current(): boolean;
  /** The reading's runtime: the card and the app's state take it. */
  setRuntime(runtime: RuntimeStatus): void;
  /** The install guide cards under the checklist. */
  showGuides(guides: InstallGuide[]): void;
  /** HTML for the checklist's slot; "" empties it. */
  showHolder(html: string): void;
  /** This PC has a checklist: mount it with the reading. */
  showChecklist(result: HostCheckResult): void;
}

/** The checklist covers Docker Desktop, so its guide card goes when there is one. */
export function guidesBesideChecklist(guides: InstallGuide[], checklist: boolean): InstallGuide[] {
  return checklist ? guides.filter((g) => g.id !== "docker-desktop-windows") : guides;
}

/** The note in the checklist's slot when the reading failed. */
export function runtimeCheckFailed(error: unknown): string {
  return `<div class="docker-guide-wait warn" data-runtime-check-failed role="status">
      <p>CashPilot could not check this computer: ${escapeHtml(String(error))}. The install guides below are shown without that check.</p>
      <button class="btn btn-secondary btn-sm" data-runtime-recheck>Check again</button>
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
  deps.setRuntime(result.runtime);
  const checklist = !!result.facts?.supported && (result.steps?.length ?? 0) > 0;
  deps.showGuides(guidesBesideChecklist(guides, checklist));
  if (!checklist) {
    deps.showHolder("");
    return;
  }
  deps.showChecklist(result);
}
