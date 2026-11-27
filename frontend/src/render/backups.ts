// The "Back these up" card on the dashboard: the services whose node identity,
// keystore or wallet exists only on this machine's disk.
//
// Advice, not a fault: amber while any service lacks a confirmed backup, one quiet
// line once every one has. Each row names the volume and what it holds in the
// catalog's own words, because "this volume IS the money" is what makes someone
// act, and offers "I have a backup" and "Download backup". Port of the web
// console's payouts card (CashPilot #530).
//
// Pure: takes plain state, returns HTML.

import type { StateBackupRow } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";

/** Why the card is asking, in words, never a colour alone. */
export function backupStatusText(row: StateBackupRow): string {
  const day = String(row.confirmedAt || "").slice(0, 10);
  switch (row.state) {
    case "confirmed": {
      const text = row.method === "export"
        ? t("backups.confirmed.export", { day })
        : row.method === "restore" ? t("backups.confirmed.restore", { day }) : t("backups.confirmed", { day });
      return row.tracksChanges ? text : t("backups.confirmed.untracked", { confirmed: text });
    }
    case "changed":
      return t("backups.changed", { day });
    case "recheck":
      return t("backups.recheck", { day });
    case "expired":
      return t("backups.expired", { day });
    default:
      return t("backups.none");
  }
}

function backupRow(row: StateBackupRow): string {
  const ok = row.state === "confirmed";
  const slug = escapeHtml(row.slug);
  const volumes = (row.volumes || []).map((v) => `
        <li><code>${escapeHtml(v.target)}</code><div class="backup-holds">${escapeHtml(v.holds)}</div></li>`).join("");
  const actions = (ok
    ? `<button class="btn btn-ghost btn-sm" data-backup-action="undo" data-slug="${slug}">${ht("backups.undo")}</button>`
    : `<button class="btn btn-ghost btn-sm" data-backup-action="confirm" data-slug="${slug}">${ht("backups.haveBackup")}</button>`)
    + `\n        <button class="btn btn-ghost btn-sm" data-backup-action="export" data-slug="${slug}">${ht("backups.download")}</button>`;
  return `
    <div class="attn-row ${ok ? "backup-ok" : "attn-warn"}" data-backup-row="${slug}">
      <span class="attn-tag">${ok ? ht("backups.tag.ok") : ht("backups.tag.todo")}</span>
      <span class="attn-what">${escapeHtml(row.name || row.slug)}</span>
      <span class="attn-why">${escapeHtml(backupStatusText(row))}<ul class="backup-volumes">${volumes}</ul></span>
      <span class="attn-actions">${actions}</span>
    </div>`;
}

/** The whole card; "" when nothing deployed keeps irreplaceable state. */
export function renderStateBackups(rows: StateBackupRow[] | null | undefined): string {
  const list = rows || [];
  if (!list.length) return "";
  const open = list.filter((r) => r.state !== "confirmed").length;
  const body = `<div class="attn-list">${list.map(backupRow).join("")}</div>`;
  if (!open) {
    return `
    <section class="attention backups backups-quiet" aria-label="${ht("backups.label")}">
      <details>
        <summary class="muted">${ht("backups.quiet", { n: list.length })}</summary>
        ${body}
      </details>
    </section>`;
  }
  return `
    <section class="attention backups" aria-labelledby="backups-title">
      <div class="block-head">
        <h2 class="block-title" id="backups-title">${ht("backups.title")} <span class="count">${open}</span></h2>
      </div>
      <p class="backup-intro">${ht("backups.intro", { command: "docker volume rm" })}</p>
      ${body}
    </section>`;
}

/**
 * The export form, opened under one row. The secret is typed here, sent once and
 * cleared; CashPilot keeps nothing that could open the file.
 */
export function backupExportForm(slug: string): string {
  const s = escapeHtml(slug);
  return `
    <form class="backup-form" data-slug="${s}">
      <label><input type="radio" name="mode" value="passphrase" checked> ${ht("backups.form.passphrase")}</label>
      <label><input type="radio" name="mode" value="recipient"> ${ht("backups.form.recipient")}</label>
      <input type="password" name="secret" autocomplete="new-password" required aria-label="${ht("backups.form.secret")}">
      <input type="password" name="secret_repeat" autocomplete="new-password" required placeholder="${ht("backups.form.repeat")}" aria-label="${ht("backups.form.repeat")}">
      <button type="submit" class="btn btn-primary btn-sm">${ht("backups.form.save")}</button>
      <p class="backup-holds">${ht("backups.form.note")}</p>
    </form>`;
}
