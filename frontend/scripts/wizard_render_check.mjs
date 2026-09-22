#!/usr/bin/env node
// Browser-free checks on the HTML the wizard's credential form actually renders.
//
// scripts/fields_check.mjs already proves the right LIST of credentials is chosen.
// This file proves the list survives the trip into markup, which is where the
// damage a user can see actually happens:
//
//   1. EVERY FIELD GETS AN INPUT, AND EVERY INPUT CARRIES data-wizard-env.
//      readWizardForm() and hydrateWizardForm() both select on that attribute, so an
//      input without it is typed into, reads as saved, and is silently discarded.
//   2. A SECRET IS MASKED. type="text" on a password puts it on screen in plain
//      sight, in a window people screen-share while setting a provider up.
//   3. A {hostname} DEFAULT IS EXPANDED. Shipping the literal token names the
//      device "cashpilot-{hostname}" in the provider's dashboard forever.
//   4. REQUIRED IS MARKED. The asterisk is the only thing telling someone which
//      boxes they cannot leave empty before Deploy.
//   5. NO CREDENTIAL VALUE IS EVER WRITTEN INTO THE MARKUP. This function is not
//      given the saved credentials and must not grow a path to them: saved values
//      arrive later, through the DOM, in hydrateWizardForm().
//
// The services below are the real catalog entries (services/bandwidth/*.yml) plus the
// backend's collector fields (internal/collectors/fields.go), because the interesting
// cases are real: Honeygain has the {hostname} default and an optional field, and
// Repocket's collector needs an account password the container never sees.
//
//   node scripts/wizard_render_check.mjs      # against ./.harness-build

import { renderWizardServiceSetup } from "../.harness-build/render/wizard.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

/** Every <input> in the card, with the attributes the app depends on. */
function inputs(html) {
  return [...html.matchAll(/<input\b[^>]*>/g)].map((match) => {
    const tag = match[0];
    const attr = (name) => (tag.match(new RegExp(`\\b${name}="([^"]*)"`)) || [])[1];
    return {
      tag,
      env: attr("data-wizard-env"),
      type: attr("type"),
      value: attr("value"),
      placeholder: attr("placeholder"),
    };
  });
}

/** The <span> label text of the field with this key, asterisk and all. */
function labelOf(html, key) {
  const label = html.split(`data-wizard-env="${key}"`)[0];
  const spans = [...label.matchAll(/<span>([^<]*)<\/span>/g)];
  return spans.length ? spans[spans.length - 1][1] : "";
}

function envVar(key, extra = {}) {
  return { key, label: key, required: true, secret: false, description: key, default: "", ...extra };
}

/** A catalog entry in the shape ListServices() returns. */
function service(overrides = {}) {
  return {
    name: "Service",
    slug: "service",
    category: "bandwidth",
    status: "active",
    website: "https://example.com",
    description: "A service.",
    shortDescription: "A service.",
    referral: { signupUrl: "", code: "", program: null, bonus: { referrer: "", referee: "" } },
    docker: { image: "example@sha256:abc", env: [], ports: [], volumes: [] },
    requirements: { residentialIp: true },
    cashout: { method: "redirect", dashboardUrl: "", minAmount: 0, currency: "USD", notes: "" },
    manualOnly: false,
    ...overrides,
  };
}

// services/bandwidth/honeygain.yml: a secret, a non-secret, and an OPTIONAL field
// whose catalog default carries {hostname}.
const honeygain = service({
  name: "Honeygain",
  slug: "honeygain",
  shortDescription: "Bandwidth sharing",
  website: "https://honeygain.com",
  referral: { signupUrl: "https://r.honeygain.me/CASHPILOT", code: "CASHPILOT", program: true, bonus: {} },
  cashout: { dashboardUrl: "https://dashboard.honeygain.com" },
  docker: {
    image: "honeygain/honeygain@sha256:abc",
    env: [
      envVar("HONEYGAIN_EMAIL", { label: "Email", description: "Your Honeygain account email" }),
      envVar("HONEYGAIN_PASSWORD", { label: "Password", secret: true, description: "Your Honeygain account password" }),
      envVar("HONEYGAIN_DEVICE_NAME", {
        label: "Device name",
        required: false,
        description: "Name shown in your Honeygain dashboard",
        default: "cashpilot-{hostname}",
      }),
    ],
  },
});

// services/bandwidth/repocket.yml: the container takes an API key, and the catalog
// description for it is a sentence with an <a href> link in it.
const repocket = service({
  name: "Repocket",
  slug: "repocket",
  shortDescription: "Bandwidth sharing - max 5 devices, API key auth",
  website: "https://repocket.com",
  docker: {
    image: "repocket/repocket@sha256:abc",
    env: [
      envVar("RP_EMAIL", { label: "Email", description: "Your Repocket account email" }),
      envVar("RP_API_KEY", {
        label: "API Key",
        secret: true,
        description: 'Your Repocket API key — log in at <a href="https://app.repocket.com/b">app.repocket.com</a> and copy it',
      }),
    ],
  },
});

