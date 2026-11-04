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

import type { ImportField, ImportFile, ImportPreview, ImportResult, ImportRow } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** The same limits the backend enforces, checked first so a big file never crosses. */
export const IMPORT_MAX_FILES = 4;
export const IMPORT_MAX_BYTES = 64 * 1024;

export const IMPORT_SOURCES: [string, string][] = [
  ["auto", "Tell from the files"],
  ["money4band", "money4band"],
  ["cashfactory", "CashFactory"],
  ["internetincome", "InternetIncome"],
];

const FIELD_STATE: Record<string, string> = {
  imported: "imported",
  missing: "missing",
  placeholder: "still the example value",
  retype: "needs retyping (uses $ or quotes a shell would change)",
  invalid: "not in the format this service needs",
};

const ROW_STATE: Record<string, string> = {
  ready: "Ready",
  missing_fields: "Needs a value",
  invalid: "Check a value",
  unusable: "Cannot bring over",
  not_deployable: "Nothing to deploy",
};

/** Why these files cannot be sent, or "" when they can. */
export function importFilesProblem(files: ImportFile[]): string {
  if (!files.length) return "Add a file or paste one.";
  if (files.length > IMPORT_MAX_FILES) return `At most ${IMPORT_MAX_FILES} files at a time.`;
  const total = files.reduce((n, f) => n + new TextEncoder().encode(f.content).length, 0);
  if (total > IMPORT_MAX_BYTES) return `The files add up to more than ${IMPORT_MAX_BYTES / 1024} KiB.`;
  return "";
}

/** Step 1: the tool, the files and the paste box. */
export function renderImportInput(source: string): string {
  const options = IMPORT_SOURCES.map(([value, label]) =>
    `<option value="${escapeHtml(value)}"${value === source ? " selected" : ""}>${escapeHtml(label)}</option>`).join("");
  return `
    <section class="card" id="import-input">
      <div class="card-header"><span class="card-title">1. Your old config</span></div>
      <p class="muted compact-copy">
        money4band: <code>config/user-config.json</code> (or its <code>.env</code>).
        CashFactory: <code>.env</code>.
        InternetIncome: <code>properties.conf</code>, plus <code>earnapp.txt</code> and <code>proxyrack.txt</code>
        if you have them, so your linked devices come over.
        Up to ${IMPORT_MAX_FILES} files, ${IMPORT_MAX_BYTES / 1024} KiB in total. Nothing in the files is run or fetched,
        and the files themselves are not kept.
      </p>
      <label class="setting-row"><span><strong>Tool</strong></span>
        <select id="import-source">${options}</select></label>
      <label class="setting-row"><span><strong>Files</strong></span>
        <input type="file" id="import-files" multiple /></label>
      <label class="setting-row"><span><strong>Or paste one file here</strong></span>
        <textarea id="import-paste" rows="8" spellcheck="false" autocomplete="off"></textarea></label>
      <div class="card-actions">
        <button class="primary compact-btn" id="import-preview-btn">Check what comes over</button>
      </div>
    </section>`;
}

function fieldCell(slug: string, field: ImportField): string {
  const state = FIELD_STATE[field.state] || field.state;
  let shown = "";
  if (field.secret) {
    shown = field.state === "imported" ? "set" : "";
  } else if ((field.state === "imported" || field.state === "invalid") && field.value) {
    shown = `<code>${escapeHtml(field.value)}</code>`;
  }
  let input = "";
  if (field.state !== "imported") {
    const type = field.secret ? "password" : "text";
    const pattern = field.pattern ? ` pattern="${escapeHtml(field.pattern)}"` : "";
    input = `<input class="import-extra" type="${type}" autocomplete="off" spellcheck="false"${pattern}
      data-import-slug="${escapeHtml(slug)}" data-import-key="${escapeHtml(field.key)}"
      placeholder="${escapeHtml(field.label)}${field.required ? "" : " (optional)"}" />`;
  }
  return `<div class="import-field"><strong>${escapeHtml(field.label)}</strong>: ${shown} <span class="muted">${escapeHtml(state)}</span>${input}</div>`;
}

/** Whether the user may tick a row: anything but a service that cannot come over. */
export function rowSelectable(row: Pick<ImportRow, "state">): boolean {
  return row.state !== "unusable" && row.state !== "not_deployable";
}

function rowHtml(row: ImportRow): string {
  const tick = rowSelectable(row)
    ? `<input type="checkbox" class="import-pick" data-import-slug="${escapeHtml(row.slug)}"${row.selected ? " checked" : ""} aria-label="Bring ${escapeHtml(row.name)} over" />`
    : "";
  const extras: string[] = [];
  if (row.existing?.deployed) extras.push("already deployed on this computer");
  if (row.existing?.configured) extras.push("already set up here; values already set are kept");
  return `<tr>
    <td>${tick}</td>
    <td><strong>${escapeHtml(row.name)}</strong><div class="muted">${escapeHtml(ROW_STATE[row.state] || row.state)}</div></td>
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
    ? `<div class="table-scroll"><table class="breakdown-table"><thead><tr><th></th><th>Service</th><th>Settings</th></tr></thead><tbody>${rows.map(rowHtml).join("")}</tbody></table></div>`
    : `<p class="muted">These files set up no service CashPilot can bring over.</p>`;
  const skipped = preview.notImported || [];
  const skippedHtml = skipped.length
    ? `<h4>Not imported</h4><ul>${skipped.map((s) => `<li><code>${escapeHtml(s.key)}</code>: ${escapeHtml(s.reason)}</li>`).join("")}</ul>`
    : "";
  return `
    <section class="card" id="import-preview">
      <div class="card-header"><span class="card-title">2. What CashPilot found</span></div>
      <p class="muted compact-copy">Read as <strong>${escapeHtml(preview.sourceName)}</strong>.</p>
      ${notices}
      ${table}
      ${skippedHtml}
      ${rows.some(rowSelectable) ? `<div class="card-actions"><button class="primary compact-btn" id="import-apply-btn">Save the ticked services</button></div>` : ""}
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

/** Step 3: what was saved, each with a Deploy button that runs the normal deploy. */
export function renderImportSaved(result: ImportResult, preview: ImportPreview | null, deployed: string[]): string {
  const names: Record<string, string> = {};
  const labels: Record<string, Record<string, string>> = {};
  for (const row of preview?.rows || []) {
    names[row.slug] = row.name;
    labels[row.slug] = Object.fromEntries(row.fields.map((f) => [f.key, f.label]));
  }
  const items = result.saved.map((slug) => {
    const kept = (result.kept?.[slug] || []).map((k) => labels[slug]?.[k] || k);
    const keptNote = kept.length
      ? `<div class="muted">Already set here and left as it was: ${escapeHtml(kept.join(", "))}.</div>`
      : "";
    const done = deployed.includes(slug);
    return `<li>
      <strong>${escapeHtml(names[slug] || slug)}</strong>
      ${done ? `<span class="muted">deployed</span>` : `<button class="compact-btn" data-import-deploy="${escapeHtml(slug)}">Deploy</button>`}
      ${keptNote}
    </li>`;
  }).join("");
  return `
    <section class="card" id="import-saved">
      <div class="card-header"><span class="card-title">3. Deploy what you imported</span></div>
      <p class="muted compact-copy">
        Saved encrypted, like every credential in CashPilot; the deploy form now opens with these values.
        Stop the old tool on this computer before you deploy: two copies behind one connection earn as one,
        and a device id must not run twice.
      </p>
      <ul class="import-saved">${items}</ul>
    </section>`;
}
