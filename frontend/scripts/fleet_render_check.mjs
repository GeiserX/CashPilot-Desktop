#!/usr/bin/env node
// Browser-free checks on the fleet panel's rendering. CashPilot-Desktop-tft.
//
// WHY THIS SHAPE
// --------------
// frontend/ had no test runner at all, and the rule that matters most in this
// app now lives here: a platform with NO reading must render as an em dash,
// never as 0.00. The Go side proves null survives to JSON, with negative
// controls. Nothing proved the render.
//
// It drives the REAL function against constructed state and asserts on the HTML
// it returns. It deliberately does NOT assert on the source text: the repository
// has been bitten repeatedly by tests that matched their own prose — a check
// that main.ts CONTAINS a null guard passes against a build where the guard is
// unreachable.
//
// Modelled on CashPilot's own browser-free harnesses (currency_check.mjs,
// fleet_staleness_check.mjs, ...), which are wired into CI the same way.
//
//   node scripts/fleet_render_check.mjs        # against ./.harness-build

import { renderFleetSection } from "../.harness-build/render/fleet.js";
import { escapeHtml, formatBalance, relativeTime } from "../.harness-build/render/format.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

/** A FleetView with sane defaults, overridable per case. */
function view(overrides = {}) {
  return {
    serverUrl: "http://cashpilot.local:8080",
    reportedAt: new Date(Date.now() - 12 * 60_000).toISOString(),
    windowDays: 30,
    currency: "USD",
    platforms: [],
    totalUsd: null,
    withoutReadings: [],
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// The rule this whole harness exists for.
// ---------------------------------------------------------------------------

{
  const html = renderFleetSection(
    view({
      platforms: [{ slug: "storj", usd: null, shared: false }],
      totalUsd: null,
      withoutReadings: ["storj"],
    }),
  );
  check("an unknown platform renders an em dash", html.includes("&mdash;"), html.slice(0, 400));
  check(
    "an unknown platform does NOT render a zero amount",
    !/\$\s*0\.00/.test(html),
    "a missing reading was shown as money",
  );
  check("an unknown TOTAL says so in words", html.includes("nothing collected yet"));
  check("the platforms with no reading are named", html.includes("storj"));
}

{
  // The mirror. A guard that renders everything as a dash is as useless as none:
  // a real, measured 0.00 must still print as money.
  const html = renderFleetSection(
    view({ platforms: [{ slug: "grass", usd: 0, shared: false }], totalUsd: 0 }),
  );
  check("a MEASURED zero renders as money, not a dash", /0\.00/.test(html), html.slice(0, 400));
  check("a measured zero is not called unknown", !html.includes("nothing collected yet"));
}

{
  const html = renderFleetSection(
    view({ platforms: [{ slug: "grass", usd: 4.25, shared: false }], totalUsd: 4.25 }),
  );
  check("a known amount is rendered", /4\.25/.test(html), html.slice(0, 400));
  check("a known total is labelled as partial", html.includes("known platforms only"));
}

// ---------------------------------------------------------------------------
// Per-platform, not per-device.
// ---------------------------------------------------------------------------

{
  const html = renderFleetSection(
    view({
      platforms: [
        { slug: "grass", usd: 4, shared: true },
        { slug: "honeygain", usd: 1, shared: false },
      ],
      totalUsd: 5,
    }),
  );
  check("a shared platform is marked", html.includes(">shared<"), html.slice(0, 600));
  check("a shared platform gets the explanatory class", html.includes("earning-chip shared"));
  check(
    "the header explains what shared means for the number",
    html.includes("not this machine's"),
    "the panel lets the figure imply this machine earned it",
  );
  check(
    "a platform on ONE worker is not marked shared",
    (html.match(/>shared</g) || []).length === 1,
    "an unshared platform was marked shared",
  );
}

{
  const html = renderFleetSection(view({ platforms: [{ slug: "grass", usd: 4, shared: false }], totalUsd: 4 }));
  check("no shared sentence when nothing is shared", !html.includes("more than one machine, so"));
}

// ---------------------------------------------------------------------------
// Escaping. serverUrl and the slugs come from a server the user typed the
// address of, and this string goes into innerHTML.
// ---------------------------------------------------------------------------

{
  const html = renderFleetSection(
    view({
      serverUrl: '"><img src=x onerror=alert(1)>',
      platforms: [{ slug: "<script>alert(2)</script>", usd: 1, shared: false }],
      withoutReadings: ["<b>bold</b>"],
    }),
  );
  check("the server URL is escaped", !html.includes("<img src=x"), html.slice(0, 400));
  check("a platform slug is escaped", !html.includes("<script>alert(2)"));
  check("the no-reading list is escaped", !html.includes("<b>bold</b>"));
  check("the escaped forms are present", html.includes("&lt;script&gt;"));
}

// ---------------------------------------------------------------------------
// Freshness. The heartbeat is on a timer, so a figure with no age reads as
// current when it may be an hour old.
// ---------------------------------------------------------------------------

{
  const now = Date.UTC(2026, 0, 2, 12, 0, 0);
  check("minutes", relativeTime(new Date(now - 12 * 60_000).toISOString(), now) === "12 minutes ago");
  check("singular minute", relativeTime(new Date(now - 60_000).toISOString(), now) === "1 minute ago");
  check("just now", relativeTime(new Date(now - 5_000).toISOString(), now) === "just now");
  check("hours", relativeTime(new Date(now - 3 * 3600_000).toISOString(), now) === "3 hours ago");
  check("days", relativeTime(new Date(now - 50 * 3600_000).toISOString(), now) === "2 days ago");
  check(
    "an unparseable stamp yields nothing rather than 'Invalid Date'",
    relativeTime("not a date", now) === "",
  );

  const html = renderFleetSection(view({ reportedAt: "not a date" }));
  check("an unparseable stamp is simply omitted", !html.includes("Invalid"), html.slice(0, 400));
}

// ---------------------------------------------------------------------------
// Empty and absent shapes. The Go side sends null, not [], for "none".
// ---------------------------------------------------------------------------

{
  const html = renderFleetSection(view({ platforms: null, withoutReadings: null }));
  check("null lists do not throw", typeof html === "string");
  check("an empty panel says why", html.includes("no figures for the platforms on this machine yet"));
}

// ---------------------------------------------------------------------------
// The helpers, since they are now shared and their edge cases are load-bearing.
// ---------------------------------------------------------------------------

check("escapeHtml handles null", escapeHtml(null) === "");
check("escapeHtml escapes quotes", escapeHtml(`"'`) === "&quot;&#039;");
check(
  "formatBalance falls back for a reward token",
  formatBalance(12.5, "GRASS") === "12.50 GRASS",
  formatBalance(12.5, "GRASS"),
);
check("formatBalance strips a hostile code", !formatBalance(1, '"><b>').includes("<b>"));
check("formatBalance survives a non-finite amount", formatBalance(NaN, "USD").includes("0.00"));

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures) {
  console.error(`\n${failures} FAILED`);
  process.exit(1);
}
console.log("fleet render check passed");
