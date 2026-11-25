// Settings > Environment Variables: one row per setting the backend reports.
//
// Pure: takes the setting the backend sent, returns HTML, touches no global.

import type { EnvSetting } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** The SaveSettings key a setting's input posts under. */
export function envInputName(key: string) {
  const names: Record<string, string> = {
    CASHPILOT_HOSTNAME_PREFIX: "hostnamePrefix",
    CASHPILOT_COLLECT_INTERVAL: "collectIntervalMinutes",
    CASHPILOT_DISPLAY_CURRENCY: "displayCurrency",
    CASHPILOT_FLEET_BIND: "fleetBindAddress",
    CASHPILOT_FLEET_PORT: "fleetPort",
    CASHPILOT_FLEET_SERVER: "fleetServerEnabled",
    CASHPILOT_DRIFT_WARNINGS: "driftWarnings",
    CASHPILOT_ISOLATION: "isolation",
    CASHPILOT_UPSTREAM_URL: "upstreamUrl",
    CASHPILOT_UPSTREAM_KEY: "upstreamKey",
    TZ: "timezone",
  };
  return names[key] || key;
}

export function renderEnvSetting(item: EnvSetting) {
  return `
    <label class="setting-row">
      <span>
        <strong>${escapeHtml(item.label)}</strong>
        <small>${escapeHtml(item.key)} · ${escapeHtml(item.source)}</small>
      </span>
      ${item.secret ? secretInput(item) : `<input data-setting="${escapeHtml(envInputName(item.key))}" value="${escapeHtml(item.value)}" ${item.readOnly ? "readonly" : ""} />`}
      <small>${escapeHtml(item.help)}</small>
    </label>
  `;
}

// A Secret setting never puts its stored value in the page. The box starts empty
// and its placeholder says whether a value is saved. Show reveals only what the
// user types; a read-only secret is copied from memory by main.ts.
function secretInput(item: EnvSetting): string {
  const saved = item.value !== "";
  const common = `type="password" autocomplete="new-password" spellcheck="false" aria-label="${escapeHtml(item.label)}"`;
  if (item.readOnly) {
    return `<span class="secret-field">
        <input ${common} readonly placeholder="${saved ? "Saved. Use Copy to get it." : "Not set"}" />
        ${saved ? `<button type="button" class="secondary compact-btn" data-secret-copy="${escapeHtml(item.key)}">Copy</button>` : ""}
      </span>`;
  }
  const placeholder = saved ? "Saved. Leave blank to keep it, or type a new one." : "Not set";
  return `<span class="secret-field">
        <input data-setting="${escapeHtml(envInputName(item.key))}" data-secret ${common} placeholder="${placeholder}" />
        <button type="button" class="secondary compact-btn" data-secret-toggle aria-pressed="false">Show</button>
        ${saved ? `<button type="button" class="secondary compact-btn" data-secret-clear aria-pressed="false">Remove</button>` : ""}
      </span>`;
}

/** One input of the settings form, as saveSettingsFromForm reads it. */
export interface SettingField { name: string; value: string; readOnly: boolean; secret: boolean; clear: boolean }

/**
 * What Save sends to SaveSettings. A secret box left blank is left out, so the
 * backend keeps the saved value; only Remove sends it empty, which unpairs.
 */
export function settingsSaveValues(fields: SettingField[]): Record<string, string> {
  const values: Record<string, string> = {};
  for (const f of fields) {
    if (f.readOnly) continue;
    if (f.secret && f.value === "" && !f.clear) continue;
    values[f.name] = f.value;
  }
  return values;
}
