// The pre-deploy reality check, shown in the wizard right above the Deploy button.
//
// It answers one question: what, on THIS machine, will stop this service earning?
// The backend assesses the service (internal/preflight). The app's latest
// Docker reading says whether a local deploy can run. Three rules hold it together, with checks in
// scripts/preflight_render_check.mjs:
//
//   1. It never blocks. Even "this will earn nothing here" renders as a panel the
//      user reads and then deploys anyway if they want to. Nothing here disables
//      the Deploy button, and nothing here is a modal. The one exception is not a
//      verdict: a Docker app without a ready runtime says so and offers Set up
//      Docker, because the deploy itself is refused.
//   2. What was NOT checked is always shown. A clean result that hides the list
//      reads as a guarantee about things nobody looked at.
//   3. The service findings appear only after the backend answers.
//
// Pure: takes a report (or null) and returns an HTML string.

import type { PreflightReport, RuntimeStatus, Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { runtimeState, setupDockerButton } from "./runtime_state.js";

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

/** Why a local Docker deploy cannot proceed, or "" if Docker is ready or unneeded. */
export function localDockerGap(service: Service | undefined, runtime: RuntimeStatus): string {
  if (runtime.available || !service?.docker?.image?.trim()) return "";
  if (!service.custom && runtime.nativeServices?.includes(service.name)) return "";
  switch (runtimeState(runtime).kind) {
    case "unknown":
      return "Docker status is unknown. Check Docker before deploying on this computer.";
    case "not-installed":
      return `Docker is not set up on this computer yet, so ${service.name} cannot run here.`;
    default:
      return `Docker is not ready on this computer yet, so ${service.name} cannot run here.`;
  }
}

/**
 * Render the pre-deploy check for a service on this computer.
 *
 * `report` is null while the check is still running, or when it failed — both
 * render nothing rather than an empty panel that implies an answer.
 */
export function renderPreflight(report: PreflightReport | null | undefined, workerId: number | null = null, dockerGap = ""): string {
  const summary = dockerGap || report?.summary;
  if (workerId !== null || !summary) return "";
  const verdict = tone(dockerGap ? "will_earn_nothing" : report?.verdict ?? "");
  const findings = report?.findings || [];
  const notChecked = report?.notChecked || [];

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
      <p class="preflight-summary">${escapeHtml(summary)}</p>
      ${dockerGap || report?.blocking ? `<p class="preflight-blocked" data-preflight-blocked>Deploy is refused until then. ${setupDockerButton()}</p>` : ""}
      ${list}
      ${unchecked}
    </section>
  `;
}
