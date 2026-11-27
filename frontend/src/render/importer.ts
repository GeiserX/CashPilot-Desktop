// Import from another tool: read money4band, CashFactory or InternetIncome config
// files, show the backend's dry run, save the ticked services. The web console leads
// (app/static/js/import.js); this is its port.
//
// What these functions must never get wrong:
//
//   - a secret's value is never in the HTML. The backend never sends it; even if one
//     slipped through, a secret field renders as "set" and nothing else;
//   - a row that cannot be brought over (unusable, nothing to deploy) has no tick;
//   - everything from the files (names, keys, values, reasons) is escaped.
//
// Pure: takes plain data, returns HTML strings or values.

import { renderDeployFeedback, type DeployFeedback } from "./deploy_consent.js";
import type { ImportField, ImportFile, ImportPreview, ImportResult, ImportRow } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";

/** The same limits the backend enforces, checked first so a big file never crosses. */
export const IMPORT_MAX_FILES = 4;
export const IMPORT_MAX_BYTES = 64 * 1024;

export function importSources(): [string, string][] {
  return [
    ["auto", t("importer.source.auto")],
    ["money4band", "money4band"],
    ["cashfactory", "CashFactory"],
    ["internetincome", "InternetIncome"],
  ];
}

function fieldStateText(state: string): string {
  switch (state) {
    case "imported": return t("importer.field.imported");
    case "missing": return t("importer.field.missing");
    case "placeholder": return t("importer.field.placeholder");
    case "retype": return t("importer.field.retype");
    case "invalid": return t("importer.field.invalid");
    default: return "";
  }
}

function rowStateText(state: string): string {
  switch (state) {
    case "ready": return t("importer.row.ready");
    case "missing_fields": return t("importer.row.missingFields");
    case "invalid": return t("importer.row.invalid");
    case "unusable": return t("importer.row.unusable");
    case "not_deployable": return t("importer.row.notDeployable");
    default: return "";
  }
}

/** Why these files cannot be sent, or "" when they can. */
export function importFilesProblem(files: ImportFile[]): string {
  if (!files.length) return t("importer.addFile");
  if (files.length > IMPORT_MAX_FILES) return t("importer.tooManyFiles", { n: IMPORT_MAX_FILES });
  const total = files.reduce((n, f) => n + new TextEncoder().encode(f.content).length, 0);
  if (total > IMPORT_MAX_BYTES) return t("importer.tooBig", { kib: IMPORT_MAX_BYTES / 1024 });
  return "";
}

/** Step 1: the tool, the files and the paste box. */
export function renderImportInput(source: string): string {
  const options = importSources().map(([value, label]) =>
    `<option value="${escapeHtml(value)}"${value === source ? " selected" : ""}>${escapeHtml(label)}</option>`).join("");
  return `
    <section class="card" id="import-input">
      <div class="card-header"><span class="card-title">${ht("importer.step1")}</span></div>
      <p class="muted compact-copy">
        ${ht("importer.whichFiles", { m4bConfig: "config/user-config.json", env: ".env", properties: "properties.conf", earnapp: "earnapp.txt", proxyrack: "proxyrack.txt" })}
        ${ht("importer.limits", { n: IMPORT_MAX_FILES, kib: IMPORT_MAX_BYTES / 1024 })}
        ${ht("importer.notRun")}
      </p>
      <label class="setting-row"><span><strong>${ht("importer.tool")}</strong></span>
        <select id="import-source">${options}</select></label>
      <label class="setting-row"><span><strong>${ht("importer.files")}</strong></span>
        <input type="file" id="import-files" multiple /></label>
      <label class="setting-row"><span><strong>${ht("importer.paste")}</strong></span>
        <textarea id="import-paste" rows="8" spellcheck="false" autocomplete="off"></textarea></label>
      <div class="card-actions">
        <button class="primary compact-btn" id="import-preview-btn">${ht("importer.check")}</button>
      </div>
    </section>`;
}

function fieldCell(slug: string, field: ImportField): string {
  const state = fieldStateText(field.state) || field.state;
  let shown = "";
  if (field.secret) {
    shown = field.state === "imported" ? ht("importer.set") : "";
  } else if ((field.state === "imported" || field.state === "invalid") && field.value) {
    shown = `<code>${escapeHtml(field.value)}</code>`;
  }
  let input = "";
  if (field.state !== "imported") {
    const type = field.secret ? "password" : "text";
    const pattern = field.pattern ? ` pattern="${escapeHtml(field.pattern)}"` : "";
    input = `<input class="import-extra" type="${type}" autocomplete="off" spellcheck="false"${pattern}
      data-import-slug="${escapeHtml(slug)}" data-import-key="${escapeHtml(field.key)}"
      placeholder="${field.required ? escapeHtml(field.label) : ht("importer.optional", { label: field.label })}" />`;
  }
  return `<div class="import-field"><strong>${escapeHtml(field.label)}</strong>: ${shown} <span class="muted">${escapeHtml(state)}</span>${input}</div>`;
}

/** Whether the user may tick a row: anything but a service that cannot come over. */
export function rowSelectable(row: Pick<ImportRow, "state">): boolean {
  return row.state !== "unusable" && row.state !== "not_deployable";
}

