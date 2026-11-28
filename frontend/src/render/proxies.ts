// The Proxies section of the Fleet page: proxies the owner runs copies of services
// through, so this machine earns on more than one exit IP. It mirrors the Proxies
// section of the web console's fleet page, documented at PROXIES_DOCS_URL; the
// Desktop differences are in this repo's docs/proxies.md.
//
// Pure: takes the state the backend sent, returns HTML, touches no global.
//
// Two rules this file exists to keep:
//   - a proxy's user, password or auth key is never printed. The backend never sends
//     them (only "set: true"), and the editor's boxes for them are always empty;
//   - an exit IP nobody has seen yet renders as an em dash, never as an address.

import type { ProxiesState, ProxyCopyView, ProxyView } from "../wails";
import { deployNotice, type DeployStatus } from "./deploy_consent.js";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, raw, t } from "./i18n.js";
import { ackText } from "./route.js";
import { dockerBlocked, dockerBlockedNote, type RuntimeInput } from "./runtime_state.js";

const GB = 1_000_000_000;

/** The published page on proxied copies, opened from the section's help link. */
export const PROXIES_DOCS_URL = "https://geiserx.github.io/CashPilot/proxies/";

/** Bytes in the decimal GB providers sell, one decimal under 100 GB. */
export function formatGB(bytes: number): string {
  const gb = (Number.isFinite(bytes) ? bytes : 0) / GB;
  return `${gb < 100 ? gb.toFixed(1) : Math.round(gb)} GB`;
}

function kindLabel(kind: string): string {
  switch (kind) {
    case "socks5": return "SOCKS5";
    case "http": return "HTTP CONNECT";
    case "tailscale": return t("proxies.kind.tailscale");
    default: return kind;
  }
}

function secretName(name: string): string {
  switch (name) {
    case "username": return t("proxies.secret.username");
    case "password": return t("proxies.secret.password");
    case "auth_key": return t("proxies.secret.authKey");
    default: return name.replace("_", " ");
  }
}

function target(p: ProxyView): string {
  if (p.kind === "tailscale") return ht("proxies.exitNode", { node: p.exitNode ?? "" });
  return `${escapeHtml(p.host)}:${escapeHtml(String(p.port))}`;
}

function exitText(ip: string, country: string): string {
  if (!ip) return "&mdash;";
  return `${escapeHtml(ip)}${country ? ` (${escapeHtml(country)})` : ""}`;
}

/** The cap line: off, or used of cap, where it stops, and when it resets. */
export function capText(p: ProxyView): string {
  const cap = p.cap;
  if (!cap.enabled) return t("proxies.noCap");
  const used = formatGB(cap.usedBytes);
  const total = formatGB(cap.capBytes);
  return cap.resetDay
    ? t("proxies.cap.resetDay", { used, total, pct: String(cap.thresholdPct), day: String(cap.resetDay) })
    : t("proxies.cap.resetByHand", { used, total, pct: String(cap.thresholdPct) });
}

function copyRow(proxyId: number, c: ProxyCopyView, capped: boolean, blocked: string): string {
  const running = c.status === "running";
  const status = c.stoppedByCap ? t("proxies.stoppedByCap") : c.status;
  return `
    <li class="proxy-copy" data-proxy-copy="${escapeHtml(c.slug)}">
      <span><strong>${escapeHtml(c.name || c.slug)}</strong> &middot; ${escapeHtml(status)} &middot; ${ht("proxies.copyExit", { ip: raw(exitText(c.exitIp, c.exitCountry)) })}</span>
      <span class="device-actions">
        ${running
          ? `<button class="secondary compact-btn" data-proxy-copy-stop="${proxyId}" data-slug="${escapeHtml(c.slug)}">${ht("proxies.stop")}</button>`
          : capped
            // A capped proxy refuses every start until its usage is reset or the cap changes.
            ? `<button class="secondary compact-btn" disabled title="${ht("proxies.cappedStartTitle")}">${ht("proxies.start")}</button>`
            // A proxied copy is a container on this computer: Start waits for Docker.
            : `<button class="secondary compact-btn" data-proxy-copy-start="${proxyId}" data-slug="${escapeHtml(c.slug)}"${blocked ? ` disabled title="${escapeHtml(blocked)}"` : ""}>${ht("proxies.start")}</button>`}
        <button class="danger compact-btn" data-proxy-copy-remove="${proxyId}" data-slug="${escapeHtml(c.slug)}">${ht("proxies.remove")}</button>
      </span>
    </li>`;
}

