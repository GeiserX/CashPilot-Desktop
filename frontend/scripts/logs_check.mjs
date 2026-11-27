#!/usr/bin/env node
// Browser-free checks on the dashboard's log view (src/render/logs.ts).
//
//   1. A LINE FROM TODAY SHOWS ITS LOCAL TIME ONLY, with Docker's full stamp in
//      the tooltip, and the message keeps its own indent.
//   2. ONE LINE FROM ANOTHER DAY DATES EVERY LINE, in local time.
//   3. A LINE WITHOUT A STAMP passes through whole, and a time that cannot exist
//      stays as text.
//   4. THE HTML ESCAPES what the app logged, and has one row per entry.
//
//   TZ=Europe/Madrid node scripts/logs_check.mjs      # against ./.harness-build

import { logEntries, renderLogLines } from "../.harness-build/render/logs.js";

process.env.TZ = "Europe/Madrid";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const now = new Date("2026-11-24T18:00:00Z");
const logs =
  '2026-11-24T15:52:39.534636172Z level=info msg="proxy connected"\n' +
  "2026-11-24T15:52:40.000000001Z   indented message\n";

{
  const { entries, width } = logEntries(logs, now);
  check("today shows the local time", entries[0].shown === "16:52:39", JSON.stringify(entries[0]));
  check("the full stamp is kept", entries[0].stamp === "2026-11-24T15:52:39.534636172Z");
  check("the message keeps its own indent", entries[1].message === "  indented message", JSON.stringify(entries[1]));
  check("the time column is 8 wide", width === 8);
}
{
  const { entries, width } = logEntries("2026-11-23T20:00:00.1Z old\n" + logs, now);
  check(
    "another day dates every line",
    JSON.stringify(entries.map((e) => e.shown)) === JSON.stringify(["2026-11-23 21:00:00", "2026-11-24 16:52:39", "2026-11-24 16:52:40"]),
    JSON.stringify(entries.map((e) => e.shown)),
  );
  check("the dated column is 19 wide", width === 19);
}
{
  const { entries } = logEntries("2026-11-24T23:30:00Z late\n", new Date("2026-11-24T12:00:00Z"));
  check("tomorrow in local time is dated", entries[0].shown === "2026-11-25 00:30:00", entries[0].shown);
}
{
  const { entries, width } = logEntries("Error: nope\n\tat frame\r\n2026-13-45T99:99:99Z broken", now);
  check("no stamp, no time", entries.every((e) => e.shown === ""), JSON.stringify(entries));
  check("lines pass through whole", entries.map((e) => e.message).join("|") === "Error: nope|\tat frame|2026-13-45T99:99:99Z broken");
  check("no time column", width === 0);
}
{
  const html = renderLogLines('2026-11-24T15:52:39Z <img src=x onerror=alert(1)> "q"\nplain', now, false);
  check("the log text is escaped", !html.includes("<img") && html.includes("&lt;img"), html);
  check("one row per entry", (html.match(/class="log-line"/g) || []).length === 2, html);
  check("the full stamp is the tooltip", html.includes('title="2026-11-24T15:52:39Z">16:52:39</span>'), html);
  check("the switch says it is off", html.includes('aria-pressed="false"'), html);
  check("the switch says it is on", renderLogLines("x", now, true).includes('aria-pressed="true"'));
}

if (failures) {
  console.error(`\n${failures} of ${checks} log checks failed`);
  process.exit(1);
}
console.log(`logs: ${checks} checks passed`);
