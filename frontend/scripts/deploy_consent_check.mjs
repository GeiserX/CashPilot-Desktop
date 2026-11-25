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
//   8. THE SLOT NEVER STAYS ON "Deploying..." ONCE THE CALL ENDED: a deploy, a
//      question the deploy asks later and Cancel, a refusal, a rejection and a
//      Sia wallet deploy (its seed notice and its no-phrase refusal) all end with
//      their own answer there.
//   9. A CONTINUE SERVES ONE DEPLOY: the approved step is passed with that call.
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
  runDeployWithStatus,
} from "../.harness-build/render/deploy_consent.js";
import { runWorkerDeploy, workerDeployText } from "../.harness-build/render/where.js";
import { walletChoiceRefusal, walletDeployAnswer } from "../.harness-build/render/wallet.js";
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

// Records every call, in order, and every note written next to Deploy.
const DEPLOYED = { kind: "ok", text: "Deployed." };
function calls({ adminStep = async () => step, consent = true, deploy = async () => DEPLOYED } = {}) {
  const log = [];
  const said = [];
  return {
    log,
    said,
    adminStep: () => { log.push("adminStep"); return adminStep(); },
    askConsent: async (s) => { log.push(`askConsent:${s.kind}`); return consent; },
    say: (kind, text) => { log.push(`say:${kind}`); said.push({ kind, text }); },
    deploy: (approval) => { log.push(approval ? "deploy:approved" : "deploy"); return deploy(approval); },
  };
}
const BUSY = "Deploying MystNodes...";

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
  const outcome = await runCheckedDeploy(hgFields, { HG_EMAIL: "", HG_PASSWORD: " ", HG_DEVICE: "" }, true, BUSY, c);
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
  const outcome = await runCheckedDeploy(fields, empty, true, BUSY, c);
  check("RULE 2: so MystNodes with its logins empty goes on to the admin-step screen", outcome.kind === "done" && c.log.join(",") === "adminStep,askConsent:firewall-rule,say:busy,deploy:approved,say:ok", c.log.join(","));
}

// RULE 3
{
  const cancel = calls({ consent: false });
  const outcome = await runCheckedDeploy(fields, full, true, BUSY, cancel);
  check("RULE 3: Cancel deploys nothing", outcome.kind === "cancelled", JSON.stringify(outcome));
  check("RULE 3: Cancel approves nothing", cancel.log.join(",") === "adminStep,askConsent:firewall-rule", cancel.log.join(","));

  const go = calls();
  const done = await runCheckedDeploy(fields, full, true, BUSY, go);
  check("RULE 3: Continue deploys", done.kind === "done" && done.answer === DEPLOYED, JSON.stringify(done));
  check("RULE 3: the screen, then the approval, then the deploy", go.log.join(",") === "adminStep,askConsent:firewall-rule,say:busy,deploy:approved,say:ok", go.log.join(","));

  const none = calls({ adminStep: async () => ({ needed: false }) });
  await runCheckedDeploy(fields, full, true, BUSY, none);
  check("RULE 3: no step, no screen", none.log.join(",") === "adminStep,say:busy,deploy,say:ok", none.log.join(","));
}

