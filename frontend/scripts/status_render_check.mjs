#!/usr/bin/env node
// Browser-free checks on the two service-row status decorations.
// CashPilot-Desktop-tft.
//
// Both carry the same rule, which is the one this app keeps turning on:
//
//   NOT MEASURED is not the same claim as MEASURED ZERO.
//
// A service with no health entry has not been scored yet. Rendering a red
// "0/100" against it invents an accusation. A Mysterium blob that is missing or
// unparseable means we could not read the provider's answer; rendering
// "0.0000 MYST" turns our failure into their loss.
//
// Both must render NOTHING in those cases, and nothing is a specific, testable
// output: the empty string.
//
//   node scripts/status_render_check.mjs      # against ./.harness-build

import { renderHealthBadge } from "../.harness-build/render/health.js";
import { renderMystNodes } from "../.harness-build/render/myst.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function health(overrides = {}) {
  return { score: 92, uptimePercent: 99.2, restarts: 1, crashes: 0, stops: 0, ...overrides };
}

// ---------------------------------------------------------------------------
// The health pill.
// ---------------------------------------------------------------------------
check(
  "THE RULE: a service with NO health entry renders nothing at all",
  renderHealthBadge(undefined) === "",
  `got: ${JSON.stringify(renderHealthBadge(undefined))}`
);
check(
  "and it is genuinely empty, not a blank badge element",
  !renderHealthBadge(undefined).includes("<span"),
  renderHealthBadge(undefined)
);

const scored = renderHealthBadge(health());
check("CONTROL: a scored service DOES render", scored.includes("<span"), scored);
check("a healthy score is shown with its uptime", scored.includes("92") && scored.includes("99"), scored);

// A measured zero is a real finding and must NOT be suppressed -- the opposite
// error to the rule above, and just as wrong.
const measuredZero = renderHealthBadge(health({ score: 0, uptimePercent: 0 }));
check(
  "a MEASURED zero still renders -- suppressing it would hide a dead service",
  measuredZero.includes("<span"),
  measuredZero
);

const unstable = renderHealthBadge(health({ crashes: 4 }));
check("a repeatedly crashing service is labelled unstable", unstable.includes("unstable"), unstable);
check("and the crash count is visible, not only in the tooltip", /4 crashes/.test(unstable), unstable);
check(
  "one crash is singular",
  /1 crash(?!es)/.test(renderHealthBadge(health({ crashes: 1 }))),
  renderHealthBadge(health({ crashes: 1 }))
);
/** The VISIBLE pill text, excluding the tooltip. */
function label(html) {
  const m = html.match(/>([^<>]*)<\/span>/);
  return m ? m[1] : "";
}
// The tooltip deliberately lists every raw count, including "0 crashes" -- that
// is its job. The check is that the pill a user READS stays quiet when there is
// nothing to report.
check(
  "a clean service mentions no crashes in the VISIBLE label",
  !label(scored).includes("crash"),
  `label was: ${label(scored)}`
);
check(
  "CONTROL: the tooltip does still carry the raw counts",
  scored.includes("0 crashes"),
  scored
);

// Colour must track the score, since that is the signal read at a glance.
check("a low score uses the error tone", renderHealthBadge(health({ score: 20 })).includes("--error"), "");
check("a middling score uses the warning tone", renderHealthBadge(health({ score: 65 })).includes("--warning"), "");
check("a high score uses the success tone", scored.includes("--success"), scored);
check(
  "an unstable service is red even with a high score -- crashes outrank the number",
  renderHealthBadge(health({ score: 95, crashes: 5 })).includes("--error"),
  ""
);

// ---------------------------------------------------------------------------
// The Mysterium node list, at the parsing boundary.
// ---------------------------------------------------------------------------
const NODES = JSON.stringify([
  { name: "attic", identity: "0xabcdef1234567890abcd", online: true, earnings30dMyst: 1.5, lifetimeMyst: 12.25 },
]);

for (const [caseName, input] of [
  ["undefined", undefined],
  ["an empty string", ""],
  ["unparseable JSON", "{not json"],
  ["a JSON object rather than an array", '{"nodes":[]}'],
  ["an empty array", "[]"],
]) {
  check(`THE RULE: ${caseName} renders nothing`, renderMystNodes(input) === "", `got: ${JSON.stringify(renderMystNodes(input))}`);
}

const rendered = renderMystNodes(NODES);
check("CONTROL: a real blob DOES render -- else every case above is vacuous", rendered.includes("attic"), rendered);
check("the 30-day figure is shown", rendered.includes("1.5000 MYST"), rendered);
check("the lifetime figure is shown", rendered.includes("12.2500 MYST"), rendered);
check("the header appears once there is something to head", rendered.includes("Per-node earnings"), rendered);

const unnamed = renderMystNodes(
  JSON.stringify([{ name: "", identity: "0xabcdef1234567890abcd", online: false, earnings30dMyst: 0, lifetimeMyst: 0 }])
);
check("an unnamed node falls back to a shortened identity", /0xabcd…abcd/.test(unnamed), unnamed);
check(
  "a node with a genuinely zero balance still renders -- that is a reading, not a gap",
  unnamed.includes("0.0000 MYST"),
  unnamed
);

const nonFinite = renderMystNodes(
  JSON.stringify([{ name: "n", identity: "0xaaaaaaaaaaaaaaaaaaaa", online: true, earnings30dMyst: null, lifetimeMyst: "x" }])
);
check("a non-numeric amount does not render NaN", !nonFinite.includes("NaN"), nonFinite);

const hostileNode = renderMystNodes(
  JSON.stringify([{ name: "<img src=x onerror=alert(1)>", identity: "0xaaaaaaaaaaaaaaaaaaaa", online: true, earnings30dMyst: 1, lifetimeMyst: 1 }])
);
check("a hostile node name is escaped", !hostileNode.includes("<img src=x"), hostileNode.slice(0, 160));

if (failures) {
  console.error(`\n${failures} of ${checks} status-decoration checks failed`);
  process.exit(1);
}
console.log(`${checks}/${checks} checks passed`);
console.log("status decoration render check passed");
