#!/usr/bin/env node
// Browser-free checks on the questions asked before a service is removed.
//
// THE RULE: removing a service never destroys unrecoverable data as a side effect, and
// the question that CAN destroy it says what is lost, by name.
//
// The old dialog said "this deletes the managed container and its Docker volumes" and
// then deleted them. For Mysterium that volume is the node keystore; for Storj it is an
// identity that takes hours of proof-of-work to regenerate and whose loss forfeits the
// held payout balance. One confirm, no backup, gone.
//
//   node scripts/removal_check.mjs      # against ./.harness-build

import { deletableVolumes, deleteDataConfirmText, planHasCritical, removeConfirmText } from "../.harness-build/render/removal.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function plan(overrides = {}) {
  return {slug: "mysterium", name: "cashpilot-mysterium", volumes: null, binds: null, catalogKnown: true, ...overrides};
}

const keystore = {
  target: "/var/lib/mysterium-node",
  source: "mysterium-data",
  volume: true,
  critical: true,
  holds: "Node identity keystore. Destroying it creates a brand-new identity.",
};

const scratch = {target: "/cache", source: "scratch-data", volume: true, critical: false, holds: ""};

const bigDisk = {target: "/app/config", source: "/Volumes/Storj", volume: false, critical: false, holds: ""};

// --- The first question never promises to delete the data --------------------------

const withData = plan({volumes: [keystore]});
const firstQuestion = removeConfirmText("Mysterium", withData);
check(
  "the remove question says the data stays",
  /stays on this computer/i.test(firstQuestion),
  firstQuestion,
);
check(
  "the remove question does not claim to delete volumes",
  !/delete[^.]*volume/i.test(firstQuestion),
  firstQuestion,
);

// A service with nothing stored gets the short version rather than a promise about
// data it does not have.
const bare = removeConfirmText("EarnFM", plan());
check("a service with no stored data is not told its data survives", !/stays on this computer/i.test(bare), bare);

// A host folder is named, so a user who pointed Storj at a 12 TB disk can see it is
// not in scope.
const withBind = removeConfirmText("Storj", plan({volumes: [keystore], binds: [bigDisk]}));
check("host folders are named in the remove question", withBind.includes("/Volumes/Storj"), withBind);

// --- The second question is separate, and names what is lost ------------------------

const dataQuestion = deleteDataConfirmText("Mysterium", withData);
check("a service with a named volume gets a second question", dataQuestion !== null);
check("the second question names the volume", dataQuestion.includes("mysterium-data"), dataQuestion);
check("the second question names the container path", dataQuestion.includes("/var/lib/mysterium-node"), dataQuestion);
check(
  "the second question repeats what the catalog says is lost",
  dataQuestion.includes("Node identity keystore"),
  dataQuestion,
);
check("the second question says it cannot be recovered", /cannot be recovered/i.test(dataQuestion), dataQuestion);
check("the second question says Cancel keeps the data", /cancel to keep it/i.test(dataQuestion), dataQuestion);

// A service whose data is merely re-downloadable must not be dressed up as
// irreversible: a warning that cries wolf on a cache is a warning nobody reads on a
// keystore.
const scratchQuestion = deleteDataConfirmText("Some Service", plan({volumes: [scratch]}));
check(
  "a non-critical volume is not called unrecoverable",
  !/cannot be recovered/i.test(scratchQuestion),
  scratchQuestion,
);
check("a non-critical volume still names itself", scratchQuestion.includes("scratch-data"), scratchQuestion);

// Nothing deletable means no second question at all: a dialog whose answer changes
// nothing teaches people to click through dialogs.
check("no named volume means no second question", deleteDataConfirmText("EarnFM", plan()) === null);
check(
  "a bind-only service gets no second question",
  deleteDataConfirmText("Storj", plan({binds: [bigDisk]})) === null,
);

// --- planHasCritical drives the extra yes the backend demands -----------------------

check("a critical volume is reported as critical", planHasCritical(withData));
check("a plain volume is not", !planHasCritical(plan({volumes: [scratch]})));
check("a mixed plan is critical", planHasCritical(plan({volumes: [scratch, keystore]})));
check("an empty plan is not critical", !planHasCritical(plan()));

// --- A null slice from Go must not be read as a missing field -----------------------

check("null volumes read as none", deletableVolumes(plan()).length === 0);
check("null binds do not throw", removeConfirmText("EarnFM", plan({volumes: [scratch]})).length > 0);

if (failures > 0) {
  console.error(`\n${failures} of ${checks} removal checks FAILED`);
  process.exit(1);
}
console.log(`removal_check: ${checks} checks passed`);
