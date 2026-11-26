#!/usr/bin/env node
// Browser-free checks on the Where box and the worker copies (render/where.ts,
// render/workercopies.ts): a lead Desktop deploys to a CashPilot Docker worker and
// commands its copies, as the web UI does.
//
//   1. THE SETUP CARD OFFERS THIS COMPUTER AND EACH WORKER THAT CAN TAKE A DEPLOY NOW,
//      NAMES ESCAPED, AND KEEPS A PICK ACROSS A REDRAW.
//   2. NO WORKER ONLINE SAYS SO; NO WORKERS AT ALL, OR A SERVICE A WORKER CANNOT RUN, SHOWS NO BOX.
//   3. A DEPLOY TO A WORKER WITH A PROXY ROUTE OR MORE THAN ONE COPY IS REFUSED, NOT DROPPED.
//   4. THE BACKEND'S QUESTIONS ARE PUT TO THE OWNER, AND ONLY A YES SENDS AGAIN;
//      A DECLINED DEPLOY SAVES NOTHING.
//   5. EACH WORKER COPY HAS STOP OR START, RESTART AND REMOVE; A COPY CASHPILOT DID
//      NOT DEPLOY HAS NONE; A WORKER THAT TAKES NO COMMANDS NOW SAYS WHY AND HAS ITS BUTTONS OFF.
//   6. ONLY REMOVE ASKS, WITH THE WEB UI'S WORDING.
//   7. A SERVICE THAT KEEPS ITS WALLET SEED IN A FILE (SIA'S HOSTD) OFFERS NO WORKER:
//      THE BOX SAYS IT RUNS ON THIS COMPUTER AND WHY, AND THE CARD SHOWS THE WALLET
//      CHOICE, NEVER A SEED FIELD.
//   8. A YES TO A REPLACEMENT NAMES THE CONTAINER ITS QUESTION SHOWED. A WORKER THAT
//      FINDS ANOTHER CONTAINER ASKS AGAIN, AND ONLY A NEW YES SENDS AGAIN; AN OLDER
//      WORKER THAT NAMES NONE IS ASKED ONCE, AS BEFORE. A YES FOR ONE CONTAINER IS
//      NEVER SENT AS AN OLDER WORKER'S YES, NOR THE REVERSE: A WORKER ROLLED BACK OR
//      UPDATED WHILE THE DIALOG IS OPEN IS ASKED ABOUT AGAIN.
//
//   node scripts/where_check.mjs      # against ./.harness-build

import { runWorkerDeploy, whereChoice, whereChoiceSection, workerDeployText } from "../.harness-build/render/where.js";
import { copyCommandQuestion, workerCopiesSection } from "../.harness-build/render/workercopies.js";
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
const targets = [
  { id: 4, name: "garage <b>", online: true, reason: "", services: ["honeygain"] },
  { id: 5, name: "attic", online: true, reason: "", services: [] },
  { id: 6, name: "old", online: true, reason: "old joined before this Desktop could send commands. Remove it on the Fleet page.", services: [] },
  { id: 7, name: "away", online: false, reason: "away is offline, so it cannot take commands now.", services: [] },
];

{
  const box = whereChoiceSection(honeygain, targets);
  check("RULE 1: a select carries the slug", /<select data-wizard-where="honeygain">/.test(box), box);
  check("RULE 1: this computer comes first and is the default", /<option value="">This computer<\/option>/.test(box) && !/ selected/.test(box), box);
  check("RULE 1: each ready worker is offered, escaped", box.includes('<option value="4">Worker garage &lt;b&gt; (runs it now)</option>') && box.includes('<option value="5">Worker attic</option>') && !box.includes("<b>"), box);
  check("RULE 1: a worker that cannot take commands is not offered", !box.includes('value="6"') && !box.includes('value="7"'), box);
  check("RULE 1: an online worker that cannot take commands says why", box.includes("old joined before this Desktop could send commands"), box);
  check("RULE 1: the box says the worker keeps no credentials", box.includes("keeps none of them"), box);
  const picked = whereChoiceSection(honeygain, targets, 5);
  check("RULE 1: a pick survives a redraw", /<option value="5" selected>/.test(picked), picked);
  const card = renderWizardServiceSetup(honeygain, {}, "box", null, "", null, null, null, undefined, null, false, targets, 5);
  check("RULE 1: the setup card carries the box and the pick", /<option value="5" selected>/.test(card), card);
  check("RULE 1: the setup card has no box without workers", !renderWizardServiceSetup(honeygain, {}, "box").includes("data-wizard-where"));
  check("RULE 1: a worker id is sent", whereChoice("5") === 5);
  for (const v of ["", null, undefined, "0", "x"]) check(`RULE 1: ${JSON.stringify(v)} is this computer`, whereChoice(v) === null);
}

