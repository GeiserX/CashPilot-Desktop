// The Secure VM section of the Security page.
//
// The Secure VM is an opt-in layer: one virtual machine per computer, fenced
// off this computer and the LAN, that holds the desktop-only earners a user
// places in it. The page has three actions (Install, Verify, Undo) and, per
// earner, a choice between "This computer" and "Secure VM". A locked choice
// says why. Each earner lists the protection layers it really got.
//
// The words are the guard's own (internal/securevm): Network is connected,
// disconnected, off or absent; Fence is held, cut, reprove-needed or unproven.
// Only held and connected is green. An unknown reading shows as unknown, never
// as "no", and an unknown drop count shows as an em dash, never 0.
//
// Pure: takes the SecureVMStatus the backend sent, returns HTML. Every string
// the user reads is a securevm.* message in locales/en.json.

import type { SecureVMApp, SecureVMProof, SecureVMRemoveResult, SecureVMStatus } from "../wails";
import { escapeHtml, relativeTime } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";

function networkLabel(n: string): string {
  switch (n) {
    case "connected": return t("securevm.network.connected");
    case "disconnected": return t("securevm.network.disconnected");
    case "off": return t("securevm.network.off");
    case "absent": return t("securevm.network.absent");
  }
  return n;
}

function fenceLabel(f: string): string {
  switch (f) {
    case "held": return t("securevm.fence.held");
    case "cut": return t("securevm.fence.cut");
    case "reprove-needed": return t("securevm.fence.reprove");
    case "unproven": return t("securevm.fence.unproven");
  }
  return f;
}

function stepLabel(id: string): string {
  switch (id) {
    case "canary": return t("securevm.step.canary");
    case "red-both-layers-open": return t("securevm.step.redBothLayersOpen");
    case "l2-pf-alone": return t("securevm.step.pfAlone");
    case "l1-softnet-alone": return t("securevm.step.softnetAlone");
    case "green-production": return t("securevm.step.greenProduction");
    case "bonjour": return t("securevm.step.bonjour");
    case "function": return t("securevm.step.function");
    case "devices": return t("securevm.step.devices");
  }
  return id;
}

function kindLabel(k: string): string {
  switch (k) {
    case "red": return t("securevm.kind.red");
    case "green": return t("securevm.kind.green");
    case "check": return t("securevm.kind.check");
  }
  return k;
}

function outcomeLabel(o: string): string {
  switch (o) {
    case "went-red": return t("securevm.outcome.wentRed");
    case "stayed-green": return t("securevm.outcome.stayedGreen");
    case "held": return t("securevm.outcome.held");
    case "broken": return t("securevm.outcome.broken");
    case "leak": return t("securevm.outcome.leak");
    case "unproven": return t("securevm.outcome.unproven");
  }
  return o;
}

function triLabel(v: string): string {
  switch (v) {
    case "yes": return t("securevm.tri.yes");
    case "no": return t("securevm.tri.no");
    case "unknown": return t("securevm.tri.unknown");
  }
  return v;
}

function layerLabel(l: string): string {
  switch (l) {
    case "lan-fence": return t("securevm.layer.lanFence");
    case "secure-vm": return t("securevm.layer.secureVm");
    case "vm-fence": return t("securevm.layer.vmFence");
  }
  return l;
}

function gb(n: number): string {
  return `${Math.round(n)} GB`;
}

function bytesGB(n: number): string {
  return `${(n / 1024 ** 3).toFixed(0)} GB`;
}

/** Whether the VM is online with its proven fence: the only green state. */
export function secureVMGreen(s: SecureVMStatus): boolean {
  return s.network === "connected" && s.fence === "held";
}

/** The guard writes its reasons in lower case, as a clause; the page shows a sentence. */
function sentence(s: string): string {
  const t = s.trim();
  if (!t) return t;
  return t[0].toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? "" : ".");
}

function stateLine(s: SecureVMStatus): string {
  const tone = secureVMGreen(s) ? "" : s.network === "off" ? "muted" : "text-warn";
  const net = networkLabel(s.network);
  const fence = fenceLabel(s.fence);
  return `<p class="${tone}" data-securevm-state="${escapeHtml(s.network)}/${escapeHtml(s.fence)}"><strong>${escapeHtml(net)}</strong> · ${escapeHtml(fence)}</p>`;
}

function progressLine(s: SecureVMStatus): string {
  if (!s.busy) return "";
  const pct = s.fraction >= 0 ? ` ${Math.round(s.fraction * 100)}%` : "";
  const bar = s.fraction >= 0
    ? `<div class="payout-progress" style="max-width:none"><div class="payout-progress-bar" style="width:${Math.round(s.fraction * 100)}%"></div></div>`
    : "";
  return `<div data-securevm-busy="${escapeHtml(s.busy)}"><p class="muted">${escapeHtml(s.step || "…")}${pct}</p>${bar}</div>`;
}

