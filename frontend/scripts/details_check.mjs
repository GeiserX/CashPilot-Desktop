#!/usr/bin/env node
// Browser-free checks on what a service card says about a provider.
//
// THE RULE that pays the bills: the signup button opens the catalog's signup_url,
// character for character. That URL carries the referral code. A link that gets
// trimmed, canonicalised, or replaced by the provider's bare front page still opens
// a working signup page, still lets the user create an account, and earns nothing —
// silently, forever. The Go half of this rule is referral_signup_test.go, which
// proves the URL reaches the frontend intact; this half proves the button does not
// lose it on the last hop.
//
// The other rule is about not stating things we do not know. An undocumented
// minimum renders as an em dash, never 0 ("cash out today"), and a balance is never
// counted down against a minimum in a different unit — Storj reports USD while its
// catalog minimum is in STORJ, and subtracting one from the other used to produce a
// confident "0.50 to go" out of dollars and tokens.
//
//   node scripts/details_check.mjs      # against ./.harness-build

import {
  UNKNOWN,
  composeExportControl,
  credentialHint,
  disclosureFacts,
  minimumIn,
  payoutFacts,
  signupButton,
} from "../.harness-build/render/details.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function valueOf(facts, label) {
  return facts.find((fact) => fact.label === label)?.value;
}

const honeygain = {
  slug: "honeygain",
  name: "Honeygain",
  website: "https://www.honeygain.com",
  referral: {signupUrl: "https://dashboard.honeygain.com/ref/SERGIB4014", code: "SERGIB4014"},
  collector: {credentialHint: "Use your account email and password (same as <a href='https://dashboard.honeygain.com'>dashboard.honeygain.com</a>)."},
  payment: {methods: ["paypal", "crypto"], frequency: "On request", currency: "USD"},
  cashout: {minAmount: 20, currency: "USD"},
  disclosure: {sells: "Residential bandwidth", thirdPartyTraffic: "Yes. Other people browse through your connection."},
  manualOnly: false,
};

// ---------------------------------------------------------------------------
// THE RULE: the signup button uses signup_url verbatim.
// ---------------------------------------------------------------------------
const button = signupButton(honeygain);

check(
  "THE RULE: the signup button carries the catalog's signup URL character for character",
  button.includes(`data-url="${honeygain.referral.signupUrl}"`),
  button,
);

check(
  "the button does not fall back to the bare website while a referral link exists",
  !button.includes(honeygain.website),
  button,
);

check(
  "the referral code survives into the button",
  button.includes(honeygain.referral.code),
  button,
);

// Codes sit in two different places across the catalog — a path segment
// (/ref/CODE) and a query value (?aff=CODE) — and a URL that is "tidied up" loses
// only the second kind. Both shapes are checked, or a stripped query string looks
// fine against a path-segment fixture.
const traffmonetizer = {
  ...honeygain,
  slug: "traffmonetizer",
  name: "Traffmonetizer",
  website: "https://traffmonetizer.com",
  referral: {signupUrl: "https://traffmonetizer.com/?aff=2111758", code: "2111758"},
};

check(
  "THE RULE, for a code that lives in the query string: the URL reaches the button whole",
  signupButton(traffmonetizer).includes(`data-url="${traffmonetizer.referral.signupUrl}"`),
  signupButton(traffmonetizer),
);

check(
  "a code in the query string is not lost to a canonicalised link",
  signupButton(traffmonetizer).includes("aff=2111758"),
  signupButton(traffmonetizer),
);

check(
  "a service with no referral link falls back to the provider's own site",
  signupButton({...honeygain, referral: {signupUrl: "", code: ""}}).includes(`data-url="${honeygain.website}"`),
);

check(
  "a service with neither renders no button at all, rather than a dead one",
  signupButton({...honeygain, referral: {signupUrl: "", code: ""}, website: ""}) === "",
);

// ---------------------------------------------------------------------------
// The credential hint: where this provider hides the token.
// ---------------------------------------------------------------------------
const hint = credentialHint(honeygain);

check(
  "the hint's words reach the card",
  hint.includes("Use your account email and password") && hint.includes("dashboard.honeygain.com"),
  hint,
);

check(
  "the catalog's markup is not injected into the app",
  !hint.includes("<a ") && !hint.includes("href="),
  hint,
);

