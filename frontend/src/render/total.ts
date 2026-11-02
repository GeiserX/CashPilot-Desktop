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
  // The flag alone is not enough. `formatBalance` coerces a non-finite value to
  // 0, so a summary claiming `totalKnown: true` while carrying an absent or NaN
  // total would render a confident zero -- reintroducing, through the back door,
  // precisely the bug this module exists to remove. A known total must also be
  // a total.
  return (
    summary?.totalKnown === true &&
    typeof summary.total === "number" &&
    Number.isFinite(summary.total)
  );
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

/**
 * The line under the balance that survives a cash-out.
 *
 * The balance drops by every cash-out, so on its own it says what is waiting and
 * never what the services made. Once a cash-out is recorded this says the rest:
 * "Earned all time X (Y cashed out)". Empty when nothing was ever cashed out, or
 * when the summary predates the ledger (absent is not zero). A ledger that could
 * not be read says so instead of a figure, and an unknown balance total keeps the
 * all-time figure an em dash.
 */
export interface LifetimeSummaryLike extends TotalSummaryLike {
  paidOut?: number | null;
  earnedAllTime?: number | null;
  unpricedPayouts?: number;
}

export function allTimeNote(summary: LifetimeSummaryLike | null | undefined, displayCurrency: string): string {
  if (!summary || !("paidOut" in summary)) return "";
  const paid = summary.paidOut;
  if (paid === null || paid === undefined || !Number.isFinite(paid)) {
    return "Earned all time: cash-outs could not be read";
  }
  const unpriced = summary.unpricedPayouts || 0;
  if (!(paid > 0) && unpriced === 0) return "";
  const all = summary.earnedAllTime;
  const allText = typeof all === "number" && Number.isFinite(all) ? formatBalance(all, displayCurrency) : UNKNOWN_TOTAL;
  const unpricedText = unpriced > 0 ? `, plus ${unpriced} cash-out${unpriced === 1 ? "" : "s"} with no rate` : "";
  return `Earned all time ${allText} (${formatBalance(paid, displayCurrency)} cashed out${unpricedText})`;
}
