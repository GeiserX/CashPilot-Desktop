#!/usr/bin/env node
// Browser-free checks on Fleet > Add Worker or Mobile.
//
// THE RULE: the fleet API key never reaches the rendered HTML in clear text, not
// in the snippet and not in a Copy button's attributes. main.ts copies the real
// snippet from memory on click.
//
//   node scripts/fleet_snippets_check.mjs      # against ./.harness-build

import { KEY_MASK, maskFleetKey, renderFleetSnippets } from "../.harness-build/render/fleet_snippets.js";

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

if (failures > 0) {
  console.error(`\n${failures} of ${checks} fleet snippet checks FAILED`);
  process.exit(1);
}
console.log(`fleet_snippets_check: ${checks} checks passed`);
