// The "Almost no traffic" rows of the Needs attention list: a copy that runs but
// gets almost no work while the other copies of its service here are busy
// (stall_alerts.go, the web app's stall alert, GeiserX/CashPilot#808).
//
// The container's own row says it runs, so only this row says it idles. The
// backend writes the message; this adds when it began and the Logs button.
//
// Pure: takes plain state, returns HTML.

import type { StallAlert } from "../wails";
import type { AttentionItem } from "./attention.js";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "24 Nov, 14:00" in this computer's time zone, or "" for no time. */
export function stallSince(since: number): string {
  if (!Number.isFinite(since) || since <= 0) return "";
  const d = new Date(since * 1000);
  return `${d.getDate()} ${MONTHS[d.getMonth()]}, ${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

export function stallItems(stalls: StallAlert[] | null | undefined): AttentionItem[] {
  return (stalls || []).map((s) => {
    const since = stallSince(s.since);
    const copyAttr = s.copy > 1 ? ` data-copy="${s.copy}"` : "";
    return {
      level: "warn",
      tag: "Almost no traffic",
      what: s.service || s.slug,
      where: `copy ${s.copy}`,
      why: `${escapeHtml(s.message)}${since ? ` Low since ${escapeHtml(since)}.` : ""}`,
      actions: `<button class="btn btn-ghost btn-sm" data-row-action="logs" data-slug="${escapeHtml(s.slug)}"${copyAttr}>Logs</button>`,
    };
  });
}
