// The "Money waiting" panel under Needs attention: what to claim, withdraw or keep
// active before a provider writes off the money (internal/obligations, a port of
// the web app's #814). Each row says which provider, what to do, by when and what
// is lost if not.
//
// Catalog text goes through escapeHtml. A failed check reads "Could not check",
// never an all-clear. The panel hides while there is nothing to do, like the web
// one: an empty panel every day teaches people to skip the one day it matters.
//
// Pure: takes plain state, returns HTML.

import type { ReminderItem, RemindersView } from "../wails";
import type { AttentionItem } from "./attention.js";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, intlLocale, t } from "./i18n.js";

/** A reminder action's tag, or "" for one this file does not know. */
function actionTag(action: string): string {
  switch (action) {
    case "claim":
      return t("reminders.tag.claim");
    case "withdraw":
      return t("reminders.tag.withdraw");
    case "keep-active":
      return t("reminders.tag.keepActive");
    default:
      return "";
  }
}

/** "22 Jan 2027" for a YYYY-MM-DD day, drawn as that day whatever the time zone. */
export function reminderDate(iso: string): string {
  const [y, m, d] = String(iso || "").split("-").map(Number);
  if (!y || !m || !d || m > 12) return iso;
  const month = new Intl.DateTimeFormat(intlLocale(), { month: "short", timeZone: "UTC" }).format(Date.UTC(y, m - 1, 1));
  return t("reminders.date", { day: d, month, year: y });
}

/** "by 22 Jan 2027, 52 days", "… today", "… 3 days ago"; "" with no due date. */
export function reminderWhen(item: ReminderItem): string {
  if (!item.due) return "";
  const date = reminderDate(item.due);
  const by = item.censored ? t("reminders.byOrEarlier", { date }) : t("reminders.by", { date });
  const n = item.daysLeft;
  if (n == null) return by;
  if (n < 0) return t("reminders.ago", { by, n: -n });
  if (n === 0) return t("reminders.today", { by });
  return t("reminders.left", { by, n });
}

function urgent(item: ReminderItem): boolean {
  return item.stage === "overdue" || (item.daysLeft != null && item.daysLeft <= 7);
}

function button(action: string, item: { slug: string; rule: string }, label: string, extra = ""): string {
  return `<button class="btn btn-ghost btn-sm" data-reminder-action="${action}" data-slug="${escapeHtml(item.slug)}" data-rule="${escapeHtml(item.rule)}"${extra}>${escapeHtml(label)}</button>`;
}

function reminderRow(item: ReminderItem): string {
  const cash = item.kind === "cash-out";
  let tag = actionTag(item.action) || t("reminders.tag.act");
  if (cash) tag = t("reminders.tag.cashOut");
  if (item.stage === "overdue") tag = t("reminders.tag.overdue");
  if (item.stage === "recheck") tag = t("reminders.tag.arrived");
  const level = item.stage === "overdue" ? "fail" : urgent(item) || item.stage === "recheck" ? "warn" : "ask";

  let what = escapeHtml(item.title);
  if (cash && item.balance != null) {
    what += `: ${escapeHtml(`${Number(item.balance).toFixed(2)} ${item.currency || "USD"}`)}`;
  }
  const parts = [`<span class="mw-title">${what}</span>`];
  const when = reminderWhen(item);
  if (when) parts.push(`<span class="mw-when">${escapeHtml(when)}</span>`);
  if (item.stage === "recheck") parts.push(`<span class="mw-loss">${ht("reminders.recheck")}</span>`);
  else if (item.stage === "overdue") parts.push(`<span class="mw-loss">${ht("reminders.overdue")}</span>`);
  parts.push(`<span class="mw-loss">${escapeHtml(item.loss)}</span>`);
  if (item.how) parts.push(`<details class="mw-how"><summary>${ht("reminders.how")}</summary>${escapeHtml(item.how)}</details>`);

  const actions: string[] = [];
  if (item.url) actions.push(`<button class="btn btn-primary btn-sm" data-url="${escapeHtml(item.url)}">${ht("reminders.open", { name: item.name })}</button>`);
  if (item.canDone) {
    const label = item.stage === "recheck" ? t("reminders.arrivedButton") : cash || item.action === "withdraw" ? t("reminders.requested") : t("reminders.done");
    actions.push(button("done", item, label, ` data-occurrence="${escapeHtml(item.occurrence)}"`));
  }
  if (item.canSnooze) actions.push(button("snooze", item, t("reminders.snoozeDay"), ` data-days="1"`));
  const more: string[] = [];
  if (item.canSnooze) more.push(button("snooze", item, t("reminders.snoozeWeek"), ` data-days="7"`));
  more.push(button("mute", item, t("reminders.notForMe")));
  more.push(button("mute", { slug: item.slug, rule: "*" }, t("reminders.muteAll", { name: item.name })));
  if (item.source) more.push(`<button class="btn btn-ghost btn-sm" data-url="${escapeHtml(item.source)}">${ht("reminders.source", { day: item.checked })}</button>`);
  actions.push(`<details class="mw-more"><summary class="btn btn-ghost btn-sm">${ht("reminders.more")}</summary><div class="mw-menu">${more.join("")}</div></details>`);

  return `
    <div class="attn-row attn-${level} mw-row" data-reminder="${escapeHtml(item.slug)}:${escapeHtml(item.rule)}">
      <span class="attn-tag">${escapeHtml(tag)}</span>
      <span class="attn-what">${escapeHtml(item.name)}</span>
      <span class="attn-why mw-why">${parts.join("")}</span>
      <span class="attn-actions">${actions.join("")}</span>
    </div>`;
}

