#!/usr/bin/env node
// Browser-free checks on the Route box on a service's setup card (render/route.ts).
//
//   1. A SERVICE THE CATALOG CAN ROUTE GETS A BOX WITH THIS COMPUTER'S OWN IP AND EACH
//      PROXY, ITS NAME AND EXIT ESCAPED, AND THE RECORDED ROUTE SELECTED.
//   2. A SERVICE THE CATALOG CANNOT ROUTE GETS NO BOX, AND SAYS WHY.
//   3. WITH CASHPILOT_PROXIED_ONLY THE OWN-IP CHOICE IS GONE.
//   4. THE BOX SENDS A PROXY ID, OR null FOR THIS COMPUTER'S OWN IP.
//   5. A SERVICE ROUTED HERE, RUNNING OR NOT, ASKS BEFORE IT RUNS FROM THIS COMPUTER'S IP.
//   6. THE SETUP CARD CARRIES THE BOX.
//   7. A ROUTED DEPLOY WITH A CHOSEN IMAGE OR MORE THAN ONE COPY IS REFUSED, NOT DROPPED.
//   8. A REFUSED OR DECLINED DEPLOY SAVES NO CREDENTIALS; THE CONFIRMATION COMES FIRST.
//
//   node scripts/route_check.mjs      # against ./.harness-build

import { leavesRoute, routeChoice, routeChoiceSection, runRouteDeploy } from "../.harness-build/render/route.js";
import { renderWizardServiceSetup } from "../.harness-build/render/wizard.js";

let failures = 0;
let checks = 0;
function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const honeygain = { slug: "honeygain", name: "Honeygain", description: "", docker: { image: "honeygain/honeygain", env: [] } };
const route = {
  proxies: [{ id: 3, name: "Home <b>", exitIp: "203.0.113.9", country: "DE" }, { id: 4, name: "", exitIp: "", country: "" }],
  refusals: { honeygain: "", storj: "Storj publishes a port <on the host>." },
  recorded: { honeygain: 3 },
  running: { honeygain: 3 },
  proxiedOnly: false,
};

const box = routeChoiceSection(honeygain, route);
check("RULE 1: a select carries the slug", /<select data-wizard-route="honeygain">/.test(box), box);
check("RULE 1: this computer's own IP is offered", /<option value="">Direct: this computer's own IP<\/option>/.test(box), box);
check("RULE 1: the proxy name and exit are escaped", box.includes("Through Home &lt;b&gt;, exit 203.0.113.9 (DE)") && !box.includes("<b>"), box);
check("RULE 1: an unnamed proxy reads by its id", box.includes("Through proxy 4"), box);
check("RULE 1: the recorded route is selected", /<option value="3" selected>/.test(box), box);

const storj = { slug: "storj", name: "Storj", description: "", docker: { image: "storjlabs/storagenode", env: [] } };
const refused = routeChoiceSection(storj, route);
check("RULE 2: no box for a service that cannot be routed", !refused.includes("<select"), refused);
check("RULE 2: it says why, escaped", refused.includes("publishes a port &lt;on the host&gt;."), refused);
check("RULE 2: no box at all without proxies", routeChoiceSection(honeygain, { ...route, proxies: [] }) === "");
check("RULE 2: no box for a custom service", routeChoiceSection({ ...honeygain, custom: true }, route) === "");

const only = routeChoiceSection(honeygain, { ...route, proxiedOnly: true });
check("RULE 3: proxied-only drops the own-IP choice", !only.includes('value=""') && only.includes("only through a proxy"), only);

check("RULE 4: a proxy id is sent", routeChoice("3") === 3);
for (const v of ["", null, undefined, "0", "-2", "x"]) check(`RULE 4: ${JSON.stringify(v)} sends null`, routeChoice(v) === null);

check("RULE 5: a recorded route asks", leavesRoute("honeygain", route) === true);
check("RULE 5: an unrouted service does not", leavesRoute("storj", route) === false && leavesRoute("honeygain", null) === false);

const card = renderWizardServiceSetup(honeygain, {}, "box", null, "", null, null, route);
check("RULE 6: the setup card carries the box", card.includes('data-wizard-route="honeygain"'), card);
check("RULE 6: the box is not a credential", !/data-wizard-env="[^"]*"[^>]*data-wizard-route/.test(card));

function recorder(answer) {
  const log = [];
  return {
    log,
    calls: {
      save: async () => { log.push("save"); },
      confirm: () => { log.push("confirm"); return answer; },
      routed: async (id) => { log.push(`routed ${id}`); return { exitIp: "203.0.113.7" }; },
      direct: async (leave) => { log.push(`direct ${leave}`); return { slug: "honeygain" }; },
    },
  };
}

for (const [image, instances] of [["ghcr.io/me/honeygain:1", null], [null, 3]]) {
  const r = recorder(true);
  const out = await runRouteDeploy("honeygain", 3, image, instances, route, r.calls);
  check(`RULE 7: image ${image} instances ${instances} is refused`, out.kind === "refused" && out.message.includes("Image box"), JSON.stringify(out));
  check(`RULE 7: image ${image} instances ${instances} runs and saves nothing`, r.log.length === 0, r.log.join(","));
}
{
  const r = recorder(true);
  const out = await runRouteDeploy("honeygain", 3, "", 1, route, r.calls);
  check("RULE 7: the catalog image, once, deploys through the proxy", out.kind === "routed" && r.log.join(",") === "save,routed 3", r.log.join(","));
}
{
  const r = recorder(false);
  const out = await runRouteDeploy("honeygain", null, null, null, route, r.calls);
  check("RULE 8: a declined switch saves no credentials", out.kind === "cancelled" && r.log.join(",") === "confirm", r.log.join(","));
}
{
  const r = recorder(true);
  const out = await runRouteDeploy("honeygain", null, "ghcr.io/me/honeygain:1", 2, route, r.calls);
  check("RULE 8: a confirmed switch saves, then leaves the route", out.kind === "direct" && r.log.join(",") === "confirm,save,direct true", r.log.join(","));
}
{
  const r = recorder(true);
  const out = await runRouteDeploy("storj", null, null, null, route, r.calls);
  check("RULE 8: an unrouted service deploys without asking", out.kind === "direct" && r.log.join(",") === "save,direct false", r.log.join(","));
}

if (failures) {
  console.error(`\n${failures} of ${checks} route checks failed`);
  process.exit(1);
}
console.log(`route: ${checks} checks passed`);
