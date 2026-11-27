// The "Needs attention" list at the top of the dashboard, the web console's
// first block after the money.
//
// It answers one question: what does a person have to do right now? Each row is
// one problem with the action that fixes it beside it. Failures sort first, then
// warnings. With nothing to do it says so in one line instead of an empty box,
// because an empty card every day trains people to stop looking at it.
//
// Pure: takes plain state, returns HTML.

import { deployNotice, type DeployStatus } from "./deploy_consent.js";
import type { FenceStatus, HealthScore, ProducerReport, ServiceCopy } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";
import { dockerBlocked, dockerBlockedNote, runtimeAttention, type RuntimeInput } from "./runtime_state.js";

export interface AttentionInput {
  runtime: RuntimeInput;
  services: { slug: string; name: string; supportUrl?: string }[];
  deployments: { slug: string; name: string; status: string; runtime?: string }[];
  copies?: Record<string, ServiceCopy[]> | null;
  earnings: { platform: string; error?: string; errorKind?: string }[];
  outdated?: string[] | null;
  health?: Record<string, HealthScore> | null;
  /** Per slug, how its container differs from what a redeploy would build. */
  drift?: Record<string, string[]> | null;
  driftDeploys?: Record<string, DeployStatus>;
  producerStates?: Record<string, ProducerReport> | null;
  /** The LAN fence: which earners can reach the LAN, and whether the fence holds. */
  isolation?: FenceStatus | null;
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
  /** Already-escaped HTML for a deploy result under the row. */
  deployStatus?: string;
  /** Already-escaped HTML: why the row's action waits for Docker, with the way there. */
  blocked?: string;
}

/** The web console's reading of a container state: up, down or somewhere between. */
export function containerState(status: string): "ok" | "warn" | "fail" {
  const s = String(status || "unknown").toLowerCase();
  if (s === "running") return "ok";
  if (s === "exited" || s === "stopped" || s === "dead") return "fail";
  return "warn";
}

// gatedNote keeps the isolation-off advice from reaching a fence-gated earner, which
// isolation off refuses: it says that earner stays stopped until the fence is back.
function gatedNote(slugs: string[], nameOf: (slug: string) => string): string {
  if (!slugs.length) return "";
  const names = slugs.map((slug) => nameOf(slug)).join(", ");
  return ` ${ht("attention.gated", { n: slugs.length, names })}`;
}

