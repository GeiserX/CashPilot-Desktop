// The Proxies section of the Fleet page: proxies the owner runs copies of services
// through, so this machine earns on more than one exit IP. It mirrors the web
// console's fleet page (GeiserX/CashPilot docs/proxies.md).
//
// Pure: takes the state the backend sent, returns HTML, touches no global.
//
// Two rules this file exists to keep:
//   - a proxy's user, password or auth key is never printed. The backend never sends
//     them (only "set: true"), and the editor's boxes for them are always empty;
//   - an exit IP nobody has seen yet renders as an em dash, never as an address.

import type { ProxiesState, ProxyCopyView, ProxyView } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

const GB = 1_000_000_000;

/** Bytes in the decimal GB providers sell, one decimal under 100 GB. */
export function formatGB(bytes: number): string {
  const gb = (Number.isFinite(bytes) ? bytes : 0) / GB;
  return `${gb < 100 ? gb.toFixed(1) : Math.round(gb)} GB`;
}

function kindLabel(kind: string): string {
  return ({ socks5: "SOCKS5", http: "HTTP CONNECT", tailscale: "Tailscale exit node" } as Record<string, string>)[kind] || kind;
}

function target(p: ProxyView): string {
  if (p.kind === "tailscale") return `exit node ${escapeHtml(p.exitNode)}`;
  return `${escapeHtml(p.host)}:${escapeHtml(String(p.port))}`;
}

function exitText(ip: string, country: string): string {
  if (!ip) return "&mdash;";
  return `${escapeHtml(ip)}${country ? ` (${escapeHtml(country)})` : ""}`;
}

/** The cap line: off, or used of cap, where it stops, and when it resets. */
export function capText(p: ProxyView): string {
  const cap = p.cap;
  if (!cap.enabled) return "No traffic cap";
  const reset = cap.resetDay ? `resets on day ${cap.resetDay}` : "reset by hand";
  return `${formatGB(cap.usedBytes)} of ${formatGB(cap.capBytes)} used, stops at ${cap.thresholdPct}%, ${reset}`;
}

function copyRow(proxyId: number, c: ProxyCopyView, capped: boolean): string {
  const running = c.status === "running";
  const status = c.stoppedByCap ? "stopped by the cap" : c.status;
  return `
    <li class="proxy-copy" data-proxy-copy="${escapeHtml(c.slug)}">
      <span><strong>${escapeHtml(c.name || c.slug)}</strong> &middot; ${escapeHtml(status)} &middot; exit ${exitText(c.exitIp, c.exitCountry)}</span>
      <span class="device-actions">
        ${running
          ? `<button class="secondary compact-btn" data-proxy-copy-stop="${proxyId}" data-slug="${escapeHtml(c.slug)}">Stop</button>`
          : capped
            // A capped proxy refuses every start until its usage is reset or the cap changes.
            ? `<button class="secondary compact-btn" disabled title="Reset the usage or raise the cap to start it">Start</button>`
            : `<button class="secondary compact-btn" data-proxy-copy-start="${proxyId}" data-slug="${escapeHtml(c.slug)}">Start</button>`}
        <button class="danger compact-btn" data-proxy-copy-remove="${proxyId}" data-slug="${escapeHtml(c.slug)}">Remove</button>
      </span>
    </li>`;
}

/** One proxy's card. */
export function renderProxyCard(p: ProxyView): string {
  const secrets = Object.entries(p.secrets || {})
    .filter(([, set]) => set)
    .map(([name]) => `<span class="badge">${escapeHtml(name.replace("_", " "))} stored</span>`)
    .join("");
  const test = p.lastTest
    ? p.lastTest.ok
      ? `<p class="muted compact-copy">Last test passed ${escapeHtml(p.lastTest.at)}.</p>`
      : `<p class="tip">Last test failed ${escapeHtml(p.lastTest.at)}: ${escapeHtml(p.lastTest.error)}</p>`
    : "";
  const capped = p.cap.cappedAt ? `<p class="tip">Stopped: ${escapeHtml(p.cap.capReason || "this proxy reached its traffic cap")}.</p>` : "";
  return `
    <article class="fleet-device proxy-card" data-proxy="${p.id}">
      <div class="split">
        <div>
          <strong>${escapeHtml(p.name)}</strong>
          <p class="muted compact-copy">${escapeHtml(kindLabel(p.kind))} &middot; ${target(p)}${p.rotating ? " &middot; rotating exit IP" : ""}</p>
          <p class="muted compact-copy">Exit ${exitText(p.lastExitIp, p.lastCountry)} &middot; ${escapeHtml(capText(p))}</p>
        </div>
        <div class="device-actions">
          <button class="secondary compact-btn" data-proxy-test="${p.id}">Test</button>
          <button class="secondary compact-btn" data-proxy-edit="${p.id}">Edit</button>
          ${p.cap.enabled ? `<button class="secondary compact-btn" data-proxy-reset="${p.id}">Reset usage</button>` : ""}
          <button class="danger compact-btn" data-proxy-delete="${p.id}">Delete</button>
        </div>
      </div>
      ${secrets ? `<div class="badge-row">${secrets}</div>` : ""}
      ${capped}
      ${test}
      ${(p.copies || []).length ? `<ul class="proxy-copies">${p.copies.map((c) => copyRow(p.id, c, !!p.cap.cappedAt)).join("")}</ul>` : `<p class="muted compact-copy">No copies run through this proxy yet.</p>`}
    </article>`;
}