/** One proxy's card. `blocked` is why Docker cannot start its copies now, or "". */
export function renderProxyCard(p: ProxyView, blocked = ""): string {
  const secrets = Object.entries(p.secrets || {})
    .filter(([, set]) => set)
    .map(([name]) => `<span class="badge">${ht("proxies.stored", { name: secretName(name) })}</span>`)
    .join("");
  const test = p.lastTest
    ? p.lastTest.ok
      ? `<p class="muted compact-copy">${ht("proxies.testPassed", { at: p.lastTest.at ?? "" })}</p>`
      : `<p class="tip">${ht("proxies.testFailed", { at: p.lastTest.at ?? "", error: p.lastTest.error ?? "" })}</p>`
    : "";
  const capped = p.cap.cappedAt ? `<p class="tip">${p.cap.capReason ? ht("proxies.stoppedReason", { reason: p.cap.capReason }) : ht("proxies.stoppedCap")}</p>` : "";
  return `
    <article class="fleet-device proxy-card" data-proxy="${p.id}">
      <div class="split">
        <div>
          <strong>${escapeHtml(p.name)}</strong>
          <p class="muted compact-copy">${escapeHtml(kindLabel(p.kind))} &middot; ${target(p)}${p.rotating ? ` &middot; ${ht("proxies.rotating")}` : ""}</p>
          <p class="muted compact-copy">${ht("proxies.exit", { ip: raw(exitText(p.lastExitIp, p.lastCountry)) })} &middot; ${escapeHtml(capText(p))}</p>
        </div>
        <div class="device-actions">
          <button class="secondary compact-btn" data-proxy-test="${p.id}">${ht("proxies.test")}</button>
          <button class="secondary compact-btn" data-proxy-edit="${p.id}">${ht("proxies.edit")}</button>
          ${p.cap.enabled ? `<button class="secondary compact-btn" data-proxy-reset="${p.id}">${ht("proxies.resetUsage")}</button>` : ""}
          <button class="danger compact-btn" data-proxy-delete="${p.id}">${ht("proxies.delete")}</button>
        </div>
      </div>
      ${secrets ? `<div class="badge-row">${secrets}</div>` : ""}
      ${capped}
      ${test}
      ${(p.copies || []).length ? `<ul class="proxy-copies">${p.copies.map((c) => copyRow(p.id, c, !!p.cap.cappedAt, blocked)).join("")}</ul>` : `<p class="muted compact-copy">${ht("proxies.noCopies")}</p>`}
    </article>`;
}

/**
 * The add or edit form. A secret box is always empty: on an edit, blank keeps what
 * is stored while the proxy points at the same server, and the "Remove the stored
 * ..." box drops it. A new host, port or login server needs the secret typed again.
 */
