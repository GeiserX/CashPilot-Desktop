// Pure formatting helpers, extracted from main.ts so they can be TESTED.
//
// main.ts does `const root = document.querySelector("#app")!` at module scope
// and imports the Wails runtime, so importing it from a Node harness needs a
// DOM, a CSS loader and Wails stubs. Nothing in this file touches the document,
// the network or any global — importing it is free, which is the whole point.
//
// This is the first step CashPilot-Desktop-tft asks for. Behaviour is unchanged:
// both functions are moved verbatim, and main.ts imports them from here.

/** Escape the five characters that matter in an innerHTML sink. */
export function escapeHtml(value: string | undefined | null): string {
  return String(value || "").replace(
    /[&<>"']/g,
    (ch) =>
      ({
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#039;",
      })[ch] || ch,
  );
}

import { intlLocale, t } from "./i18n.js";

const miningCurrencies = new Map([
  ["BTC", 8],
  ["XMR", 12],
  ["KAS", 8],
  ["ETC", 9],
  // Coins unMineable pays in. Its API writes balances with 8 decimals.
  ["BNB", 8],
  ["DOGE", 8],
  ["LTC", 8],
  ["SHIB", 8],
  ["TRX", 8],
]);

export function miningBalancePrecision(currency: string): number | undefined {
  return miningCurrencies.get(currency.trim().toUpperCase());
}

/**
 * A coin amount with no fixed precision, as the web's formatAmount writes it: at
 * least 8 decimals, more for a small amount so 8 significant digits show, up to
 * 12, with trailing zeros cut. unMineable pays in whichever coin the miner picks,
 * so a pool balance in a coin this file does not list must not round to 0.00.
 */
export function coinAmount(value: number): string {
  const amount = Number.isFinite(value) ? value : 0;
  const places = amount !== 0 ? Math.min(12, Math.max(8, 7 - Math.floor(Math.log10(Math.abs(amount))))) : 8;
  return amount.toFixed(places).replace(/(\.\d\d\d*?)0+$/, "$1");
}

/**
 * Format an amount in a currency, degrading gracefully for reward tokens.
 *
 * `code` is sanitised to A-Z0-9 because it is interpolated UNESCAPED into a
 * couple of innerHTML sinks (the topbar and the services-table balance cells);
 * stripping everything else closes those injection points and keeps Intl happy.
 *
 * Intl.NumberFormat throws RangeError on non-ISO codes — reward tokens like
 * MYST or GRASS — so those fall back to a plain "1234.00 CODE" string.
 */
export function formatBalance(value: number, currency: string, pool = false): string {
  const code = (currency || "USD").toUpperCase().replace(/[^A-Z0-9]/g, "");
  const amount = Number.isFinite(value) ? value : 0;
  const precision = miningBalancePrecision(code);
  if (precision !== undefined) {
    const native = new Intl.NumberFormat(intlLocale(), { maximumFractionDigits: precision }).format(amount);
    return `${native} ${code}`;
  }
  // A pool pays in a coin, never in fiat.
  if (pool) return `${coinAmount(amount)} ${code}`;
  try {
    return new Intl.NumberFormat(intlLocale(), {
      style: "currency",
      currency: code,
      maximumFractionDigits: 2,
    }).format(amount);
  } catch {
    return `${amount.toFixed(2)} ${code}`;
  }
}

/**
 * Turn an RFC3339 stamp into "just now" / "12 minutes ago", in the current
 * language.
 *
 * An unparseable value yields "" so the caller can omit the phrase entirely
 * rather than render "Invalid Date".
 */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  const seconds = Math.max(0, Math.round((now - then) / 1000));
  if (seconds < 60) return t("time.justNow");
  const ago = new Intl.RelativeTimeFormat(intlLocale(), { numeric: "always" });
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return ago.format(-minutes, "minute");
  const hours = Math.round(minutes / 60);
  if (hours < 24) return ago.format(-hours, "hour");
  return ago.format(-Math.round(hours / 24), "day");
}