/**
 * The add or edit form. A secret box is always empty: on an edit, blank keeps what
 * is stored, and the "Remove the stored ..." box drops it.
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
  const secret = (name: string, label: string) => {
    const stored = !!(v.secrets as Record<string, boolean>)[name];
    return `
    <label>
      <span>${escapeHtml(label)}</span>
      <input data-proxy-field="${name}" type="password" autocomplete="new-password" value="" placeholder="${stored ? "Stored; leave blank to keep it" : ""}" />
      ${stored ? `<small><input type="checkbox" data-proxy-field="clear_${name}" /> Remove the stored ${escapeHtml(label.toLowerCase())}</small>` : ""}
    </label>`;
  };
  const kind = (value: string, label: string) => `<option value="${value}" ${v.kind === value ? "selected" : ""}>${escapeHtml(label)}</option>`;
  const capGB = v.cap.enabled ? String(v.cap.capBytes / GB) : "";
  return `
    <form class="custom-editor panel" data-proxy-editor="${v.id}" onsubmit="return false">
      <h3>${p ? `Edit ${escapeHtml(p.name)}` : "Add a proxy"}</h3>
      <p class="muted compact-copy">Use the host, port and proxy login your provider gives for SOCKS5 or HTTP, not your dashboard login. Any provider works.</p>
      ${p && (p.copies || []).length ? `<p class="muted compact-copy">A copy picks up a change the next time it starts. To apply it to a running copy now, press Stop, then Start.</p>` : ""}
      <div class="credential-grid">
        ${text("name", "Name", v.name, "Residential gate")}
        <label><span>Type</span><select data-proxy-field="kind">${kind("socks5", "SOCKS5")}${kind("http", "HTTP CONNECT")}${kind("tailscale", "Tailscale exit node")}</select></label>
        ${text("host", "Host", v.host, "gate.example.net", "SOCKS5 and HTTP only.")}
        ${text("port", "Port", v.port ? String(v.port) : "", "7000")}
        ${secret("username", "User")}
        ${secret("password", "Password")}
        ${secret("auth_key", "Tailscale auth key")}
        ${text("exit_node", "Exit node", v.exitNode, "laptop or 100.x address", "Tailscale only.")}
        ${text("login_server", "Login server (optional)", v.loginServer, "https://headscale.example.net", "Tailscale only; empty for Tailscale's own.")}
        ${text("cap_gb", "Traffic cap in GB (optional)", capGB, "10", "Empty for no cap.")}
        ${text("cap_threshold_pct", "Stop at percent", String(v.cap.thresholdPct || 90), "90")}
        ${text("reset_day", "Reset day (optional)", v.cap.resetDay ? String(v.cap.resetDay) : "", "1", "1 to 28; empty to reset by hand.")}
        <label><span>Rotating exit IP</span><input type="checkbox" data-proxy-field="rotating" ${v.rotating ? "checked" : ""} /></label>
      </div>
      <div class="actions left">
        <button class="primary" data-proxy-save type="button">${p ? "Save changes" : "Add proxy"}</button>
        <button class="secondary" data-proxy-cancel type="button">Cancel</button>
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

/** The whole section. editing is the proxy open in the editor, "new", or null. */
export function renderProxiesSection(state: ProxiesState, editing: ProxyView | "new" | null): string {
  const proxies = state.proxies || [];
  const candidates = state.candidates || [];
  const editor = editing === "new" ? renderProxyEditor(null) : editing ? renderProxyEditor(editing) : "";
  const options = candidates
    .map((c) => `<option value="${escapeHtml(c.slug)}" ${c.refusal ? "disabled" : ""}>${escapeHtml(c.name)}${c.refusal ? ` (cannot: ${escapeHtml(c.refusal)})` : ""}</option>`)
    .join("");
  const proxyOptions = proxies.map((p) => `<option value="${p.id}" ${p.cap.cappedAt ? "disabled" : ""}>${escapeHtml(p.name)}${p.cap.cappedAt ? " (capped)" : ""}</option>`).join("");
  const runForm = proxies.length && candidates.some((c) => !c.refusal)
    ? `
      <div class="fleet-form" data-proxy-run>
        <select data-proxy-run-service>${options}</select>
        <select data-proxy-run-proxy>${proxyOptions}</select>
        <button class="primary compact-btn" data-proxy-run-deploy>Run a copy through this proxy</button>
      </div>`
    : `<p class="muted compact-copy">${proxies.length ? "Deploy a service that can run behind a proxy first; the copy runs with its settings." : "Add a proxy to run copies through it."}</p>`;
  return `
    <section class="card proxies-panel">
      <div class="card-header">
        <span class="card-title">Proxies</span>
        ${editing ? "" : `<button class="secondary compact-btn" data-proxy-new>Add proxy</button>`}
      </div>
      <p class="muted">A proxied copy is an extra copy of a deployed service whose traffic leaves through a proxy instead of this machine's connection. If the proxy goes down, the copy reaches nothing; it never falls back to your own IP.</p>
      ${state.tunnelImageError ? `<p class="tip">${escapeHtml(state.tunnelImageError)}</p>` : ""}
      ${editor}
      ${runForm}
      <div class="fleet-list">${proxies.map(renderProxyCard).join("")}</div>
    </section>`;
}