export function attentionItems(input: AttentionInput): AttentionItem[] {
  const items: AttentionItem[] = [];
  const nameOf = (slug: string) => input.services.find((s) => s.slug === slug)?.name || slug;
  const known = (slug: string) => input.services.some((s) => s.slug === slug);
  // Start, Redeploy and Turn on isolation run containers: they wait for Docker.
  const blocked = dockerBlocked(input.runtime);
  const blockedAttr = blocked ? ` disabled title="${escapeHtml(blocked)}"` : "";
  const blockedNote = dockerBlockedNote(input.runtime);

  // No Docker engine answers: always a row, with the way to the Docker setup
  // (render/runtime_state.ts), so "Nothing needs you" never shows then.
  const runtime = runtimeAttention(input.runtime, input.deployments.some((dep) => dep.runtime && dep.runtime !== "native"));
  if (runtime) items.push(runtime);

  // A provider that disabled the account is ONE cause for the stopped container, its
  // copies, its crashes and the missing balance. Each of those rows would point at a
  // fix that cannot work (Start, Fix credentials), so the service gets one card that
  // names the cause and where to take it, and none of the others.
  const disabled = new Set(
    Object.values(input.producerStates || {})
      .filter((r) => r && r.state === "account_disabled")
      .map((r) => r.slug)
  );
  for (const slug of disabled) {
    const svc = input.services.find((s) => s.slug === slug);
    const name = nameOf(slug);
    const support = /^https:\/\//.test(svc?.supportUrl || "") ? svc!.supportUrl! : "";
    items.push({
      level: "fail",
      tag: t("attention.tag.disabled"),
      what: nameOf(slug),
      why: ht("attention.disabled", { name }),
      actions: support
        ? `<button class="btn btn-ghost btn-sm" data-url="${escapeHtml(support)}">${ht("attention.contact", { name })}</button>`
        : "",
    });
  }

  // A fence-gated earner (Mysterium) has no Docker restart policy: CashPilot starts
  // it itself, only behind a confirmed fence. Down and waiting for that, it is not a
  // container to start by hand.
  const fence = input.isolation;
  const waiting = new Set(fence?.waiting || []);
  const down = (slug: string, name: string, status: string, copy: number, native = false) => {
    if (disabled.has(slug)) return;
    const st = containerState(status);
    if (st === "ok") return;
    if (waiting.has(slug)) {
      const fenced = fence?.mode === "on";
      items.push({
        level: "warn",
        tag: t("attention.tag.waiting"),
        what: nameOf(slug),
        why: fence?.state === "applied" && fence.error
          ? ht("attention.waiting.error", { error: fence.error })
          : fenced
            ? ht("attention.waiting.fenced")
            : ht("attention.waiting.notOn"),
        actions: `<button class="btn btn-ghost btn-sm" data-row-action="logs" data-slug="${escapeHtml(slug)}">${ht("attention.logs")}</button>`,
      });
      return;
    }
    const copyAttr = copy > 1 ? ` data-copy="${copy}"` : "";
    const wait = native ? "" : blockedAttr;
    items.push({
      level: st,
      tag: st === "fail" ? t("attention.tag.down") : t("attention.tag.degraded"),
      what: nameOf(slug),
      where: copy > 1 ? t("attention.copy", { n: copy }) : undefined,
      why: name
        ? ht("attention.containerIsNamed", { status: status || t("attention.unknown"), name })
        : ht("attention.containerIs", { status: status || t("attention.unknown") }),
      actions: `<button class="btn btn-ghost btn-sm" data-row-action="logs" data-slug="${escapeHtml(slug)}"${copyAttr}>${ht("attention.logs")}</button>
        <button class="btn btn-primary btn-sm" data-row-action="start" data-slug="${escapeHtml(slug)}"${copyAttr}${wait}>${ht("attention.start")}</button>`,
      blocked: native ? undefined : blockedNote || undefined,
    });
  };
  for (const dep of input.deployments) down(dep.slug, dep.name, dep.status, 1, dep.runtime === "native");
  for (const list of Object.values(input.copies || {})) {
    for (const copy of list) down(copy.slug, copy.name, copy.status, copy.instance);
  }

  for (const dep of input.deployments) {
    const crashes = input.health?.[dep.slug]?.crashes ?? 0;
    if (crashes >= 3 && !disabled.has(dep.slug)) {
      items.push({ level: "warn", tag: t("attention.tag.unstable"), what: nameOf(dep.slug), why: ht("attention.crashes", { n: crashes }), actions: "" });
    }
  }

  for (const record of input.earnings) {
    if (!record.error || disabled.has(record.platform)) continue;
    if (record.errorKind === "unread") {
      // The login worked; nothing here could read the figure (no node of this
      // service runs on this machine). New credentials would not help.
      items.push({
        level: "warn",
        tag: t("attention.tag.noNodeRead"),
        what: nameOf(record.platform),
        why: escapeHtml(record.error),
        actions: `<button class="btn btn-ghost btn-sm" data-row-action="collect" data-slug="${escapeHtml(record.platform)}">${ht("attention.retry")}</button>`,
      });
      continue;
    }
    items.push({
      level: "warn",
      tag: t("attention.tag.noReading"),
      what: nameOf(record.platform),
      why: ht("attention.couldNotRead", { error: record.error }),
      // A timeout or a provider outage clears on a retry; a rejected login needs
      // the credentials. The message cannot always tell which, so offer both.
      actions: `<button class="btn btn-ghost btn-sm" data-row-action="collect" data-slug="${escapeHtml(record.platform)}">${ht("attention.retry")}</button>${
        known(record.platform)
          ? `\n        <button class="btn btn-ghost btn-sm" data-open-service="${escapeHtml(record.platform)}">${ht("attention.fixCredentials")}</button>`
          : ""
      }`,
    });
  }

  // A container made before a catalog fix: it runs, and so looks healthy, without
  // what the catalog now gives it. Bitping ran three months without NET_RAW this way.
  for (const [slug, found] of Object.entries(input.drift || {})) {
    if (!found || !found.length) continue;
    const s = escapeHtml(slug);
    const status = input.driftDeploys?.[slug];
    items.push({
      level: "fail",
      tag: t("attention.tag.needsFixing"),
      what: nameOf(slug),
      why: ht("attention.drift", { found: found.join("; ") }),
      actions: `<button class="btn btn-primary btn-sm" data-drift-action="redeploy" data-slug="${s}"${blockedAttr}>${ht("attention.redeploy")}</button>
        <button class="btn btn-ghost btn-sm" data-drift-action="ignore" data-slug="${s}" title="${ht("attention.ignoreTitle")}">${ht("attention.ignore")}</button>`,
      deployStatus: `<div data-drift-deploy-status="${s}">${status ? deployNotice(status.kind, status.text) : ""}</div>`,
      blocked: blockedNote || undefined,
    });
  }

  // The LAN fence. With isolation off nothing is said: that is the user's choice in
  // Settings, and the Settings help names what it costs.
  if (fence && !fence.mode && fence.setting !== "off" && fence.state === "failed") {
    // The check itself failed (Docker did not answer), so no mode was settled.
    items.push({
      level: "fail",
      tag: t("attention.tag.fenceUnknown"),
      what: t("attention.lanIsolation"),
      why: fence.error ? ht("attention.fenceUnknown.error", { error: fence.error }) : ht("attention.fenceUnknown"),
      actions: "",
    });
  } else if (fence && fence.mode === "on" && fence.state === "failed") {
    items.push({
      level: "fail",
      tag: t("attention.tag.fenceDown"),
      what: t("attention.lanIsolation"),
      why: `${fence.error ? ht("attention.fenceDown.error", { error: fence.error }) : ht("attention.fenceDown")}${gatedNote(fence.waiting || [], nameOf)}`,
      actions: "",
    });
  } else if (fence && fence.mode === "unsupported") {
    // Podman: the fence cannot be set up there, so earners run unfenced. Said once,
    // with no switch, because no switch can fix it.
    items.push({
      level: "warn",
      tag: t("attention.tag.notFenced"),
      what: t("attention.lanIsolation"),
      why: `${fence.error ? ht("attention.notFenced.error", { error: fence.error.replace(/\.\s*$/, "") }) : ht("attention.notFenced")}${
        fence.setting === "on" ? ` ${ht("attention.notFenced.settingOn")}` : ""
      }`,
      actions: "",
    });
  }
  const outside = fence && fence.mode !== "off" && fence.mode !== "unsupported" ? fence.outside || [] : [];
  if (outside.length) {
    const stopped = fence?.outsideStopped || [];
    const names = outside.map((slug) => nameOf(slug)).join(", ");
    items.push({
      level: "warn",
      tag: t("attention.tag.exposed"),
      what: outside.length === 1 ? nameOf(outside[0]) : t("attention.nServices", { n: outside.length }),
      why: `${ht("attention.exposed", { n: outside.length, names })}${
        stopped.length ? ` ${ht("attention.exposedStopped", { names: stopped.map((slug) => nameOf(slug)).join(", ") })}` : ""
      }`,
      actions: `<button class="btn btn-primary btn-sm" data-isolation-action="enable"${blockedAttr}>${ht("attention.turnOnIsolation")}</button>`,
      blocked: blockedNote || undefined,
    });
  }

  for (const slug of input.outdated || []) {
    items.push({
      level: "warn",
      tag: t("attention.tag.outdated"),
      what: nameOf(slug),
      why: ht("attention.outdated"),
      actions: known(slug) ? `<button class="btn btn-ghost btn-sm" data-open-service="${escapeHtml(slug)}">${ht("attention.open")}</button>` : "",
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
export function renderAttention(items: AttentionItem[], up: number, total: number, completedDeploys: DeployStatus[] = []): string {
  const rows = items.map((i) => `
    <div class="attn-row attn-${i.level}">
      <span class="attn-tag">${escapeHtml(i.tag)}</span>
      <span class="attn-what">${escapeHtml(i.what)}${i.where ? ` <span class="attn-where">${escapeHtml(i.where)}</span>` : ""}</span>
      <span class="attn-why">${i.why}</span>
      <span class="attn-actions">${i.actions}</span>
      ${i.blocked || ""}
      ${i.deployStatus || ""}
    </div>`).join("");
  return `
    <section class="attention${items.length ? "" : " attention-quiet"}" aria-labelledby="attention-title">
      <div class="block-head">
        <h2 class="block-title" id="attention-title">${ht("attention.title")}${items.length ? ` <span class="count">${items.length}</span>` : ""}</h2>
      </div>
      ${items.length
        ? `<div class="attn-list">${rows}</div>`
        : `<p class="attn-clear">${ht("attention.allClear", { up, total })}</p>`}
      ${completedDeploys.length ? `<div class="drift-deploy-results">${completedDeploys.map((result) => deployNotice(result.kind, result.text)).join("")}</div>` : ""}
    </section>
  `;
}
