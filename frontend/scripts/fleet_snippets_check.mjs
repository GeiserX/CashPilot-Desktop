#!/usr/bin/env node
// Browser-free checks on Fleet > Add Worker or Mobile.
//
// THE RULE: the fleet API key never reaches the rendered HTML in clear text, not
// in the snippet and not in a Copy button's attributes. main.ts copies the real
// snippet from memory on click.
//
//   node scripts/fleet_snippets_check.mjs      # against ./.harness-build

import { KEY_MASK, maskFleetKey, renderFleetSnippets } from "../.harness-build/render/fleet_snippets.js";
import { fleetClosedReasons, renderFleetConnect } from "../.harness-build/render/fleet_reach.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

// The snippets as GetFleetState builds them (app.go).
function fleet(apiKey) {
  const ui = "http://192.168.1.10:8085";
  return {
    apiKey,
    workerSnippet: `CASHPILOT_UI_URL=${ui}\nCASHPILOT_API_KEY=${apiKey}\nCASHPILOT_WORKER_NAME=cashpilot-worker\nCASHPILOT_WORKER_URL=http://<worker-lan-ip>:8081`,
    mobileSnippet: `CASHPILOT_UI_URL=${ui}\nCASHPILOT_API_KEY=${apiKey}\nDevice type: mobile`,
  };
}

for (const key of ["fk-0123456789abcdef0123", 'a"b<c>&d']) {
  const html = renderFleetSnippets(fleet(key));
  const escaped = key.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  check(`${key}: the key is not in the HTML`, !html.includes(key) && !html.includes(escaped), html);
  check(`${key}: both snippets show the key masked`, html.split(`CASHPILOT_API_KEY=${KEY_MASK}`).length === 3, html);
  check(`${key}: no button carries the snippet`, !/data-copy=/.test(html), html);
}

const html = renderFleetSnippets(fleet("fk-0123456789abcdef0123"));
check("each snippet has its own Copy button", /data-fleet-copy="worker"/.test(html) && /data-fleet-copy="mobile"/.test(html), html);
check("the page says Copy gives the real key", /Copy puts the real key in your clipboard/.test(html), html);
check("the rest of the snippet is still shown", html.includes("CASHPILOT_UI_URL=http://192.168.1.10:8085") && html.includes("Device type: mobile"), html);
check("an empty key masks nothing", maskFleetKey("CASHPILOT_API_KEY=\nX=1", "") === "CASHPILOT_API_KEY=\nX=1");

// THE RULE for the whole card: a worker snippet shows only when another machine can
// report here. A fresh install (hub off, API on 127.0.0.1) says so instead, with the
// change that would let them in and its risk, and offers nothing to copy.
const reach = (over) => ({ open: false, hubOff: false, thisPcOnly: false, notListening: false, listen: "127.0.0.1:8085", restartPending: false, ...over });
const card = (r, uiUrl = "http://127.0.0.1:8085") => {
  const f = { ...fleet("fk-0123456789abcdef0123"), reach: r, uiUrl, localApiUrl: "http://127.0.0.1:8085", apiListening: !r.notListening };
  return renderFleetConnect(f, renderFleetSnippets(f));
};

const fresh = card(reach({ hubOff: true, thisPcOnly: true }));
check("closed: says other machines cannot connect", fresh.includes("Other machines cannot connect to this PC now"), fresh);
check("closed: no worker snippet to copy", !fresh.includes("data-fleet-copy") && !fresh.includes("CASHPILOT_UI_URL="), fresh);
check("closed: no LAN address is offered", !fresh.includes("192.168.1.10"), fresh);
check("closed: names hub mode off", fresh.includes("Accept workers (hub mode) is off"), fresh);
check("closed: names the loopback listener", fresh.includes("listens on 127.0.0.1:8085, which only this PC can reach"), fresh);
check("closed: gives the change that lets them in", fresh.includes("<strong>Accept workers (hub mode)</strong> to true") && fresh.includes("<strong>Fleet Bind Address</strong> to this PC's LAN or Tailscale address") && fresh.includes("restart CashPilot"), fresh);
check("closed: says the risk, trusted network only", fresh.includes("only on a home network or a tailnet you trust"), fresh);
check("closed: a button opens Settings", /data-view="settings"/.test(fresh), fresh);

const hubOnly = card(reach({ hubOff: true, listen: "0.0.0.0:8085" }));
check("hub off on every address: closed, names only hub mode", hubOnly.includes("Other machines cannot connect") && hubOnly.includes("hub mode) is off") && !hubOnly.includes("only this PC can reach") && !hubOnly.includes("Fleet Bind Address</strong>"), hubOnly);

const loopOnly = card(reach({ thisPcOnly: true }));
check("loopback with hub on: closed, asks only for the bind address", loopOnly.includes("Other machines cannot connect") && !loopOnly.includes("hub mode) is off") && !loopOnly.includes("<strong>Accept workers (hub mode)</strong> to true") && loopOnly.includes("Fleet Bind Address"), loopOnly);

const down = card(reach({ notListening: true, listen: "" }));
check("not listening: closed, says the API is not running", down.includes("Other machines cannot connect") && down.includes("not running") && down.includes("Not running"), down);
check("not listening alone gives one reason", fleetClosedReasons(reach({ notListening: true, listen: "" })).length === 1);

const open = card(reach({ open: true, listen: "0.0.0.0:8085" }), "http://192.168.1.10:8085");
check("open: the snippets show", open.includes('data-fleet-copy="worker"') && open.includes('data-fleet-copy="mobile"'), open);
check("open: the UI URL names the address it listens on", open.includes("http://192.168.1.10:8085"), open);
check("open: no closed notice", !open.includes("Other machines cannot connect") && !open.includes("data-fleet-closed"), open);
check("open: the key stays masked", !open.includes("fk-0123456789abcdef0123"), open);

const pending = card(reach({ hubOff: true, thisPcOnly: true, restartPending: true }));
check("a saved bind waits for a restart, and the page says so", pending.includes("takes effect after you restart CashPilot"), pending);
check("no pending note without a pending change", !fresh.includes("takes effect after you restart"), fresh);

if (failures > 0) {
  console.error(`\n${failures} of ${checks} fleet snippet checks FAILED`);
  process.exit(1);
}
console.log(`fleet_snippets_check: ${checks} checks passed`);
