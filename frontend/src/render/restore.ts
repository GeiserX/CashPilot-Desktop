// Settings > Restore from a backup (issue #298): put a node identity or wallet back
// on this machine from a .cpbackup file. Desktop port of the web's restore card
// (CashPilot #741). The rules live in the Go side (restore_binding.go); this file
// only shows them.
//
// Pure: takes plain state, returns HTML.

import type { RestorePreview, RestoreResult, UnfinishedRestore } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

const GUIDE = "https://github.com/GeiserX/CashPilot-Desktop/blob/main/docs/backup.md#restore-a-backup-from-the-command-line";

/** The Settings card: what it does, the unfinished restores, and the file button. */
export function renderRestoreCard(unfinished: UnfinishedRestore[] | null | undefined): string {
  const rows = (unfinished || []).map((u) => `
        <li class="restore-unfinished" data-restore-unfinished="${escapeHtml(u.slug)}">
          <strong>${escapeHtml(u.service || u.slug)}</strong>: a restore started ${escapeHtml(String(u.startedAt || "").replace("T", " ").slice(0, 16))}
          did not finish. Its data may be half-written, so Deploy and Start refuse it.
          ${(u.volumes || []).length ? `Volumes it made: ${(u.volumes || []).map((v) => `<code>${escapeHtml(v)}</code>`).join(", ")}.` : ""}
          <button class="btn btn-ghost btn-sm" data-restore-discard="${escapeHtml(u.slug)}">Delete the partial restore</button>
        </li>`).join("");
  const stuck = rows ? `
      <div class="restore-stuck">
        <p class="muted compact-copy">A restore did not finish:</p>
        <ul>${rows}</ul>
      </div>` : "";
  return `
    <section class="card" id="restore-card">
      <div class="card-header">
        <div>
          <span class="card-title">Restore from a backup</span>
          <p class="muted compact-copy">Put a node identity or wallet back from a <code>.cpbackup</code> file made with a passphrase:
            Mysterium, ProxyBase or Anyone. The restore only creates a new container on this machine, and never writes over a
            service that is here. To replace one, download its backup first, then remove it and say yes to deleting its data.
            Files sealed to a key, Storj and Sia use the command line steps in the backup guide: ${GUIDE}</p>
        </div>
        <button class="compact-btn" id="restore-choose">Choose a backup file</button>
      </div>
      ${stuck}
      <div id="restore-preview"></div>
    </section>`;
}

/** What restoring the chosen file would do, and the form that does it. */
export function renderRestorePreview(p: RestorePreview): string {
  const service = escapeHtml(p.service || p.slug);
  const creates = (p.creates || []).map((c) =>
    `<li>volume <code>${escapeHtml(c.volume)}</code> at <code>${escapeHtml(c.target)}</code></li>`).join("");
  const runsOn = p.runsOn || [];
  // Never less strict than the Go side: a file this app did not make always asks.
  const needsName = p.needsName || !p.known;
  const typeIt = `To go on, type <strong>${service}</strong>:`;
  const origin = p.known
    ? `<p class="muted compact-copy">This app made this file.</p>`
    : `<p class="restore-warn">This app did not make this file${p.madeOn ? ` (it says it came from ${escapeHtml(p.madeOn)})` : ""},
        so it cannot tell whose identity or wallet is inside. Restore only a file you made yourself.${runsOn.length && p.ok ? "" : ` ${typeIt}`}</p>`;
  // Any machine that runs the service now may run the very node in the file, made
  // here or not: the export record says who wrote the file, not where its identity
  // went since.
  const elsewhere = p.ok && runsOn.length
    ? `<p class="restore-warn">${service} runs now on ${escapeHtml(runsOn.join(", "))}.${p.known
        ? " This app made this file, but it cannot tell whether that node came from it."
        : ""} If this file is a backup of that node, restoring it puts one identity on two machines: remove it there first. ${typeIt}</p>`
    : "";
  const confirmName = needsName
    ? `<input type="text" name="confirm_name" autocomplete="off" spellcheck="false" aria-label="Type the service name">`
    : "";
  const refused = p.ok ? "" : `<p class="restore-refused" role="alert">This machine cannot restore it now: ${escapeHtml(p.why)}</p>`;
  return `
    <form class="restore-form" data-token="${escapeHtml(p.token)}" data-service="${service}">
      <p><code>${escapeHtml(p.file)}</code>: restoring <strong>${service}</strong> creates a new container
        <code>${escapeHtml(p.container)}</code> with:</p>
      <ul>${creates}</ul>
      <p class="muted compact-copy">It never writes over anything: ${service} must not be on this machine and these volumes must
        not exist. The backup is written, read back and compared before the container first starts.</p>
      ${refused}${origin}${elsewhere}${confirmName}
      <input type="password" name="passphrase" autocomplete="off" required aria-label="Passphrase of the backup" placeholder="Passphrase of the backup">
      <button type="submit" class="btn btn-primary btn-sm"${p.ok ? "" : " disabled"}>Restore</button>
    </form>`;
}

/** The toast after a restore that ran. */
export function restoreResultText(service: string, r: RestoreResult): string {
  const files = `${r.files} ${r.files === 1 ? "file" : "files"}`;
  if (r.started) return `${service} was restored (${files}, checked against the backup) and started.`;
  return `${service} was restored (${files}, checked against the backup). It waits for the LAN fence and starts once it holds${r.waiting ? `: ${r.waiting}` : "."}`;
}
