// The note under Needs attention while a CashPilot server raises the "Almost no
// traffic" alerts of this computer's copies (AppState.stallsServer,
// stall_alerts.go stallServer). The server compares each copy with the whole
// fleet from the traffic counters each heartbeat carries, so this computer raises
// none for them. It still checks its proxied copies, which the server does not.
//
// Without the note, an empty list here would read as "no copy is idle" while the
// alerts are on the server.
//
// Pure: takes plain state, returns HTML.

import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** The note, or "" while this computer raises the alerts itself. */
export function renderStallServerNote(serverUrl: string | null | undefined): string {
  const url = (serverUrl || "").trim();
  if (!url) return "";
  return `
    <section class="attention attention-quiet" aria-label="Almost no traffic">
      <p class="mw-note">Almost no traffic: your CashPilot at <code>${escapeHtml(url)}</code> compares the copies on this computer with the rest of your fleet, and shows those alerts. This computer still checks its proxied copies itself.</p>
    </section>`;
}