function errorLines(s: SecureVMStatus, now: number): string {
  const out: string[] = [];
  if (s.reason && !secureVMGreen(s)) out.push(`<p class="muted compact-copy">${escapeHtml(sentence(s.reason))}</p>`);
  if (s.startError) out.push(`<p class="text-warn" data-securevm-start-error>${escapeHtml(s.startError)}</p>`);
  if (s.lowDisk) out.push(`<p class="text-warn" data-securevm-low-disk>${ht("securevm.lowDisk", { free: bytesGB(s.freeDisk) })}</p>`);
  if (s.lastError && !s.busy) out.push(`<p class="text-warn" data-securevm-last-error>${ht("securevm.lastError", { error: s.lastError })}</p>`);
  if (s.proving) out.push(`<p class="muted">${ht("securevm.proving")}</p>`);
  if (s.installed) {
    if (s.provedAt && !s.provedAt.startsWith("0001")) out.push(`<p class="muted compact-copy">${ht("securevm.provedAt", { when: relativeTime(s.provedAt, now) })}</p>`);
    else if (!s.reason) out.push(`<p class="muted compact-copy">${ht("securevm.neverProved")}</p>`);
    const since = s.blockedSince && !s.blockedSince.startsWith("0001") ? relativeTime(s.blockedSince, now) : "";
    out.push(`<p class="muted compact-copy" data-securevm-blocked>${s.blocked == null ? ht("securevm.blockedUnknown") : since ? ht("securevm.blockedSince", { n: s.blocked, since }) : ht("securevm.blocked", { n: s.blocked })}</p>`);
  }
  return out.join("");
}

function actions(s: SecureVMStatus, askUndo: boolean): string {
  const busy = !!s.busy;
  const dis = busy ? " disabled" : "";
  if (!s.installed) {
    return `<div class="header-actions"><button class="btn btn-primary" data-securevm-action="install"${dis}>${ht("securevm.install")}</button></div>`;
  }
  if (askUndo) {
    return `<div data-securevm-undo-ask><p>${ht("securevm.undoAsk")}</p><div class="header-actions">
      <button class="btn btn-primary" data-securevm-action="undo"${dis}>${ht("securevm.undoYes")}</button>
      <button class="btn btn-ghost" data-securevm-action="undo-cancel">${ht("securevm.undoNo")}</button></div></div>`;
  }
  const running = s.network === "connected" || s.network === "disconnected";
  const update = s.updateNeeded
    ? `<p class="text-warn" data-securevm-update>${ht("securevm.updateWhy")}</p><div class="header-actions"><button class="btn btn-primary" data-securevm-action="install"${dis}>${ht("securevm.update")}</button></div>`
    : s.unfinished
      ? `<p class="text-warn" data-securevm-unfinished>${ht("securevm.finishWhy")}</p><div class="header-actions"><button class="btn btn-primary" data-securevm-action="install"${dis}>${ht("securevm.finish")}</button></div>`
      : "";
  return `${update}<div class="header-actions">
    <button class="btn btn-ghost" data-securevm-action="verify"${dis || (!running ? " disabled" : "")}>${ht("securevm.verify")}</button>
    ${running
      ? `<button class="btn btn-ghost" data-securevm-action="stop"${dis}>${ht("securevm.stop")}</button>`
      : `<button class="btn btn-ghost" data-securevm-action="start"${dis}>${ht("securevm.start")}</button>`}
    <button class="btn btn-ghost" data-securevm-action="screen"${!running ? " disabled" : ""}>${ht("securevm.screen")}</button>
    <button class="btn btn-ghost" data-securevm-action="undo-ask"${dis}>${ht("securevm.undo")}</button>
  </div>`;
}

/** The last proof, step by step. */
export function renderSecureVMProof(p: SecureVMProof | null | undefined): string {
  if (!p) return "";
  const rows = (p.steps || []).map((st) => {
    const bad = st.outcome === "leak" || st.outcome === "broken" || st.outcome === "unproven";
    return `<div class="row" data-proof-step="${escapeHtml(st.id)}" data-proof-outcome="${escapeHtml(st.outcome)}">
      <span>${escapeHtml(stepLabel(st.id))} <span class="muted">(${escapeHtml(kindLabel(st.kind))})</span></span>
      <span class="${bad ? "text-warn" : ""}">${escapeHtml(outcomeLabel(st.outcome))}</span></div>`;
  }).join("");
  return `<section data-securevm-proof="${p.green ? "green" : "red"}">
    <div class="block-head"><span class="block-title">${ht("securevm.proofHead")}</span></div>
    <p class="${p.green ? "" : "text-warn"}">${p.green ? ht("securevm.proofGreen") : ht("securevm.proofRed")}</p>
    ${p.at && !p.at.startsWith("0001") ? `<p class="muted compact-copy">${escapeHtml(new Date(p.at).toLocaleString())}</p>` : ""}
    ${rows}</section>`;
}

function layerList(a: SecureVMApp): string {
  const names = (a.layers || []).map(layerLabel);
  return names.length ? t("securevm.layers", { layers: names.join(", ") }) : t("securevm.noLayers");
}

