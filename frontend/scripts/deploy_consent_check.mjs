#!/usr/bin/env node
// Browser-free checks on what a click on Deploy does before the deploy
// (src/render/deploy_consent.ts) and on the slot the setup card gives it.
//
//   1. A FIELD MARKED * THAT IS EMPTY STOPS THE DEPLOY BEFORE ANY CALL: no admin
//      step asked, no approval, no deploy. The note names every empty field and
//      says nothing was deployed, on any OS.
//   2. A FIELD WITH A DEFAULT IS NEVER MISSING; ONE OF ONLY SPACES IS. A LOGIN
//      ONLY THE EARNINGS COLLECTOR READS IS NEVER MISSING: the deploy does not
//      read it, so the card marks it "(for earnings)", not *.
//   3. WHEN WINDOWS WILL SHOW ITS ADMIN PROMPT, CASHPILOT'S SCREEN COMES FIRST:
//      Cancel approves nothing and deploys nothing; Continue approves, then deploys.
//   4. THE SCREEN says what changes, what Windows asks (Windows PowerShell by its
//      full path), what follows and how to undo it, with Continue and Cancel.
//   5. A DEPLOY THROUGH A PROXY asks no admin step. A step that cannot be read
//      (Windows Firewall off, or not readable) shows no screen, approves nothing,
//      deploys nothing, and its reason reaches the card.
//   6. A REJECTED DEPLOY REACHES THE CARD: the reason, as an alert, never empty.
//   7. THE SETUP CARD has the slot right under its Deploy button, above the facts.
//
//   node scripts/deploy_consent_check.mjs      # against ./.harness-build

import {
  POWERSHELL_PATH,
  adminConsentScreen,
  adminConsentText,
  deployErrorText,
  deployNotice,
  missingFieldsNotice,
  missingRequiredFields,
  runCheckedDeploy,
} from "../.harness-build/render/deploy_consent.js";
import { serviceFormFields } from "../.harness-build/render/fields.js";
import { renderWizardServiceSetup } from "../.harness-build/render/wizard.js";

let failures = 0;
let checks = 0;
function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

// MystNodes as the walk saw it: two container settings with defaults, and the two
// logins the collector needs (marked * in v1.6.0, though the deploy never read them).
const myst = {
  slug: "mysterium",
  name: "MystNodes",
  description: "",
  docker: {
    image: "mysteriumnetwork/myst:1.39.9",
    env: [
      { key: "UI_ADDRESS", label: "WebUI address", required: false, default: "127.0.0.1" },
      { key: "UDP_PORTS", label: "UDP port range", required: false, default: "23000:23029" },
    ],
  },
};
const collectorFields = {
  mysterium: [
    { key: "MYSTNODES_EMAIL", label: "MystNodes email", description: "Email used for my.mystnodes.com", required: true },
    { key: "MYSTNODES_PASSWORD", label: "MystNodes password", description: "Password", secret: true, required: true },
  ],
};
const fields = serviceFormFields(myst, collectorFields);
const empty = { UI_ADDRESS: "127.0.0.1", UDP_PORTS: "23000:23029", MYSTNODES_EMAIL: "", MYSTNODES_PASSWORD: "" };
const full = { ...empty, MYSTNODES_EMAIL: "a@example.com", MYSTNODES_PASSWORD: "pw" };
const step = {
  needed: true,
  kind: "firewall-rule",
  ruleName: "CashPilot: block mysterium from private networks (1a2b3c4d)",
  program: "C:\\Users\\u\\AppData\\Roaming\\CashPilot\\native\\mysterium\\myst.exe",
  networks: ["100.64.0.0/10", "169.254.0.0/16"],
};

// Records every call, in order.
function calls({ adminStep = async () => step, consent = true, deploy = async () => "deployed" } = {}) {
  const log = [];
  return {
    log,
    adminStep: () => { log.push("adminStep"); return adminStep(); },
    askConsent: async (s) => { log.push(`askConsent:${s.kind}`); return consent; },
    approve: async () => { log.push("approve"); },
    deploy: () => { log.push("deploy"); return deploy(); },
  };
}

// A service whose container needs two values typed in (Honeygain's shape).
const hg = {
  slug: "honeygain",
  name: "Honeygain",
  description: "",
  docker: {
    image: "honeygain/honeygain:0.8.1",
    env: [
      { key: "HG_EMAIL", label: "Email", required: true },
      { key: "HG_PASSWORD", label: "Password", required: true, secret: true },
      { key: "HG_DEVICE", label: "Device name", required: true, default: "cashpilot-{hostname}" },
    ],
  },
};
const hgFields = serviceFormFields(hg, {});

