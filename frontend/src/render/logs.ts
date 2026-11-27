// The dashboard's log view. Docker starts every line with an RFC 3339 time in
// nanoseconds, which is often longer than the message, so nearly every entry
// wrapped onto two or three lines. Each entry now gets one line, with the time
// shown short and in local time; the full value stays in its tooltip. The web
// dashboard shows logs the same way (CashPilot app/static/js/app.js logEntries).

import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node
import { ht } from "./i18n.js";

export interface LogEntry {
  /** The time as Docker wrote it, or "" for a line without one. */
  stamp: string;
  /** What the view shows: HH:MM:SS, or YYYY-MM-DD HH:MM:SS when any line is from another day. */
  shown: string;
  message: string;
}

const LOG_TIME = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(\.\d+)?(Z|[+-]\d{2}:\d{2}) ?/;

const pad = (n: number) => String(n).padStart(2, "0");
const localDay = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;

/** The entries of a log text, and the width in characters of their time column. */
export function logEntries(text: string, now: Date): {entries: LogEntry[]; width: number} {
  const parsed = String(text ?? "").replace(/\r?\n$/, "").split(/\r?\n/).map((raw) => {
    const m = LOG_TIME.exec(raw);
    // Date reads at most milliseconds.
    const at = m ? new Date(`${m[1]}${(m[2] || "").slice(0, 4)}${m[3]}`) : null;
    if (!m || !at || Number.isNaN(at.getTime())) return {stamp: "", day: "", time: "", message: raw};
    const time = `${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`;
    return {stamp: m[0].trim(), day: localDay(at), time, message: raw.slice(m[0].length)};
  });
  const today = localDay(now);
  const stamped = parsed.filter((e) => e.stamp);
  const dated = stamped.some((e) => e.day !== today);
  const entries = parsed.map((e) => ({
    stamp: e.stamp,
    shown: e.stamp ? (dated ? `${e.day} ${e.time}` : e.time) : "",
    message: e.message,
  }));
  return {entries, width: stamped.length ? (dated ? 19 : 8) : 0};
}

/** The log view's inner HTML: the Wrap lines switch, then one row per entry. */
export function renderLogLines(text: string, now: Date, wrap: boolean): string {
  const {entries, width} = logEntries(text, now);
  const rows = entries
    .map((e) => {
      const ts = e.shown ? `<span class="log-ts" title="${escapeHtml(e.stamp)}">${escapeHtml(e.shown)}</span>` : "";
      return `<div class="log-line">${ts}${escapeHtml(e.message)}</div>`;
    })
    .join("");
  return (
    `<div class="log-bar"><button type="button" class="btn btn-ghost btn-sm" data-log-wrap aria-pressed="${wrap}">${ht("logs.wrap")}</button></div>` +
    `<div class="log-lines" style="--log-ts:${width}ch">${rows}</div>`
  );
}