export function renderProxyEditor(p: ProxyView | null): string {
  const v = p || {
    id: 0, name: "", kind: "socks5", host: "", port: 0, exitNode: "", loginServer: "", rotating: false,
    secrets: { username: false, password: false, auth_key: false },
    cap: { enabled: false, capBytes: 0, thresholdPct: 90, resetDay: 0, usedBytes: 0, periodStart: "", cappedAt: "", capReason: "" },
    lastExitIp: "", lastCountry: "", lastCheckedAt: "", lastTest: null, copies: [],
  };
  const text = (name: string, label: string, value: string, placeholder = "", help = "") => `
    <label>
      <span>${escapeHtml(label)}</span>
      <input data-proxy-field="${name}" type="text" spellcheck="false" autocomplete="off" placeholder="${escapeHtml(placeholder)}" value="${escapeHtml(value)}" />
      ${help ? `<small class="muted">${escapeHtml(help)}</small>` : ""}
    </label>`;
  const secret = (name: string, label: string, clearLabel: string) => {
    const stored = !!(v.secrets as Record<string, boolean>)[name];
    return `
    <label>
      <span>${escapeHtml(label)}</span>
      <input data-proxy-field="${name}" type="password" autocomplete="new-password" value="" placeholder="${stored ? (name === "auth_key" ? ht("proxies.storedKeepLogin") : ht("proxies.storedKeepHost")) : ""}" />
      ${stored ? `<small><input type="checkbox" data-proxy-field="clear_${name}" /> ${escapeHtml(clearLabel)}</small>` : ""}
    </label>`;
  };
  const kind = (value: string, label: string) => `<option value="${value}" ${v.kind === value ? "selected" : ""}>${escapeHtml(label)}</option>`;
  const capGB = v.cap.enabled ? String(v.cap.capBytes / GB) : "";
  return `
    <form class="custom-editor panel" data-proxy-editor="${v.id}" onsubmit="return false">
      <h3>${p ? ht("proxies.editTitle", { name: p.name }) : ht("proxies.addTitle")}</h3>
      <p class="muted compact-copy">${ht("proxies.editorIntro")}</p>
      ${p && (p.copies || []).length ? `<p class="muted compact-copy">${ht("proxies.applyChange")}</p>` : ""}
      <div class="credential-grid">
        ${text("name", t("proxies.name"), v.name, t("proxies.namePlaceholder"))}
        <label><span>${ht("proxies.type")}</span><select data-proxy-field="kind">${kind("socks5", kindLabel("socks5"))}${kind("http", kindLabel("http"))}${kind("tailscale", kindLabel("tailscale"))}</select></label>
        ${text("host", t("proxies.host"), v.host, "gate.example.net", t("proxies.hostHelp"))}
        ${text("port", t("proxies.port"), v.port ? String(v.port) : "", "7000")}
        ${secret("username", t("proxies.user"), t("proxies.clear.username"))}
        ${secret("password", t("proxies.password"), t("proxies.clear.password"))}
        ${secret("auth_key", t("proxies.authKey"), t("proxies.clear.authKey"))}
        ${text("exit_node", t("proxies.exitNodeLabel"), v.exitNode, t("proxies.exitNodePlaceholder"), t("proxies.tailscaleOnly"))}
        ${text("login_server", t("proxies.loginServer"), v.loginServer, "https://headscale.example.net", t("proxies.loginServerHelp"))}
        ${text("cap_gb", t("proxies.capGb"), capGB, "10", t("proxies.capGbHelp"))}
        ${text("cap_threshold_pct", t("proxies.stopAt"), String(v.cap.thresholdPct || 90), "90")}
        ${text("reset_day", t("proxies.resetDay"), v.cap.resetDay ? String(v.cap.resetDay) : "", "1", t("proxies.resetDayHelp"))}
        <label><span>${ht("proxies.rotatingLabel")}</span><input type="checkbox" data-proxy-field="rotating" ${v.rotating ? "checked" : ""} /></label>
      </div>
      <div class="actions left">
        <button class="primary" data-proxy-save type="button">${p ? ht("proxies.saveChanges") : ht("proxies.addProxy")}</button>
        <button class="secondary" data-proxy-cancel type="button">${ht("proxies.cancel")}</button>
      </div>
    </form>`;
}

/**
 * The values to send from the editor's fields. Checkboxes arrive as "true"/"false";
 * a blank secret is left out, so the stored one is kept.
 */
export function proxyValuesFrom(fields: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, raw] of Object.entries(fields)) {
    const value = String(raw ?? "");
    if ((key === "username" || key === "password" || key === "auth_key") && value === "") continue;
    out[key] = key === "username" || key === "password" || key === "auth_key" ? value : value.trim();
  }
  return out;
}

/**
 * Whether a Fleet redraw that started as `request` may still write the page. The
 * page reads the backend twice before it draws, so the owner may have opened another
 * view, or a newer redraw may have started, in the meantime; then this one is dropped.
 */
export function fleetRedrawIsCurrent(request: number, latest: number, activeView: string): boolean {
  return request === latest && activeView === "fleet";
}