// RULE 1
{
  const c = calls();
  const outcome = await runCheckedDeploy(hgFields, { HG_EMAIL: "", HG_PASSWORD: " ", HG_DEVICE: "" }, true, c);
  check("RULE 1: empty container values stop the deploy", outcome.kind === "missing", JSON.stringify(outcome));
  check("RULE 1: no call at all, so no admin prompt and no deploy", c.log.length === 0, c.log.join(","));
  const names = (outcome.missing || []).map((f) => f.label);
  check("RULE 1: both are named, the one with a default is not", names.join("|") === "Email|Password", names.join("|"));
  const note = missingFieldsNotice(outcome.missing);
  check("RULE 1: the note names each field", note.includes('data-missing-field="HG_EMAIL"') && note.includes("Password"), note);
  check("RULE 1: the note says nothing ran", note.includes("Nothing was deployed"), note);
  check("RULE 1: the note does not talk about Windows, which macOS and Linux never ask", !note.includes("Windows"), note);
  check("RULE 1: the note is an alert", note.includes('role="alert"'), note);
  check("RULE 1: labels are escaped", missingFieldsNotice([{ key: "k", label: "<b>x</b>" }]).includes("&lt;b&gt;x&lt;/b&gt;"));
}

// RULE 2
{
  const withDefault = [{ key: "A", label: "A", required: true, default: "x" }, { key: "B", label: "B", required: true }];
  const missing = missingRequiredFields(withDefault, { A: "", B: "   " });
  check("RULE 2: a default is never missing; spaces are", missing.map((f) => f.key).join() === "B", JSON.stringify(missing));
  check("RULE 2: an optional empty field is never missing", missingRequiredFields([{ key: "C", label: "C" }], {}).length === 0);
  check("RULE 2: the collector logins come marked as for earnings", fields.filter((f) => f.earnings).map((f) => f.key).join() === "MYSTNODES_EMAIL,MYSTNODES_PASSWORD", JSON.stringify(fields));
  check("RULE 2: empty collector logins are never missing", missingRequiredFields(fields, empty).length === 0);
  const c = calls();
  const outcome = await runCheckedDeploy(fields, empty, true, c);
  check("RULE 2: so MystNodes with its logins empty goes on to the admin-step screen", outcome.kind === "done" && c.log.join(",") === "adminStep,askConsent:firewall-rule,approve,deploy", c.log.join(","));
}

// RULE 3
{
  const cancel = calls({ consent: false });
  const outcome = await runCheckedDeploy(fields, full, true, cancel);
  check("RULE 3: Cancel deploys nothing", outcome.kind === "cancelled", JSON.stringify(outcome));
  check("RULE 3: Cancel approves nothing", cancel.log.join(",") === "adminStep,askConsent:firewall-rule", cancel.log.join(","));

  const go = calls();
  const done = await runCheckedDeploy(fields, full, true, go);
  check("RULE 3: Continue deploys", done.kind === "done" && done.result === "deployed", JSON.stringify(done));
  check("RULE 3: the screen, then the approval, then the deploy", go.log.join(",") === "adminStep,askConsent:firewall-rule,approve,deploy", go.log.join(","));

  const none = calls({ adminStep: async () => ({ needed: false }) });
  await runCheckedDeploy(fields, full, true, none);
  check("RULE 3: no step, no screen", none.log.join(",") === "adminStep,deploy", none.log.join(","));
}

