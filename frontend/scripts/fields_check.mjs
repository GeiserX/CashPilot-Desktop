#!/usr/bin/env node
// Browser-free checks on the wizard's credential form.
//
// THE RULE: the form asks for every credential the service needs, and the two
// halves of "needs" are not the same list.
//
//   docker.env       what the CONTAINER authenticates with
//   collectorFields  what the EARNINGS COLLECTOR authenticates with
//
// Repocket is the case that proves they differ: the container takes RP_EMAIL plus
// an API key from the dashboard, while the collector signs in to Firebase with the
// account password. When the web catalog renamed the container's keys, the frontend
// kept its own collector table, nobody added an entry, and the form silently stopped
// asking for the account password — so "Collect Earnings" could only ever answer
// "Repocket email and password are required".
//
//   node scripts/fields_check.mjs      # against ./.harness-build

import { serviceFormFields, stripHtml } from "../.harness-build/render/fields.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function service(slug, env) {
  return { slug, docker: { env } };
}

function envVar(key, extra = {}) {
  return { key, label: key, required: true, secret: false, description: key, default: "", ...extra };
}

const repocket = service("repocket", [
  envVar("RP_EMAIL", { label: "Email" }),
  envVar("RP_API_KEY", { label: "API Key", secret: true }),
]);

const backendFields = {
  repocket: [
    { key: "REPOCKET_EMAIL", label: "Repocket account email", description: "Account email", required: true },
    { key: "REPOCKET_PASSWORD", label: "Repocket account password", description: "Account password", required: true, secret: true },
  ],
};

// ---------------------------------------------------------------------------
// THE RULE.
// ---------------------------------------------------------------------------
const repocketKeys = serviceFormFields(repocket, backendFields).map((f) => f.key);

check(
  "THE RULE: the collector's own credentials get a field even when docker.env names none of them",
  repocketKeys.includes("REPOCKET_EMAIL") && repocketKeys.includes("REPOCKET_PASSWORD"),
  `rendered keys: ${JSON.stringify(repocketKeys)}`,
);

check(
  "the container's own contract is still asked for",
  repocketKeys.includes("RP_EMAIL") && repocketKeys.includes("RP_API_KEY"),
  `rendered keys: ${JSON.stringify(repocketKeys)}`,
);

check(
  "docker.env comes first, so the deploy fields lead the card",
  repocketKeys.slice(0, 2).join(",") === "RP_EMAIL,RP_API_KEY",
  `rendered keys: ${JSON.stringify(repocketKeys)}`,
);

check(
  "the account password renders masked",
  serviceFormFields(repocket, backendFields).find((f) => f.key === "REPOCKET_PASSWORD")?.secret === true,
);

// ---------------------------------------------------------------------------
// One key, one input. A collector field that docker.env already declares must not
// produce a second box for the same key: two inputs write the same credential slot,
// and whichever renders last wins, so the user cannot tell which one was saved.
// ---------------------------------------------------------------------------
const grass = service("grass", [envVar("GRASS_ACCESS_TOKEN", { secret: true })]);
const grassKeys = serviceFormFields(grass, {
  grass: [{ key: "GRASS_ACCESS_TOKEN", label: "Grass access token", description: "token", required: true, secret: true }],
}).map((f) => f.key);

check(
  "a key declared by BOTH halves renders exactly one input",
  grassKeys.length === 1 && grassKeys[0] === "GRASS_ACCESS_TOKEN",
  `rendered keys: ${JSON.stringify(grassKeys)}`,
);

check(
  "the surviving duplicate keeps docker.env's deploy metadata (it carries the default)",
  serviceFormFields(
    service("x", [envVar("TOKEN", { default: "cashpilot-{hostname}" })]),
    { x: [{ key: "TOKEN", label: "other", description: "other" }] },
  )[0].default === "cashpilot-{hostname}",
);

// ---------------------------------------------------------------------------
// Degrading safely: no backend map at all (an old state object, a failed refresh)
// must still render the deploy form rather than throwing on the way in.
// ---------------------------------------------------------------------------
check(
  "a missing collectorFields map still renders the docker.env fields",
  serviceFormFields(repocket, null).map((f) => f.key).join(",") === "RP_EMAIL,RP_API_KEY",
);

check(
  "a service with no fields at all renders none",
  serviceFormFields(service("manual", []), {}).length === 0,
);

// ---------------------------------------------------------------------------
// Placeholders come from the catalog, which writes HTML links into descriptions.
// ---------------------------------------------------------------------------
check(
  "a catalog description's markup is stripped before it becomes a placeholder",
  serviceFormFields(service("s", [envVar("K", { description: 'Copy it from <a href="https://x">x</a> now' })]), {})[0]
    .description === "Copy it from x now",
);

check("stripHtml leaves plain text alone", stripHtml("no markup here") === "no markup here");

console.log(`${checks - failures}/${checks} checks passed`);
if (failures > 0) process.exit(1);
