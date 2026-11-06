// The "Needs attention" list at the top of the dashboard, the web console's
// first block after the money.
//
// It answers one question: what does a person have to do right now? Each row is
// one problem with the action that fixes it beside it. Failures sort first, then
// warnings. With nothing to do it says so in one line instead of an empty box,
// because an empty card every day trains people to stop looking at it.
//
// Pure: takes plain state, returns HTML.

import type { FenceStatus, HealthScore, ProducerReport, ServiceCopy } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

export interface AttentionInput {
  runtime: { available: boolean; nativeAvailable: boolean; message: string };
  services: { slug: string; name: string; supportUrl?: string }[];
  deployments: { slug: string; name: string; status: string; runtime?: string }[];
  copies?: Record<string, ServiceCopy[]> | null;
  earnings: { platform: string; error?: string; errorKind?: string }[];
  outdated?: string[] | null;
  health?: Record<string, HealthScore> | null;
  /** Per slug, how its container differs from what a redeploy would build. */
  drift?: Record<string, string[]> | null;
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
  } else if (!input.runtime.available && input.deployments.some((dep) => dep.runtime && dep.runtime !== "native")) {
    // Native services still run, so this is not the whole app being down, but
    // every container service is.
    items.push({
      level: "fail",
      tag: "Offline",
      what: "Container runtime",
      why: `${escapeHtml(input.runtime.message || "Docker is not reachable.")} Container services cannot start until it is back; native ones keep running.`,
      actions: "",
    });
  }

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
    const name = escapeHtml(nameOf(slug));
    const support = /^https:\/\//.test(svc?.supportUrl || "") ? svc!.supportUrl! : "";
    items.push({
      level: "fail",
      tag: "Account disabled",
      what: nameOf(slug),
      why: `${name} turned this account off, so it earns nothing and no balance can be read. Restarting will not help: contact ${name} to get it back. CashPilot leaves its containers as they are. Once ${name} turns the account back on, remove the service and deploy it again.`,
      actions: support
        ? `<button class="btn btn-ghost btn-sm" data-url="${escapeHtml(support)}">Contact ${name}</button>`
        : "",
    });
  }

  const down = (slug: string, name: string, status: string, copy: number) => {
    if (disabled.has(slug)) return;
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
  for (const dep of input.deployments) down(dep.slug, dep.name, dep.status, 1);
  for (const list of Object.values(input.copies || {})) {
    for (const copy of list) down(copy.slug, copy.name, copy.status, copy.instance);
  }

  for (const dep of input.deployments) {
    const crashes = input.health?.[dep.slug]?.crashes ?? 0;
    if (crashes >= 3 && !disabled.has(dep.slug)) {
      items.push({ level: "warn", tag: "Unstable", what: nameOf(dep.slug), why: `${crashes} crashes in the last 7 days`, actions: "" });
    }
  }

  for (const record of input.earnings) {
    if (!record.error || disabled.has(record.platform)) continue;
    if (record.errorKind === "unread") {
      // The login worked; nothing here could read the figure (no node of this
      // service runs on this machine). New credentials would not help.
      items.push({
        level: "warn",
        tag: "No node read",
        what: nameOf(record.platform),
        why: escapeHtml(record.error),
        actions: `<button class="btn btn-ghost btn-sm" data-row-action="collect" data-slug="${escapeHtml(record.platform)}">Retry</button>`,
      });
      continue;
    }
    items.push({
      level: "warn",
      tag: "No reading",
      what: nameOf(record.platform),
      why: `CashPilot could not read this balance: ${escapeHtml(record.error)}`,
      // A timeout or a provider outage clears on a retry; a rejected login needs
      // the credentials. The message cannot always tell which, so offer both.
      actions: `<button class="btn btn-ghost btn-sm" data-row-action="collect" data-slug="${escapeHtml(record.platform)}">Retry</button>${
        known(record.platform)
          ? `\n        <button class="btn btn-ghost btn-sm" data-open-service="${escapeHtml(record.platform)}">Fix credentials</button>`
          : ""
      }`,
    });
  }

  // A container made before a catalog fix: it runs, and so looks healthy, without
  // what the catalog now gives it. Bitping ran three months without NET_RAW this way.
  for (const [slug, found] of Object.entries(input.drift || {})) {
    if (!found || !found.length) continue;
    const s = escapeHtml(slug);
    items.push({
      level: "fail",
      tag: "Needs fixing",
      what: nameOf(slug),
      why: `container does not match the catalog: ${found.map((d) => escapeHtml(d)).join("; ")}. Redeploy it: CashPilot keeps the settings it was deployed with.`,
      actions: `<button class="btn btn-primary btn-sm" data-drift-action="redeploy" data-slug="${s}">Redeploy</button>
        <button class="btn btn-ghost btn-sm" data-drift-action="ignore" data-slug="${s}" title="Stop warning about this service until the catalog changes it again">Ignore</button>`,
    });
  }

  // The LAN fence. With isolation off nothing is said: that is the user's choice in
  // Settings, and the Settings help names what it costs.
  const fence = input.isolation;
  if (fence && !fence.mode && fence.setting !== "off" && fence.state === "failed") {
    // The check itself failed (Docker did not answer), so no mode was settled.
    items.push({
      level: "fail",
      tag: "Fence unknown",
      what: "LAN isolation",
      why: `CashPilot could not check the fence that keeps earners off your LAN${fence.error ? `: ${escapeHtml(fence.error)}` : "."} Container deploys are refused until it can.`,
      actions: "",
    });
  } else if (fence && fence.mode === "on" && fence.state === "failed") {
    items.push({
      level: "fail",
      tag: "Fence down",
      what: "LAN isolation",
      why: `the fence that keeps earners off your LAN could not be applied${fence.error ? `: ${escapeHtml(fence.error)}` : "."} Container deploys are refused until it can be. To deploy without it, set LAN isolation to off in Settings.`,
      actions: "",
    });
  } else if (fence && fence.mode === "unsupported") {
    // Podman: the fence cannot be set up there, so earners run unfenced. Said once,
    // with no switch, because no switch can fix it.
    items.push({
      level: "warn",
      tag: "Not fenced",
      what: "LAN isolation",
      why: `${fence.error ? escapeHtml(fence.error.replace(/\.\s*$/, "")) : "CashPilot cannot fence earners off the LAN on this container runtime"}. Container earners run on its default network and can reach your LAN and this computer.${
        fence.setting === "on" ? " With LAN isolation set to on, container deploys are refused; set it to auto or off in Settings to deploy without the fence." : ""
      }`,
      actions: "",
    });
  }
  const outside = fence && fence.mode !== "off" && fence.mode !== "unsupported" ? fence.outside || [] : [];
  if (outside.length) {
    const stopped = fence?.outsideStopped || [];
    const names = outside.map((slug) => escapeHtml(nameOf(slug))).join(", ");
    items.push({
      level: "warn",
      tag: "Exposed",
      what: outside.length === 1 ? nameOf(outside[0]) : `${outside.length} services`,
      why: `${names} can reach your LAN and this computer. Turn on isolation to redeploy ${outside.length === 1 ? "it" : "them"} onto CashPilot's fenced network, with the same settings and data.${
        stopped.length ? ` Stopped services (${stopped.map((slug) => escapeHtml(nameOf(slug))).join(", ")}) are left alone and move on their next deploy.` : ""
      }`,
      actions: `<button class="btn btn-primary btn-sm" data-isolation-action="enable">Turn on isolation</button>`,
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
