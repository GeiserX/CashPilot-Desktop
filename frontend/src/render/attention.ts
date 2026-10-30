// The "Needs attention" list at the top of the dashboard, the web console's
// first block after the money.
//
// It answers one question: what does a person have to do right now? Each row is
// one problem with the action that fixes it beside it. Failures sort first, then
// warnings. With nothing to do it says so in one line instead of an empty box,
// because an empty card every day trains people to stop looking at it.
//
// Pure: takes plain state, returns HTML.

import type { HealthScore, ServiceCopy } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

export interface AttentionInput {
  runtime: { available: boolean; nativeAvailable: boolean; message: string };
  services: { slug: string; name: string }[];
  deployments: { slug: string; name: string; status: string }[];
  copies?: Record<string, ServiceCopy[]> | null;
  earnings: { platform: string; error?: string }[];
  outdated?: string[] | null;
  health?: Record<string, HealthScore> | null;
}

export interface AttentionItem {
  level: "fail" | "warn";
  tag: string;
  what: string;
  where?: string;
  /** Already-escaped HTML. */
  why: string;
  /** Already-escaped HTML. */
  actions: string;
}

/** The web console's reading of a container state: up, down or somewhere between. */
export function containerState(status: string): "ok" | "warn" | "fail" {
  const s = String(status || "unknown").toLowerCase();
  if (s === "running") return "ok";
  if (s === "exited" || s === "stopped" || s === "dead") return "fail";
  return "warn";
}

export function attentionItems(input: AttentionInput): AttentionItem[] {
  const items: AttentionItem[] = [];
  const nameOf = (slug: string) => input.services.find((s) => s.slug === slug)?.name || slug;
  const known = (slug: string) => input.services.some((s) => s.slug === slug);

  if (!input.runtime.available && !input.runtime.nativeAvailable) {
    items.push({
      level: "fail",
      tag: "Offline",
      what: "Runtime",
      why: `${escapeHtml(input.runtime.message || "Neither Docker nor the native runtime can run anything.")} Services cannot start until it is back.`,
      actions: "",
    });
  }

  const down = (slug: string, name: string, status: string, copy: number) => {
    const st = containerState(status);
    if (st === "ok") return;
    const copyAttr = copy > 1 ? ` data-copy="${copy}"` : "";
    items.push({
      level: st,
      tag: st === "fail" ? "Down" : "Degraded",
      what: nameOf(slug),
      where: copy > 1 ? `copy ${copy}` : undefined,
      why: `container is ${escapeHtml(status || "unknown")}${name ? ` (<code>${escapeHtml(name)}</code>)` : ""}`,
      actions: `<button class="btn btn-ghost btn-sm" data-row-action="logs" data-slug="${escapeHtml(slug)}"${copyAttr}>Logs</button>
        <button class="btn btn-primary btn-sm" data-row-action="start" data-slug="${escapeHtml(slug)}"${copyAttr}>Start</button>`,
    });
  };
  for (const dep of input.deployments) {
    down(dep.slug, dep.name, dep.status, 1);
    for (const copy of input.copies?.[dep.slug] || []) down(copy.slug, copy.name, copy.status, copy.instance);
  }

  for (const dep of input.deployments) {
    const crashes = input.health?.[dep.slug]?.crashes ?? 0;
    if (crashes >= 3) {
      items.push({ level: "warn", tag: "Unstable", what: nameOf(dep.slug), why: `${crashes} crashes in the last 7 days`, actions: "" });
    }
  }

  for (const record of input.earnings) {
    if (!record.error) continue;
    items.push({
      level: "warn",
      tag: "No reading",
      what: nameOf(record.platform),
      why: `CashPilot could not read this balance: ${escapeHtml(record.error)}`,
      actions: known(record.platform)
        ? `<button class="btn btn-ghost btn-sm" data-open-service="${escapeHtml(record.platform)}">Fix credentials</button>`
        : "",
    });
  }

  for (const slug of input.outdated || []) {
    items.push({
      level: "warn",
      tag: "Outdated",
      what: nameOf(slug),
      why: "runs an image that no longer matches the catalog. Re-deploy it to update.",
      actions: known(slug) ? `<button class="btn btn-ghost btn-sm" data-open-service="${escapeHtml(slug)}">Open</button>` : "",
    });
  }

  const order = { fail: 0, warn: 1 };
  // Array.prototype.sort is stable, so within a level the order above holds.
  return items.sort((a, b) => order[a.level] - order[b.level]);
}

/**
 * The whole block. `up` and `total` are the container counts for the all-clear
 * line, so a quiet list still says what it checked.
 */
export function renderAttention(items: AttentionItem[], up: number, total: number): string {
  const rows = items.map((i) => `
    <div class="attn-row attn-${i.level}">
      <span class="attn-tag">${escapeHtml(i.tag)}</span>
      <span class="attn-what">${escapeHtml(i.what)}${i.where ? ` <span class="attn-where">${escapeHtml(i.where)}</span>` : ""}</span>
      <span class="attn-why">${i.why}</span>
      <span class="attn-actions">${i.actions}</span>
    </div>`).join("");
  return `
    <section class="attention${items.length ? "" : " attention-quiet"}" aria-labelledby="attention-title">
      <div class="block-head">
        <h2 class="block-title" id="attention-title">Needs attention${items.length ? ` <span class="count">${items.length}</span>` : ""}</h2>
      </div>
      ${items.length
        ? `<div class="attn-list">${rows}</div>`
        : `<p class="attn-clear">Nothing needs you. ${up} of ${total} container${total === 1 ? "" : "s"} up.</p>`}
    </section>
  `;
}
