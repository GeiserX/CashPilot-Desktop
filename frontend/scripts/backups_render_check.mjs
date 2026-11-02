#!/usr/bin/env node
// Browser-free checks on the "Back these up" card.
//
// THE RULE: a service whose node identity or wallet exists only on this disk is
// shown, by name, with what the volume holds, until the user says it is backed up;
// and once every one is, the card folds to one quiet line instead of nagging.
//
//   node scripts/backups_render_check.mjs      # against ./.harness-build

import { backupExportForm, backupStatusText, renderStateBackups } from "../.harness-build/render/backups.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function row(overrides = {}) {
  return {
    slug: "mysterium",
    name: "Mysterium",
    volumes: [{target: "/var/lib/mysterium-node", holds: "Node identity keystore. Losing it forfeits unsettled earnings."}],
    state: "none",
    confirmedAt: "",
    method: "",
    tracksChanges: true,
    ...overrides,
  };
}

check("nothing to back up renders nothing", renderStateBackups([]) === "" && renderStateBackups(null) === "");

const open = renderStateBackups([row(), row({slug: "storj", name: "Storj", state: "confirmed", confirmedAt: "2026-10-01T09:00:00Z", method: "manual"})]);
check("an unconfirmed service opens the amber card", open.includes("attn-warn") && open.includes("Back these up"), open);
check("the count is the services still asking", open.includes('<span class="count">1</span>'), open);
check("the row names the volume", open.includes("/var/lib/mysterium-node"));
check("the row says what the volume holds, in the catalog's words", open.includes("Node identity keystore. Losing it forfeits unsettled earnings."));
check("an unconfirmed row offers I have a backup", open.includes('data-backup-action="confirm" data-slug="mysterium"'));
check("every row offers Download backup", (open.match(/data-backup-action="export"/g) || []).length === 2);
check("a confirmed row offers Undo instead of confirm", open.includes('data-backup-action="undo" data-slug="storj"'));
check("the open card is not folded", !open.includes("<details>"));

const quiet = renderStateBackups([row({state: "confirmed", confirmedAt: "2026-10-01T09:00:00Z", method: "export"})]);
check("all confirmed folds to one quiet line", quiet.includes("<details>") && quiet.includes("Backups confirmed for 1 service with irreplaceable state"), quiet);
check("the quiet line is not amber", !quiet.includes("attn-warn") && !quiet.includes("Back these up"), quiet);

check("none says no backup", backupStatusText(row()) === "No backup confirmed");
check("changed says to back it up again", backupStatusText(row({state: "changed", confirmedAt: "2026-05-01T00:00:00Z"})).includes("The volume changed since the backup of 2026-05-01"));
check("expired says six months", backupStatusText(row({state: "expired", confirmedAt: "2026-01-01T00:00:00Z"})).includes("more than six months ago"));
check("recheck asks once more", backupStatusText(row({state: "recheck", confirmedAt: "2026-01-01T00:00:00Z"})).includes("Confirm it once more"));
check("an export says it was saved from here", backupStatusText(row({state: "confirmed", confirmedAt: "2026-10-01T09:00:00Z", method: "export"})).includes("(saved from here)"));
check("an untracked confirmation says only the time limit applies",
  backupStatusText(row({state: "confirmed", confirmedAt: "2026-10-01T09:00:00Z", tracksChanges: false})).includes("only the six-month limit applies"));

const hostile = renderStateBackups([row({name: "<img src=x onerror=alert(1)>", volumes: [{target: "/x", holds: "<script>"}]})]);
check("names and holds are escaped", !hostile.includes("<img") && !hostile.includes("<script>"), hostile);

const form = backupExportForm("mysterium");
check("the form has both modes", form.includes('value="passphrase"') && form.includes('value="recipient"'));
check("the secret fields are password inputs", (form.match(/type="password"/g) || []).length === 2);
check("the form says a lost passphrase cannot be recovered", form.includes("cannot be opened by anyone, CashPilot included"));

if (failures > 0) {
  console.error(`\n${failures} of ${checks} backup checks FAILED`);
  process.exit(1);
}
console.log(`backups_render_check: ${checks} checks passed`);
