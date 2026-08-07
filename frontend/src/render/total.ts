import { formatBalance } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/**
 * The headline total, rendered honestly.
 *
 * A total of `0` means two different things and the number alone cannot tell
 * them apart:
 *
 * - a user who has genuinely earned nothing, and
 * - a user whose balances could not be PRICED at all.
 *
 * The backend distinguishes them with `totalKnown` (see `computeEarningsSummary`
 * in app.go). This is not a rare edge: every balance is converted by routing
 * through USD, so one missing display-currency rate — a single failed fiat fetch
 * — makes every platform unpriceable at once, for every user not on USD. The old
 * code rendered that as a confident `$0.00` directly above a breakdown still
 * listing real money.
 *
 * `totalKnown` must be explicitly `true`. Anything else — false, absent, a
 * summary that has not loaded — is unknown, and unknown renders as an em dash.
 * Absent is not zero, and it is not true either.
 */
export interface TotalSummaryLike {
  total?: number;
  totalKnown?: boolean;
  ratesStale?: boolean;
}

/** The em dash shown wherever a figure is not known. */
export const UNKNOWN_TOTAL = "—";

export function totalIsKnown(summary: TotalSummaryLike | null | undefined): boolean {
  return summary?.totalKnown === true;
}

/**
 * The string for the headline/topbar balance. Never returns a formatted `0`
 * for an unknown total.
 */
export function totalText(
  summary: TotalSummaryLike | null | undefined,
  displayCurrency: string,
): string {
  if (!totalIsKnown(summary)) return UNKNOWN_TOTAL;
  return formatBalance(summary?.total ?? 0, displayCurrency);
}

/**
 * The caption under the headline. An unknown total needs to say WHY, otherwise
 * the em dash reads as a bug. "Rates may be stale" is the weaker, partial-sum
 * case and must not be reused for it: stale describes a number that is slightly
 * old, not one that is absent.
 */
export function totalCaption(summary: TotalSummaryLike | null | undefined): string {
  if (!totalIsKnown(summary)) return "Balances could not be priced — check your display currency";
  if (summary?.ratesStale) return "Rates may be stale";
  return "Across convertible services";
}
