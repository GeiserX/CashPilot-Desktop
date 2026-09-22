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

import type { HealthScore, ProducerReport } from "../wails";
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

// ---------------------------------------------------------------------------
// The producer badge: is it actually EARNING, as distinct from merely running?
// ---------------------------------------------------------------------------
//
// The status pill one column over is the container's state, and a container that
// has produced nothing for a month is still "running" in green. That is the most
// common complaint in this whole product category, and the pill is structurally
// incapable of showing it. So this is a second, separate badge, fed by
// internal/health.
//
// It carries the same rule as the health pill above, turned the other way round:
// NOT CHECKED IS NOT THE SAME CLAIM AS CHECKED AND FINE. There is no green
// "earning" badge, because nothing Desktop can see from a container proves money
// moved. A verdict we could not reach says so, quietly, instead of leaving the
// row reading as if all is well.

// renderProducerBadge renders the earning verdict for one deployed service.
//
// A service that is not up gets nothing: the status pill already says it is
// stopped, and "not earning" underneath it would be a second copy of the same
// news. A service with no verdict at all (an older backend, or a refresh that
// raced startup) also gets nothing — an empty badge would be an invented claim.
export function renderProducerBadge(
  report: ProducerReport | undefined,
  containerState: string
): string {
  if (!report) return "";
  if (containerState !== "running" && containerState !== "restarting") return "";

  const reasons = (report.reasons || []).filter((r) => !!r);
  const title = reasons.join(" ");
  if (report.state === "failing" || report.state === "idle") {
    // Amber for idle, red for a concrete failure: one is nobody buying, the
    // other is something the user can go and fix.
    const tone = report.state === "failing"
      ? "color: var(--error); background: rgba(248, 113, 113, 0.14); border-color: rgba(248, 113, 113, 0.32);"
      : "color: var(--warning); background: rgba(245, 158, 11, 0.14); border-color: rgba(245, 158, 11, 0.32);";
    return badge(tone, "not earning", title || "This service is running but not earning.");
  }
  // Anything else, including a state this frontend does not know, is "we did not
  // check" -- the honest reading of an answer we cannot interpret.
  return badge(
    "color: var(--text-muted); background: rgba(148, 163, 184, 0.12); border-color: rgba(148, 163, 184, 0.28);",
    "earning not checked",
    title || "CashPilot cannot tell whether this service is earning."
  );
}

function badge(tone: string, label: string, title: string): string {
  return `<span class="badge" style="margin-left: 6px; text-transform: none; ${tone}" title="${escapeHtml(title)}">${escapeHtml(label)}</span>`;
}
