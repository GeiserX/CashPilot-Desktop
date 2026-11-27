// The account-wide earnings panel, extracted from main.ts so it can be TESTED.
//
// It carries the rule this whole feature turns on — a platform with NO reading
// renders as an em dash, never 0.00 — and until now nothing verified it past the
// JSON boundary. See CashPilot-Desktop-tft.
//
// Pure: takes a FleetView, returns an HTML string, touches no global.

import type { FleetView } from "../wails";
import { escapeHtml, formatBalance, relativeTime } from "./format.js";  // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, raw } from "./i18n.js";

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
          <span class="card-title">${ht("fleet.title")}</span>
          <p class="muted compact-copy">
            ${ht("fleet.window", { days: String(fleet.windowDays || 30), server: fleet.serverUrl, age: raw(age ? ` &middot; ${escapeHtml(age)}` : "") })}
            ${shared ? ht("fleet.shared", { n: shared }) : ""}
          </p>
        </div>
        <div class="fleet-total">
          <strong>${money(fleet.totalUsd)}</strong>
          <small>${fleet.totalUsd === null || fleet.totalUsd === undefined ? ht("fleet.nothingYet") : ht("fleet.knownOnly")}</small>
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
              ${p.shared ? `<small title="${ht("fleet.sharedTitle")}">${ht("fleet.sharedTag")}</small>` : ""}
            </div>
          `,
                )
                .join("")
            : `<p class="muted">${ht("fleet.noFigures")}</p>`
        }
      </div>
      ${
        withoutReadings.length
          ? `<p class="muted compact-copy">${ht("fleet.noReading", { slugs: withoutReadings.join(", ") })}</p>`
          : ""
      }
    </section>
  `;
}
