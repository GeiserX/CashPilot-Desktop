#!/usr/bin/env node
// Browser-free checks on the per-service earning chip. CashPilot-Desktop-tft.
//
// THE RULE THIS GUARDS, and it is about money being misreported:
//
//   a figure is shown in the DISPLAY currency only when a live rate actually
//   converted it. When the rate is missing, the chip must fall back to the
//   NATIVE balance and name that currency.
//
// `balanceDisplay === 0` on a convertible service means the rate was missing,
// not that the balance is zero. Rendering "0.00 EUR" there tells the user a
// service earned nothing, in precisely the case where it earned something and
// we merely could not convert it. That is the same class of lie as showing
// 0.00 for a platform with no reading, one level down.
//
// It drives the REAL function and asserts on the HTML it returns. It
// deliberately does NOT assert on source text: this repository and its sibling
// have both been bitten by tests that matched their own prose.
//
//   node scripts/earnings_render_check.mjs     # against ./.harness-build

import { renderEarningBreakdown } from "../.harness-build/render/earnings.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

/** A ServiceEarning with sane defaults, overridable per case. */
function earning(overrides = {}) {
  return {
    platform: "honeygain",
    name: "Honeygain",
    balance: 12.5,
    currency: "USD",
    balanceDisplay: 11.4,
    convertible: true,
    error: "",
    cashout: {
      minAmount: 20,
      currency: "USD",
      percent: 62.5,
      eligible: false,
      comparable: true,
      method: "redirect",
      dashboardUrl: "https://example.invalid",
      notes: "",
    },
    ...overrides,
  };
}

/** Just the <strong> figure, which is what the user reads first. */
function primary(html) {
  const m = html.match(/<strong>([\s\S]*?)<\/strong>/);
  return m ? m[1].trim() : "";
}

// ---------------------------------------------------------------------------
// THE RULE.
// ---------------------------------------------------------------------------
const converted = renderEarningBreakdown(earning(), "EUR");
check("a converted balance shows the display currency", primary(converted).includes("11.4"), converted);

const unconverted = renderEarningBreakdown(earning({ balanceDisplay: 0 }), "EUR");
check(
  "THE RULE: a missing rate shows the NATIVE balance, not a display-currency 0",
  primary(unconverted).includes("12.50"),
  `primary was: ${primary(unconverted)}`
);
// formatBalance renders an ISO code as its SYMBOL ($12.50), and a reward
// ticker as "12.50 MYST". So "did it stay native?" is asked as "is the display
// currency's symbol absent?", not by looking for the literal code.
check(
  "and it never renders as zero in the display currency",
  !primary(unconverted).includes("\u20ac") && !/^0\.00|\u20ac0\.00/.test(primary(unconverted)),
  `primary was: ${primary(unconverted)}`
);
// A reward token makes the fallback legible: the native CODE is printed, so
// this case asserts the currency is named rather than merely not-euro.
const tokenUnconverted = renderEarningBreakdown(
  earning({ balance: 40, balanceDisplay: 0, currency: "MYST" }),
  "EUR"
);
check(
  "a reward token falls back to its own ticker",
  primary(tokenUnconverted).includes("MYST") && primary(tokenUnconverted).includes("40.00"),
  `primary was: ${primary(tokenUnconverted)}`
);

// A service that genuinely holds nothing is a DIFFERENT case from a missing
// rate, and it is allowed to say zero -- in its own currency.
const genuinelyZero = renderEarningBreakdown(
  earning({ balance: 0, balanceDisplay: 0, convertible: true }),
  "EUR"
);
check(
  "a real zero balance still reports zero, in the NATIVE currency",
  /0\.00/.test(primary(genuinelyZero)) && !primary(genuinelyZero).includes("\u20ac"),
  `primary was: ${primary(genuinelyZero)}`
);

// ---------------------------------------------------------------------------
// The other two states stay distinct.
// ---------------------------------------------------------------------------
const errored = renderEarningBreakdown(earning({ error: "credentials expired" }), "EUR");
check("an errored service says so instead of showing a figure", primary(errored) === "Needs attention", errored);
check("the error itself is the subtitle", errored.includes("credentials expired"), errored);
check("an errored chip carries the error class", errored.includes('earning-chip error'), errored);
check(
  "an errored service shows NO progress bar -- a stale balance is not progress",
  !errored.includes("payout-progress"),
  errored
);

const notConvertible = renderEarningBreakdown(earning({ convertible: false }), "EUR");
check(
  "a non-convertible service shows its native figure, not a converted one",
  primary(notConvertible).includes("12.50") && !primary(notConvertible).includes("\u20ac"),
  `primary was: ${primary(notConvertible)}`
);
check("and says it was not converted", notConvertible.includes("not converted"), notConvertible);

// ---------------------------------------------------------------------------
// The cash-out bar.
// ---------------------------------------------------------------------------
check("a comparable threshold renders a bar", converted.includes("payout-progress-bar"), converted);
check(
  "an INCOMPARABLE threshold renders none -- a bar implies a comparison we cannot make",
  !renderEarningBreakdown(earning({ cashout: { ...earning().cashout, comparable: false } }), "EUR")
    .includes("payout-progress"),
  "incomparable still drew a bar"
);
check(
  "a zero minimum renders none -- everything would read as 100%",
  !renderEarningBreakdown(earning({ cashout: { ...earning().cashout, minAmount: 0 } }), "EUR")
    .includes("payout-progress"),
  "a zero minimum still drew a bar"
);

const over = renderEarningBreakdown(earning({ cashout: { ...earning().cashout, percent: 250 } }), "EUR");
check("a percentage over 100 is clamped", /width:\s*100\.0%/.test(over), over);
const under = renderEarningBreakdown(earning({ cashout: { ...earning().cashout, percent: -40 } }), "EUR");
check("a negative percentage is clamped to zero", /width:\s*0\.0%/.test(under), under);

check(
  "an eligible balance says it is ready",
  renderEarningBreakdown(earning({ cashout: { ...earning().cashout, eligible: true } }), "EUR")
    .includes("ready to cash out"),
  "eligible did not say so"
);

// ---------------------------------------------------------------------------
// Escaping. name, currency and error all originate off-machine.
// ---------------------------------------------------------------------------
const hostile = renderEarningBreakdown(
  earning({ name: '<img src=x onerror=alert(1)>', error: "<script>alert(1)</script>" }),
  "EUR"
);
check("a hostile name is escaped", !hostile.includes("<img src=x"), hostile.slice(0, 160));
check("a hostile error is escaped", !hostile.includes("<script>"), hostile.slice(0, 160));
check("the escaped form survives", hostile.includes("&lt;script&gt;"), hostile.slice(0, 160));

// ---------------------------------------------------------------------------
// Controls. A control that PASSES means the check above it proves nothing.
// ---------------------------------------------------------------------------
check(
  "CONTROL: the renderer distinguishes converted from unconverted at all",
  converted !== unconverted,
  "both branches produced identical HTML"
);
check(
  "CONTROL: a platform with no name falls back to its slug rather than rendering blank",
  renderEarningBreakdown(earning({ name: "" }), "EUR").includes("honeygain"),
  "an unnamed platform rendered nothing identifiable"
);

if (failures) {
  console.error(`\n${failures} of ${checks} earning-chip checks failed`);
  process.exit(1);
}
console.log(`${checks}/${checks} checks passed`);
console.log("earning chip render check passed");
