#!/usr/bin/env node
// Browser-free checks on the console dashboard: the attention list, the bar
// chart, the services table and the catalog table.
//
// The rules they guard:
//
//   1. Everything a person must act on is listed, failures first, each with the
//      action that fixes it; an empty list says what it checked.
//   2. A balance with no reading is an em dash, never 0.00, in the table as in
//      the earning chip. A figure is in the display currency only when a live
//      rate converted it.
//   3. A platform CashPilot reads but does not run here keeps its row.
//   4. The catalog keeps the sign-up (referral) link on every row that has one.
//
// It drives the REAL functions and asserts on the HTML they return, never on
// source text.
//
//   node scripts/console_render_check.mjs     # against ./.harness-build

import { attentionItems, renderAttention } from "../.harness-build/render/attention.js";
import { chartWindow, niceMax, renderEarningsBars } from "../.harness-build/render/chart.js";
import { renderServicesTable, SERVICE_COLUMNS } from "../.harness-build/render/services.js";
import { catalogRow, estimateText, renderCatalogTable } from "../.harness-build/render/catalog.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const services = [
  { slug: "honeygain", name: "Honeygain" },
  { slug: "iproyal", name: "IPRoyal Pawns" },
  { slug: "repocket", name: "Repocket" },
  { slug: "mysterium", name: "MystNodes" },
];
const dep = (slug, status) => ({ slug, containerId: slug, name: `cashpilot-${slug}`, image: `${slug}/img`, status, runtime: "docker", cpuPercent: 1, memoryMb: 10, customImage: "", instances: 1 });
const runtimeOk = { available: true, nativeAvailable: true, message: "" };

