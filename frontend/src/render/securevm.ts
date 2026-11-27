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
// the user reads is in SECUREVM_TEXT, so a translation changes one object.

import type { SecureVMApp, SecureVMProof, SecureVMRemoveResult, SecureVMStatus } from "../wails";
import { escapeHtml, relativeTime } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

export const SECUREVM_TEXT = {
  title: "Secure VM",
  intro: "A separate virtual machine for desktop-only earners. A network fence keeps it off this computer and your home network, and CashPilot proves the fence before the VM goes online.",
  unavailable: "Not available on this computer.",
  installHead: "What the Secure VM needs",
  installCost: (memory: string, cores: string, disk: string, download: string) =>
    `${memory} of memory and ${cores} processor cores while it runs, up to ${disk} of disk, and a ${download} download first.`,
  installOnce: "macOS asks for your administrator password once, to set up the fence. Install takes about 30 to 60 minutes, most of it the download.",
  licence: "For personal, non-commercial use: Apple's macOS licence allows a macOS virtual machine on your own Mac for that.",
  install: "Install",
  update: "Update the Secure VM",
  finish: "Finish setting up",
  finishWhy: "The Secure VM and its fence are ready, but the setup stopped before the VM was ready for earners. Finish it; this needs no administrator password.",
  updateWhy: "CashPilot was updated. Update the Secure VM so its guard runs the new version. macOS asks for your administrator password, and CashPilot proves the fence again.",
  verify: "Verify the fence",
  start: "Start",
  stop: "Stop",
  screen: "Show its screen",
  undo: "Remove the Secure VM",
  undoAsk: "This removes the VM, the earners in it and their sign-ins, the fence and everything CashPilot set up for it. macOS asks for your administrator password.",
  undoYes: "Remove it",
  undoNo: "Keep it",
  network: {
    connected: "Online",
    disconnected: "Cut off the network",
    off: "Stopped",
    absent: "Not installed",
  } as Record<string, string>,
  fence: {
    held: "Fence held",
    cut: "Fence changed: VM cut off",
    "reprove-needed": "Fence needs a new proof",
    unproven: "Fence not proven yet",
  } as Record<string, string>,
  provedAt: (when: string) => `Proven ${when}.`,
  neverProved: "Not proven yet.",
  proving: "Proving the fence now. The VM stays cut off until the proof is green.",
  blocked: (n: string, since: string) => `The fence blocked ${n} packets since the VM started${since ? ` ${since}` : ""}.`,
  blockedUnknown: "Blocked packets: —",
  lowDisk: (free: string) => `Low disk space: ${free} free. The VM stops working when the disk is full.`,
  lastError: "The last action failed",
  proofHead: "Last fence proof",
  proofGreen: "Green: nothing reached this computer or your network.",
  proofRed: "Not green: the VM stays cut off the network.",
  steps: {
    canary: "The test listener on this Mac",
    "red-both-layers-open": "A hole opened on purpose",
    "l2-pf-alone": "The packet filter alone",
    "l1-softnet-alone": "Tart's network filter alone",
    "green-production": "The whole fence",
    bonjour: "This Mac's local services (Bonjour)",
    function: "The VM reaches the internet",
    devices: "No shared folder, clipboard or audio",
  } as Record<string, string>,
  kinds: { red: "opened on purpose", green: "must stay blocked", check: "check" } as Record<string, string>,
  outcomes: {
    "went-red": "reached, as it should",
    "stayed-green": "blocked",
    held: "passed",
    broken: "did not work",
    leak: "LEAKED",
    unproven: "not run",
  } as Record<string, string>,
  appsHead: "Where each earner runs",
  appsEmpty: "No earner here can run in the Secure VM.",
  host: "This computer",
  vm: "Secure VM",
  signIn: "Sign in",
  tri: { yes: "yes", no: "no", unknown: "unknown" } as Record<string, string>,
  installed: "Installed",
  running: "Running",
  signedIn: "Signed in",
  layers: "Protection",
  noLayers: "None beyond this computer's own.",
  layerNames: {
    "lan-fence": "LAN fence",
    "secure-vm": "Secure VM",
    "vm-fence": "VM network fence",
  } as Record<string, string>,
  needsVM: "Install the Secure VM first.",
  removedHead: "Removed",
  keptHead: "Kept on purpose",
  leftHead: "Still there",
  nothingLeft: "Nothing is left: CashPilot checked every item again after the removal.",
};

