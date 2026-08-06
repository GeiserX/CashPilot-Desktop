// The per-service health pill, extracted from main.ts so it can be TESTED.
//
// It carries the same rule as the earnings chip, one column over: a service
// with NO health entry yet renders NOTHING, never a 0/100 badge. Nothing
// scored is not the same claim as scored zero, and a red "0" against a service
// that has simply not been measured yet is a fabricated accusation.
//
// See CashPilot-Desktop-tft.
//
// Pure: takes a HealthScore or undefined, returns an HTML string.

import type { HealthScore } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

// renderHealthBadge renders a compact, color-coded pill for a deployed service's
// rolling health: the 0-100 score plus uptime%. Colour tracks the score — green
// >= 80, amber 50-79, red < 50 — reusing the theme's own status variables. A
// service with no health entry yet (nothing scored) renders nothing rather than a
// misleading 0/NaN badge. The title surfaces the raw lifecycle counts behind it.
export function renderHealthBadge(health: HealthScore | undefined): string {
  if (!health) return "";
  const score = Math.round(health.score);
  const uptime = Math.round(health.uptimePercent);
  const crashes = health.crashes;
  // "Unstable" surfaces the crash accounting the native supervisor now records (Phase C1):
  // a service that has crashed repeatedly in the health window. It reads off the same 7-day
  // aggregate the score does, so it flags sustained crashing rather than an instantaneous
  // loop — hence the honest "unstable" label. Unstable always shows the error tone.
  const unstable = crashes >= 3;
  const tone = unstable || score < 50
    ? "color: var(--error); background: rgba(248, 113, 113, 0.14); border-color: rgba(248, 113, 113, 0.32);"
    : score < 80
      ? "color: var(--warning); background: rgba(245, 158, 11, 0.14); border-color: rgba(245, 158, 11, 0.32);"
      : "color: var(--success); background: rgba(34, 197, 94, 0.12); border-color: rgba(34, 197, 94, 0.32);";
  const title = `Health ${score}/100 · ${uptime}% uptime · ${health.restarts} restarts · ${crashes} crashes · ${health.stops} stops`;
  // Surface crashes in the visible pill (previously only in the tooltip) so a crash-looping
  // earner is legible at a glance, not just via hover.
  const crashNote = crashes > 0 ? ` · ${crashes} crash${crashes === 1 ? "" : "es"}` : "";
  const label = unstable ? `unstable · ${uptime}% up${crashNote}` : `${score} · ${uptime}% up${crashNote}`;
  return `<span class="badge" style="margin-left: 6px; text-transform: none; ${tone}" title="${escapeHtml(title)}">${escapeHtml(label)}</span>`;
}
