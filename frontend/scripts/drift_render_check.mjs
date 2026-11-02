#!/usr/bin/env node
// Browser-free checks on the "Needs fixing" warning: a running container that no
// longer matches what a redeploy would build from the current catalog.
//
// Bitping containers made before the catalog added NET_RAW ran three months with
// every probe failing while the dashboard showed them healthy. The backend now says
// how such a container differs (AppState.drift); these checks hold that the
// dashboard shows it, with the redeploy and the ignore beside it, and shows nothing
// for a service with no difference.
//
//   node scripts/drift_render_check.mjs      # against ./.harness-build

import { attentionItems, renderAttention } from "../.harness-build/render/attention.js";
import { renderServicesTable } from "../.harness-build/render/services.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const runtimeOk = { available: true, nativeAvailable: false, message: "" };
const services = [
  { slug: "bitping", name: "Bitping" },
  { slug: "honeygain", name: "Honeygain" },
];
const dep = (slug) => ({ slug, name: `cashpilot-${slug}`, image: `${slug}/img`, status: "running", cpuPercent: 0, memoryMb: 0, runtime: "existing-docker" });
const deployments = [dep("bitping"), dep("honeygain")];
const drift = { bitping: ["missing NET_RAW, which the catalog now grants; the container was made without it"] };

// The attention list.
const items = attentionItems({ runtime: runtimeOk, services, deployments, earnings: [], drift });
const row = items.find((i) => i.tag === "Needs fixing");
check("a drifting container gets a Needs fixing row", !!row, JSON.stringify(items));
check("the row names the service", row?.what === "Bitping", row?.what);
check("the row says what differs", !!row?.why.includes("missing NET_RAW"), row?.why);
check("the row is a failure, sorted with the failures", row?.level === "fail", row?.level);
check("the row offers a redeploy of that service", !!row?.actions.includes('data-drift-action="redeploy" data-slug="bitping"'), row?.actions);
check("the row offers to ignore it", !!row?.actions.includes('data-drift-action="ignore" data-slug="bitping"'), row?.actions);
check("only the drifting service gets a row", items.filter((i) => i.tag === "Needs fixing").length === 1, JSON.stringify(items.map((i) => i.what)));

const quiet = attentionItems({ runtime: runtimeOk, services, deployments, earnings: [], drift: null });
check("no drift, no row", !quiet.some((i) => i.tag === "Needs fixing"), JSON.stringify(quiet));
const empty = attentionItems({ runtime: runtimeOk, services, deployments, earnings: [], drift: { bitping: [] } });
check("an empty finding list is no row", !empty.some((i) => i.tag === "Needs fixing"), JSON.stringify(empty));

const hostile = attentionItems({ runtime: runtimeOk, services, deployments, earnings: [], drift: { bitping: ["<img src=x onerror=alert(1)>"] } });
check("a finding is escaped", !renderAttention(hostile, 2, 2).includes("<img src=x"), renderAttention(hostile, 2, 2).slice(0, 200));

// The badge in the services table, beside "update available".
const table = (d) => renderServicesTable({ services, deployments, earnings: [], displayCurrency: "USD", drift: d });
const html = table(drift);
const bitpingRow = html.split("<tr").find((r) => r.includes("cashpilot-bitping") || r.includes(">Bitping<"));
check("the drifting service carries a needs fixing badge", !!bitpingRow?.includes("needs fixing"), bitpingRow?.slice(0, 400));
check("the badge says what differs", !!bitpingRow?.includes("missing NET_RAW"), bitpingRow?.slice(0, 400));
check("only one badge in the table", html.split("needs fixing").length - 1 === 1, String(html.split("needs fixing").length - 1));
check("no drift, no badge", !table(null).includes("needs fixing"));

if (failures) {
  console.error(`\n${failures} of ${checks} drift checks failed`);
  process.exit(1);
}
console.log(`${checks}/${checks} checks passed`);
console.log("drift render check passed");