// RULE 4
{
  const html = adminConsentScreen("MystNodes", step);
  const text = adminConsentText("MystNodes", step);
  check("RULE 4: the full PowerShell path", POWERSHELL_PATH === "%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\powershell.exe" && text.prompt.includes(POWERSHELL_PATH), text.prompt);
  check("RULE 4: it says Windows shows a permission prompt naming Windows PowerShell", text.prompt.includes("permission prompt") && text.prompt.includes("Windows PowerShell"), text.prompt);
  check("RULE 4: it says what No does", text.prompt.includes("If you choose No") && text.prompt.includes("CashPilot adds no rule and MystNodes does not run"), text.prompt);
  // A redeploy stops the running node before the prompt shows, so No does change
  // something: the screen says so and never promises that nothing changes.
  check("RULE 4: No never promises that nothing changes", !/nothing changes/i.test(text.prompt), text.prompt);
  check("RULE 4: No says a running node stays stopped", text.prompt.includes("If MystNodes was running, the deploy stopped it before the prompt, and it stays stopped"), text.prompt);
  check("RULE 4: what changes names the rule, the program and the ranges", text.changes.includes(step.ruleName) && text.changes.includes(step.program) && text.changes.includes("100.64.0.0/10") && text.changes.includes("169.254.0.0/16"), text.changes);
  check("RULE 4: the changes do not promise that the rest of the PC is unchanged", !/nothing else/i.test(text.changes), text.changes);
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
  await runCheckedDeploy(fields, full, false, BUSY, routed);
  check("RULE 5: a deploy through a proxy asks no admin step", routed.log.join(",") === "say:busy,deploy,say:ok", routed.log.join(","));

  const off = "mysterium was not started: it needs a Windows Firewall rule that blocks it from 100.64.0.0/10, and Windows Firewall is off for the network this computer is on, so the rule would do nothing. Fix that, then click Deploy again";
  const unreadable = calls({ adminStep: () => Promise.reject(off) });
  const outcome = await runCheckedDeploy(fields, full, true, BUSY, unreadable);
  check("RULE 5: an unreadable step stops with its reason", outcome.kind === "blocked" && outcome.message === off, JSON.stringify(outcome));
  check("RULE 5: and shows no screen, approves nothing, deploys nothing", unreadable.log.join(",") === "adminStep", unreadable.log.join(","));
  const silent = await runCheckedDeploy(fields, full, true, BUSY, calls({ adminStep: () => Promise.reject(new Error("")) }));
  check("RULE 5: an empty reason still says something", silent.kind === "blocked" && silent.message.length > 20, JSON.stringify(silent));
}

