#!/usr/bin/env node
// Browser-free checks on custom services: an image the user adds themselves.
//
//   1. A STORED SECRET'S VALUE IS NEVER IN THE HTML. The backend sends it blank; even
//      if a value slipped through, the editor must not print it. The box is a
//      password box and says a value is stored and blank keeps it.
//   2. EVERY CUSTOM SERVICE SAYS IT IS UNREVIEWED: in the catalog, on the dashboard,
//      in the custom list and on its deploy card.
//   3. THE DEPLOY CARD HAS NO CREDENTIAL INPUTS AND NO IMAGE BOX. A custom service
//      runs its saved definition; anything typed there would be dropped.
//   4. THE DASHBOARD OFFERS NO COLLECT FOR IT AND SHOWS NO BALANCE, never 0.00.
//   5. THE EDITOR'S ROWS BECOME THE DEFINITION: blank rows dropped, a named secret
//      with a blank value kept (blank means keep).
//   6. USER TEXT IS ESCAPED.
//
//   node scripts/custom_check.mjs      # against ./.harness-build

import { customDefinitionFrom, renderCustomEditor, renderCustomServiceCard, renderCustomServicesSection, UNTRUSTED_TEXT } from "../.harness-build/render/custom.js";
import { renderWizardServiceSetup } from "../.harness-build/render/wizard.js";
import { renderCatalogTable } from "../.harness-build/render/catalog.js";
import { renderServicesTable } from "../.harness-build/render/services.js";

let failures = 0;
let checks = 0;
function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const view = {
  slug: "custom-mine",
  name: "Mine",
  image: "org/mine:latest",
  env: [
    { key: "TOKEN", value: "LEAKED-SECRET", secret: true, isSet: true },
    { key: "MODE", value: "fast", secret: false, isSet: true },
  ],
  command: "run",
  dataPath: "/data",
  memLimit: "512m",
  volume: "cashpilot-custom-mine-data",
  warnings: ["No version tag: every redeploy pulls whatever the publisher pushed last. Pin a tag or digest."],
};
const service = {
  name: "Mine", slug: "custom-mine", category: "custom", status: "custom", custom: true, website: "",
  description: "Added by you from org/mine:latest. CashPilot has not reviewed this image.", shortDescription: "Your image: org/mine:latest",
  referral: { signupUrl: "", code: "", program: null, bonus: { referrer: "", referee: "" } },
  docker: { image: "org/mine:latest", env: [], ports: [], volumes: ["cashpilot-custom-mine-data:/data"] },
  requirements: {}, cashout: { method: "", dashboardUrl: "", minAmount: 0, currency: "", notes: "" }, platforms: [], manualOnly: false,
};
const catalogService = {
  ...service, name: "Honeygain", slug: "honeygain", category: "bandwidth", status: "active", custom: false, website: "https://honeygain.com",
  docker: { image: "honeygain/honeygain:1", env: [{ key: "HONEYGAIN_EMAIL", label: "Email", required: true, secret: false, description: "", default: "" }], ports: [], volumes: [] },
};

// RULE 1
const editor = renderCustomEditor(view);
check("RULE 1: the stored secret's value is not in the editor", !editor.includes("LEAKED-SECRET"), editor);
const secretTag = (editor.match(/<input data-custom-env-value[^>]*placeholder="stored, leave blank to keep"[^>]*>/) || [])[0] || "";
check("RULE 1: the secret box is a password box that says a value is stored", /type="password"/.test(secretTag) && /value=""/.test(secretTag), editor);
check("CONTROL: a plain value is shown in the editor", editor.includes('value="fast"'), editor);
check("RULE 1: the list never prints a value either", !renderCustomServicesSection([view], [], null).includes("LEAKED-SECRET"));
check("RULE 1: the editor opened from the list never prints it", !renderCustomServicesSection([view], [], view).includes("LEAKED-SECRET"));

