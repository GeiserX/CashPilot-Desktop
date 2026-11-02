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

import { allTimeNote, totalText, totalCaption, totalIsKnown, UNKNOWN_TOTAL } from "../.harness-build/render/total.js";

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

// ---------------------------------------------------------------------------
// Earned all time: the balance drops on every cash-out, so once one is recorded
// the card says what the services made. Port of GeiserX/CashPilot#487.
// ---------------------------------------------------------------------------
const cashedOut = { total: 120, totalKnown: true, paidOut: 37.54, earnedAllTime: 157.54, unpricedPayouts: 0 };
const cashedNote = allTimeNote(cashedOut, "USD");
check(
  "a recorded cash-out shows what was earned all time and what was cashed out",
  cashedNote.startsWith("Earned all time") && cashedNote.includes("157.54") && cashedNote.includes("37.54") && cashedNote.includes("cashed out"),
  `got: ${JSON.stringify(cashedNote)}`
);
check(
  "nothing ever cashed out shows no line",
  allTimeNote({ total: 120, totalKnown: true, paidOut: 0, earnedAllTime: 120, unpricedPayouts: 0 }, "USD") === ""
);
check(
  "a summary from before the ledger (no paidOut field) shows no line, not a zero",
  allTimeNote({ total: 120, totalKnown: true }, "USD") === "" && allTimeNote(null, "USD") === ""
);
const unreadable = allTimeNote({ total: 120, totalKnown: true, paidOut: null, earnedAllTime: null, unpricedPayouts: 0 }, "USD");
check(
  "a ledger that could not be read says so, never a zero",
  unreadable === "Earned all time: cash-outs could not be read",
  `got: ${JSON.stringify(unreadable)}`
);
const unknownTotal = allTimeNote({ total: 0, totalKnown: false, paidOut: 10, earnedAllTime: null, unpricedPayouts: 0 }, "USD");
check(
  "an unknown balance total keeps the all-time figure an em dash",
  unknownTotal.startsWith(`Earned all time ${UNKNOWN_TOTAL} (`) && unknownTotal.includes("10.00 cashed out"),
  `got: ${JSON.stringify(unknownTotal)}`
);
const onlyUnpriced = allTimeNote({ total: 5, totalKnown: true, paidOut: 0, earnedAllTime: 5, unpricedPayouts: 2 }, "USD");
check(
  "cash-outs with no rate are named, not silently dropped",
  onlyUnpriced.includes("plus 2 cash-outs with no rate"),
  `got: ${JSON.stringify(onlyUnpriced)}`
);

console.log(`${checks - failures}/${checks} total-render checks passed`);
if (failures) process.exit(1);
