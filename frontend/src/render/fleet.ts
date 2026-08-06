// The account-wide earnings panel, extracted from main.ts so it can be TESTED.
//
// It carries the rule this whole feature turns on — a platform with NO reading
// renders as an em dash, never 0.00 — and until now nothing verified it past the
// JSON boundary. See CashPilot-Desktop-tft.
//
// Pure: takes a FleetView, returns an HTML string, touches no global.

import type { FleetView } from "../wails";
import { escapeHtml, formatBalance, relativeTime } from "./format.js";  // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/**
 * Render the paired server's account-level figures.
 *
 * It sits BELOW the local numbers rather than replacing them, and it is labelled
 * as the ACCOUNT's rather than this machine's, because those are different
 * claims. Earnings are collected per PLATFORM from the provider; if two machines
 * run the same service the provider reports one balance and nothing can split
 * it. Saying "this machine earned X" would be false in exactly the case a fleet
 * user is in.
 *
 * `now` is injectable so the "12 minutes ago" phrasing is testable without
 * freezing the clock.
 */
export function renderFleetSection(fleet: FleetView, now: number = Date.now()): string {
  const platforms = fleet.platforms || [];
  const withoutReadings = fleet.withoutReadings || [];
  const shared = platforms.filter((p) => p.shared).length;

  // null is UNKNOWN, and it renders as a dash. `?? 0` here would report a loss
  // that did not happen — most convincingly to the user whose collector is
  // broken, who is precisely the person who must not be told everything is fine.
  const money = (usd: number | null | undefined) =>
    usd === null || usd === undefined ? "&mdash;" : escapeHtml(formatBalance(usd, fleet.currency || "USD"));

  const age = fleet.reportedAt ? relativeTime(fleet.reportedAt, now) : "";

  return `
    <section class="card fleet-panel">
      <div class="card-header">
        <div>
          <span class="card-title">Across your CashPilot account</span>
          <p class="muted compact-copy">
            What the platforms this machine runs earned on your account over the last
            ${escapeHtml(String(fleet.windowDays || 30))} days, reported by
            <code>${escapeHtml(fleet.serverUrl)}</code>${age ? ` &middot; ${escapeHtml(age)}` : ""}.
            ${shared ? `${shared} of these run on more than one machine, so the figure is the account's, not this machine's.` : ""}
          </p>
        </div>
        <div class="fleet-total">
          <strong>${money(fleet.totalUsd)}</strong>
          <small>${fleet.totalUsd === null || fleet.totalUsd === undefined ? "nothing collected yet" : "known platforms only"}</small>
        </div>
      </div>
      <div class="earnings-breakdown">
        ${
          platforms.length
            ? platforms
                .map(
                  (p) => `
            <div class="earning-chip${p.shared ? " shared" : ""}">
              <span>${escapeHtml(p.slug)}</span>
              <strong>${money(p.usd)}</strong>
              ${p.shared ? `<small title="More than one machine on your fleet runs this, so the provider reports one balance for all of them.">shared</small>` : ""}
            </div>
          `,
                )
                .join("")
            : `<p class="muted">This server has no figures for the platforms on this machine yet.</p>`
        }
      </div>
      ${
        withoutReadings.length
          ? `<p class="muted compact-copy">No reading at all for ${escapeHtml(withoutReadings.join(", "))} &mdash; usually a collector that does not exist yet, or credentials never entered. They are missing from the total rather than counted as zero.</p>`
          : ""
      }
    </section>
  `;
}