const T = SECUREVM_TEXT;

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
  const net = T.network[s.network] || s.network;
  const fence = T.fence[s.fence] || s.fence;
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
  if (s.lowDisk) out.push(`<p class="text-warn" data-securevm-low-disk>${escapeHtml(T.lowDisk(bytesGB(s.freeDisk)))}</p>`);
  if (s.lastError && !s.busy) out.push(`<p class="text-warn" data-securevm-last-error><strong>${escapeHtml(T.lastError)}:</strong> ${escapeHtml(s.lastError)}</p>`);
  if (s.proving) out.push(`<p class="muted">${escapeHtml(T.proving)}</p>`);
  if (s.installed) {
    if (s.provedAt && !s.provedAt.startsWith("0001")) out.push(`<p class="muted compact-copy">${escapeHtml(T.provedAt(relativeTime(s.provedAt, now)))}</p>`);
    else if (!s.reason) out.push(`<p class="muted compact-copy">${escapeHtml(T.neverProved)}</p>`);
    const since = s.blockedSince && !s.blockedSince.startsWith("0001") ? relativeTime(s.blockedSince, now) : "";
    out.push(`<p class="muted compact-copy" data-securevm-blocked>${escapeHtml(s.blocked == null ? T.blockedUnknown : T.blocked(String(s.blocked), since))}</p>`);
  }
  return out.join("");
}

function actions(s: SecureVMStatus, askUndo: boolean): string {
  const busy = !!s.busy;
  const dis = busy ? " disabled" : "";
  if (!s.installed) {
    return `<div class="header-actions"><button class="btn btn-primary" data-securevm-action="install"${dis}>${escapeHtml(T.install)}</button></div>`;
  }
  if (askUndo) {
    return `<div data-securevm-undo-ask><p>${escapeHtml(T.undoAsk)}</p><div class="header-actions">
      <button class="btn btn-primary" data-securevm-action="undo"${dis}>${escapeHtml(T.undoYes)}</button>
      <button class="btn btn-ghost" data-securevm-action="undo-cancel">${escapeHtml(T.undoNo)}</button></div></div>`;
  }
  const running = s.network === "connected" || s.network === "disconnected";
  const update = s.updateNeeded
    ? `<p class="text-warn" data-securevm-update>${escapeHtml(T.updateWhy)}</p><div class="header-actions"><button class="btn btn-primary" data-securevm-action="install"${dis}>${escapeHtml(T.update)}</button></div>`
    : s.unfinished
      ? `<p class="text-warn" data-securevm-unfinished>${escapeHtml(T.finishWhy)}</p><div class="header-actions"><button class="btn btn-primary" data-securevm-action="install"${dis}>${escapeHtml(T.finish)}</button></div>`
      : "";
  return `${update}<div class="header-actions">
    <button class="btn btn-ghost" data-securevm-action="verify"${dis || (!running ? " disabled" : "")}>${escapeHtml(T.verify)}</button>
    ${running
      ? `<button class="btn btn-ghost" data-securevm-action="stop"${dis}>${escapeHtml(T.stop)}</button>`
      : `<button class="btn btn-ghost" data-securevm-action="start"${dis}>${escapeHtml(T.start)}</button>`}
    <button class="btn btn-ghost" data-securevm-action="screen"${!running ? " disabled" : ""}>${escapeHtml(T.screen)}</button>
    <button class="btn btn-ghost" data-securevm-action="undo-ask"${dis}>${escapeHtml(T.undo)}</button>
  </div>`;
}

/** The last proof, step by step. */
export function renderSecureVMProof(p: SecureVMProof | null | undefined): string {
  if (!p) return "";
  const rows = (p.steps || []).map((st) => {
    const bad = st.outcome === "leak" || st.outcome === "broken" || st.outcome === "unproven";
    return `<div class="row" data-proof-step="${escapeHtml(st.id)}" data-proof-outcome="${escapeHtml(st.outcome)}">
      <span>${escapeHtml(T.steps[st.id] || st.id)} <span class="muted">(${escapeHtml(T.kinds[st.kind] || st.kind)})</span></span>
      <span class="${bad ? "text-warn" : ""}">${escapeHtml(T.outcomes[st.outcome] || st.outcome)}</span></div>`;
  }).join("");
  return `<section data-securevm-proof="${p.green ? "green" : "red"}">
    <div class="block-head"><span class="block-title">${escapeHtml(T.proofHead)}</span></div>
    <p class="${p.green ? "" : "text-warn"}">${escapeHtml(p.green ? T.proofGreen : T.proofRed)}</p>
    ${p.at && !p.at.startsWith("0001") ? `<p class="muted compact-copy">${escapeHtml(new Date(p.at).toLocaleString())}</p>` : ""}
    ${rows}</section>`;
}

