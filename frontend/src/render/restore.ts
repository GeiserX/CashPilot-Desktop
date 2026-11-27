// Settings > Restore from a backup (issue #298): put a node identity or wallet back
// on this machine from a .cpbackup file. Desktop port of the web's restore card
// (CashPilot #741). The rules live in the Go side (restore_binding.go); this file
// only shows them.
//
// Pure: takes plain state, returns HTML.

import type { RestorePreview, RestoreResult, UnfinishedRestore } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, raw, t } from "./i18n.js";

const GUIDE = "https://github.com/GeiserX/CashPilot-Desktop/blob/main/docs/backup.md#restore-a-backup-from-the-command-line";

/** The Settings card: what it does, the unfinished restores, and the file button. */
export function renderRestoreCard(unfinished: UnfinishedRestore[] | null | undefined): string {
  const rows = (unfinished || []).map((u) => `
        <li class="restore-unfinished" data-restore-unfinished="${escapeHtml(u.slug)}">
          ${ht("restore.unfinished", { service: u.service || u.slug, at: String(u.startedAt || "").replace("T", " ").slice(0, 16) })}
          ${(u.volumes || []).length ? ht("restore.volumesMade", { volumes: raw((u.volumes || []).map((v) => `<code>${escapeHtml(v)}</code>`).join(", ")) }) : ""}
          <button class="btn btn-ghost btn-sm" data-restore-discard="${escapeHtml(u.slug)}">${ht("restore.discard")}</button>
        </li>`).join("");
  const stuck = rows ? `
      <div class="restore-stuck">
        <p class="muted compact-copy">${ht("restore.stuck")}</p>
        <ul>${rows}</ul>
      </div>` : "";
  return `
    <section class="card" id="restore-card">
      <div class="card-header">
        <div>
          <span class="card-title">${ht("restore.title")}</span>
          <p class="muted compact-copy">${ht("restore.help", { ext: ".cpbackup", guide: GUIDE })}</p>
        </div>
        <button class="compact-btn" id="restore-choose">${ht("restore.choose")}</button>
      </div>
      ${stuck}
      <div id="restore-preview"></div>
    </section>`;
}

/** What restoring the chosen file would do, and the form that does it. */
export function renderRestorePreview(p: RestorePreview): string {
  const name = p.service || p.slug;
  const service = escapeHtml(name);
  const creates = (p.creates || []).map((c) =>
    `<li>${ht("restore.volumeAt", { volume: c.volume, target: c.target })}</li>`).join("");
  const runsOn = p.runsOn || [];
  // Never less strict than the Go side: a file this app did not make always asks.
  const needsName = p.needsName || !p.known;
  const typeIt = ht("restore.typeIt", { service: name });
  const origin = p.known
    ? `<p class="muted compact-copy">${ht("restore.known")}</p>`
    : `<p class="restore-warn">${p.madeOn ? ht("restore.unknownFrom", { from: p.madeOn }) : ht("restore.unknown")}${runsOn.length && p.ok ? "" : ` ${typeIt}`}</p>`;
  // Any machine that runs the service now may run the very node in the file, made
  // here or not: the export record says who wrote the file, not where its identity
  // went since.
  const elsewhere = p.ok && runsOn.length
    ? `<p class="restore-warn">${ht("restore.runsOn", { service: name, where: runsOn.join(", ") })}${p.known
        ? ` ${ht("restore.knownElsewhere")}`
        : ""} ${ht("restore.oneIdentity")} ${typeIt}</p>`
    : "";
  const confirmName = needsName
    ? `<input type="text" name="confirm_name" autocomplete="off" spellcheck="false" aria-label="${ht("restore.typeName")}">`
    : "";
  const refused = p.ok ? "" : `<p class="restore-refused" role="alert">${ht("restore.refused", { why: p.why })}</p>`;
  return `
    <form class="restore-form" data-token="${escapeHtml(p.token)}" data-service="${service}">
      <p>${ht("restore.creates", { file: p.file, service: name, container: p.container })}</p>
      <ul>${creates}</ul>
      <p class="muted compact-copy">${ht("restore.neverOverwrites", { service: name })}</p>
      ${refused}${origin}${elsewhere}${confirmName}
      <input type="password" name="passphrase" autocomplete="off" required aria-label="${ht("restore.phrase")}" placeholder="${ht("restore.phrase")}">
      <button type="submit" class="btn btn-primary btn-sm"${p.ok ? "" : " disabled"}>${ht("restore.restore")}</button>
    </form>`;
}

/** The toast after a restore that ran. */
export function restoreResultText(service: string, r: RestoreResult): string {
  if (r.started) return t("restore.result.started", { service, n: r.files });
  return r.waiting
    ? t("restore.result.waitingWhy", { service, n: r.files, why: r.waiting })
    : t("restore.result.waiting", { service, n: r.files });
}