// internal/collectors/fields.go: what the EARNINGS COLLECTOR signs in with, which for
// Repocket is an account password the container never sees.
const collectorFields = {
  repocket: [
    {
      key: "REPOCKET_EMAIL",
      label: "Repocket account email",
      description: "Email used for app.repocket.com",
      required: true,
    },
    {
      key: "REPOCKET_PASSWORD",
      label: "Repocket account password",
      description: "Account password for app.repocket.com — NOT the API key",
      secret: true,
      required: true,
    },
  ],
};

// ---------------------------------------------------------------------------
// CONTROL: the card renders at all. Without this every "is absent" check below
// would pass just as happily against an empty string.
// ---------------------------------------------------------------------------
const hg = renderWizardServiceSetup(honeygain, collectorFields, "macmini");
check("CONTROL: the card renders", hg.includes('<article class="wizard-setup-card"'), hg);
check("CONTROL: it is the card for this service", hg.includes('data-form-slug="honeygain"'), hg);
check("CONTROL: it renders inputs", inputs(hg).length > 0, hg);

// ---------------------------------------------------------------------------
// RULE 1: every field gets an input, and every input carries data-wizard-env.
// ---------------------------------------------------------------------------
const hgKeys = inputs(hg).map((input) => input.env);
check(
  "RULE 1: every docker.env variable gets its own input",
  hgKeys.join(",") === "HONEYGAIN_EMAIL,HONEYGAIN_PASSWORD,HONEYGAIN_DEVICE_NAME",
  `rendered inputs: ${JSON.stringify(hgKeys)}`,
);
check(
  "RULE 1: every input carries data-wizard-env, or save and hydrate cannot see it",
  inputs(hg).every((input) => typeof input.env === "string" && input.env.length > 0),
  inputs(hg)
    .map((input) => input.tag)
    .join("\n      "),
);

const rp = renderWizardServiceSetup(repocket, collectorFields, "macmini");
const rpKeys = inputs(rp).map((input) => input.env);
check(
  "RULE 1: the collector's own credentials get inputs too, after the container's",
  rpKeys.join(",") === "RP_EMAIL,RP_API_KEY,REPOCKET_EMAIL,REPOCKET_PASSWORD",
  `rendered inputs: ${JSON.stringify(rpKeys)}`,
);
check(
  "RULE 1: a service whose collector needs nothing renders only docker.env",
  inputs(renderWizardServiceSetup(repocket, {}, "macmini"))
    .map((input) => input.env)
    .join(",") === "RP_EMAIL,RP_API_KEY",
);
check(
  "RULE 1: no backend map at all still renders the deploy form",
  inputs(renderWizardServiceSetup(repocket, null, "macmini")).length === 2,
);

// ---------------------------------------------------------------------------
// RULE 2: a secret is masked, and only a secret.
// ---------------------------------------------------------------------------
const typeOf = (html, key) => inputs(html).find((input) => input.env === key)?.type;
check("RULE 2: a secret docker.env field renders masked", typeOf(hg, "HONEYGAIN_PASSWORD") === "password", hg);
check("RULE 2: a secret collector field renders masked", typeOf(rp, "REPOCKET_PASSWORD") === "password", rp);
check("RULE 2: the container's API key renders masked", typeOf(rp, "RP_API_KEY") === "password", rp);
check("RULE 2: a non-secret field is readable text", typeOf(hg, "HONEYGAIN_EMAIL") === "text", hg);
check("RULE 2: a non-secret collector field is readable text too", typeOf(rp, "REPOCKET_EMAIL") === "text", rp);
check(
  "RULE 2: an optional non-secret field is not masked either",
  typeOf(hg, "HONEYGAIN_DEVICE_NAME") === "text",
  hg,
);
check(
  "RULE 2: every input is one of the two types, never a bare input with no type",
  inputs(hg).concat(inputs(rp)).every((input) => input.type === "password" || input.type === "text"),
);

// ---------------------------------------------------------------------------
// RULE 3: a {hostname} default is expanded.
// ---------------------------------------------------------------------------
const deviceName = inputs(hg).find((input) => input.env === "HONEYGAIN_DEVICE_NAME");
check("RULE 3: the hostname default is expanded to this machine", deviceName?.value === "cashpilot-macmini", hg);
check("RULE 3: the literal token never reaches the markup", !hg.includes("{hostname}"), hg);
const noHost = renderWizardServiceSetup(honeygain, collectorFields, "");
check(
  "RULE 3: an unknown hostname falls back to 'desktop', not to the raw token",
  inputs(noHost).find((input) => input.env === "HONEYGAIN_DEVICE_NAME")?.value === "cashpilot-desktop",
  noHost,
);
check("RULE 3: and a null hostname does the same", !renderWizardServiceSetup(honeygain, collectorFields, null).includes("{hostname}"));