check("a service with no hint renders nothing", credentialHint({...honeygain, collector: {credentialHint: ""}}) === "");

// ---------------------------------------------------------------------------
// Getting paid.
// ---------------------------------------------------------------------------
const paid = payoutFacts(honeygain);

check("the minimum is shown with the unit it is counted in", valueOf(paid, "Minimum to cash out") === "20 USD", JSON.stringify(paid));
check("the payment methods are shown", valueOf(paid, "Paid by") === "paypal, crypto");
check("the payout frequency is shown", valueOf(paid, "Paid out") === "On request");

const undocumented = payoutFacts({
  ...honeygain,
  payment: {methods: [], frequency: "", currency: ""},
  cashout: {minAmount: 0, currency: ""},
});

check(
  "an undocumented minimum renders as an em dash, never 0",
  valueOf(undocumented, "Minimum to cash out") === UNKNOWN,
  JSON.stringify(undocumented),
);
check("undocumented payment methods render as an em dash", valueOf(undocumented, "Paid by") === UNKNOWN);
check("an undocumented frequency renders as an em dash", valueOf(undocumented, "Paid out") === UNKNOWN);

// ---------------------------------------------------------------------------
// The unit rule: a balance is only counted down against a minimum in its own unit.
// ---------------------------------------------------------------------------
const storj = {
  ...honeygain,
  slug: "storj",
  cashout: {minAmount: 4, currency: "STORJ"},
  payment: {methods: ["crypto"], frequency: "Monthly", currency: "STORJ"},
};

check(
  "THE RULE: a USD balance is never counted down against a token minimum",
  minimumIn(storj, "USD") === null,
  `minimumIn = ${minimumIn(storj, "USD")}`,
);

check(
  "and the card says nothing about how far away the payout is, rather than something wrong",
  valueOf(payoutFacts(storj, {amount: 3.5, currency: "USD"}), "Still to go") === undefined,
  JSON.stringify(payoutFacts(storj, {amount: 3.5, currency: "USD"})),
);

check(
  "the minimum still shows in its own unit",
  valueOf(payoutFacts(storj, {amount: 3.5, currency: "USD"}), "Minimum to cash out") === "4 STORJ",
);

check(
  "matching units do count down",
  valueOf(payoutFacts(honeygain, {amount: 12.5, currency: "USD"}), "Still to go") === "7.5 USD",
  JSON.stringify(payoutFacts(honeygain, {amount: 12.5, currency: "USD"})),
);

check(
  "an entry with no declared unit is taken at face value, which is what the catalog means by omitting it",
  minimumIn({...honeygain, cashout: {minAmount: 20, currency: ""}}, "USD") === 20,
);

check(
  "a balance past the minimum says it can be cashed out now",
  valueOf(payoutFacts(honeygain, {amount: 25, currency: "USD"}), "Still to go") === "Nothing — this can be cashed out now",
);

check("an undocumented minimum is never compared against anything", minimumIn(undocumentedService(), "USD") === null);

function undocumentedService() {
  return {...honeygain, cashout: {minAmount: 0, currency: ""}};
}

// ---------------------------------------------------------------------------
// What it does with your machine.
// ---------------------------------------------------------------------------
const disclosure = disclosureFacts(honeygain);

check("the disclosure the catalog documents is shown", valueOf(disclosure, "What it sells") === "Residential bandwidth");
check(
  "an undocumented disclosure renders as an em dash, not a reassuring blank",
  valueOf(disclosure, "What it collects") === UNKNOWN && valueOf(disclosure, "Account rules") === UNKNOWN,
  JSON.stringify(disclosure),
);

// ---------------------------------------------------------------------------
// The compose export control.
// ---------------------------------------------------------------------------
const control = composeExportControl(honeygain);

check("the export button names the service it exports", control.includes(`data-compose-export="honeygain"`), control);
check(
  "every architecture a catalog image can be built for is offered, plus 'this machine'",
  ['value=""', 'value="amd64"', 'value="arm64"', 'value="arm"'].every((option) => control.includes(option)),
  control,
);
check(
  "a manually tracked service offers no compose export, because there is no container to export",
  composeExportControl({...honeygain, manualOnly: true}) === "",
);

console.log(`${checks - failures}/${checks} checks passed`);
if (failures > 0) process.exit(1);