/**
 * Whether the open proxy editor in the DOM is carried over a redraw. Its typed values
 * (a new password included) live only in the DOM until Save, so a redraw caused by an
 * action on another card keeps it when the redraw opens the same proxy's editor ("0"
 * for the Add form). Another proxy's editor, or none, is drawn fresh.
 */
export function keepsLiveProxyEditor(liveId: string | null | undefined, freshId: string | null | undefined): boolean {
  return liveId != null && freshId != null && liveId === freshId;
}

/** The ticks the copy form holds, checked, for slug inside root. */
export function proxyAcks(root: ParentNode, slug: string): string[] {
  return [...root.querySelectorAll<HTMLInputElement>("[data-proxy-ack-for]")]
    .filter((el) => el.checked && el.dataset.proxyAckFor === slug)
    .map((el) => el.value);
}

/**
 * The whole section. editing is the proxy open in the editor, "new", or null.
 * runtime is the shared Docker status: a copy runs in Docker on this computer, so
 * Run a copy and Start wait for it, with the way to Runtime beside them.
 */
export function renderProxiesSection(state: ProxiesState, editing: ProxyView | "new" | null, deployStatus?: DeployStatus, runtime?: Pick<RuntimeInput, "available" | "reason">): string {
  const proxies = state.proxies || [];
  const blocked = runtime ? dockerBlocked(runtime) : "";
  const blockedNote = runtime ? dockerBlockedNote(runtime) : "";
  const candidates = state.candidates || [];
  const editor = editing === "new" ? renderProxyEditor(null) : editing ? renderProxyEditor(editing) : "";
  const options = candidates
    .map((c) => `<option value="${escapeHtml(c.slug)}" ${c.refusal ? "disabled" : ""}>${c.refusal ? ht("proxies.candidateRefused", { name: c.name, refusal: c.refusal }) : escapeHtml(c.name)}</option>`)
    .join("");
  // The ticks a service needs behind a proxy (Honeygain's account risk); Deploy sends
  // only the chosen service's ticks, and the backend refuses the copy without them.
  const acks = candidates
    .filter((c) => !c.refusal)
    .flatMap((c) => (c.needs || []).map((key) => `<label class="muted compact-copy"><input type="checkbox" data-proxy-ack-for="${escapeHtml(c.slug)}" value="${escapeHtml(key)}"> ${escapeHtml(ackText(key))}</label>`))
    .join("");
  const proxyOptions = proxies.map((p) => `<option value="${p.id}" ${p.cap.cappedAt ? "disabled" : ""}>${p.cap.cappedAt ? ht("proxies.proxyCapped", { name: p.name }) : escapeHtml(p.name)}</option>`).join("");
  const runForm = proxies.length && candidates.some((c) => !c.refusal)
    ? `
      <div class="fleet-form" data-proxy-run>
        <select data-proxy-run-service>${options}</select>
        <select data-proxy-run-proxy>${proxyOptions}</select>
        <button class="primary compact-btn" data-proxy-run-deploy${deployStatus?.kind === "busy" || blocked ? " disabled" : ""}>${ht("proxies.runCopy")}</button>
      </div>${acks}`
    : `<p class="muted compact-copy">${proxies.length ? ht("proxies.deployFirst") : ht("proxies.addFirst")}</p>`;
  return `
    <section class="card proxies-panel">
      <div class="card-header">
        <span class="card-title">${ht("proxies.title")}</span>
        ${editing ? "" : `<button class="secondary compact-btn" data-proxy-new>${ht("proxies.addProxy")}</button>`}
      </div>
      <p class="muted">${ht("proxies.intro")} <button class="text-link" data-url="${PROXIES_DOCS_URL}">${ht("proxies.howItWorks")}</button></p>
      ${state.tunnelImageError ? `<p class="tip">${escapeHtml(state.tunnelImageError)}</p>` : ""}
      ${editor}
      ${runForm}
      ${proxies.length ? blockedNote : ""}
      <div class="deploy-panel" data-proxy-deploy-status aria-live="polite">${deployStatus ? deployNotice(deployStatus.kind, deployStatus.text) : ""}</div>
      <div class="fleet-list">${proxies.map((p) => renderProxyCard(p, blocked)).join("")}</div>
    </section>`;
}