// ---------------------------------------------------------------------------
// RULE 1: the attention list
// ---------------------------------------------------------------------------
const items = attentionItems({
  runtime: runtimeOk,
  services,
  deployments: [dep("honeygain", "running"), dep("iproyal", "exited")],
  copies: { honeygain: [{ slug: "honeygain", containerId: "h2", name: "cashpilot-honeygain-2", image: "i", status: "restarting", cpuPercent: 0, memoryMb: 0, instance: 2 }] },
  earnings: [{ platform: "repocket", error: '<img src=x onerror=alert(1)>' }],
  outdated: ["honeygain"],
  health: { iproyal: { score: 20, uptimePercent: 30, samples: 9, restarts: 0, crashes: 4, stops: 0 } },
});
const tags = items.map((i) => i.tag);
check("RULE 1: a stopped container is listed as Down", tags.includes("Down"), tags.join(","));
check("RULE 1: failures sort before warnings", items[0].level === "fail" && items.slice(1).every((i) => i.level === "warn"), tags.join(","));
const downRow = items.find((i) => i.tag === "Down");
check("RULE 1: Down offers Start for that service", /data-row-action="start" data-slug="iproyal"/.test(downRow.actions), downRow.actions);
const copyRow = items.find((i) => i.where === "copy 2");
check("RULE 1: a copy that is not up is listed, and its buttons act on that copy", copyRow && copyRow.tag === "Degraded" && /data-copy="2"/.test(copyRow.actions), JSON.stringify(copyRow));
check("RULE 1: a running service is not listed", !items.some((i) => i.what === "Honeygain" && (i.tag === "Down" || i.tag === "Degraded") && !i.where));
const noReading = items.find((i) => i.tag === "No reading");
check("RULE 1: a collector error is listed with a way to fix the credentials", noReading && /data-open-service="repocket"/.test(noReading.actions), JSON.stringify(noReading));
check("RULE 1: the error text is escaped", noReading && !noReading.why.includes("<img") && noReading.why.includes("&lt;img"), noReading?.why);
check("RULE 1: an outdated image is listed", tags.includes("Outdated"));
check("RULE 1: repeated crashes are listed", tags.includes("Unstable"));
const offline = attentionItems({ runtime: { available: false, nativeAvailable: false, message: "Docker is not running." }, services, deployments: [], earnings: [] });
check("RULE 1: no runtime at all is a failure", offline.length === 1 && offline[0].level === "fail" && offline[0].why.includes("Docker is not running."), JSON.stringify(offline));
check("RULE 1: native alone is not an outage", attentionItems({ runtime: { available: false, nativeAvailable: true, message: "" }, services, deployments: [], earnings: [] }).length === 0);
const dockerDown = attentionItems({ runtime: { available: false, nativeAvailable: true, message: "Docker is not running." }, services, deployments: [{ ...dep("honeygain", "exited"), runtime: "docker" }], earnings: [] });
check("RULE 1: Docker down with container services stored is an outage, even with native up", dockerDown.some((i) => i.tag === "Offline" && i.what === "Container runtime"), JSON.stringify(dockerDown));
const nativeOnly = attentionItems({ runtime: { available: false, nativeAvailable: true, message: "" }, services, deployments: [{ ...dep("honeygain", "running"), runtime: "native" }], earnings: [] });
check("RULE 1: a native-only install with Docker absent is not an outage", !nativeOnly.some((i) => i.tag === "Offline"), JSON.stringify(nativeOnly));
const orphanCopy = attentionItems({ runtime: runtimeOk, services, deployments: [], copies: { iproyal: [{ slug: "iproyal", containerId: "c", name: "cashpilot-iproyal-2", image: "i", status: "exited", cpuPercent: 0, memoryMb: 0, instance: 2 }] }, earnings: [] });
check("RULE 1: a stopped copy is listed even when its first copy is missing", orphanCopy.some((i) => i.tag === "Down" && i.where === "copy 2"), JSON.stringify(orphanCopy));
check("RULE 1: an unreadable balance can be retried as well as fixed", noReading && /data-row-action="collect" data-slug="repocket"/.test(noReading.actions), noReading?.actions);
const quiet = renderAttention([], 3, 3);
check("RULE 1: an empty list says what it checked", quiet.includes("Nothing needs you. 3 of 3 containers up.") && !quiet.includes("attn-row"), quiet);
const loud = renderAttention(items, 1, 3);
check("RULE 1: the list shows its count", loud.includes(`<span class="count">${items.length}</span>`), loud);
check("CONTROL: rendered rows match the items", (loud.match(/class="attn-row /g) || []).length === items.length, loud);

// ---------------------------------------------------------------------------
// The chart
// ---------------------------------------------------------------------------
const days = Array.from({ length: 30 }, (_, i) => ({ day: `D${i + 1}`, amount: 0.5 + (i % 3) / 10 }));
check("the 7-day window is the last seven days", chartWindow(days, 7).map((d) => d.day).join() === "D24,D25,D26,D27,D28,D29,D30");
check("one bar per day in the window", (renderEarningsBars(days, "USD", 7).match(/<rect /g) || []).length === 7);
check("the 30-day window draws thirty bars", (renderEarningsBars(days, "USD", 30).match(/<rect /g) || []).length === 30);
const flat = renderEarningsBars(days.map((d) => ({ ...d, amount: 0 })), "USD", 7);
check("no money in the window is 'nothing collected', not a row of empty bars", flat.includes("No earnings collected yet") && !flat.includes("<rect"), flat);
check("the axis top is round and covers the data", niceMax(0.73) === 1 && niceMax(1.2) === 2 && niceMax(0) === 1 && niceMax(37) === 50);

// ---------------------------------------------------------------------------
// RULES 2 and 3: the services table
// ---------------------------------------------------------------------------
const cashout = (o = {}) => ({ minAmount: 10, currency: "USD", percent: 40, eligible: false, comparable: true, method: "", dashboardUrl: "", notes: "", ...o });
const item = (platform, o = {}) => ({ platform, name: platform, balance: 4, currency: "USD", balanceDisplay: 4, convertible: true, error: "", cashout: cashout(), ...o });
const table = renderServicesTable({
  services,
  deployments: [dep("honeygain", "running"), dep("iproyal", "exited"), dep("repocket", "running"), dep("mysterium", "running")],
  earnings: [
    { platform: "honeygain", balance: 4, currency: "USD" },
    { platform: "repocket", balance: 0, currency: "USD", error: "login rejected" },
    { platform: "grass", balance: 120, currency: "GRASS" },
  ],
  breakdown: [
    item("honeygain", { cashout: cashout({ percent: 100, eligible: true }) }),
    item("iproyal", { balance: 3.88, balanceDisplay: 3.88 }),
    item("repocket", { balance: 0, balanceDisplay: 0, error: "login rejected" }),
    item("mysterium", { balance: 23.12, currency: "MYST", balanceDisplay: 0, cashout: cashout({ comparable: false }) }),
    item("grass", { name: "Grass", balance: 120, currency: "GRASS", balanceDisplay: 0, convertible: false, cashout: cashout({ comparable: false }) }),
  ],
  copies: { honeygain: [{ slug: "honeygain", containerId: "h2", name: "cashpilot-honeygain-2", image: "i", status: "running", cpuPercent: 0, memoryMb: 0, instance: 2 }] },
  displayCurrency: "EUR",
});
const rowOf = (needle) => (table.split("</tr>").find((r) => r.includes(needle)) || "");
const repocket = rowOf(">Repocket<");
check("RULE 2: an errored balance is an em dash, never 0.00", repocket.includes("&mdash;") && repocket.includes("no reading") && !/0[.,]00/.test(repocket), repocket);
const myst = rowOf(">MystNodes<");
check("RULE 2: an unconverted balance stays native and says so", myst.includes("23.12 MYST") && myst.includes("no rate") && !myst.includes("€"), myst);
const converted = renderServicesTable({ services, deployments: [dep("mysterium", "running")], earnings: [], breakdown: [item("mysterium", { balance: 23.12, currency: "MYST", balanceDisplay: 1.82 })], displayCurrency: "USD" });
check("RULE 2: a converted balance shows the display figure with the native one under it", converted.includes("$1.82") && converted.includes("23.12 MYST"), converted);
const grass = rowOf(">Grass<");
check("RULE 3: a platform read but not run here keeps its row", grass.includes("not run by this machine") && grass.includes("120.00 GRASS"), grass);
check("RULE 3: and it offers no container actions", !/data-row-action="(stop|start|remove|logs)"/.test(grass), grass);
const honeygain = rowOf("Honeygain #1");
check("a cash-out-ready balance says so", honeygain.includes("ready") && honeygain.includes("eligible"), honeygain);
check("a minimum in another unit draws no bar", !myst.includes("payout-progress"), myst);
check("a running service offers Stop, a stopped one Start", /data-row-action="stop" data-slug="honeygain"/.test(table) && /data-row-action="start" data-slug="iproyal"/.test(table));
check("every icon button has an accessible name", (table.match(/btn-icon/g) || []).length > 0 && (table.match(/aria-label="/g) || []).length === (table.match(/btn-icon/g) || []).length, "");
const headCols = (table.match(/<th[ >]/g) || []).length;
check("the header has one column per cell", headCols === SERVICE_COLUMNS, String(headCols));
for (const row of table.split("</tr>").filter((r) => r.includes("<td") && !r.includes("colspan"))) {
  const n = (row.match(/<td[ >]/g) || []).length;
  check("every row has as many cells as the header", n === SERVICE_COLUMNS, `${n}: ${row.slice(0, 200)}`);
}
const empty = renderServicesTable({ services, deployments: [], earnings: [], displayCurrency: "USD" });
check("nothing deployed and nothing read is the empty state", empty.includes("No services deployed yet") && !empty.includes("<table"), empty);

// ---------------------------------------------------------------------------
// RULE 4: the catalog
// ---------------------------------------------------------------------------
const svc = (slug, o = {}) => ({ slug, name: slug, category: "bandwidth", shortDescription: `${slug} desc`, description: "", website: `https://${slug}.example`, referral: { signupUrl: `https://${slug}.example/ref/CODE` }, platforms: ["docker"], earnings: { monthlyLow: 0, monthlyHigh: 5, currency: "USD", per: "device", notes: "" }, manualOnly: false, ...o });
const cat = renderCatalogTable([svc("alpha"), svc("beta"), svc("gamma", { manualOnly: true })], [dep("beta", "running")], []);
const alphaRow = cat.split("</tr>").find((r) => r.includes("alpha desc")) || "";
check("RULE 4: the sign-up row carries its own referral link", alphaRow.includes('data-url="https://alpha.example/ref/CODE"'), alphaRow);
check("services running here are listed first", cat.indexOf("beta desc") < cat.indexOf("alpha desc") && cat.indexOf("Deployed here") < cat.indexOf("Not running"), "");
check("a deployed service is managed, not deployed again", /data-service="beta">Manage</.test(cat), cat);
check("a manual-only service is visited, never deployed", /data-url="https:\/\/gamma.example\/ref\/CODE">Visit</.test(cat) && !cat.includes('data-service="gamma"'), cat);
check("the estimate reads like the web one", estimateText(svc("x")) === "$0-5 / device" && estimateText(svc("y", { earnings: { monthlyLow: 0, monthlyHigh: 0, currency: "USD", per: "" } })) === "—");
const stoppedHere = renderCatalogTable([svc("delta")], [dep("delta", "exited")], []);
check("a deployed but stopped service is not marked running", !stoppedHere.includes("status-dot running") && stoppedHere.includes("status-dot stopped"), stoppedHere);
check("text from the catalog is escaped", !catalogRow(svc("<b>x</b>"), undefined, false).includes("<b>x</b>"));

console.log(`${checks - failures}/${checks} console checks passed`);
if (failures > 0) process.exit(1);
