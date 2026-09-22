// The pre-deploy reality check, shown in the wizard right above the Deploy button.
//
// It answers one question: what, on THIS machine, will stop this service earning?
// The backend (internal/preflight) decides; this file only puts the answer on
// screen. Three rules hold it together, and each has a check in
// scripts/preflight_render_check.mjs:
//
//   1. It never blocks. Even "this will earn nothing here" renders as a panel the
//      user reads and then deploys anyway if they want to. Nothing here disables
//      the Deploy button, and nothing here is a modal.
//   2. What was NOT checked is always shown. A clean result that hides the list
//      reads as a guarantee about things nobody looked at.
//   3. Nothing renders at all until there is an answer. A blank panel with a
//      reassuring heading, drawn while the check is still running, is a claim we
//      have not earned yet.
//
// Pure: takes a report (or null) and returns an HTML string.

import type { PreflightReport } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** The four verdicts, in the same vocabulary the Go side and the web app use. */
const TONES: Record<string, { label: string; color: string; tint: string }> = {
  will_earn_nothing: { label: "will earn nothing here", color: "var(--error)", tint: "rgba(248, 113, 113, 0.14)" },
  reduced_earnings: { label: "will earn less here", color: "var(--warning)", tint: "rgba(245, 158, 11, 0.14)" },
  check_these: { label: "check these", color: "var(--warning)", tint: "rgba(245, 158, 11, 0.14)" },
  looks_fine: { label: "looks fine", color: "var(--success)", tint: "rgba(34, 197, 94, 0.12)" },
};

/** An unrecognised verdict still renders, in neutral colours and with no claim. */
const NEUTRAL = { label: "", color: "var(--warning)", tint: "rgba(245, 158, 11, 0.10)" };

function tone(verdict: string) {
  return TONES[verdict] || NEUTRAL;
}

/**
 * Render the pre-deploy check for one service.
 *
 * `report` is null while the check is still running, or when it failed — both
 * render nothing rather than an empty panel that implies an answer.
 */
export function renderPreflight(report: PreflightReport | null | undefined): string {
  if (!report || !report.summary) return "";
  const verdict = tone(report.verdict);
  const findings = report.findings || [];
  const notChecked = report.notChecked || [];

  const badge = verdict.label
    ? `<span class="badge" style="text-transform: none; color: ${verdict.color}; background: ${verdict.tint}; border-color: ${verdict.color};">${escapeHtml(verdict.label)}</span>`
    : "";

  const list = findings.length
    ? `<ul class="preflight-findings">
        ${findings
          .map((finding) => {
            const row = tone(finding.verdict);
            return `<li style="border-left: 3px solid ${row.color};">${escapeHtml(finding.message)}</li>`;
          })
          .join("")}
      </ul>`
    : "";

  // Always rendered when the backend named anything: the honest half of a clean
  // result is the list of things nobody looked at.
  const unchecked = notChecked.length
    ? `<p class="muted preflight-unchecked">Not checked: ${escapeHtml(notChecked.join("; "))}.</p>`
    : "";

  return `
    <section class="preflight" style="border-left: 3px solid ${verdict.color}; background: ${verdict.tint};">
      <div class="preflight-head">
        <strong>Before you deploy</strong>
        ${badge}
      </div>
      <p class="preflight-summary">${escapeHtml(report.summary)}</p>
      ${list}
      ${unchecked}
    </section>
  `;
}
