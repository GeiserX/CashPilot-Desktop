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

/** One setting row. extra is HTML drawn under the help, such as the fence's state now. */
export function renderEnvSetting(item: EnvSetting, extra = "") {
  return `
    <label class="setting-row">
      <span>
        <strong>${escapeHtml(item.label)}</strong>
        <small>${escapeHtml(item.key)} · ${escapeHtml(item.source)}</small>
      </span>
      ${item.secret ? secretInput(item) : `<input data-setting="${escapeHtml(envInputName(item.key))}" value="${escapeHtml(item.value)}" data-initial="${escapeHtml(item.value)}" ${item.readOnly ? "readonly" : ""} />`}
      <small>${escapeHtml(item.help)}</small>${extra}
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

/**
 * One input of the settings form, as saveSettingsFromForm reads it. `initial` is
 * the value a plain field was drawn with; secrets have none.
 */
export interface SettingField { name: string; value: string; readOnly: boolean; secret: boolean; clear: boolean; initial?: string }

/** The parts of a settings input that settingField reads. An HTMLInputElement has them all. */
export interface SettingInput {
  value: string;
  readOnly: boolean;
  dataset: { setting?: string; clear?: string; initial?: string };
  hasAttribute(name: string): boolean;
}

/**
 * Reads one drawn input of the settings form as Save sees it. `initial` comes
 * from the data-initial renderEnvSetting draws a plain field with, so a field
 * the user did not touch is left out of the save.
 */
export function settingField(input: SettingInput): SettingField {
  return {
    name: input.dataset.setting || "",
    value: input.value,
    readOnly: input.readOnly,
    secret: input.hasAttribute("data-secret"),
    clear: input.dataset.clear === "true",
    initial: input.dataset.initial,
  };
}

/**
 * What Save sends to SaveSettings. A secret box left blank is left out, so the
 * backend keeps the saved value; only Remove sends it empty, which unpairs.
 * A plain field still holding the value it was drawn with is left out too: the
 * backend keeps every key Save does not send. So a form drawn before a join or
 * a leave in the "How this computer runs" card cannot put the old server address
 * or hub setting back.
 */
export function settingsSaveValues(fields: SettingField[]): Record<string, string> {
  const values: Record<string, string> = {};
  for (const f of fields) {
    if (f.readOnly) continue;
    if (f.secret && f.value === "" && !f.clear) continue;
    if (!f.secret && f.initial !== undefined && f.value === f.initial) continue;
    values[f.name] = f.value;
  }
  return values;
}

/**
 * The plain fields to fill back in after the settings page is drawn again: each
 * one the user had typed into and not saved, keyed by name. A field whose saved
 * value changed underneath (a Save, or another card) keeps the new value, so a
 * redraw never puts back text that is no longer what the user was editing.
 * Secrets are left out; their box starts empty on every draw.
 */
export function unsavedDrafts(before: SettingField[], after: SettingField[]): Map<string, string> {
  const drawn = new Map(after.filter((f) => !f.secret && !f.readOnly).map((f) => [f.name, f.initial]));
  const out = new Map<string, string>();
  for (const f of before) {
    if (f.secret || f.readOnly || f.initial === undefined || f.value === f.initial) continue;
    if (drawn.has(f.name) && drawn.get(f.name) === f.initial) out.set(f.name, f.value);
  }
  return out;
}