/** The whole panel, or "" when there is nothing to show. */
export function renderReminders(view: RemindersView | null | undefined): string {
  if (!view) return "";
  if (view.joined) {
    return `
    <section class="attention money-waiting attention-quiet" aria-label="${ht("reminders.title")}">
      <p class="mw-note">${ht("reminders.joined")}</p>
    </section>`;
  }
  const head = (count: string) => `
      <div class="block-head">
        <h2 class="block-title" id="money-waiting-title">${ht("reminders.title")}${count}</h2>
      </div>`;
  if (view.unknown) {
    return `
    <section class="attention money-waiting" aria-labelledby="money-waiting-title">${head("")}
      <div class="attn-list"><div class="attn-row attn-warn"><span class="attn-tag">${ht("reminders.tag.unknown")}</span><span class="attn-what">${ht("reminders.title")}</span><span class="attn-why">${ht("reminders.unknown")}</span><span class="attn-actions"></span></div></div>
    </section>`;
  }
  const items = view.items || [];
  const muted = view.muted || [];
  if (!items.length && !muted.length) return "";
  const foot = muted.length
    ? `<div class="attn-foot mw-foot">${ht("reminders.mutedList")} ${muted
        .map((m) => `${escapeHtml(m.name)}, ${escapeHtml(m.title)} <button class="btn btn-ghost btn-sm" data-reminder-action="unmute" data-slug="${escapeHtml(m.slug)}" data-rule="${escapeHtml(m.rule)}">${ht("reminders.unmute")}</button>`)
        .join(" · ")}</div>`
    : "";
  return `
    <section class="attention money-waiting" aria-labelledby="money-waiting-title">${head(items.length ? ` <span class="count">${items.length}</span>` : "")}
      <p class="mw-intro">${ht("reminders.intro")}</p>
      ${items.length ? `<div class="attn-list">${items.map(reminderRow).join("")}</div>` : ""}
      ${foot}
    </section>`;
}

/**
 * Needs attention gets one row while a reminder is overdue or due within 7 days,
 * so "Nothing needs you" is never said the week a deadline falls.
 */
export function reminderAttention(view: RemindersView | null | undefined): AttentionItem[] {
  if (!view || view.joined || view.unknown) return [];
  const due = (view.items || []).filter(urgent);
  if (!due.length) return [];
  const overdue = due.some((i) => i.stage === "overdue");
  const list = due.map((i) => `${escapeHtml(i.name)}: ${escapeHtml(i.title)}, ${escapeHtml(reminderWhen(i))}`).join("; ");
  return [{
    level: overdue ? "fail" : "warn",
    tag: overdue ? t("reminders.tag.overdue") : t("reminders.tag.deadline"),
    what: due.length === 1 ? due[0].name : t("reminders.count", { n: due.length }),
    why: `${list}. ${ht("reminders.seeBelow")}`,
    actions: "",
  }];
}

/** The attention list with the reminder row in place, failures still first. */
export function withReminders(items: AttentionItem[], view: RemindersView | null | undefined): AttentionItem[] {
  const order = { fail: 0, warn: 1 };
  return [...items, ...reminderAttention(view)].sort((a, b) => order[a.level] - order[b.level]);
}
