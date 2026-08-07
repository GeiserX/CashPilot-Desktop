#!/usr/bin/env node
// Browser-free checks on the headline/topbar total. CashPilot-Desktop-0mb.
//
// The rule, again, and this time at the most-read number in the product:
//
//   NOT MEASURED is not the same claim as MEASURED ZERO.
//
// Every balance is priced by routing through USD, so ONE missing
// display-currency rate makes every platform unpriceable at once. The backend
// then has nothing to add up and `total` keeps its zero value. Rendering that
// as "$0.00" tells a user with real money that they have none -- and it does it
// directly above a breakdown that still lists the money, so the page contradicts
// itself.
//
// `totalKnown` is what separates the two. It must be explicitly true; false,
// missing, or a summary that never loaded are all UNKNOWN.
//
//   node scripts/total_render_check.mjs      # against ./.harness-build

import { totalText, totalCaption, totalIsKnown, UNKNOWN_TOTAL } from "../.harness-build/render/total.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

// ---------------------------------------------------------------------------
// THE RULE.
// ---------------------------------------------------------------------------
const unpriceable = { total: 0, totalKnown: false, ratesStale: true };

check(
  "THE RULE: an unpriceable total renders the em dash, never a formatted zero",
  totalText(unpriceable, "JPY") === UNKNOWN_TOTAL,
  `got: ${JSON.stringify(totalText(unpriceable, "JPY"))}`
);
check(
  "and it contains no digit at all, so no currency formatter can leak a 0 through",
  !/\d/.test(totalText(unpriceable, "JPY")),
  `got: ${JSON.stringify(totalText(unpriceable, "JPY"))}`
);

// A genuine zero is a real measurement and must still be shown as one --
// otherwise every brand new user is told their total is unavailable.
const genuineZero = { total: 0, totalKnown: true, ratesStale: false };
check(
  "a GENUINE zero still renders as a number, not the em dash",
  totalText(genuineZero, "USD") !== UNKNOWN_TOTAL && /0/.test(totalText(genuineZero, "USD")),
  `got: ${JSON.stringify(totalText(genuineZero, "USD"))}`
);

// The two cases produce the SAME total. If the renderer ever branched on the
// number instead of the flag, this pair could not both hold.
check(
  "the two cases are distinguished despite carrying an identical total of 0",
  unpriceable.total === genuineZero.total &&
    totalText(unpriceable, "USD") !== totalText(genuineZero, "USD"),
  `unpriceable=${JSON.stringify(totalText(unpriceable, "USD"))} genuine=${JSON.stringify(totalText(genuineZero, "USD"))}`
);

// ---------------------------------------------------------------------------
// Absent is not true.
// ---------------------------------------------------------------------------
check(
  "a summary with NO totalKnown field is unknown, not assumed good",
  totalText({ total: 250 }, "USD") === UNKNOWN_TOTAL,
  `got: ${JSON.stringify(totalText({ total: 250 }, "USD"))}`
);
check(
  "a null summary is unknown",
  totalText(null, "USD") === UNKNOWN_TOTAL
);
check(
  "an undefined summary is unknown",
  totalText(undefined, "USD") === UNKNOWN_TOTAL
);
check(
  "totalKnown must be the boolean true -- a truthy 1 does not qualify",
  !totalIsKnown({ total: 5, totalKnown: 1 })
);

// ---------------------------------------------------------------------------
// A flag saying "known" is not enough -- there must actually be a total.
// formatBalance coerces a non-finite value to 0, so these would each render a
// confident zero and reintroduce the very bug this module removes.
// ---------------------------------------------------------------------------
check(
  "totalKnown:true with NO total renders the em dash, not a zero",
  totalText({ totalKnown: true }, "USD") === UNKNOWN_TOTAL,
  `got: ${JSON.stringify(totalText({ totalKnown: true }, "USD"))}`
);
check(
  "totalKnown:true with NaN renders the em dash",
  totalText({ totalKnown: true, total: NaN }, "USD") === UNKNOWN_TOTAL,
  `got: ${JSON.stringify(totalText({ totalKnown: true, total: NaN }, "USD"))}`
);
check(
  "totalKnown:true with Infinity renders the em dash",
  totalText({ totalKnown: true, total: Infinity }, "USD") === UNKNOWN_TOTAL,
  `got: ${JSON.stringify(totalText({ totalKnown: true, total: Infinity }, "USD"))}`
);
check(
  "totalKnown:true with a STRING total renders the em dash",
  totalText({ totalKnown: true, total: "250" }, "USD") === UNKNOWN_TOTAL,
  `got: ${JSON.stringify(totalText({ totalKnown: true, total: "250" }, "USD"))}`
);
check(
  "but a real 0 is finite and still renders as a number",
  totalText({ totalKnown: true, total: 0 }, "USD") !== UNKNOWN_TOTAL
);

// ---------------------------------------------------------------------------
// A known total is formatted normally.
// ---------------------------------------------------------------------------
const priced = { total: 250, totalKnown: true, ratesStale: false };
check(
  "a known total renders the real figure",
  /250/.test(totalText(priced, "USD")),
  `got: ${JSON.stringify(totalText(priced, "USD"))}`
);

// ---------------------------------------------------------------------------
// The caption has to explain the em dash, or it reads as a bug.
// ---------------------------------------------------------------------------
check(
  "the unknown caption says the balances could not be priced",
  /could not be priced/i.test(totalCaption(unpriceable)),
  `got: ${JSON.stringify(totalCaption(unpriceable))}`
);
check(
  "the unknown caption does NOT reuse 'stale' -- stale means slightly old, not absent",
  !/stale/i.test(totalCaption(unpriceable)),
  `got: ${JSON.stringify(totalCaption(unpriceable))}`
);
check(
  "a known-but-stale total keeps the weaker stale wording",
  /stale/i.test(totalCaption({ total: 250, totalKnown: true, ratesStale: true })),
  `got: ${JSON.stringify(totalCaption({ total: 250, totalKnown: true, ratesStale: true }))}`
);
check(
  "a known, fresh total gets the plain caption",
  totalCaption(priced) === "Across convertible services",
  `got: ${JSON.stringify(totalCaption(priced))}`
);

console.log(`${checks - failures}/${checks} total-render checks passed`);
if (failures) process.exit(1);
