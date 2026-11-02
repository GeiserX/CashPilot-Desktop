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

/** Why the card is asking, in words, never a colour alone. */
export function backupStatusText(row: StateBackupRow): string {
  const day = String(row.confirmedAt || "").slice(0, 10);
  switch (row.state) {
    case "confirmed": {
      let text = `Backup confirmed ${day}${row.method === "export" ? " (saved from here)" : ""}`;
      if (!row.tracksChanges) text += ". CashPilot cannot read this volume right now, so only the six-month limit applies";
      return text;
    }
    case "changed":
      return `The volume changed since the backup of ${day}. Back it up again`;
    case "recheck":
      return `Confirmed ${day}, before CashPilot could tell when the volume changes. Confirm it once more`;
    case "expired":
      return `Last confirmed ${day}, more than six months ago`;
    default:
      return "No backup confirmed";
  }
}

function backupRow(row: StateBackupRow): string {
  const ok = row.state === "confirmed";
  const slug = escapeHtml(row.slug);
  const volumes = (row.volumes || []).map((v) => `
        <li><code>${escapeHtml(v.target)}</code><div class="backup-holds">${escapeHtml(v.holds)}</div></li>`).join("");
  const actions = (ok
    ? `<button class="btn btn-ghost btn-sm" data-backup-action="undo" data-slug="${slug}">Undo</button>`
    : `<button class="btn btn-ghost btn-sm" data-backup-action="confirm" data-slug="${slug}">I have a backup</button>`)
    + `\n        <button class="btn btn-ghost btn-sm" data-backup-action="export" data-slug="${slug}">Download backup</button>`;
  return `
    <div class="attn-row ${ok ? "backup-ok" : "attn-warn"}" data-backup-row="${slug}">
      <span class="attn-tag">${ok ? "Backed up" : "Back up"}</span>
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
    <section class="attention backups backups-quiet" aria-label="Backups">
      <details>
        <summary class="muted">Backups confirmed for ${list.length} ${list.length === 1 ? "service" : "services"} with irreplaceable state</summary>
        ${body}
      </details>
    </section>`;
  }
  return `
    <section class="attention backups" aria-labelledby="backups-title">
      <div class="block-head">
        <h2 class="block-title" id="backups-title">Back these up <span class="count">${open}</span></h2>
      </div>
      <p class="backup-intro">These services keep a node identity, a keystore or a wallet on this disk, and nothing else holds a copy.
        CashPilot refuses to delete these volumes, but it cannot protect them from a wiped disk, a new machine or
        <code>docker volume rm</code>. Keep a copy somewhere else, then mark it as backed up. CashPilot asks again when the
        volume changes, and after six months.</p>
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
      <label><input type="radio" name="mode" value="passphrase" checked> Passphrase (12+ characters)</label>
      <label><input type="radio" name="mode" value="recipient"> X25519 public key (hex)</label>
      <input type="password" name="secret" autocomplete="new-password" required aria-label="Passphrase or public key">
      <input type="password" name="secret_repeat" autocomplete="new-password" required placeholder="Repeat the passphrase" aria-label="Repeat the passphrase">
      <button type="submit" class="btn btn-primary btn-sm">Encrypt and save</button>
      <p class="backup-holds">Only the volumes above are read, with the service paused for the moment it takes. Lose the passphrase
        or the private key and this file cannot be opened by anyone, CashPilot included.</p>
    </form>`;
}
