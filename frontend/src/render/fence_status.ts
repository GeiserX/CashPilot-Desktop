// The LAN fence in one line, for the dashboard and for Settings › LAN isolation.
//
// Settings let a person pick auto, on or off, but nothing said whether the fence is
// in place now or how many earners sit behind it. This line reads the status the
// fence check keeps (internal/runtime/lanfence.go): the setting, whether the fence
// was applied at the last check, and the running earners on the fenced network.
//
// Pure: takes the FenceStatus the backend sent, returns HTML, touches no global.

import type { FenceStatus } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

function earners(n: number): string {
  return n === 1 ? "1 running earner" : `${n} running earners`;
}

/** The fence's state in plain words, without the setting. */
export function fenceStatusText(fence: FenceStatus): string {
  const inside = (fence.inside || []).length;
  const outside = (fence.outside || []).length;
  const outsideNote = outside ? ` ${earners(outside)} ${outside === 1 ? "is" : "are"} still outside it and can reach your LAN until redeployed.` : "";
  switch (fence.mode) {
    case "on":
      if (fence.state === "failed") return "Down: the fence could not be applied, so container deploys are refused. Needs attention says why.";
      if (fence.state !== "applied") return "Not checked yet. CashPilot checks it at start, before each deploy and every five minutes.";
      if (!inside && !outside) return "In place. No earner runs yet, so nothing is behind it.";
      return `In place: ${earners(inside)} behind it.${outsideNote}`;
    case "legacy":
      return outside
        ? `Not in place: ${earners(outside)} can reach your LAN. Turn on isolation on the dashboard to fence ${outside === 1 ? "it" : "them"}.`
        : "Not in place: this install deploys earners on Docker's default network, where they can reach your LAN. Set LAN isolation to on to fence the next deploy.";
    case "off":
      return "Off: container earners use Docker's default network and can reach your LAN.";
    case "unsupported":
      return "Not fenced: CashPilot cannot fence earners on this container runtime (Podman).";
  }
  // No mode: Docker could not be reached, or the first check has not finished.
  // With Docker running, the check at start sets the fence up even before any
  // earner runs, so "set up on the first deploy" is said only while Docker is down.
  if (fence.state === "failed") return "Not known: CashPilot could not check it. Needs attention says why.";
  if (fence.state === "n/a") return "No container earner runs now: Docker is not running. The fence is set up when Docker runs, at the latest on the first deploy.";
  return "Not checked yet. CashPilot checks it at start, before each deploy and every five minutes.";
}

/**
 * One line: the setting, then the fence's state. "" when there is no Docker
 * runtime. tag is "p" on the dashboard and "small" inside a Settings row, whose
 * label may hold only inline content.
 */
export function renderFenceStatusLine(fence: FenceStatus | null | undefined, tag: "p" | "small" = "p"): string {
  if (!fence) return "";
  return `<${tag} class="muted compact-copy" data-fence-status="${escapeHtml(fence.mode || "unknown")}"><strong>LAN fence now</strong> (setting: ${escapeHtml(fence.setting || "auto")}). ${escapeHtml(fenceStatusText(fence))}</${tag}>`;
}
