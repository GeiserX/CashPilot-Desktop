#!/usr/bin/env node
// Browser-free checks on Settings > Restore from a backup.
//
// THE RULE: the preview says what the restore creates, never offers Restore when this
// machine refuses it, asks for the service name for a file this app did not make,
// and names the unfinished restores that hold a service.
//
//   node scripts/restore_render_check.mjs      # against ./.harness-build

import { renderRestoreCard, renderRestorePreview, restoreResultText } from "../.harness-build/render/restore.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function preview(overrides = {}) {
  return {
    token: "tok123",
    file: "cashpilot-mysterium-box-20261101.cpbackup",
    slug: "mysterium",
    service: "Mysterium",
    bytes: 2048,
    sha256: "ab".repeat(32),
    known: true,
    madeOn: "box",
    container: "cashpilot-mysterium",
    creates: [{target: "/var/lib/mysterium-node", volume: "mysterium-data"}],
    ok: true,
    why: "",
    runsOn: [],
    ...overrides,
  };
}

const card = renderRestoreCard([]);
check("the card has the file button", card.includes('id="restore-choose"'), card);
check("the card says it only creates", card.includes("only creates a new container"));
check("the card points key-sealed files to the command line", card.includes("Files sealed to a key") && card.includes("docs/backup.md#restore-a-backup-from-the-command-line"));
check("no unfinished restore, no list", !card.includes("did not finish"));

const stuck = renderRestoreCard([{slug: "mysterium", service: "Mysterium", volumes: ["mysterium-data"], startedAt: "2026-11-16T10:20:30Z"}]);
check("an unfinished restore is listed by name", stuck.includes("<strong>Mysterium</strong>") && stuck.includes("did not finish"), stuck);
check("it says Deploy and Start refuse it", stuck.includes("Deploy and Start refuse it"));
check("it names the volumes the restore made", stuck.includes("<code>mysterium-data</code>"));
check("it offers to delete the partial restore", stuck.includes('data-restore-discard="mysterium"'));

const known = renderRestorePreview(preview());
check("the preview names the container and the volume", known.includes("cashpilot-mysterium") && known.includes("<code>mysterium-data</code> at <code>/var/lib/mysterium-node</code>"), known);
check("the preview carries the token", known.includes('data-token="tok123"'));
check("a known file asks for no name", !known.includes('name="confirm_name"') && known.includes("This app made this file"));
check("the passphrase is a password field", known.includes('type="password" name="passphrase"'));
check("Restore is offered when this machine accepts it", /<button type="submit"[^>]*>Restore<\/button>/.test(known) && !/<button type="submit"[^>]*disabled/.test(known));

const foreign = renderRestorePreview(preview({known: false, madeOn: "other-box", runsOn: ["other-box"]}));
check("a foreign file asks for the name typed", foreign.includes('name="confirm_name"') && foreign.includes("type <strong>Mysterium</strong>"), foreign);
check("a foreign file says where it claims to come from", foreign.includes("it says it came from other-box"));
check("a foreign file warns about machines running the service", foreign.includes("runs now on other-box"));

const refused = renderRestorePreview(preview({ok: false, why: "the volume mysterium-data already exists on this machine"}));
check("a refusal is shown", refused.includes("cannot restore it now: the volume mysterium-data already exists"), refused);
check("a refusal disables Restore", /<button type="submit"[^>]*disabled>Restore<\/button>/.test(refused), refused);

const hostile = renderRestorePreview(preview({service: "<img src=x onerror=alert(1)>", file: "<script>.cpbackup", why: "<b>", ok: false, known: false, madeOn: "<i>"}));
check("names, file and reasons are escaped", !hostile.includes("<img") && !hostile.includes("<script>") && !hostile.includes("<b>") && !hostile.includes("<i>"), hostile);
const hostileCard = renderRestoreCard([{slug: '"><img src=x>', service: "<img>", volumes: ["<v>"], startedAt: ""}]);
check("the unfinished list is escaped", !hostileCard.includes("<img") && !hostileCard.includes("<v>"), hostileCard);

check("a started restore says so", restoreResultText("Mysterium", {files: 3, started: true}).includes("3 files, checked against the backup) and started"));
check("one file is singular", restoreResultText("Mysterium", {files: 1, started: true}).includes("(1 file,"));
check("a node waiting for the fence says so", restoreResultText("Mysterium", {files: 2, started: false, waiting: "the fence is not applied"}).includes("waits for the LAN fence and starts once it holds: the fence is not applied"));

if (failures > 0) {
  console.error(`\n${failures} of ${checks} restore checks FAILED`);
  process.exit(1);
}
console.log(`restore_render_check: ${checks} checks passed`);