function appRow(a: SecureVMApp, s: SecureVMStatus): string {
  const busy = !!s.busy;
  const inVM = a.placement === "vm";
  const vmLocked = !a.canVm;
  const vmTitle = vmLocked ? a.locked : !s.installed ? t("securevm.needsVM") : "";
  const choice = (where: "host" | "vm", label: string, disabled: boolean, title: string) =>
    `<button class="btn btn-sm ${a.placement === where ? "btn-primary" : "btn-ghost"}" data-securevm-place="${where}" data-slug="${escapeHtml(a.slug)}"${disabled || busy ? " disabled" : ""}${title ? ` title="${escapeHtml(title)}"` : ""}${a.placement === where ? ' aria-pressed="true"' : ""}>${escapeHtml(label)}</button>`;
  const status = inVM
    ? `<p class="muted compact-copy">${ht("securevm.appState", { installed: triLabel(a.installed), running: triLabel(a.running), signedIn: triLabel(a.signedIn) })}</p>`
    : "";
  const signIn = inVM && a.signedIn !== "yes"
    ? `<button class="btn btn-sm btn-ghost" data-securevm-signin="${escapeHtml(a.slug)}"${busy ? " disabled" : ""}>${ht("securevm.signIn")}</button>`
    : "";
  return `<div class="row" data-securevm-app="${escapeHtml(a.slug)}" data-placement="${escapeHtml(a.placement || "")}">
    <div>
      <strong>${escapeHtml(a.name)}</strong>
      ${status}
      ${a.detail ? `<p class="text-warn compact-copy">${escapeHtml(a.detail)}</p>` : ""}
      ${vmLocked ? `<p class="muted compact-copy" data-securevm-locked>${escapeHtml(a.locked)}</p>` : ""}
      ${a.note ? `<p class="muted compact-copy">${escapeHtml(a.note)}</p>` : ""}
      <p class="muted compact-copy">${escapeHtml(layerList(a))}</p>
    </div>
    <div class="header-actions">
      ${choice("host", t("securevm.host"), false, "")}
      ${choice("vm", t("securevm.vm"), vmLocked || !s.installed, vmTitle)}
      ${signIn}
    </div>
  </div>`;
}

/** What Undo removed, kept on purpose and left behind. */
export function renderSecureVMRemoval(r: SecureVMRemoveResult | null | undefined): string {
  if (!r) return "";
  const list = (head: string, items: string[] | null, tag: string) =>
    items && items.length ? `<p><strong>${escapeHtml(head)}</strong></p><ul data-securevm-${tag}>${items.map((i) => `<li>${escapeHtml(i)}</li>`).join("")}</ul>` : "";
  const left = r.left && r.left.length ? list(t("securevm.leftHead"), r.left, "left") : `<p data-securevm-clean>${ht("securevm.nothingLeft")}</p>`;
  return `<section data-securevm-removal>${list(t("securevm.removedHead"), r.removed, "removed")}${list(t("securevm.keptHead"), r.kept, "kept")}${left}</section>`;
}

export interface SecureVMView {
  askUndo?: boolean;
  removal?: SecureVMRemoveResult | null;
  now?: number;
}

/** The whole Secure VM section. */
export function renderSecureVM(s: SecureVMStatus | null | undefined, view: SecureVMView = {}): string {
  const now = view.now ?? Date.now();
  if (!s) return `<section data-securevm="loading"><p class="muted">…</p></section>`;
  const head = `<div class="block-head"><span class="block-title">${ht("securevm.title")}</span></div><p class="muted compact-copy">${ht("securevm.intro")}</p>`;
  if (!s.available) {
    return `<section data-securevm="unavailable">${head}<p class="muted">${s.unavailable ? escapeHtml(s.unavailable) : ht("securevm.unavailable")}</p>${renderSecureVMRemoval(view.removal)}</section>`;
  }
  const cost = !s.installed
    ? `<p><strong>${ht("securevm.installHead")}</strong></p>
       <p class="muted compact-copy">${ht("securevm.installCost", { memory: gb(s.cost.memoryGb), cores: s.cost.cores, disk: gb(s.cost.diskGb), download: gb(s.cost.downloadGb) })}</p>
       <p class="muted compact-copy">${ht("securevm.installOnce")}</p>
       <p class="muted compact-copy" data-securevm-licence>${ht("securevm.licence")}</p>`
    : "";
  const apps = s.apps || [];
  const appsHtml = `<section data-securevm-apps>
    <div class="block-head"><span class="block-title">${ht("securevm.appsHead")}</span></div>
    ${apps.length ? apps.map((a) => appRow(a, s)).join("") : `<p class="block-empty">${ht("securevm.appsEmpty")}</p>`}
  </section>`;
  return `<section data-securevm="${s.installed ? "installed" : "absent"}">
    ${head}
    ${s.installed ? stateLine(s) : ""}
    ${cost}
    ${progressLine(s)}
    ${errorLines(s, now)}
    ${actions(s, !!view.askUndo)}
  </section>
  ${renderSecureVMRemoval(view.removal)}
  ${s.installed ? renderSecureVMProof(s.proof) : ""}
  ${appsHtml}`;
}