{
  const none = whereChoiceSection(honeygain, targets.filter((t) => t.id >= 6));
  check("RULE 2: no worker online says so", none.includes("No worker is online.") && !none.includes("<select"), none);
  check("RULE 2: no workers, no box", whereChoiceSection(honeygain, []) === "" && whereChoiceSection(honeygain, null) === "");
  check("RULE 2: a manual-only service has no box", whereChoiceSection({ ...honeygain, manualOnly: true }, targets) === "");
  check("RULE 2: a custom service has no box", whereChoiceSection({ ...honeygain, custom: true }, targets) === "");
}

{
  const offline = { ...targets[1], online: false, reason: "attic is offline." };
  for (const [label, workers] of [["offline", [offline]], ["removed", []], ["another worker online", [offline, targets[0]]]]) {
    const card = renderWizardServiceSetup(honeygain, {}, "box", null, "", null, null, null, undefined, null, false, workers, offline.id);
    check(`RULE 2: a selected worker stays selected when ${label}`,
      /<option value="5" selected[^>]*>/.test(card) && card.includes("This computer") && card.includes("Choose"),
      card.match(/<div class="where-choice">[\s\S]*?<\/div>/)?.[0] || "no Where picker");
  }
}

function calls(answers, confirms) {
  const log = { deploys: [], asked: [] };
  return {
    log,
    confirm: (message) => { log.asked.push(message); return confirms.shift() ?? false; },
    deploy: async (yes) => { log.deploys.push(yes); return answers.shift(); },
  };
}
const ok = { ask: "", message: "", worker: "attic", containerId: "c1", notes: null };

{
  for (const [proxy, copies] of [[3, null], [null, 2]]) {
    const c = calls([ok], []);
    const out = await runWorkerDeploy("honeygain", targets[1], proxy, copies, c);
    check(`RULE 3: route ${proxy} copies ${copies} is refused`, out.kind === "refused" && out.message.includes("Nothing was deployed") && c.log.deploys.length === 0, JSON.stringify(out));
  }
  const c = calls([ok], []);
  const out = await runWorkerDeploy("honeygain", targets[1], null, 1, c);
  check("RULE 3: one copy deploys from the worker's own IP", out.kind === "deployed" && c.log.deploys.length === 1, JSON.stringify(c.log));
}

{
  const identity = { ...ok, ask: "identity_in_use", message: "The Device ID for Ider already runs on first." };
  const stepUp = { ...ok, ask: "step_up_required", message: "Vault keeps a wallet or node identity." };
  let c = calls([identity, stepUp, ok], [true, true]);
  let out = await runWorkerDeploy("ider", targets[1], null, null, c);
  check("RULE 4: both questions are asked, each once", out.kind === "deployed" && c.log.asked.length === 2, JSON.stringify(c.log));
  check("RULE 4: the identity question is the web UI's", c.log.asked[0] === "The Device ID for Ider already runs on first.\n\nDeploy anyway?", c.log.asked[0]);
  check("RULE 4: each yes is sent with the deploy", JSON.stringify(c.log.deploys) === JSON.stringify([
    { confirmed: false, allowSharedIdentity: false, replaceExisting: false, expectedContainer: "", workerIdentity: "" },
    { confirmed: false, allowSharedIdentity: true, replaceExisting: false, expectedContainer: "", workerIdentity: "" },
    { confirmed: true, allowSharedIdentity: true, replaceExisting: false, expectedContainer: "", workerIdentity: "" },
  ]), JSON.stringify(c.log.deploys));
  c = calls([stepUp], [false]);
  out = await runWorkerDeploy("vault", targets[1], null, null, c);
  check("RULE 4: a no sends nothing more", out.kind === "cancelled" && c.log.deploys.length === 1, JSON.stringify(c.log));
  c = calls([stepUp, stepUp], [true]);
  out = await runWorkerDeploy("vault", targets[1], null, null, c);
  check("RULE 4: a question asked again after a yes is refused, not looped", out.kind === "refused" && c.log.deploys.length === 2, JSON.stringify(out));
  const replace = { ...ok, ask: "replace_existing", message: "Redeploy honeygain on attic? The existing container will be replaced, but its volumes are kept." };
  c = calls([replace], [false]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 4: a replacement question from the worker is shown even with a stale picker",
    out.kind === "cancelled" && c.log.asked[0] === replace.message && c.log.deploys.length === 1, JSON.stringify(c.log));
  c = calls([replace, identity, stepUp, ok], [true, true, true]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 4: the replacement approval is separate from the device and image approvals",
    out.kind === "deployed" && c.log.deploys[0].replaceExisting === false
      && c.log.deploys[3].replaceExisting === true && c.log.asked.length === 3, JSON.stringify(c.log));
  c = calls([replace], [false]);
  out = await runWorkerDeploy("honeygain", targets[0], null, null, c);
  check("RULE 4: a redeploy on a worker that runs it asks first",
    out.kind === "cancelled" && c.log.asked[0] === replace.message && c.log.deploys.length === 1, JSON.stringify(c.log));
  const text = workerDeployText({ worker: "attic", notes: ["/data stays on /old, where the running container keeps it, instead of /new"] });
  check("RULE 4: the result names the worker and what it kept", text.startsWith("Deployed to attic.") && text.includes("Kept: /data stays on /old"), text);
}

