#!/usr/bin/env node
// Browser-free checks on the LAN isolation rows of "Needs attention".
//
// New installs fence container earners off the LAN; an upgraded install stays on
// Docker's default bridge until the user switches. These checks hold that the
// dashboard names the exposed services with the switch beside them, says when the
// fence is down and deploys are refused, and says nothing when isolation is off.
//
//   node scripts/isolation_render_check.mjs      # against ./.harness-build

import { attentionItems, renderAttention } from "../.harness-build/render/attention.js";

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
  { slug: "honeygain", name: "Honeygain" },
  { slug: "repocket", name: "Repocket" },
];
const base = { runtime: runtimeOk, services, deployments: [], earnings: [] };
const fence = (over) => ({ mode: "legacy", setting: "auto", state: "n/a", backend: "", error: "", outside: [], outsideStopped: [], ...over });
const rows = (isolation) => attentionItems({ ...base, isolation });

const legacy = rows(fence({ outside: ["honeygain"], outsideStopped: ["repocket"] }));
const exposed = legacy.find((i) => i.tag === "Exposed");
check("an upgraded install with earners on the bridge gets an Exposed row", !!exposed, JSON.stringify(legacy));
check("the row names the service by its catalog name", exposed?.what === "Honeygain", exposed?.what);
check("the row says what is reachable", !!exposed?.why.includes("can reach your LAN"), exposed?.why);
check("the row offers the switch", !!exposed?.actions.includes('data-isolation-action="enable"'), exposed?.actions);
check("the row names the stopped services apart", !!exposed?.why.includes("Repocket") && exposed.why.includes("left alone"), exposed?.why);

const many = rows(fence({ outside: ["honeygain", "repocket"] })).find((i) => i.tag === "Exposed");
check("several services are counted in the row title", many?.what === "2 services", many?.what);
check("several services are all named", !!many?.why.includes("Honeygain, Repocket"), many?.why);

check("isolation off says nothing", rows(fence({ mode: "off", outside: ["honeygain"] })).length === 0, JSON.stringify(rows(fence({ mode: "off", outside: ["honeygain"] }))));
check("no status yet says nothing", rows(null).length === 0);
check("a fenced machine with nothing outside says nothing", rows(fence({ mode: "on", state: "applied" })).length === 0);

const down = rows(fence({ mode: "on", state: "failed", error: "nft: Operation not permitted" }));
const downRow = down.find((i) => i.tag === "Fence down");
check("a fence that cannot be applied gets a failure row", downRow?.level === "fail", JSON.stringify(down));
check("the failure row carries the reason", !!downRow?.why.includes("Operation not permitted"), downRow?.why);
check("the failure row says deploys are refused and how to opt out", !!downRow?.why.includes("refused") && downRow.why.includes("off in Settings"), downRow?.why);
check("a fence that failed in off mode is not reported as down", !rows(fence({ mode: "off", state: "failed", error: "x" })).some((i) => i.tag === "Fence down"));

const unknown = rows(fence({ mode: "", setting: "on", state: "failed", error: "Cannot connect to the Docker daemon" }));
const unknownRow = unknown.find((i) => i.tag === "Fence unknown");
check("a fence check that failed before any mode is reported", unknownRow?.level === "fail", JSON.stringify(unknown));
check("it says the check failed, not the apply", !!unknownRow?.why.includes("could not check") && !unknownRow.why.includes("could not be applied"), unknownRow?.why);
check("with isolation off a failed check says nothing", rows(fence({ mode: "", setting: "off", state: "failed", error: "x" })).length === 0);

const hostile = rows(fence({ mode: "on", state: "failed", error: "<img src=x onerror=alert(1)>", outside: ["<script>"] }));
const html = renderAttention(hostile, 0, 0);
check("the error and names are escaped", !html.includes("<img src=x") && !html.includes("<script>"), html.slice(0, 300));

if (failures) {
  console.error(`\n${failures} of ${checks} isolation checks failed`);
  process.exit(1);
}
console.log(`${checks}/${checks} checks passed`);
console.log("isolation render check passed");
