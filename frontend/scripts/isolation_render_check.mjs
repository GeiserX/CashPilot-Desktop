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

const podmanError = "CashPilot cannot fence earners off the LAN on Podman yet: its Docker-compatible API does not keep the network settings the fence relies on.";
const podman = rows(fence({ mode: "unsupported", state: "unsupported", error: podmanError, outside: ["honeygain"] }));
const notFenced = podman.find((i) => i.tag === "Not fenced");
check("a runtime the fence cannot work on gets one row", podman.length === 1 && !!notFenced, JSON.stringify(podman));
check("the row says why and what it means", !!notFenced?.why.includes("Podman") && notFenced.why.includes("can reach your LAN"), notFenced?.why);
check("the row offers no switch that cannot work", notFenced?.actions === "" && !podman.some((i) => i.tag === "Exposed"), JSON.stringify(podman));
check("the reason and the consequence read as two sentences", !notFenced?.why.includes(".."), notFenced?.why);
check("with auto it does not claim deploys are refused", !notFenced?.why.includes("refused"), notFenced?.why);
const podmanOn = rows(fence({ mode: "unsupported", setting: "on", state: "unsupported", error: podmanError })).find((i) => i.tag === "Not fenced");
check("with isolation on it says deploys are refused and how to opt out", !!podmanOn?.why.includes("refused") && podmanOn.why.includes("auto or off"), podmanOn?.why);

// A fence-gated earner (Mysterium) that is down and waiting for the fence is not a
// container to start by hand, and the fence-down advice never sends it to isolation off.
const gatedServices = [...services, { slug: "mysterium", name: "MystNodes" }];
const gatedRows = (isolation, status = "exited") =>
  attentionItems({ ...base, services: gatedServices, deployments: [{ slug: "mysterium", name: "cashpilot-mysterium", status }], isolation });
const waitingOn = gatedRows(fence({ mode: "on", state: "applied", waiting: ["mysterium"] }));
const waitRow = waitingOn.find((i) => i.tag === "Waiting");
check("a waiting fence-gated node gets a waiting row, not a Down row", !!waitRow && !waitingOn.some((i) => i.tag === "Down"), JSON.stringify(waitingOn));
check("the waiting row offers no Start", !!waitRow && !waitRow.actions.includes('data-row-action="start"'), waitRow?.actions);
check("the waiting row says CashPilot starts it behind the fence", !!waitRow?.why.includes("CashPilot starts it itself"), waitRow?.why);
check("the waiting row says it stays down while CashPilot is not running", !!waitRow?.why.includes("While CashPilot is not running") && waitRow.why.includes("until CashPilot starts"), waitRow?.why);
const waitingOff = gatedRows(fence({ mode: "off", setting: "off", state: "n/a", waiting: ["mysterium"] })).find((i) => i.tag === "Waiting");
check("with isolation off the waiting row says isolation must be on", !!waitingOff?.why.includes("isolation is on"), waitingOff?.why);
const refused = gatedRows(fence({ mode: "on", state: "applied", error: "could not start cashpilot-mysterium behind the LAN fence", waiting: ["mysterium"] })).find((i) => i.tag === "Waiting");
check("with the fence applied and an error the waiting row shows the error, not a promise to start", !!refused?.why.includes("could not start cashpilot-mysterium") && !refused.why.includes("as soon as"), refused?.why);
const userStopped = gatedRows(fence({ mode: "on", state: "applied", waiting: [] }));
check("a node the user stopped stays a Down row with Start", userStopped.some((i) => i.tag === "Down" && i.actions.includes('data-row-action="start"')), JSON.stringify(userStopped));

const gatedDown = gatedRows(fence({ mode: "on", state: "failed", error: "nft: Operation not permitted", waiting: ["mysterium"] })).find((i) => i.tag === "Fence down");
check("the fence-down row keeps the isolation-off advice for other earners", !!gatedDown?.why.includes("other earners") && gatedDown.why.includes("off in Settings"), gatedDown?.why);
check("the fence-down row says a fence-gated node would not start with isolation off", !!gatedDown?.why.includes("MystNodes runs only behind the fence") && gatedDown.why.includes("would not start it"), gatedDown?.why);
const plainDown = rows(fence({ mode: "on", state: "failed", error: "x" })).find((i) => i.tag === "Fence down");
check("with no fence-gated node waiting the fence-down row names none", !!plainDown && !plainDown.why.includes("only behind the fence"), plainDown?.why);

if (failures) {
  console.error(`\n${failures} of ${checks} isolation checks failed`);
  process.exit(1);
}
console.log(`${checks}/${checks} checks passed`);
console.log("isolation render check passed");
