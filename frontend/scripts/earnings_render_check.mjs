#!/usr/bin/env node
// Browser-free checks on a platform's balance and cash-out cells in the
// dashboard's services table. CashPilot-Desktop-tft.
//
// THE RULE THIS GUARDS, and it is about money being misreported:
//
//   a figure is shown in the DISPLAY currency only when a live rate actually
//   converted it. When the rate is missing, the cell must fall back to the
//   NATIVE balance and name that currency.
//
// `balanceDisplay === 0` on a convertible service means the rate was missing,
// not that the balance is zero. Rendering "0.00 EUR" there tells the user a
// service earned nothing, in precisely the case where it earned something and
// we merely could not convert it. That is the same class of lie as showing
// 0.00 for a platform with no reading, one level down.
//
// These cases guarded the per-service earning chip until the console layout
// moved the balance and the cash-out bar into the table's columns.
//
// It drives the REAL functions and asserts on the HTML they return. It
// deliberately does NOT assert on source text: this repository and its sibling
// have both been bitten by tests that matched their own prose.
//
//   node scripts/earnings_render_check.mjs     # against ./.harness-build

import { balanceCell, payoutCell } from "../.harness-build/render/services.js";

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
      method: "paypal",
      dashboardUrl: "",
      notes: "",
    },
    ...overrides,
  };
}

/** The figure itself: the cell's text before its sub-line. */
function primary(html) {
  return html.split("<div")[0].trim();
}

const cell = (item) => balanceCell(item, undefined, "EUR");

// ---------------------------------------------------------------------------
// THE RULE.
// ---------------------------------------------------------------------------
const converted = cell(earning());
check("a converted balance shows the display currency", primary(converted).includes("11.4") && primary(converted).includes("€"), converted);
check("and the native amount stays under it, so the conversion is auditable", converted.includes("$12.50"), converted);

const unconverted = cell(earning({ balanceDisplay: 0 }));
check("THE RULE: a missing rate shows the NATIVE balance, not a display-currency 0", primary(unconverted).includes("12.50"), `primary was: ${primary(unconverted)}`);
check(
  "and it never renders as zero in the display currency",
  !primary(unconverted).includes("€") && !/0\.00/.test(primary(unconverted)),
  `primary was: ${primary(unconverted)}`
);
check("and it says no rate converted it", unconverted.includes("no rate"), unconverted);

const tokenUnconverted = cell(earning({ balance: 40, balanceDisplay: 0, currency: "MYST" }));
check(
  "a reward token falls back to its own ticker",
  primary(tokenUnconverted).includes("MYST") && primary(tokenUnconverted).includes("40.00"),
  `primary was: ${primary(tokenUnconverted)}`
);

// A service that genuinely holds nothing is a DIFFERENT case from a missing
// rate, and it is allowed to say zero -- in its own currency, without a note.
const genuinelyZero = cell(earning({ balance: 0, balanceDisplay: 0 }));
check(
  "a real zero balance still reports zero, in the NATIVE currency",
  /0\.00/.test(primary(genuinelyZero)) && !primary(genuinelyZero).includes("€") && !genuinelyZero.includes("no rate"),
  genuinelyZero
);

// ---------------------------------------------------------------------------
// No reading.
// ---------------------------------------------------------------------------
const errored = cell(earning({ error: "credentials expired" }));
check("an errored balance is an em dash, never a figure", primary(errored).includes("&mdash;") && !/\d/.test(primary(errored).replace(/<[^>]*>/g, "")), errored);
check("it says there is no reading, and why on hover", errored.includes("no reading") && errored.includes("credentials expired"), errored);
const recordError = balanceCell(undefined, { balance: 9, currency: "USD", error: "timeout" }, "EUR");
check("an error on the raw reading alone is also a dash", recordError.includes("&mdash;") && !recordError.includes("9.00"), recordError);
check("no reading at all is a dash", balanceCell(undefined, undefined, "EUR").includes("&mdash;"));

const notConvertible = cell(earning({ convertible: false }));
check(
  "a non-convertible service shows its native figure, not a converted one",
  primary(notConvertible).includes("12.50") && !primary(notConvertible).includes("€"),
  `primary was: ${primary(notConvertible)}`
);
check("and says it was not converted", notConvertible.includes("not converted"), notConvertible);

// ---------------------------------------------------------------------------
// The cash-out bar.
// ---------------------------------------------------------------------------
check("a comparable threshold renders a bar", payoutCell(earning()).includes("payout-progress-bar"));
check(
  "an INCOMPARABLE threshold renders none -- a bar implies a comparison we cannot make",
  !payoutCell(earning({ cashout: { ...earning().cashout, comparable: false } })).includes("payout-progress")
);
check(
  "a zero minimum renders none -- everything would read as 100%",
  !payoutCell(earning({ cashout: { ...earning().cashout, minAmount: 0 } })).includes("payout-progress")
);
check(
  "an errored service shows NO progress bar -- a stale balance is not progress",
  !payoutCell(earning({ error: "x" })).includes("payout-progress")
);
const over = payoutCell(earning({ cashout: { ...earning().cashout, percent: 250 } }));
check("a percentage over 100 is clamped", /width:\s*100\.0%/.test(over), over);
const under = payoutCell(earning({ cashout: { ...earning().cashout, percent: -40 } }));
check("a negative percentage is clamped to zero", /width:\s*0\.0%/.test(under), under);
check("an eligible balance says it is ready", payoutCell(earning({ cashout: { ...earning().cashout, eligible: true } })).includes("ready"));

// ---------------------------------------------------------------------------
// Escaping. currency and error originate off-machine.
// ---------------------------------------------------------------------------
const hostile = cell(earning({ error: "<script>alert(1)</script>" }));
check("a hostile error is escaped", !hostile.includes("<script>") && hostile.includes("&lt;script&gt;"), hostile);

// ---------------------------------------------------------------------------
// Controls. A control that PASSES means the check above it proves nothing.
// ---------------------------------------------------------------------------
check("CONTROL: the renderer distinguishes converted from unconverted at all", converted !== unconverted, "both branches produced identical HTML");

if (failures) {
  console.error(`\n${failures} of ${checks} balance-cell checks failed`);
  process.exit(1);
}
console.log(`${checks}/${checks} checks passed`);
console.log("balance cell render check passed");
