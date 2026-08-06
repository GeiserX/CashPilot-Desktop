// The per-service earning chip, extracted from main.ts so it can be TESTED.
//
// It carries the same rule as the fleet panel one level down, and the rule is
// about MONEY BEING MISREPORTED: a figure is only shown in the display currency
// when a live rate actually converted it. When the rate is missing, the chip
// falls back to the NATIVE balance and says which currency that is — because a
// display-currency "0.00" here would tell the user a service earned nothing
// when in truth we simply could not convert what it did earn.
//
// See CashPilot-Desktop-tft. The Go side proves the values survive to JSON;
// nothing proved the render until this moved out of a 1600-line file.
//
// Pure: takes a ServiceEarning, returns an HTML string, touches no global.

import type { ServiceEarning } from "../wails";
import { escapeHtml, formatBalance } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/**
 * Render one service's earning chip.
 *
 * Three states, deliberately distinct:
 *
 * - **error** — "Needs attention", with the error itself as the subtitle. No
 *   figure at all, because a balance we could not refresh is not news about
 *   the money.
 * - **convertible, and converted** — the display-currency figure, with the
 *   native amount underneath so the conversion stays auditable.
 * - **convertible, but not converted** — the NATIVE figure. `balanceDisplay`
 *   of 0 on a convertible service means the live rate is missing, not that
 *   the balance is zero, and showing "0.00 EUR" would be a lie in the exact
 *   case the user most needs the truth.
 */
export function renderEarningBreakdown(item: ServiceEarning, displayCurrency: string): string {
  const native = `${item.balance.toFixed(2)} ${item.currency}`;
  // When a service is convertible but its display balance is 0 the live rate is
  // missing, so show the native `balance currency` instead of a misleading
  // display-currency 0.
  const primary = item.error
    ? "Needs attention"
    : item.convertible && item.balanceDisplay !== 0
      ? formatBalance(item.balanceDisplay, displayCurrency)
      : formatBalance(item.balance, item.currency);
  const cashout = item.cashout;
  const showBar = !item.error && cashout.comparable && cashout.minAmount > 0;
  const pct = Math.max(0, Math.min(100, cashout.percent || 0));
  const sub = item.error
    ? escapeHtml(item.error)
    : item.convertible
      ? `${escapeHtml(native)}${cashout.eligible ? " · ready to cash out" : ""}`
      : `${escapeHtml(item.currency)} · not converted`;
  return `
    <div class="earning-chip ${item.error ? "error" : ""}" title="${escapeHtml(native)}">
      <span>${escapeHtml(item.name || item.platform)}</span>
      <strong>${escapeHtml(primary)}</strong>
      <small>${sub}</small>
      ${showBar ? `
        <div class="payout-progress" title="${pct.toFixed(0)}% of ${escapeHtml(formatBalance(cashout.minAmount, cashout.currency))} minimum">
          <div class="payout-progress-bar" style="width: ${pct.toFixed(1)}%"></div>
        </div>
      ` : ""}
    </div>
  `;
}
