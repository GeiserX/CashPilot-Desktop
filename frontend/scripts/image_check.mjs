#!/usr/bin/env node
// Browser-free checks on the Image box under a service's Advanced section and the
// "custom image" badge.
//
//   1. THE BOX STARTS WITH THE RECORDED CHOICE, AND LEFT ALONE IT SENDS null. The
//      backend reads null as "keep what this deployment runs", so a redeploy from a
//      card nobody opened cannot drop a choice; "" is the explicit way back to the
//      catalog image.
//   2. THE BOX IS NOT A CREDENTIAL. readWizardForm() collects every data-wizard-env
//      input into the saved credentials, so an Image box carrying that attribute would
//      store the image as a credential and send it to the container as an env var.
//   3. THE HINT NAMES THE CATALOG IMAGE AND WHAT A CUSTOM IMAGE GETS. It receives the
//      service's credentials, so the user is told before typing one.
//   4. A MANUAL SERVICE HAS NO BOX. It has no image to replace.
//   5. THE BADGE SHOWS ONLY ON A CHOSEN IMAGE, AND ESCAPES IT.
//
//   node scripts/image_check.mjs      # against ./.harness-build

import { customImageBadge, imageChoice, imageChoiceSection } from "../.harness-build/render/image.js";
import { renderWizardServiceSetup } from "../.harness-build/render/wizard.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function imageInput(html) {
  const tag = (html.match(/<input\b[^>]*\bdata-wizard-image="[^"]*"[^>]*>/) || [])[0] || "";
  const attr = (name) => (tag.match(new RegExp(`\\b${name}="([^"]*)"`)) || [])[1];
  return { tag, value: attr("value"), initial: attr("data-initial"), placeholder: attr("placeholder"), env: attr("data-wizard-env") };
}

const honeygain = {
  name: "Honeygain",
  slug: "honeygain",
  category: "bandwidth",
  status: "active",
  website: "https://honeygain.com",
  description: "Bandwidth sharing",
  shortDescription: "Bandwidth sharing",
  referral: { signupUrl: "", code: "", program: null, bonus: { referrer: "", referee: "" } },
  docker: { image: "honeygain/honeygain@sha256:abc", env: [{ key: "HONEYGAIN_EMAIL", label: "Email", required: true, secret: false, description: "Email", default: "" }], ports: [], volumes: [] },
  requirements: { residentialIp: true },
  cashout: { method: "redirect", dashboardUrl: "", minAmount: 0, currency: "USD", notes: "" },
  manualOnly: false,
};

// RULE 1
const fresh = imageInput(imageChoiceSection(honeygain, ""));
check("CONTROL: the section renders an Image box", fresh.tag !== "", imageChoiceSection(honeygain, ""));
check("RULE 1: with no choice the box is empty and shows the catalog image as placeholder",
  fresh.value === "" && fresh.initial === "" && fresh.placeholder === "honeygain/honeygain@sha256:abc", fresh.tag);
const chosenHtml = imageChoiceSection(honeygain, "honeygain/honeygain:0.8.1");
const chosen = imageInput(chosenHtml);
check("RULE 1: a recorded choice fills the box and is its initial value",
  chosen.value === "honeygain/honeygain:0.8.1" && chosen.initial === "honeygain/honeygain:0.8.1", chosen.tag);
check("RULE 1: a recorded choice opens the section, so it is never hidden", /<details class="image-choice" open>/.test(chosenHtml), chosenHtml);
check("RULE 1: no choice leaves the section closed", !/<details class="image-choice" open>/.test(imageChoiceSection(honeygain, "")));
check("RULE 1: a box left as it was sends null", imageChoice("honeygain/honeygain:0.8.1", "honeygain/honeygain:0.8.1") === null);
check("RULE 1: an untouched empty box sends null too", imageChoice("", "") === null);
check("RULE 1: clearing a chosen image sends \"\"", imageChoice("  ", "honeygain/honeygain:0.8.1") === "");
check("RULE 1: a typed image is sent trimmed", imageChoice(" mirror.test/hg:1 ", "") === "mirror.test/hg:1");

// RULE 2
const card = renderWizardServiceSetup(honeygain, {}, "macmini", null, "honeygain/honeygain:0.8.1");
const inCard = imageInput(card);
check("RULE 2: the wizard card carries the Image box with the recorded choice", inCard.value === "honeygain/honeygain:0.8.1", card);
check("RULE 2: the Image box never carries data-wizard-env", inCard.tag !== "" && inCard.env === undefined, inCard.tag);

// RULE 3
const hint = imageChoiceSection(honeygain, "");
check("RULE 3: the hint names the catalog image", hint.includes("<code>honeygain/honeygain@sha256:abc</code>"), hint);
check("RULE 3: the hint says the image gets the credentials and nothing extra",
  hint.includes("receives your Honeygain credentials") && hint.includes("no extra capabilities, devices or ports"), hint);
check("RULE 3: the hint says the choice is kept until cleared", hint.includes("keeps it on every redeploy until you clear this box"), hint);

// RULE 4
check("RULE 4: a manual service has no Image box", imageChoiceSection({ ...honeygain, manualOnly: true }, "") === "");

// RULE 5
check("RULE 5: no badge on the catalog image", customImageBadge("") === "" && customImageBadge(undefined) === "");
const badge = customImageBadge('evil"/><script>');
check("RULE 5: a chosen image gets the badge", badge.includes("custom image"), badge);
check("RULE 5: the image in the badge's title is escaped", !badge.includes("<script>") && badge.includes("&quot;"), badge);

if (failures > 0) {
  console.error(`\n${failures} of ${checks} image checks FAILED`);
  process.exit(1);
}
console.log(`image checks: ${checks} passed`);
