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
export function formatBalance(value: number, currency: string): string {
  const code = (currency || "USD").toUpperCase().replace(/[^A-Z0-9]/g, "");
  const amount = Number.isFinite(value) ? value : 0;
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: code,
      maximumFractionDigits: 2,
    }).format(amount);
  } catch {
    return `${amount.toFixed(2)} ${code}`;
  }
}

/**
 * Turn an RFC3339 stamp into "just now" / "12 minutes ago".
 *
 * An unparseable value yields "" so the caller can omit the phrase entirely
 * rather than render "Invalid Date".
 */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  const seconds = Math.max(0, Math.round((now - then) / 1000));
  if (seconds < 60) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  const days = Math.round(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}