// RULE 4
{
  const html = adminConsentScreen("MystNodes", step);
  const text = adminConsentText("MystNodes", step);
  check("RULE 4: the full PowerShell path", POWERSHELL_PATH === "%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\powershell.exe" && text.prompt.includes(POWERSHELL_PATH), text.prompt);
  check("RULE 4: it says Windows shows a permission prompt naming Windows PowerShell", text.prompt.includes("permission prompt") && text.prompt.includes("Windows PowerShell"), text.prompt);
  check("RULE 4: it says what No does", text.prompt.includes("If you choose No"), text.prompt);
  check("RULE 4: what changes names the rule, the program and the ranges", text.changes.includes(step.ruleName) && text.changes.includes(step.program) && text.changes.includes("100.64.0.0/10") && text.changes.includes("169.254.0.0/16"), text.changes);
  check("RULE 4: what follows", /restart/i.test(text.after), text.after);
  check("RULE 4: how to undo", text.undo.includes("Removing MystNodes") && text.undo.includes("Outbound Rules"), text.undo);
  for (const part of ["changes", "prompt", "after", "undo"]) {
    check(`RULE 4: the screen shows ${part}`, html.includes(`data-consent="${part}"`), html);
  }
  check("RULE 4: Continue and Cancel", html.includes('data-deploy-consent="go">Continue<') && html.includes('data-deploy-consent="cancel">Cancel<'), html);
  check("RULE 4: a dialog with a title", html.includes('role="alertdialog"') && html.includes("Add a Windows Firewall rule for MystNodes?"), html);
  const hostile = adminConsentScreen("<img>", { ...step, ruleName: "<script>", program: "C:\\<x>" });
  check("RULE 4: every value is escaped", !hostile.includes("<script>") && !hostile.includes("<img>") && !hostile.includes("<x>"), hostile);
}

// RULE 5
{
  const routed = calls();
  await runCheckedDeploy(fields, full, false, routed);
  check("RULE 5: a deploy through a proxy asks no admin step", routed.log.join(",") === "deploy", routed.log.join(","));

  const off = "mysterium was not started: it needs a Windows Firewall rule that blocks it from 100.64.0.0/10, and Windows Firewall is off for the network this computer is on, so the rule would do nothing and Windows was not asked for anything. Fix that, then click Deploy again";
  const unreadable = calls({ adminStep: () => Promise.reject(off) });
  const outcome = await runCheckedDeploy(fields, full, true, unreadable);
  check("RULE 5: an unreadable step stops with its reason", outcome.kind === "blocked" && outcome.message === off, JSON.stringify(outcome));
  check("RULE 5: and shows no screen, approves nothing, deploys nothing", unreadable.log.join(",") === "adminStep", unreadable.log.join(","));
  const silent = await runCheckedDeploy(fields, full, true, calls({ adminStep: () => Promise.reject(new Error("")) }));
  check("RULE 5: an empty reason still says something", silent.kind === "blocked" && silent.message.length > 20, JSON.stringify(silent));
}

// RULE 6
{
  // What the Go binding rejects with after a No at the Windows prompt.
  const declined = "mysterium is not running: you chose No at the Windows permission prompt, so CashPilot did not add the Windows Firewall rule that blocks it from 100.64.0.0/10, 169.254.0.0/16, and it never runs without that rule. Nothing else changed on this PC. To run it, click Deploy again and choose Yes at the prompt";
  const rejected = calls({ deploy: () => Promise.reject(declined) });
  let caught = null;
  try {
    await runCheckedDeploy(fields, full, true, rejected);
  } catch (error) {
    caught = error;
  }
  check("RULE 6: the rejection reaches the caller", caught === declined, String(caught));
  const note = deployNotice("failed", deployErrorText(caught));
  check("RULE 6: the note carries the reason", note.includes("you chose No at the Windows permission prompt") && note.includes("click Deploy again"), note);
  check("RULE 6: the note is an alert", note.includes('role="alert"') && note.includes("did not finish"), note);
  check("RULE 6: an Error's message is used", deployErrorText(new Error("boom")) === "boom");
  check("RULE 6: an empty rejection still says something", deployErrorText("").length > 20 && deployErrorText(undefined).length > 20);
  check("RULE 6: text is escaped", !deployNotice("failed", "<b>").includes("<b>"));
}

// RULE 7
{
  const card = renderWizardServiceSetup(myst, collectorFields, "host");
  const deploy = card.indexOf('data-wizard-action="deploy"');
  const slot = card.indexOf('data-deploy-panel="mysterium"');
  const facts = card.indexOf('data-output-slug="mysterium"');
  check("RULE 7: the card has the slot", slot > 0, card);
  check("RULE 7: the slot sits under Deploy and above the facts and output", deploy > 0 && slot > deploy && slot < facts, `${deploy} ${slot} ${facts}`);
  check("RULE 7: the logins say they are for earnings, not *", card.includes("MystNodes email (for earnings)") && card.includes("MystNodes password (for earnings)") && !card.includes("MystNodes email *"), card);
}

if (failures) {
  console.error(`\n${failures} of ${checks} deploy-consent checks failed`);
  process.exit(1);
}
console.log(`deploy_consent: ${checks} checks passed`);