function layerList(a: SecureVMApp): string {
  const names = (a.layers || []).map((l) => T.layerNames[l] || l);
  return names.length ? names.join(", ") : T.noLayers;
}

function appRow(a: SecureVMApp, s: SecureVMStatus): string {
  const busy = !!s.busy;
  const inVM = a.placement === "vm";
  const vmLocked = !a.canVm;
  const vmTitle = vmLocked ? a.locked : !s.installed ? T.needsVM : "";
  const choice = (where: "host" | "vm", label: string, disabled: boolean, title: string) =>
    `<button class="btn btn-sm ${a.placement === where ? "btn-primary" : "btn-ghost"}" data-securevm-place="${where}" data-slug="${escapeHtml(a.slug)}"${disabled || busy ? " disabled" : ""}${title ? ` title="${escapeHtml(title)}"` : ""}${a.placement === where ? ' aria-pressed="true"' : ""}>${escapeHtml(label)}</button>`;
  const status = inVM
    ? `<p class="muted compact-copy">${escapeHtml(T.installed)}: ${escapeHtml(T.tri[a.installed] || a.installed)} · ${escapeHtml(T.running)}: ${escapeHtml(T.tri[a.running] || a.running)} · ${escapeHtml(T.signedIn)}: ${escapeHtml(T.tri[a.signedIn] || a.signedIn)}</p>`
    : "";
  const signIn = inVM && a.signedIn !== "yes"
    ? `<button class="btn btn-sm btn-ghost" data-securevm-signin="${escapeHtml(a.slug)}"${busy ? " disabled" : ""}>${escapeHtml(T.signIn)}</button>`
    : "";
  return `<div class="row" data-securevm-app="${escapeHtml(a.slug)}" data-placement="${escapeHtml(a.placement || "")}">
    <div>
      <strong>${escapeHtml(a.name)}</strong>
      ${status}
      ${a.detail ? `<p class="text-warn compact-copy">${escapeHtml(a.detail)}</p>` : ""}
      ${vmLocked ? `<p class="muted compact-copy" data-securevm-locked>${escapeHtml(a.locked)}</p>` : ""}
      ${a.note ? `<p class="muted compact-copy">${escapeHtml(a.note)}</p>` : ""}
      <p class="muted compact-copy">${escapeHtml(T.layers)}: ${escapeHtml(layerList(a))}</p>
    </div>
    <div class="header-actions">
      ${choice("host", T.host, false, "")}
      ${choice("vm", T.vm, vmLocked || !s.installed, vmTitle)}
      ${signIn}
    </div>
  </div>`;
}

/** What Undo removed, kept on purpose and left behind. */
export function renderSecureVMRemoval(r: SecureVMRemoveResult | null | undefined): string {
  if (!r) return "";
  const list = (head: string, items: string[] | null, tag: string) =>
    items && items.length ? `<p><strong>${escapeHtml(head)}</strong></p><ul data-securevm-${tag}>${items.map((i) => `<li>${escapeHtml(i)}</li>`).join("")}</ul>` : "";
  const left = r.left && r.left.length ? list(T.leftHead, r.left, "left") : `<p data-securevm-clean>${escapeHtml(T.nothingLeft)}</p>`;
  return `<section data-securevm-removal>${list(T.removedHead, r.removed, "removed")}${list(T.keptHead, r.kept, "kept")}${left}</section>`;
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
  const head = `<div class="block-head"><span class="block-title">${escapeHtml(T.title)}</span></div><p class="muted compact-copy">${escapeHtml(T.intro)}</p>`;
  if (!s.available) {
    return `<section data-securevm="unavailable">${head}<p class="muted">${escapeHtml(s.unavailable || T.unavailable)}</p>${renderSecureVMRemoval(view.removal)}</section>`;
  }
  const cost = !s.installed
    ? `<p><strong>${escapeHtml(T.installHead)}</strong></p>
       <p class="muted compact-copy">${escapeHtml(T.installCost(gb(s.cost.memoryGb), String(s.cost.cores), gb(s.cost.diskGb), gb(s.cost.downloadGb)))}</p>
       <p class="muted compact-copy">${escapeHtml(T.installOnce)}</p>
       <p class="muted compact-copy" data-securevm-licence>${escapeHtml(T.licence)}</p>`
    : "";
  const apps = s.apps || [];
  const appsHtml = `<section data-securevm-apps>
    <div class="block-head"><span class="block-title">${escapeHtml(T.appsHead)}</span></div>
    ${apps.length ? apps.map((a) => appRow(a, s)).join("") : `<p class="block-empty">${escapeHtml(T.appsEmpty)}</p>`}
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