// ---------------------------------------------------------------------------
// RULE 4: required is marked, optional is not.
// ---------------------------------------------------------------------------
check("RULE 4: a required field is marked with an asterisk", labelOf(hg, "HONEYGAIN_EMAIL") === "Email *", hg);
check("RULE 4: so is a required secret", labelOf(hg, "HONEYGAIN_PASSWORD") === "Password *", hg);
check(
  "RULE 4: a required collector field is marked too",
  labelOf(rp, "REPOCKET_PASSWORD") === "Repocket account password *",
  rp,
);
check(
  "RULE 4: an OPTIONAL field is not marked, or the asterisk means nothing",
  labelOf(hg, "HONEYGAIN_DEVICE_NAME") === "Device name",
  hg,
);

// ---------------------------------------------------------------------------
// RULE 5: no credential value is ever written into the markup.
// ---------------------------------------------------------------------------
check(
  "RULE 5: a field with no catalog default renders an EMPTY value attribute",
  inputs(rp).every((input) => input.value === ""),
  inputs(rp)
    .map((input) => `${input.env}=${JSON.stringify(input.value)}`)
    .join(", "),
);
// A saved credential reaches the input through hydrateWizardForm(), which sets
// input.value on the live node. If the render ever learned to pre-fill instead, the
// secret would be written into a value attribute -- into the page source, into any
// innerHTML dump, into a screenshot of the DOM inspector. So a stray value on a field
// object must stay out of the markup.
const carrier = renderWizardServiceSetup(repocket, {
  repocket: [{ key: "REPOCKET_PASSWORD", label: "Password", description: "pw", secret: true, value: "hunter2-SECRET" }],
}, "macmini");
check("RULE 5: a value riding on a field object never reaches the markup", !carrier.includes("hunter2-SECRET"), carrier);
check(
  "a default that would break out of its value attribute is escaped",
  !renderWizardServiceSetup(
    service({ docker: { env: [envVar("NAME", { default: 'x" onfocus="alert(1)' })] } }),
    null,
    "macmini",
  ).includes('onfocus="alert'),
);
check(
  "RULE 5: the render takes no credential store, so the same card twice is identical",
  renderWizardServiceSetup(repocket, collectorFields, "macmini") === rp,
);

// ---------------------------------------------------------------------------
// The placeholder comes from a catalog description that ships HTML links. An
// unstripped one breaks out of the attribute it is interpolated into.
// ---------------------------------------------------------------------------
const apiKey = inputs(rp).find((input) => input.env === "RP_API_KEY");
check("a catalog link is stripped out of the placeholder", apiKey?.placeholder.includes("app.repocket.com"), rp);
check("and no anchor tag survives into the card", !rp.includes("<a "), rp);
check("nor a stray href attribute", !rp.includes("href="), rp);

// A catalog is author-edited text landing in an innerHTML sink.
const hostile = renderWizardServiceSetup(
  service({
    name: '<img src=x onerror=alert(1)>',
    slug: 'a" onmouseover="alert(1)',
    shortDescription: "<script>alert(1)</script>",
    docker: { env: [envVar("K", { label: "<b>K</b>" })] },
  }),
  null,
  "macmini",
);
check("a hostile service name is escaped, never executed", !hostile.includes("<img"), hostile);
check("a hostile description is escaped too", !hostile.includes("<script"), hostile);
check("a hostile label is escaped", !hostile.includes("<b>"), hostile);
check("and a hostile slug cannot break out of its attribute", !hostile.includes('onmouseover="alert'), hostile);

// ---------------------------------------------------------------------------
// The card's own furniture: the buttons the wizard's click handler selects on.
// ---------------------------------------------------------------------------
check("the card offers Save, Deploy and Collect", ["save", "deploy", "collect"].every((action) => hg.includes(`data-wizard-action="${action}"`)), hg);
check("the preflight slot is there for the backend's answer", hg.includes('data-preflight-slug="honeygain"'), hg);
check("and the output pane the action writes into", hg.includes('data-output-slug="honeygain"'), hg);

// A manual-only service cannot be deployed, but its collector credentials are still
// the whole reason the card exists.
const manual = renderWizardServiceSetup(
  service({ slug: "pawns", name: "Pawns", manualOnly: true, docker: { env: [] } }),
  { pawns: [{ key: "PAWNS_EMAIL", label: "Pawns email", description: "Account email", required: true }] },
  "macmini",
);
check("a manual-only service still renders its collector input", inputs(manual)[0]?.env === "PAWNS_EMAIL", manual);
check("its Deploy button is disabled", manual.includes('data-wizard-action="deploy" data-slug="pawns" disabled'), manual);

// Nothing to ask for must say so rather than render an empty grid.
const none = renderWizardServiceSetup(service({ slug: "quiet", docker: { env: [] } }), {}, "macmini");
check("a service with no credentials renders none", inputs(none).length === 0, none);
check("and says so in words", none.includes("No credentials are required by the catalog"), none);

if (failures > 0) {
  console.error(`\n${failures} of ${checks} wizard render checks FAILED`);
  process.exit(1);
}
console.log(`wizard render checks: ${checks} passed`);
