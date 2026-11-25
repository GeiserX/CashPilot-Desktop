#!/usr/bin/env node
// Browser-free checks on Settings > Environment Variables.
//
// THE RULE: a setting the backend marks Secret (the fleet API key, the pairing
// key) never reaches the rendered HTML in clear text. The row shows that a value
// is saved, lets the user type a replacement, and saving the form with the field
// left untouched keeps the saved value.
//
//   node scripts/settings_render_check.mjs      # against ./.harness-build

import { renderEnvSetting, settingsSaveValues } from "../.harness-build/render/settings.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

function setting(overrides = {}) {
  return {key: "CASHPILOT_HOSTNAME_PREFIX", label: "Hostname Prefix", value: "cashpilot", source: "Config", secret: false, readOnly: false, help: "", ...overrides};
}

// Every setting GetSettingsState marks Secret today (app.go).
const PAIRING = "pk-7f3a9c1e5b2d4f6a8c0e";
const FLEET = "fk-0123456789abcdef0123";
const secrets = [
  setting({key: "CASHPILOT_UPSTREAM_KEY", label: "Pairing key", value: PAIRING, secret: true}),
  setting({key: "CASHPILOT_API_KEY", label: "Fleet API Key", value: FLEET, secret: true, readOnly: true}),
  // A hostile value: escaping must not be the only thing keeping it out.
  setting({key: "CASHPILOT_UPSTREAM_KEY", label: "Pairing key", value: 'a"b<c>&d', secret: true}),
];

for (const item of secrets) {
  const html = renderEnvSetting(item);
  check(`${item.key}: the stored value is not in the HTML`, !html.includes(item.value) && !html.includes("a&quot;b&lt;c&gt;&amp;d"), html);
  check(`${item.key}: the input is a password field`, /<input[^>]*type="password"/.test(html), html);
  check(`${item.key}: the input has no value attribute`, !/<input[^>]*\svalue="/.test(html), html);
  check(`${item.key}: the row says a value is saved`, /Saved/.test(html), html);
}

const pairing = renderEnvSetting(secrets[0]);
check("an editable secret has a show/hide toggle", /<button type="button"[^>]*data-secret-toggle[^>]*>Show<\/button>/.test(pairing), pairing);
check("an editable secret can be removed", /data-secret-clear/.test(pairing), pairing);
check("an editable secret posts under its SaveSettings key", /data-setting="upstreamKey"/.test(pairing), pairing);
check("an editable secret says how to keep it", /leave (it )?blank to keep/i.test(pairing), pairing);

const fleet = renderEnvSetting(secrets[1]);
check("a read-only secret can be copied", /data-secret-copy="CASHPILOT_API_KEY"/.test(fleet), fleet);
check("a read-only secret offers no replace box", !/data-setting=/.test(fleet) || /readonly/.test(fleet), fleet);

const unset = renderEnvSetting(setting({key: "CASHPILOT_UPSTREAM_KEY", label: "Pairing key", value: "", secret: true}));
check("an empty secret does not claim to be saved", !/Saved/.test(unset), unset);
check("an empty secret offers nothing to remove", !/data-secret-clear/.test(unset), unset);

// A plain setting still shows its value, so the change did not hide everything.
const plain = renderEnvSetting(setting());
check("a plain setting still shows its value", plain.includes('value="cashpilot"') && !plain.includes('type="password"'), plain);

// Saving: an untouched secret is left out, so the backend keeps the saved one.
const field = (overrides) => ({name: "upstreamKey", value: "", readOnly: false, secret: true, clear: false, ...overrides});
check("an untouched secret is not sent", !("upstreamKey" in settingsSaveValues([field({})])));
check("a typed secret is sent", settingsSaveValues([field({value: "new-key"})]).upstreamKey === "new-key");
check("a removed secret is sent empty", settingsSaveValues([field({clear: true})]).upstreamKey === "");
check("a read-only field is not sent", !("x" in settingsSaveValues([field({name: "x", value: "v", readOnly: true})])));
check("an empty plain field is still sent", settingsSaveValues([field({name: "upstreamUrl", secret: false})]).upstreamUrl === "");

if (failures > 0) {
  console.error(`\n${failures} of ${checks} settings checks FAILED`);
  process.exit(1);
}
console.log(`settings_render_check: ${checks} checks passed`);