function rowHtml(row: ImportRow): string {
  const tick = rowSelectable(row)
    ? `<input type="checkbox" class="import-pick" data-import-slug="${escapeHtml(row.slug)}"${row.selected ? " checked" : ""} aria-label="${ht("importer.bringOver", { name: row.name })}" />`
    : "";
  const extras: string[] = [];
  if (row.existing?.deployed) extras.push(t("importer.alreadyDeployed"));
  if (row.existing?.configured) extras.push(t("importer.alreadySetUp"));
  return `<tr>
    <td>${tick}</td>
    <td><strong>${escapeHtml(row.name)}</strong><div class="muted">${escapeHtml(rowStateText(row.state) || row.state)}</div></td>
    <td>${(row.fields || []).map((f) => fieldCell(row.slug, f)).join("")}
      ${row.reason ? `<div class="muted">${escapeHtml(row.reason)}</div>` : ""}
      ${extras.length ? `<div class="muted">${escapeHtml(extras.join("; "))}</div>` : ""}</td>
  </tr>`;
}

/** Step 2: the dry run. */
export function renderImportPreview(preview: ImportPreview): string {
  const notices = (preview.notices || []).map((n) => `<p class="muted compact-copy">${escapeHtml(n)}</p>`).join("");
  const rows = preview.rows || [];
  const table = rows.length
    ? `<div class="table-scroll"><table class="breakdown-table"><thead><tr><th></th><th>${ht("importer.service")}</th><th>${ht("importer.settings")}</th></tr></thead><tbody>${rows.map(rowHtml).join("")}</tbody></table></div>`
    : `<p class="muted">${ht("importer.nothing")}</p>`;
  const skipped = preview.notImported || [];
  const skippedHtml = skipped.length
    ? `<h4>${ht("importer.notImported")}</h4><ul>${skipped.map((s) => `<li><code>${escapeHtml(s.key)}</code>: ${escapeHtml(s.reason)}</li>`).join("")}</ul>`
    : "";
  return `
    <section class="card" id="import-preview">
      <div class="card-header"><span class="card-title">${ht("importer.step2")}</span></div>
      <p class="muted compact-copy">${ht("importer.readAs", { source: preview.sourceName ?? "" })}</p>
      ${notices}
      ${table}
      ${skippedHtml}
      ${rows.some(rowSelectable) ? `<div class="card-actions"><button class="primary compact-btn" id="import-apply-btn">${ht("importer.save")}</button></div>` : ""}
    </section>`;
}

/** The ticked rows and the values typed for them, as ApplyImport takes them. */
export function importChoices(
  picks: { slug: string; checked: boolean }[],
  extras: { slug: string; key: string; value: string }[],
): { slugs: string[]; extra: Record<string, Record<string, string>> } {
  const slugs = picks.filter((p) => p.checked).map((p) => p.slug);
  const extra: Record<string, Record<string, string>> = {};
  for (const e of extras) {
    const value = e.value.trim();
    if (!value || !slugs.includes(e.slug)) continue;
    (extra[e.slug] ||= {})[e.key] = value;
  }
  return { slugs, extra };
}

/**
 * Step 3: what was saved, each with a Deploy button that runs the normal deploy.
 * `waitsForDocker` maps a service that needs Docker here, while Docker cannot run
 * it, to the note that says why (render/runtime_state.ts dockerBlockedNote): its
 * Deploy is disabled. A native app is never in it.
 */
export function renderImportSaved(result: ImportResult, preview: ImportPreview | null, deployed: string[], links: Record<string, string> = {}, statuses: Record<string, DeployFeedback> = {}, waitsForDocker: Record<string, string> = {}): string {
  const names: Record<string, string> = {};
  const labels: Record<string, Record<string, string>> = {};
  for (const row of preview?.rows || []) {
    names[row.slug] = row.name;
    labels[row.slug] = Object.fromEntries(row.fields.map((f) => [f.key, f.label]));
  }
  const items = result.saved.map((slug) => {
    const kept = (result.kept?.[slug] || []).map((k) => labels[slug]?.[k] || k);
    const keptNote = kept.length
      ? `<div class="muted">${ht("importer.kept", { fields: kept.join(", ") })}</div>`
      : "";
    const done = deployed.includes(slug);
    const status = statuses[slug];
    const waits = done ? "" : waitsForDocker[slug] || "";
    return `<li>
      <strong>${escapeHtml(names[slug] || slug)}</strong>
      ${done ? (status ? "" : `<span class="muted">${ht("importer.deployed")}</span>`) : `<button class="compact-btn" data-import-deploy="${escapeHtml(slug)}"${status?.kind === "busy" || waits ? " disabled" : ""}>${ht("importer.deploy")}</button>`}
      ${waits}
      <div class="deploy-panel" data-import-deploy-panel="${escapeHtml(slug)}" aria-live="polite">${renderDeployFeedback(status)}</div>
      ${keptNote}
      ${done && links[slug] ? `<pre class="muted device-link-text">${escapeHtml(links[slug])}</pre>` : ""}
    </li>`;
  }).join("");
  return `
    <section class="card" id="import-saved">
      <div class="card-header"><span class="card-title">${ht("importer.step3")}</span></div>
      <p class="muted compact-copy">
        ${ht("importer.savedIntro")}
      </p>
      <ul class="import-saved">${items}</ul>
    </section>`;
}