// RULE 6
{
  // What the Go binding rejects with after a No at the Windows prompt.
  const declined = "mysterium is not running: you chose No at the Windows permission prompt, so CashPilot did not add the Windows Firewall rule that blocks it from 100.64.0.0/10, 169.254.0.0/16, and it never runs without that rule. If it was running before this deploy, CashPilot stopped it for the deploy, and it stays stopped. To run it, click Deploy again and choose Yes at the prompt";
  const rejected = calls({ deploy: () => Promise.reject(declined) });
  const outcome = await runCheckedDeploy(fields, full, true, BUSY, rejected);
  const last = rejected.said.at(-1);
  check("RULE 6: the rejection reaches the slot as a failure, with its reason", outcome.kind === "done" && last?.kind === "failed" && last.text === declined, JSON.stringify(rejected.said));
  const note = deployNotice("failed", last?.text);
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

// RULE 8
{
  const ends = {
    deployed: async () => DEPLOYED,
    "a later confirm cancelled": async () => ({ kind: "cancelled", text: "Nothing was deployed." }),
    refused: async () => ({ kind: "cancelled", text: "A deploy to a worker runs one copy. Nothing was deployed." }),
    rejected: () => Promise.reject(new Error("boom")),
    // Sia's hostd: what runWalletDeploy answers (render/wallet.ts).
    "a new Sia wallet": async () => walletDeployAnswer({ created: true, migrated: false, address: "a", phrase: "" }, true),
    "Sia with no phrase typed": async () => walletChoiceRefusal({ mode: "import", phrase: "  " }),
  };
  for (const [name, deploy] of Object.entries(ends)) {
    for (const direct of [true, false]) {
      const c = calls({ deploy });
      await runCheckedDeploy(fields, full, direct, BUSY, c);
      const first = c.said[0];
      const last = c.said.at(-1);
      check(`RULE 8 (${name}, ${direct ? "direct" : "routed"}): "Deploying..." shows while it runs`, first?.kind === "busy" && first.text === BUSY, JSON.stringify(c.said));
      check(`RULE 8 (${name}, ${direct ? "direct" : "routed"}): the slot ends on the answer, not "Deploying..."`, c.said.length >= 2 && last.kind !== "busy" && last.text && last.text !== BUSY, JSON.stringify(c.said));
    }
  }
  const seed = calls({ deploy: ends["a new Sia wallet"] });
  await runCheckedDeploy(fields, full, true, BUSY, seed);
  check("RULE 8: the one-time seed notice is the slot's last word", seed.said.at(-1)?.kind === "ok" && seed.said.at(-1).text.includes("Write its seed phrase down now"), JSON.stringify(seed.said));
  const noPhrase = calls({ deploy: ends["Sia with no phrase typed"] });
  await runCheckedDeploy(fields, full, true, BUSY, noPhrase);
  check("RULE 8: the no-phrase refusal is the slot's last word, and says nothing ran", noPhrase.said.at(-1)?.kind === "cancelled" && noPhrase.said.at(-1).text.includes("Type the seed phrase") && noPhrase.said.at(-1).text.includes("Nothing was deployed"), JSON.stringify(noPhrase.said));
  check("RULE 8: a phrase typed is no refusal", walletChoiceRefusal({ mode: "import", phrase: "abandon about" }) === null && walletChoiceRefusal({ mode: "generate", phrase: "" }) === null && walletChoiceRefusal({ mode: "keep", phrase: "" }) === null);
  const answers = [
    walletDeployAnswer({ created: false, migrated: true, address: "" }, false).text,
    walletDeployAnswer({ created: true, migrated: false, address: "" }, false).text,
    walletDeployAnswer({ created: false, migrated: false, address: "" }, false).text,
    walletDeployAnswer(null, false).text,
  ];
  check("RULE 8: each wallet outcome says its own thing", new Set(answers).size === 4 && answers[0].includes("data volume") && answers[1].includes("kept no copy") && answers[2].includes("kept its wallet") && answers[3] === "Deployed.", JSON.stringify(answers));
  check("RULE 8: every wallet answer is a success", walletDeployAnswer(null, true).kind === "ok" && walletDeployAnswer({ created: true }, false).kind === "ok");
}

// RULE 9
{
  let received;
  const c = calls({ deploy: async (approval) => { received = approval; return DEPLOYED; } });
  await runCheckedDeploy(fields, full, true, BUSY, c);
  check("RULE 9: only the deploy call receives the firewall change from Continue", received === step, JSON.stringify(received));
  const noStep = calls({ adminStep: async () => ({ needed: false }) });
  await runCheckedDeploy(fields, full, true, BUSY, noStep);
  check("RULE 9: a deploy that needs no step receives no approval", noStep.log.includes("deploy") && !noStep.log.includes("deploy:approved"), noStep.log.join(","));
  const cancelled = calls({ consent: false });
  await runCheckedDeploy(fields, full, true, BUSY, cancelled);
  check("RULE 9: Cancel on the screen sends no deploy", !cancelled.log.some((entry) => entry.startsWith("deploy")), cancelled.log.join(","));
}

// A selected worker owns replacement and credential validation, even with a
// local copy and blank fields. The actual worker path decides whether to replace.
{
  const worker = { id: 23, name: "garage", online: true, services: ["honeygain"] };
  const sent = [];
  const c = calls();
  c.confirmReplace = () => { throw new Error("asked about this computer's copy"); };
  c.deploy = async (approval, selected) => {
    sent.push([approval, selected?.id]);
    return DEPLOYED;
  };
  const result = await runCheckedDeploy(serviceFormFields(hg), { HG_EMAIL: "", HG_PASSWORD: "" }, true, BUSY, c, {
    workerId: 23, workers: [worker], localReplacement: "Replace this computer's copy?",
  });
  check("worker deploy reaches the chosen worker with blank fields and no local approval",
    result.kind === "done" && result.answer.kind === "ok" && sent[0]?.[0] === null && sent[0]?.[1] === 23,
    JSON.stringify({ result, sent }));
  const missing = calls();
  const refused = await runCheckedDeploy(fields, full, true, BUSY, missing, { workerId: 23, workers: [] });
  check("a missing worker never falls back to this computer", refused.kind === "blocked" && missing.log.length === 0, JSON.stringify({ refused, log: missing.log }));
}

// Declining an ordinary replacement says only what happened to the deploy.
{
  const c = calls();
  c.confirmReplace = () => false;
  await runCheckedDeploy(fields, full, true, BUSY, c, { localReplacement: "Replace this computer's copy?" });
  check("declining a replacement writes a cancellation without any Windows message",
    c.said.at(-1)?.kind === "cancelled" && c.said.at(-1)?.text === "Nothing was deployed." && !c.log.includes("adminStep"),
    JSON.stringify(c.said));
}

// An invalid wizard value replaces any previous status beside Deploy.
{
  const c = calls();
  c.said.push(DEPLOYED);
  await runCheckedDeploy(fields, full, true, BUSY, c, { invalidField: "API_KEY" });
  check("invalid wizard field replaces old success with an alert and stops the deploy", c.said.at(-1)?.kind === "failed" && c.said.at(-1)?.text.includes("API_KEY") && !c.log.includes("adminStep"), JSON.stringify(c.said));
}

{
  const replacement = adminConsentText("MystNodes", { ...step, replaceRule: true });
  check("same-name firewall replacement describes removing and recreating one rule",
    replacement.title.startsWith("Replace") && /removes.*recreates/.test(replacement.changes)
      && replacement.prompt.includes("shows a permission prompt."),
    JSON.stringify(replacement));
}

for (const continueImport of [false, true]) {
  const trace = [];
  await runCheckedDeploy(hgFields, { HG_EMAIL: "saved@example.com" }, true, BUSY, {
    confirmReplace: async () => {
      await Promise.resolve();
      trace.push("import confirmation");
      return continueImport;
    },
    adminStep: async () => { trace.push("firewall check"); return step; },
    askConsent: async () => { trace.push("CashPilot consent"); return true; },
    say() {},
    deploy: async (approval) => {
      trace.push(approval === step ? "deploy with approval" : "unapproved deploy");
      return { kind: "ok", text: "Deployed." };
    },
  }, { localReplacement: "Deploy this imported service?", savedSecrets: { HG_PASSWORD: true } });
  const expected = continueImport
    ? ["import confirmation", "firewall check", "CashPilot consent", "deploy with approval"]
    : ["import confirmation"];
  check(`Import confirmation ${continueImport} precedes the firewall check and any deploy`,
    JSON.stringify(trace) === JSON.stringify(expected), JSON.stringify(trace));
}

{
  const trace = [];
  const say = (kind, text) => trace.push([kind, text]);
  const answer = await runDeployWithStatus(BUSY, say, async () => ({
    ...walletDeployAnswer({ created: true }, true),
    followUp: {
      failure: "CashPilot could not refresh the app.",
      run: async () => {
        check("the new-wallet result is visible when refresh starts", trace[1]?.[0] === "ok" && trace[1][1].includes("seed phrase"), JSON.stringify(trace));
        throw new Error("state unavailable");
      },
    },
  }));
  check("a refresh failure keeps the Sia success and seed notice",
    answer.kind === "ok" && answer.text.includes("seed phrase") && answer.text.includes("state unavailable"), JSON.stringify(trace));
}

{
  const trace = [];
  const answer = await runDeployWithStatus(BUSY, (kind, text) => trace.push([kind, text]), async () => {
    const deployed = await runWorkerDeploy("honeygain", { id: 2, name: "attic", online: true, reason: "", services: [] }, null, null, {
      confirm: () => false,
      deploy: async () => ({ ask: "", worker: "attic", credentialWarning: "CashPilot could not save the credentials on this computer." }),
    });
    return { kind: "ok", text: workerDeployText(deployed.result) };
  });
  check("a local save failure keeps the worker success",
    answer.kind === "ok" && answer.text.includes("Deployed to attic.") && answer.text.includes("CashPilot could not save the credentials"), JSON.stringify(trace));
}

if (failures) {
  console.error(`\n${failures} of ${checks} deploy-consent checks failed`);
  process.exit(1);
}
console.log(`deploy_consent: ${checks} checks passed`);
