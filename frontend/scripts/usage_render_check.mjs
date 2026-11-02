#!/usr/bin/env node
// Browser-free checks on the resource-use graphs under "Earned per day"
// (src/render/usage.ts, issue #212).
//
// THE RULE THIS GUARDS: a missing reading is a gap in the line, never a drop to
// 0. An hour the machine slept used nothing we know of; drawing it as 0 claims
// it was idle. And a network total that leaves out a host-network container is
// a minimum, shown with ≥, never a plain number that silently misses it.
//
// It drives the REAL functions and asserts on the HTML they return, never on
// source text.
//
//   node scripts/usage_render_check.mjs     # against ./.harness-build

import { linePath, pointLabel, renderUsagePanel } from "../.harness-build/render/usage.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const HOUR = 3600;
const start = 1_790_000_000 - (1_790_000_000 % HOUR);

/** A 6-point view; series default to all-null, overridable per case. */
function view(overrides = {}) {
  const n = 6;
  const empty = () => Array(n).fill(null);
  return {
    days: 7,
    step: HOUR,
    hours: Array.from({ length: n }, (_, i) => start + i * HOUR),
    containers: [{ name: "cashpilot-honeygain", slug: "honeygain", service: "Honeygain", noCounters: false }],
    container: "",
    cpuPercent: empty(),
    memoryMb: empty(),
    rxRate: empty(),
    txRate: empty(),
    rxBytes: empty(),
    txBytes: empty(),
    trafficPartial: Array(n).fill(false),
    cpuPartial: Array(n).fill(false),
    memoryPartial: Array(n).fill(false),
    ...overrides,
  };
}

/** The <figure> of one chart in the panel's HTML. */
function chart(html, id) {
  const at = html.indexOf(`data-usage-chart="${id}"`);
  return at < 0 ? "" : html.slice(at, html.indexOf("</figure>", at));
}

// linePath: a null splits the line into two segments, and an isolated reading
// becomes a dot instead of vanishing.
{
  const { path, dots } = linePath([1, 2, null, 4, 5, null, 7], (i) => i * 10, (v) => 100 - v);
  const moves = (path.match(/M/g) || []).length;
  check("a null splits the line into separate segments", moves === 2, `path: ${path}`);
  check("an isolated reading is drawn as a dot", dots.length === 1 && dots[0] === 6, `dots: ${JSON.stringify(dots)}`);
  check("no point is drawn at y=100 (the value 0) for a gap", !/ 100\.0/.test(path), `path: ${path}`);
}

// The panel: a gap in CPU stays a gap, and the hover for that hour does not exist.
{
  const html = renderUsagePanel(view({ cpuPercent: [10, 20, null, null, 30, 40], memoryMb: [100, 100, null, null, 200, 210] }));
  const cpu = chart(html, "cpu");
  const path = (cpu.match(/<path class="usage-line[^"]*" d="([^"]+)"/) || [])[1] || "";
  check("the CPU line has two segments around the missing hours", (path.match(/M/g) || []).length === 2, `path: ${path}`);
  const tips = [...cpu.matchAll(/<title>([^<]*)<\/title>/g)].map((m) => m[1]);
  check("only the hours with a reading get a tooltip", tips.length === 4, `tips: ${JSON.stringify(tips)}`);
  check("no tooltip reports 0% for a missing hour", !tips.some((t) => /0\.00%/.test(t)), `tips: ${JSON.stringify(tips)}`);
  check("a tooltip carries the exact value", tips.some((t) => t.includes("40.0%")), `tips: ${JSON.stringify(tips)}`);
  check("the caption shows the latest value", /usage-latest">40\.0%/.test(cpu), cpu.slice(0, 300));
}

// The latest hour unread: the caption shows the last real reading, never 0.
{
  const html = renderUsagePanel(view({ cpuPercent: [5, 6, 7, 8, 9, null], memoryMb: [1, 1, 1, 1, 1, null] }));
  check("a missing latest hour falls back to the last reading", /usage-latest">9\.00%/.test(chart(html, "cpu")), chart(html, "cpu").slice(0, 300));
}

// Network with a host-network container: the total is a minimum.
{
  const html = renderUsagePanel(view({
    containers: [
      { name: "cashpilot-honeygain", slug: "honeygain", service: "Honeygain", noCounters: false },
      { name: "cashpilot-mysterium", slug: "mysterium", service: "Mysterium", noCounters: true },
    ],
    cpuPercent: [1, 1, 1, 1, 1, 1],
    rxRate: [1000, 1000, 1000, 1000, 1000, 2000],
    txRate: [100, 100, 100, 100, 100, 200],
    rxBytes: [3.6e6, 3.6e6, 3.6e6, 3.6e6, 3.6e6, 7.2e6],
    txBytes: [3.6e5, 3.6e5, 3.6e5, 3.6e5, 3.6e5, 7.2e5],
    trafficPartial: [true, true, true, true, true, true],
  }));
  const net = chart(html, "network");
  check("a total missing a container's traffic is marked ≥", /usage-latest">.*≥ 2\.0 KB\/s/.test(net), net.slice(0, 400));
  check("the tooltip says how many bytes moved", /≥ 7\.2 MB down, 720 KB up moved/.test(net), net);
  check("the note names the container left out", /leave out Mysterium/.test(html), html.slice(-400));
}

// One container picked, with no traffic counters: no network line, and an em dash.
{
  const html = renderUsagePanel(view({
    containers: [{ name: "cashpilot-mysterium", slug: "mysterium", service: "Mysterium", noCounters: true }],
    container: "cashpilot-mysterium",
    cpuPercent: [3, 3, 3, 3, 3, 3],
    trafficPartial: [true, true, true, true, true, true],
  }));
  const net = chart(html, "network");
  check("a container without counters draws no network line", !/<path class="usage-line/.test(net), net);
  check("its network caption is an em dash, not 0 B/s", /usage-latest">.*Down<\/span> —/.test(net) && !/0 B\/s<\/span>/.test(net), net.slice(0, 400));
  check("the picker selects the picked container", /<option value="cashpilot-mysterium" selected>/.test(html), html.slice(0, 400));
}

// The picker always offers the machine total.
{
  const html = renderUsagePanel(view({ cpuPercent: [1, 1, 1, 1, 1, 1] }));
  check("the picker offers All containers, selected", /<option value="" selected>All containers<\/option>/.test(html), html.slice(0, 300));
  check("a container is labelled by its service", /Honeygain \(cashpilot-honeygain\)/.test(html), html.slice(0, 400));
}

// No reading at all: one line of text, no chart of zeros.
{
  const html = renderUsagePanel(view());
  check("no reading in the window renders the empty state", /No resource readings in the last 7 days/.test(html) && !/<svg/.test(html), html);
  check("not loaded yet renders no chart", !/<svg/.test(renderUsagePanel(null)));
}

// A point label shows the local time the hour starts, minutes included: in a
// half-hour timezone (India, UTC+5:30) a UTC hour starts at :30 locally, and
// printing ":00" names the wrong half hour.
{
  const saved = process.env.TZ;
  process.env.TZ = "Asia/Kolkata";
  const label = pointLabel(start);
  check("a half-hour timezone labels the hour with its minutes", /\d\d:30$/.test(label), label);
  process.env.TZ = "UTC";
  const utc = pointLabel(start);
  check("a whole-hour timezone still labels :00", /\d\d:00$/.test(utc), utc);
  if (saved === undefined) delete process.env.TZ;
  else process.env.TZ = saved;
}

console.log(`${checks - failures}/${checks} usage-render checks passed`);
if (failures) process.exit(1);