{
  const view = { reason: "", copies: [
    { slug: "honeygain", status: "running", instance: 1, managed: true },
    { slug: "honeygain", status: "exited", instance: 2, managed: true },
    { slug: "found<x>", status: "running", instance: 1, managed: false },
  ] };
  const html = workerCopiesSection(4, view);
  const buttons = [...html.matchAll(/<button[^>]*data-worker-copy="(\w+)"[^>]*data-instance="(\d+)"[^>]*>/g)].map((m) => `${m[1]}:${m[2]}`);
  check("RULE 5: a running copy has Stop, Restart and Remove; a stopped one Start and Remove", JSON.stringify(buttons) === JSON.stringify(["stop:1", "restart:1", "remove:1", "start:2", "remove:2"]), JSON.stringify(buttons));
  check("RULE 5: buttons name the worker and the service", html.includes('data-worker-id="4" data-slug="honeygain"'), html);
  check("RULE 5: copy 2 is named as a copy", html.includes("honeygain (copy 2)"), html);
  check("RULE 5: a copy CashPilot did not deploy has no buttons and says why", html.includes("found&lt;x&gt;") && html.includes("did not deploy it") && !html.includes('data-slug="found'), html);
  check("RULE 5: no button is off on a worker that takes commands", !html.includes("disabled"), html);
  const off = workerCopiesSection(4, { ...view, reason: "garage is offline, so it cannot take commands now." });
  check("RULE 5: a worker that takes no commands says why", off.includes("garage is offline"), off);
  check("RULE 5: and every button is off", (off.match(/<button/g) || []).length === (off.match(/ disabled>/g) || []).length && off.includes(" disabled>"), off);
  check("RULE 5: no copies, nothing", workerCopiesSection(4, { reason: "", copies: [] }) === "" && workerCopiesSection(4, undefined) === "");
}

{
  const copies = [
    { slug: "honeygain", status: "running", instance: 1, managed: true },
    { slug: "honeygain", status: "running", instance: 2, managed: true },
  ];
  for (const command of ["stop", "start", "restart"]) {
    check(`RULE 6: ${command} asks nothing, as on the web UI`, copyCommandQuestion(command, "honeygain", 1, "garage", copies) === null);
  }
  check("RULE 6: Remove uses the web UI's words and says the other copy goes",
    copyCommandQuestion("remove", "honeygain", 1, "garage", copies) === "Remove honeygain from garage? This will stop and delete the container. Its other copy goes too. Its data is kept for a later deploy.",
    copyCommandQuestion("remove", "honeygain", 1, "garage", copies));
  check("RULE 6: removing copy 2 names it",
    copyCommandQuestion("remove", "honeygain", 2, "garage", copies) === "Remove copy 2 of honeygain from garage? This will stop and delete the container. Its data is kept.");
}

