#!/usr/bin/env node
// Browser-free checks on the "Almost no traffic" row: a copy that runs but gets
// almost no work while the other copies of its service here are busy
// (AppState.stalls, the web app's stall alert, GeiserX/CashPilot#808).
//
// Its container row says it runs, so this row is the only place it shows. These
// checks hold that the row names the service and the copy, says since when and
// what to check, opens that copy's logs, and that no stall means no row. A proxied
// copy is named as one, and the note says when a CashPilot server raises them.
//
//   node scripts/stall_render_check.mjs      # against ./.harness-build

import { renderAttention } from "../.harness-build/render/attention.js";
import { stallItems, stallSince } from "../.harness-build/render/stall.js";
import { renderStallServerNote } from "../.harness-build/render/stall_server.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

// 24 Nov 2026, 14:00 on this computer's clock, whatever its time zone.
const since = new Date(2026, 10, 24, 14, 0).getTime() / 1000;
const message =
  "Honeygain copy 2 (cashpilot-honeygain-2) has sent under 0.1 MB an hour for 14 hours, while its other copies on this computer sent 30.0 MB an hour. It runs but gets almost no work. Look at its logs, and restart it if this lasts. CashPilot does not restart it by itself.";
const stall = { slug: "honeygain", service: "Honeygain", container: "cashpilot-honeygain-2", copy: 2, since, hours: 14, message };

const items = stallItems([stall]);
check("one stall gives one row", items.length === 1, JSON.stringify(items));
const row = items[0];
check("the row is a warning tagged Almost no traffic", row?.level === "warn" && row?.tag === "Almost no traffic", JSON.stringify(row));
check("the row names the service", row?.what === "Honeygain", row?.what);
check("the row names the copy", row?.where === "copy 2", row?.where);
check("the row says what is wrong and what to check", !!row?.why.includes("has sent under 0.1 MB an hour for 14 hours") && !!row?.why.includes("Look at its logs"), row?.why);
check("the row says since when", !!row?.why.includes("Low since 24 Nov, 14:00."), row?.why);
check("the Logs button opens copy 2", /data-row-action="logs" data-slug="honeygain" data-copy="2"/.test(row?.actions || ""), row?.actions);

const html = renderAttention(items, 3, 3);
check("the list shows the row", html.includes("Almost no traffic") && html.includes("copy 2") && html.includes("Low since 24 Nov, 14:00."), html);
check("the list is not the all-clear", !html.includes("Nothing needs you"), html);

// The first copy's Logs goes through the service, as the other rows do.
const first = stallItems([{ ...stall, copy: 1, container: "cashpilot-honeygain" }])[0];
check("copy 1 is named", first?.where === "copy 1", first?.where);
check("copy 1's Logs has no copy number", !!first?.actions.includes('data-slug="honeygain"') && !first?.actions.includes("data-copy"), first?.actions);

// The message is the backend's text and is escaped.
const hostile = stallItems([{ ...stall, message: "<img src=x onerror=alert(1)>" }])[0];
check("the message is escaped", !!hostile?.why.includes("&lt;img") && !hostile?.why.includes("<img"), hostile?.why);

check("no stalls, no rows", stallItems(null).length === 0 && stallItems([]).length === 0);
check("a missing time is left out", stallSince(0) === "" && !stallItems([{ ...stall, since: 0 }])[0].why.includes("Low since"));

// A proxied copy is named as one, with no copy number and no Logs button: the
// logs route opens the service's own copies, not a proxied one.
const proxied = stallItems([{ ...stall, copy: 0, proxyId: 3, container: "cashpilot-honeygain-p3", message: "Honeygain proxied copy (cashpilot-honeygain-p3) has sent under 0.1 MB an hour for 12 hours." }])[0];
check("a proxied copy is named as one", proxied?.where === "proxied copy", proxied?.where);
check("a proxied copy has no Logs button", proxied?.actions === "", proxied?.actions);
check("a proxied copy says what is wrong", !!proxied?.why.includes("Honeygain proxied copy"), proxied?.why);

// While a CashPilot server raises the alerts of this computer's copies, the
// dashboard says so, so an empty list does not read as "no copy is idle".
const note = renderStallServerNote("https://cashpilot.example");
check("the note names the server", note.includes("https://cashpilot.example") && note.includes("Almost no traffic"), note);
check("the note says the proxied copies are still checked here", note.includes("still checks its proxied copies"), note);
check("no server, no note", renderStallServerNote("") === "" && renderStallServerNote(undefined) === "" && renderStallServerNote(null) === "");
check("the server address is escaped", renderStallServerNote('https://x"><img src=x>').includes("&lt;img") && !renderStallServerNote('https://x"><img src=x>').includes("<img"));

if (failures) {
  console.error(`\n${failures} of ${checks} stall checks failed`);
  process.exit(1);
}
console.log(`stall checks: ${checks} passed`);
