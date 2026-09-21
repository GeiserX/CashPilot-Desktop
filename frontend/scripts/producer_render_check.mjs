#!/usr/bin/env node
// Browser-free checks on the producer badge: is this service actually EARNING?
//
// The rule it exists for is the mirror of the health pill's:
//
//   NOT CHECKED IS NOT THE SAME CLAIM AS CHECKED AND FINE.
//
// The status pill next to it says "running" in green for a container that has
// produced nothing for a month, because from the runtime's point of view nothing
// is wrong. So there is no green "earning" badge here at all: a verdict we could
// not reach must say so, and a verdict that found a problem must say what it was.
//
//   node scripts/producer_render_check.mjs      # against ./.harness-build

import { renderProducerBadge } from "../.harness-build/render/health.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

/** The VISIBLE badge text, excluding the tooltip. */
function label(html) {
  const m = html.match(/>([^<>]*)<\/span>/);
  return m ? m[1] : "";
}

/** The tooltip text. */
function tooltip(html) {
  const m = html.match(/title="([^"]*)"/);
  return m ? m[1] : "";
}

const failing = {
  slug: "mysterium",
  state: "failing",
  reasons: ["The node cannot run sudo inside its container, so every session fails at setup."],
};
const idle = { slug: "demo", state: "idle", reasons: ["Nobody is buying right now."] };
const notChecked = {
  slug: "bitping",
  state: "not-checked",
  reasons: ["This service declares no log signals, so its logs cannot tell us whether it is earning."],
};

// ---------------------------------------------------------------------------
// A running service that is not earning.
// ---------------------------------------------------------------------------
const bad = renderProducerBadge(failing, "running");
check("a running service with a failing verdict is badged", bad.includes("<span"), bad);
check("and the badge says it is not earning", /not earning/.test(label(bad)), label(bad));
check("and the reason is carried to the user", tooltip(bad).includes("cannot run sudo"), tooltip(bad));
check("a failure uses the error tone", bad.includes("--error"), bad);

const lazy = renderProducerBadge(idle, "running");
check("an idle verdict is also 'not earning' -- that is what the user cares about", /not earning/.test(label(lazy)), label(lazy));
check(
  "but idle is amber, not red: nobody buying is not something to go and fix",
  lazy.includes("--warning") && !lazy.includes("--error"),
  lazy
);

// ---------------------------------------------------------------------------
// THE RULE: unknown reads as "not checked", never as fine.
// ---------------------------------------------------------------------------
const unknown = renderProducerBadge(notChecked, "running");
check("an unchecked service still shows a badge -- silence would read as fine", unknown.includes("<span"), unknown);
check("it says it was not checked", /not checked/.test(label(unknown)), label(unknown));
check("it does not accuse the service of anything", !/not earning/.test(label(unknown)), label(unknown));
check("and it is not green", !unknown.includes("--success"), unknown);
check("it explains why it could not tell", tooltip(unknown).includes("no log signals"), tooltip(unknown));

// A state this frontend has never heard of is the same situation: we cannot read
// the answer, so we must not pretend we can.
const alien = renderProducerBadge({ slug: "x", state: "quantum", reasons: [] }, "running");
check("an unrecognised state falls back to 'not checked'", /not checked/.test(label(alien)), label(alien));
check("and it still carries a tooltip rather than an empty one", tooltip(alien).length > 0, alien);

// ---------------------------------------------------------------------------
// When there is nothing to say, say nothing.
// ---------------------------------------------------------------------------
check("no verdict at all renders nothing", renderProducerBadge(undefined, "running") === "",
  JSON.stringify(renderProducerBadge(undefined, "running")));
check("and it is genuinely empty, not a blank badge element",
  !renderProducerBadge(undefined, "running").includes("<span"), "");

for (const state of ["exited", "created", "paused", ""]) {
  check(
    `a container that is not up (${state || "state unknown"}) gets no earning verdict -- the status pill already said so`,
    renderProducerBadge(failing, state) === "",
    renderProducerBadge(failing, state)
  );
}
check("CONTROL: a restarting container DOES get one -- that is the restart-loop case",
  renderProducerBadge(failing, "restarting").includes("<span"),
  renderProducerBadge(failing, "restarting"));

// ---------------------------------------------------------------------------
// Robustness at the JSON boundary: reasons is null over the wire when Go sends
// an empty slice, and a reason is service-supplied text.
// ---------------------------------------------------------------------------
const noReasons = renderProducerBadge({ slug: "x", state: "failing", reasons: null }, "running");
check("a verdict with no reasons still renders, with fallback wording",
  noReasons.includes("<span") && tooltip(noReasons).length > 0, noReasons);

const hostile = renderProducerBadge(
  { slug: "x", state: "failing", reasons: ['"><img src=x onerror=alert(1)>'] },
  "running"
);
check("a hostile reason cannot break out of the title attribute",
  !hostile.includes("<img src=x") && !hostile.includes('"><'), hostile);

if (failures) {
  console.error(`\n${failures} of ${checks} producer-badge checks failed`);
  process.exit(1);
}
console.log(`${checks}/${checks} checks passed`);
console.log("producer badge render check passed");
