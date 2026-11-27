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
import { intlLocale, ht, t } from "./i18n.js";

/** "24 Nov, 14:00" in this computer's time zone, or "" for no time. */
export function stallSince(since: number): string {
  if (!Number.isFinite(since) || since <= 0) return "";
  const d = new Date(since * 1000);
  const month = new Intl.DateTimeFormat(intlLocale(), { month: "short" }).format(d);
  const time = `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  return t("stall.since", { day: d.getDate(), month, time });
}

export function stallItems(stalls: StallAlert[] | null | undefined): AttentionItem[] {
  return (stalls || []).map((s) => {
    const since = stallSince(s.since);
    const why = `${escapeHtml(s.message)}${since ? ` ${ht("stall.lowSince", { since })}` : ""}`;
    if (s.proxyId) {
      // A proxied copy has no Logs button: the logs route opens the service's own copies.
      return { level: "warn", tag: t("stall.tag"), what: s.service || s.slug, where: t("stall.proxiedCopy"), why, actions: "" };
    }
    const copyAttr = s.copy > 1 ? ` data-copy="${s.copy}"` : "";
    return {
      level: "warn",
      tag: t("stall.tag"),
      what: s.service || s.slug,
      where: t("stall.copy", { n: s.copy }),
      why,
      actions: `<button class="btn btn-ghost btn-sm" data-row-action="logs" data-slug="${escapeHtml(s.slug)}"${copyAttr}>${ht("stall.logs")}</button>`,
    };
  });
}
