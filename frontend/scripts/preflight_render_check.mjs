#!/usr/bin/env node
// Browser-free checks on the pre-deploy reality check.
//
// The panel exists to tell someone, before they deploy, what will stop this
// service earning on their machine. Three things have to hold, and each is the
// opposite of a failure that would be worse than showing nothing:
//
//   1. IT NEVER BLOCKS. The worst verdict is still a panel you read and then
//      deploy past. It renders no button and disables nothing.
//   2. WHAT WAS NOT CHECKED IS ALWAYS SHOWN. A clean result that hides the list
//      reads as a promise about things nobody looked at.
//   3. NO ANSWER RENDERS NOTHING. A panel drawn before the check has answered is
//      a claim we have not earned.
//
//   node scripts/preflight_render_check.mjs      # against ./.harness-build

import { minerFee, renderPreflight } from "../.harness-build/render/preflight.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function report(overrides = {}) {
  return {
    slug: "proxylite",
    name: "ProxyLite",
    verdict: "will_earn_nothing",
    summary: "ProxyLite will most likely earn nothing here. You can deploy it anyway.",
    findings: [
      {
        verdict: "will_earn_nothing",
        message: "This machine is 64-bit ARM (arm64) and ProxyLite publishes no build for it, only x86-64.",
      },
    ],
    notChecked: ["what kind of internet connection you have (home or datacentre)", "your connection speed"],
    machineArch: "arm64",
    blocking: false,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// RULE 3: no answer renders nothing.
// ---------------------------------------------------------------------------
check("no report yet renders nothing at all", renderPreflight(null) === "", JSON.stringify(renderPreflight(null)));
check("and neither does undefined", renderPreflight(undefined) === "", JSON.stringify(renderPreflight(undefined)));
check(
  "a report with no summary is not an answer either",
  renderPreflight(report({ summary: "" })) === "",
  renderPreflight(report({ summary: "" }))
);

// ---------------------------------------------------------------------------
// CONTROL: a real report DOES render, or every check above proves nothing.
// ---------------------------------------------------------------------------
const worst = renderPreflight(report());
check("CONTROL: a real report renders", worst.includes("<section"), worst);
check("the summary is shown", worst.includes("will most likely earn nothing here"), worst);
check("the finding is shown", worst.includes("publishes no build for it"), worst);
check("the verdict is named in plain words", worst.includes("will earn nothing here"), worst);

// ---------------------------------------------------------------------------
// RULE 1: it never blocks.
// ---------------------------------------------------------------------------
check("the panel renders no button of its own", !worst.includes("<button"), worst);
check("and disables nothing", !worst.includes("disabled"), worst);
check(
  "the worst verdict still invites the deploy rather than forbidding it",
  worst.includes("You can deploy it anyway"),
  worst
);

// ---------------------------------------------------------------------------
// RULE 2: what was not checked is always shown.
// ---------------------------------------------------------------------------
check("the not-checked list is shown on the worst verdict", worst.includes("Not checked:"), worst);
const clean = renderPreflight(
  report({
    verdict: "looks_fine",
    summary: "Nothing stands out — Bitping should work normally here.",
    findings: [],
    notChecked: ["your connection speed"],
  })
);
check("a CLEAN report still renders", clean.includes("<section"), clean);
check("a clean report still lists what was not checked", clean.includes("Not checked: your connection speed"), clean);
check("and it says so without inventing findings", !clean.includes("<li"), clean);

// ---------------------------------------------------------------------------
// The rest: ordering, tone, escaping, and the shapes Go can actually send.
// ---------------------------------------------------------------------------
const twoFindings = renderPreflight(
  report({
    verdict: "will_earn_nothing",
    findings: [
      { verdict: "will_earn_nothing", message: "FIRST: the provider forbids containers." },
      { verdict: "check_these", message: "SECOND: this needs a home internet connection." },
    ],
  })
);
check(
  "findings keep the order the backend chose, worst first",
  twoFindings.indexOf("FIRST") < twoFindings.indexOf("SECOND"),
  twoFindings
);

const milder = renderPreflight(report({ verdict: "check_these", summary: "Storj should work, as long as..." }));
check("a milder verdict reads as 'check these', not as a loss", milder.includes("check these"), milder);
check("and it does not claim the service earns nothing", !milder.includes("will earn nothing"), milder);

// Catalog notes are author-written text that lands in an innerHTML sink.
const hostile = renderPreflight(
  report({
    summary: "<script>alert(1)</script>",
    findings: [{ verdict: "check_these", message: "<img src=x onerror=alert(1)>" }],
    notChecked: ["<b>nope</b>"],
  })
);
check("a summary is escaped, never executed", !hostile.includes("<script"), hostile);
check("a finding is escaped too", !hostile.includes("<img"), hostile);
check("and so is the not-checked list", !hostile.includes("<b>"), hostile);

// Go marshals an empty slice as [] and a nil slice as null; both reach the UI.
const nulls = renderPreflight(report({ findings: null, notChecked: null }));
check("a report with null lists still renders its summary", nulls.includes("<section"), nulls);
check("with no findings list", !nulls.includes("<li"), nulls);
check("and no empty 'Not checked:' line", !nulls.includes("Not checked:"), nulls);

// A verdict this build has never seen must not blank the panel: the summary is
// the part the user needs.
const unknown = renderPreflight(report({ verdict: "something_new" }));
check("an unrecognised verdict still shows the summary", unknown.includes("earn nothing here. You can deploy"), unknown);

// ---------------------------------------------------------------------------
// THE MINER FEE. A miner gives part of its hashing time to its developers. The
// user reads that before deploying, wherever the miner will run. A fee the
// catalog does not state is "not stated", never 0%.
// ---------------------------------------------------------------------------
const hostingNote = "This machine reports a hosting connection. Check your provider's terms before mining.";
function miner(mining) {
  return {
    name: "XMRig (Monero CPU mining)",
    slug: "xmrig",
    category: "mining",
    // Go never sends requirements.hosting_note (json:"-"). Even if a build did,
    // it must not render: Desktop cannot see what connection this PC is on.
    requirements: { hostingNote },
    mining,
  };
}
const xmrig = minerFee(miner({
  devFeePercent: 1,
  devFeeSource: "https://github.com/xmrig/xmrig/blob/v6.27.0/src/donate.h",
  poolFeeNote: "MoneroOcean, the default pool, charges a 0% mining fee.",
}));
check("a miner shows its fee as a share of hashing time", xmrig.includes("Miner fee: 1% of hashing time"), xmrig);
check("with the pool's own fee note", xmrig.includes("MoneroOcean, the default pool"), xmrig);
check(
  "and a button to the source that states the fee",
  xmrig.includes('data-url="https://github.com/xmrig/xmrig/blob/v6.27.0/src/donate.h"'),
  xmrig
);
check("the hosting note is never shown", !xmrig.includes("hosting connection"), xmrig);
check("the fee panel offers no Deploy-blocking control", !xmrig.includes("disabled") && !xmrig.includes("data-preflight-blocked"), xmrig);

const unstated = minerFee(miner({ devFeePercent: null, devFeeSource: "", poolFeeNote: "" }));
check("a fee the catalog does not state is said to be not stated", unstated.includes("not stated"), unstated);
check("and is never shown as 0%", !unstated.includes("0%"), unstated);
check("with no source button and no empty pool note", !unstated.includes("data-url") && !unstated.includes('<p class="preflight-summary"></p>'), unstated);

const zero = minerFee(miner({ devFeePercent: 0, devFeeSource: "https://example.com/fee", poolFeeNote: "" }));
check("a stated 0% fee is a real fee and shows as 0%", zero.includes("Miner fee: 0% of hashing time"), zero);

const plain = minerFee(miner({ devFeePercent: 1, devFeeSource: "javascript:alert(1)", poolFeeNote: "<img src=x onerror=alert(1)>" }));
check("a source that is not https:// gets no button", !plain.includes("data-url"), plain);
check("the pool note is escaped", !plain.includes("<img"), plain);

check("a service that is not a miner shows no fee", minerFee({ name: "Honeygain", slug: "honeygain", mining: null }) === "");
check("nor does a missing service", minerFee(undefined) === "");

if (failures > 0) {
  console.error(`\n${failures} of ${checks} preflight render checks FAILED`);
  process.exit(1);
}
console.log(`preflight render checks: ${checks} passed`);