{
  const sia = {
    slug: "sia-hostd", name: "Sia hostd", description: "",
    docker: { image: "ghcr.io/siafoundation/hostd", env: [
      { key: "HOSTD_WALLET_SEED", label: "Wallet seed phrase", required: true, secret: true, description: "", seedFile: { path: "/data/hostd.yml", key: "recoveryPhrase", words: 12 } },
      { key: "HOSTD_API_PASSWORD", label: "Dashboard password", required: true, secret: true, description: "" },
    ] },
  };
  const box = whereChoiceSection(sia, targets, 5);
  check("RULE 7: no worker is offered for a seed-file service", !box.includes("<select") && !box.includes("data-wizard-where=") && !box.includes("<option"), box);
  check("RULE 7: the box says it runs on this computer, and why", box.includes("Where: this computer.") && box.includes("wallet seed phrase in a file") && box.includes("only on this computer"), box);
  check("RULE 7: no workers, no box", whereChoiceSection(sia, []) === "" && whereChoiceSection(sia, null) === "");
  // A pick left from before the redraw cannot bring the worker back.
  const card = renderWizardServiceSetup(sia, {}, "box", null, "", null, null, null, undefined, null, false, targets, 5);
  check("RULE 7: the card has no worker pick", !card.includes("data-wizard-where=") && card.includes("only on this computer"), card);
  check("RULE 7: the card shows the wallet choice", card.includes('data-wallet-choice="sia-hostd"'), card);
  check("RULE 7: the card shows no seed field", !card.includes('data-wizard-env="HOSTD_WALLET_SEED"') && !card.includes("Wallet seed phrase"), card);
}

{
  const id = (n) => String(n).repeat(64).slice(0, 64);
  const named = (n) => ({ ...ok, ask: "replace_existing", expectedContainer: id(n),
    message: `Redeploy honeygain on attic? The existing container (${id(n).slice(0, 12)}) will be replaced, but its volumes are kept.` });
  const identity = { ...ok, ask: "identity_in_use", message: "The Device ID for Ider already runs on first." };
  let c = calls([named(1), ok], [true]);
  let out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: the yes carries the container its question named",
    out.kind === "deployed" && c.log.asked.length === 1 && c.log.asked[0] === named(1).message
      && c.log.deploys[0].replaceExisting === false && c.log.deploys[0].expectedContainer === ""
      && c.log.deploys[1].replaceExisting === true && c.log.deploys[1].expectedContainer === id(1), JSON.stringify(c.log));
  const changed = { ...named(2), message: `The honeygain container on attic changed since you approved replacing it.\n\n${named(2).message}` };
  c = calls([named(1), changed, ok], [true, true]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: a container that changed is asked about again, and the new yes names it",
    out.kind === "deployed" && c.log.asked.length === 2 && c.log.asked[1] === changed.message
      && c.log.deploys.length === 3 && c.log.deploys[2].expectedContainer === id(2), JSON.stringify(c.log));
  c = calls([named(1), changed], [true, false]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: a no to the new container sends nothing more",
    out.kind === "cancelled" && c.log.deploys.length === 2, JSON.stringify(c.log));
  c = calls([named(1), named(1)], [true]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: the same container asked about again after a yes is refused, not looped",
    out.kind === "refused" && c.log.asked.length === 1 && c.log.deploys.length === 2, JSON.stringify(out));
  c = calls([named(1), identity, changed, ok], [true, true, true]);
  out = await runWorkerDeploy("ider", targets[1], null, null, c);
  check("RULE 8: a changed container drops only the replacement yes",
    out.kind === "deployed" && c.log.asked.length === 3 && c.log.deploys[3].allowSharedIdentity === true
      && c.log.deploys[3].expectedContainer === id(2), JSON.stringify(c.log));
  const answers = Array.from({ length: 20 }, (_, n) => named(n + 1));
  c = calls(answers, Array(20).fill(true));
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: a container that keeps changing ends the deploy",
    out.kind === "refused" && out.message.includes("Nothing was deployed") && c.log.deploys.length < 20, JSON.stringify(out));
  const older = { ...ok, ask: "replace_existing", message: "Redeploy honeygain on attic? The existing container will be replaced, but its volumes are kept. Update CashPilot on attic so it can confirm the exact container before replacing it." };
  c = calls([older, ok], [true]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: an older worker is asked once and gets the yes alone",
    out.kind === "deployed" && c.log.asked[0] === older.message
      && c.log.deploys[1].replaceExisting === true && c.log.deploys[1].expectedContainer === "", JSON.stringify(c.log));
  const rolledBack = { ...older, message: `attic no longer names the honeygain container you approved replacing, so that yes does not count.\n\n${older.message}` };
  c = calls([named(1), rolledBack, ok], [true, true]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: a yes for one container is asked again when the worker stops naming it",
    out.kind === "deployed" && c.log.asked.length === 2 && c.log.asked[1] === rolledBack.message
      && c.log.deploys.length === 3 && c.log.deploys[2].replaceExisting === true && c.log.deploys[2].expectedContainer === "", JSON.stringify(c.log));
  c = calls([named(1), rolledBack], [true, false]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: a no to the older-worker question sends nothing more",
    out.kind === "cancelled" && c.log.deploys.length === 2, JSON.stringify(c.log));
  c = calls([older, named(1), ok], [true, true]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: an older worker's yes is asked again when the worker starts naming the container",
    out.kind === "deployed" && c.log.asked.length === 2 && c.log.asked[1] === named(1).message
      && c.log.deploys[2].replaceExisting === true && c.log.deploys[2].expectedContainer === id(1), JSON.stringify(c.log));
  c = calls([older, older], [true]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 8: an older worker that asks again after a yes is refused",
    out.kind === "refused" && c.log.asked.length === 1, JSON.stringify(out));
}

