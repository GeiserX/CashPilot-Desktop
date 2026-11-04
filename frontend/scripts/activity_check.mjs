#!/usr/bin/env node
// Browser-free checks on the Activity page renderer (src/render/activity.ts).
//
// The rules: a count with no reading renders as an em dash, never 0; a
// host-network earner matched by its cgroup says what it cannot see; one that
// could not be told apart says why and what fixes it; and two containers whose
// names differ only in punctuation never share a detail box.
//
//   node scripts/activity_check.mjs      # against ./.harness-build

import { domId, num, renderActivityPage, renderActivityRow, statusChip, UNKNOWN } from "../.harness-build/render/activity.js";

let failures = 0;
let checks = 0;
function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function row(extra) {
  return {
    container: "cashpilot-honeygain", status: "ok", reason: "", members: ["cashpilot-honeygain"], networkMode: "bridge",
    listenPorts: [], lastWindow: 1, estMax: 3, remotesTotal: 4, truncated: false, opensLast: 7, attemptFailsLast: 0,
    series: [], topPorts: [{ value: "tcp/443", sightings: 4 }], topAsn: [], topCc: [], classes: { public: 4 }, dirs: { out: 4 }, alerts: 0,
    ...extra,
  };
}

function cells(html) {
  return [...html.matchAll(/<td>(.*?)<\/td>/g)].map((m) => m[1]);
}

// THE RULE: unknown is a dash, a measured zero is a zero.
check("null is the em dash", num(null) === UNKNOWN && num(undefined) === UNKNOWN);
check("a measured zero stays 0", num(0) === "0");
const first = cells(renderActivityRow(row({ opensLast: null, attemptFailsLast: null }), null, false));
check("a first window shows a dash for opens and failures, never 0", first[2] === UNKNOWN && first[4] === UNKNOWN, JSON.stringify(first));
const zero = cells(renderActivityRow(row({ opensLast: 0 }), null, false));
check("a counted zero renders 0", zero[2] === "0", JSON.stringify(zero));

// Host network, matched by cgroup.
const hostRow = row({ container: "cashpilot-mysterium", networkMode: "host", reason: "host_cgroup", opensLast: 12, attemptFailsLast: 3 });
const host = renderActivityRow(hostRow, null, false);
check("the host_cgroup chip says it matched by cgroup", host.includes("matched by cgroup"), host);
check("and that closed connections are not seen", host.includes("TIME_WAIT"), host);
check("and carries the forwarded-traffic note", host.includes("forwarded by the kernel"), host);
const hostCells = cells(host);
check("host-network counters are machine-wide, so they render as the dash even if a number arrives", hostCells[2] === UNKNOWN && hostCells[4] === UNKNOWN, JSON.stringify(hostCells));
check("its destinations still count", hostCells[5] === "4", JSON.stringify(hostCells));

// Host network, not attributable.
const v1 = statusChip(row({ status: "host_network", reason: "cgroup_v1", networkMode: "host" }));
check("cgroup v1 says it cannot be told apart and what fixes it", v1.includes("cannot be told apart") && v1.includes("cgroup v2"), v1);
const plain = statusChip(row({ status: "host_network", reason: "", networkMode: "host" }));
check("a host_network row with no reason still renders", plain.includes("host network"), plain);

// Other statuses.
check("native programs are named", statusChip(row({ status: "native" })).includes("native program"));
check("a failed helper shows its reason", statusChip(row({ status: "probe_failed", reason: "timeout" })).includes("timeout"));
const shared = statusChip(row({ reason: "shared_namespace", members: ["a", "b"] }));
check("a shared namespace names who uses it", shared.includes("used by a, b"), shared);

// Detail-box ids are injective.
check("a.b and a_b get different boxes", domId("a.b") !== domId("a_b"), `${domId("a.b")} ${domId("a_b")}`);
check("a literal escape sequence cannot collide with the character it encodes", domId("a_002eb") !== domId("a.b"), `${domId("a_002eb")} ${domId("a.b")}`);
check("ids are valid tokens", /^[A-Za-z0-9_-]+$/.test(domId("x y/z_é")), domId("x y/z_é"));
check("letters, digits and dashes pass through", domId("cashpilot-myst2") === "activity-detail-cashpilot-myst2");

// Names are escaped.
const evil = renderActivityRow(row({ container: '<img src=x onerror=alert(1)>' }), null, true);
check("container names are escaped", !evil.includes("<img"), evil);

// Page states.
const off = renderActivityPage({ enabled: false, days: 1, rawHours: 24, lastWindow: 0, round: { at: 0, workerStatus: "", leftoverProbes: [] }, networks: { present: false, ageSeconds: null, rows: null }, networksEnabled: true, containers: [], alerts: [] }, {});
check("the off page says so and has the switch unchecked", off.includes("Activity is off") && off.includes('id="activity-toggle">'), off);
const noImage = renderActivityPage({ enabled: true, days: 1, rawHours: 24, lastWindow: 0, round: { at: 5, workerStatus: "no_image", leftoverProbes: [] }, networks: { present: false, ageSeconds: null, rows: null }, networksEnabled: true, containers: [hostRow], alerts: [] }, { "cashpilot-mysterium": null });
check("a missing helper image is shown", noImage.includes("helper image could not be pulled"), noImage);
check("an open row renders its box while it loads", noImage.includes(`id="${domId("cashpilot-mysterium")}"`) && noImage.includes("Loading"), noImage);

console.log(`${checks - failures}/${checks} activity checks passed`);
if (failures) process.exit(1);