// RULE 2
const catalogHtml = renderCatalogTable([service, catalogService], [], []);
const customRow = catalogHtml.split("<tr>").find((row) => row.includes("custom-mine")) || "";
const catalogRowHtml = catalogHtml.split("<tr>").find((row) => row.includes("data-service=\"honeygain\"")) || "";
check("RULE 2: the catalog marks the custom service unreviewed", customRow.includes("custom, not reviewed") && customRow.includes(UNTRUSTED_TEXT), customRow);
check("CONTROL: a catalog service carries no such mark", catalogRowHtml !== "" && !catalogRowHtml.includes("custom, not reviewed"), catalogRowHtml);
check("RULE 2: the custom list says it", renderCustomServicesSection([view], [], null).includes(UNTRUSTED_TEXT));
check("RULE 2: the custom list flags an image with no version tag", renderCustomServicesSection([view], [], null).includes("no version tag"));
check("RULE 2: the add form says it", renderCustomEditor(null).includes(UNTRUSTED_TEXT));

// RULE 3
const card = renderWizardServiceSetup(service, {}, "box", null, "", { max: 1, reason: "" }, 1);
check("RULE 3: the wizard sends a custom service to its own card", card.includes(UNTRUSTED_TEXT) && card === renderCustomServiceCard(service, { max: 1, reason: "" }, 1), card);
check("RULE 3: the card has no credential input", !card.includes("data-wizard-env"), card);
check("RULE 3: the card has no Image box", !card.includes("data-wizard-image"), card);
check("RULE 3: the card has no save or collect button", !card.includes('data-wizard-action="save"') && !card.includes('data-wizard-action="collect"'), card);
check("RULE 3: the card can deploy", card.includes('data-wizard-action="deploy" data-slug="custom-mine"'), card);
const control = renderWizardServiceSetup(catalogService, {}, "box", null, "", null, 1);
check("CONTROL: a catalog card does have credential inputs and an Image box", control.includes("data-wizard-env") && control.includes("data-wizard-image"), control);

// RULE 4
const deployments = [
  { slug: "custom-mine", containerId: "c1", name: "cashpilot-custom-mine", image: "org/mine:latest", status: "running", runtime: "existing-docker", cpuPercent: 0, memoryMb: 0, customImage: "", instances: 1 },
  { slug: "honeygain", containerId: "c2", name: "cashpilot-honeygain", image: "honeygain/honeygain:1", status: "running", runtime: "existing-docker", cpuPercent: 0, memoryMb: 0, customImage: "", instances: 1 },
];
const table = renderServicesTable({
  services: [service, catalogService], deployments,
  earnings: [{ platform: "custom-mine", balance: 0, currency: "USD", error: "" }],
  displayCurrency: "USD",
});
const rows = table.split('<tr class="breakdown-row">');
const mineRow = rows.find((row) => row.includes("cashpilot-custom-mine") || row.includes(">Mine<")) || "";
const hgRow = rows.find((row) => row.includes(">Honeygain<")) || "";
check("RULE 4: no Collect on the custom row", mineRow !== "" && !mineRow.includes('data-action="collect"') && !/collect/i.test(mineRow.replace(/no collector|CashPilot has no collector/g, "")), mineRow);
check("CONTROL: the catalog row has Collect", /collect/i.test(hgRow), hgRow);
check("RULE 4: the custom row shows no balance, not 0.00", mineRow.includes("&mdash;") && !mineRow.includes("0.00"), mineRow);
check("RULE 2: the dashboard marks it unreviewed", mineRow.includes("custom, not reviewed"), mineRow);

// RULE 5
const def = customDefinitionFrom(
  { name: " Mine ", image: " org/mine:1 ", command: "", dataPath: " /data ", memLimit: "" },
  [{ key: " TOKEN ", value: "", secret: true }, { key: "", value: "", secret: false }, { key: "MODE", value: " x ", secret: false }],
);
check("RULE 5: fields are trimmed", def.name === "Mine" && def.image === "org/mine:1" && def.dataPath === "/data", JSON.stringify(def));
check("RULE 5: a blank row is dropped and a named blank secret is kept", def.env.length === 2 && def.env[0].key === "TOKEN" && def.env[0].value === "" && def.env[0].secret === true, JSON.stringify(def.env));
check("RULE 5: a value is sent as typed, spaces included", def.env[1].value === " x ", JSON.stringify(def.env));

// RULE 6
const evil = { ...view, name: "<img src=x onerror=alert(1)>", image: "a/b:1\"><script>" };
const evilHtml = renderCustomServicesSection([evil], [], evil);
check("RULE 6: names and images are escaped", !evilHtml.includes("<img src=x") && !evilHtml.includes("<script>"), evilHtml);

console.log(`${checks - failures}/${checks} custom-service checks passed`);
if (failures) process.exit(1);