{
  // RULE 9: a yes is about the worker that asked. Every question names a worker
  // identity; the yes carries it back, and a question that says another worker now
  // reports under that name drops every earlier yes before the deploy goes on.
  const id = (n) => String(n).repeat(64).slice(0, 64);
  const named = (n, who) => ({ ...ok, ask: "replace_existing", expectedContainer: id(n), workerIdentity: who,
    message: `Redeploy honeygain on attic? The existing container (${id(n).slice(0, 12)}) will be replaced, but its volumes are kept.` });
  const changedWorker = (who) => ({ ...ok, ask: "worker_changed", workerIdentity: who,
    message: "attic is not the worker you answered about: another CashPilot worker now reports under that name, so your earlier answers do not count. Deploy honeygain to it?" });
  const identity = { ...ok, ask: "identity_in_use", workerIdentity: "client-a@http://10.0.0.5:8081", message: "The Device ID for Ider already runs on first." };
  const a = "client-a@http://10.0.0.5:8081";
  const b = "client-b@http://10.0.0.9:8081";
  let c = calls([named(1, a), ok], [true]);
  let out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 9: the yes carries the worker that asked",
    out.kind === "deployed" && c.log.deploys[0].workerIdentity === "" && c.log.deploys[1].workerIdentity === a, JSON.stringify(c.log));
  c = calls([named(1, a), identity, changedWorker(b), named(2, b), ok], [true, true, true, true]);
  out = await runWorkerDeploy("ider", targets[1], null, null, c);
  check("RULE 9: another worker under the same name is asked about, with the backend's words",
    out.kind === "deployed" && c.log.asked[2] === changedWorker(b).message, JSON.stringify(c.log));
  check("RULE 9: that question drops every earlier yes and binds the next ones to the new worker",
    JSON.stringify(c.log.deploys[3]) === JSON.stringify({ confirmed: false, allowSharedIdentity: false, replaceExisting: false, expectedContainer: "", workerIdentity: b })
      && c.log.deploys[4].replaceExisting === true && c.log.deploys[4].expectedContainer === id(2) && c.log.deploys[4].workerIdentity === b, JSON.stringify(c.log.deploys));
  c = calls([named(1, a), changedWorker(b)], [true, false]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 9: a no to the new worker sends nothing more", out.kind === "cancelled" && c.log.deploys.length === 2, JSON.stringify(c.log));
  c = calls([named(1, a), changedWorker(b), changedWorker(b)], [true, true]);
  out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 9: the same worker question after its yes is refused, not looped",
    out.kind === "refused" && c.log.asked.length === 2 && c.log.deploys.length === 3, JSON.stringify(out));
}

{
  // RULE 10: the last round shows no question: its yes would have no deploy to go with.
  const id = (n) => String(n).repeat(64).slice(0, 64);
  const named = (n) => ({ ...ok, ask: "replace_existing", expectedContainer: id(n % 10) + String(n), message: `question ${n}` });
  const c = calls(Array.from({ length: 20 }, (_, n) => named(n + 1)), Array(20).fill(true));
  const out = await runWorkerDeploy("honeygain", targets[1], null, null, c);
  check("RULE 10: every question shown has a deploy that uses its answer",
    out.kind === "refused" && c.log.deploys.length === 8 && c.log.asked.length === 7, JSON.stringify({ out, asked: c.log.asked.length, deploys: c.log.deploys.length }));
}

if (failures) {
  console.error(`\n${failures} of ${checks} where checks failed`);
  process.exit(1);
}
console.log(`where: ${checks} checks passed`);
