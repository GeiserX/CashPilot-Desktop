// Settings > App image updates: the choice for every app (Off, Notify only,
// Update automatically), the hour of the daily check, a choice per app and what
// the updates did (internal/imageupdates, a port of the web app's Fleet page
// section, docs/image-updates.md over there).
//
// Pure: takes the view, returns HTML. main.ts wires the buttons and keeps a
// choice that is not saved yet across a redraw (unsavedImageUpdateChoices).

import type { ImageUpdatesView } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, intlLocale, t } from "./i18n.js";

export const IMAGE_UPDATE_MODES = ["off", "notify", "auto"] as const;

function modeLabel(mode: string): string {
  switch (mode) {
    case "off":
      return t("imageUpdates.mode.off");
    case "notify":
      return t("imageUpdates.mode.notify");
    case "auto":
      return t("imageUpdates.mode.auto");
    default:
      return t("imageUpdates.mode.inherit");
  }
}

/** The label of a history row's result; a result this file does not know shows as it is. */
export function imageUpdateResult(result: string): string {
  switch (result) {
    case "updated":
      return t("imageUpdates.result.updated");
    case "rolled_back":
      return t("imageUpdates.result.rolledBack");
    case "failed":
      return t("imageUpdates.result.failed");
    case "available":
      return t("imageUpdates.result.available");
    case "skipped":
      return t("imageUpdates.result.skipped");
    default:
      return result;
  }
}

/** A fault in the error colour, news muted, a pass in the plain colour. */
function resultClass(result: string): string {
  if (result === "rolled_back" || result === "failed") return "badge-stopped";
  if (result === "updated") return "badge-running";
  return "badge-external";
}

function options(value: string, inherit: boolean): string {
  const choices: string[] = inherit ? ["", ...IMAGE_UPDATE_MODES] : [...IMAGE_UPDATE_MODES];
  return choices.map((m) => `<option value="${m}"${m === (value || "") ? " selected" : ""}>${escapeHtml(modeLabel(m))}</option>`).join("");
}

/** The first 12 hex digits of a digest, or an em dash: no digest is not a build. */
export function shortDigest(digest: string): string {
  const hex = String(digest || "").replace(/^sha256:/, "").slice(0, 12);
  return hex ? escapeHtml(hex) : "—";
}

function when(at: string): string {
  const d = new Date(at);
  return Number.isNaN(d.getTime()) ? escapeHtml(at) : escapeHtml(d.toLocaleString(intlLocale()));
}

/** The card. view null with an error says it could not be read, never an empty history. */
export function renderImageUpdatesCard(view: ImageUpdatesView | null, error = ""): string {
  const head = `
    <div class="card-header">
      <div>
        <span class="card-title" id="image-updates-title">${ht("imageUpdates.title")}</span>
        <p class="muted compact-copy">${ht("imageUpdates.help")}</p>
      </div>
    </div>`;
  if (!view) {
    return `<section class="card" aria-labelledby="image-updates-title">${head}
      <p class="muted" data-image-updates-error>${ht("imageUpdates.loadFailed", { error })}</p></section>`;
  }
  const s = view.settings;
  const offer = s.offer && s.mode === "off"
    ? `<div class="deploy-notice" data-image-updates-offer><p>${ht("imageUpdates.offer")}</p>
        <button class="primary compact-btn" data-image-updates-action="enable">${ht("imageUpdates.offerButton")}</button></div>`
    : "";
  let hours = "";
  for (let h = 0; h < 24; h++) {
    hours += `<option value="${h}"${h === s.hour ? " selected" : ""}>${String(h).padStart(2, "0")}:00</option>`;
  }
  const apps = (view.apps || []).map((app) => `
      <tr>
        <td>${escapeHtml(app.name)}</td>
        <td><select data-image-updates-app="${escapeHtml(app.slug)}" aria-label="${ht("imageUpdates.appAria", { name: app.name })}">${options((s.services || {})[app.slug] || "", true)}</select></td>
      </tr>`).join("");
  const history = (view.history || []).map((row) => `
      <tr>
        <td>${when(row.at)}</td>
        <td>${escapeHtml(row.name || row.slug)}</td>
        <td><code>${shortDigest(row.oldDigest)}</code></td>
        <td><code>${shortDigest(row.newDigest)}</code></td>
        <td><span class="badge ${resultClass(row.result)}" data-result="${escapeHtml(row.result)}">${escapeHtml(imageUpdateResult(row.result))}</span></td>
        <td class="muted">${escapeHtml(row.detail)}</td>
      </tr>`).join("");
  const lastRun = s.lastRun ? t("imageUpdates.lastRun", { date: s.lastRun }) : t("imageUpdates.neverRun");
  return `<section class="card" aria-labelledby="image-updates-title">${head}
    ${offer}
    <div class="save-row">
      <label>${ht("imageUpdates.everyApp")} <select id="image-updates-mode">${options(s.mode, false)}</select></label>
      <label>${ht("imageUpdates.hour")} <select id="image-updates-hour">${hours}</select></label>
      <button class="primary compact-btn" data-image-updates-action="save">${ht("imageUpdates.save")}</button>
      <button class="compact-btn" data-image-updates-action="run"${view.running ? " disabled" : ""}>${view.running ? ht("imageUpdates.running") : ht("imageUpdates.run")}</button>
      <span class="muted">${escapeHtml(lastRun)}</span>
    </div>
    <p class="muted compact-copy">${ht("imageUpdates.explain", { seconds: view.settleSeconds })}</p>
    <details>
      <summary>${ht("imageUpdates.perApp")}</summary>
      <div class="table-scroll"><table class="catalog-table">
        <thead><tr><th>${ht("imageUpdates.col.app")}</th><th>${ht("imageUpdates.col.choice")}</th></tr></thead>
        <tbody>${apps || `<tr><td colspan="2" class="muted">${ht("imageUpdates.noApps")}</td></tr>`}</tbody>
      </table></div>
    </details>
    <h3 class="activity-h3">${ht("imageUpdates.history")}</h3>
    <div class="table-scroll"><table class="catalog-table" data-image-updates-history>
      <thead><tr><th>${ht("imageUpdates.col.when")}</th><th>${ht("imageUpdates.col.app")}</th><th>${ht("imageUpdates.col.from")}</th><th>${ht("imageUpdates.col.to")}</th><th>${ht("imageUpdates.col.result")}</th><th></th></tr></thead>
      <tbody>${history || `<tr><td colspan="6" class="muted">${ht("imageUpdates.noHistory")}</td></tr>`}</tbody>
    </table></div>
  </section>`;
}

/**
 * The unsaved choices of a card, by select: null when the slot holds no card
 * (empty, or the error), so a redraw keeps the draft held from before.
 */
export function draftOf(entries: Array<[string, string]>): Record<string, string> | null {
  return entries.length ? Object.fromEntries(entries) : null;
}

/**
 * Whether a load's answer is drawn into the slot. An empty slot and an error card
 * always take it; a card on screen takes only a successful answer, and only when
 * every change on it is saved.
 */
export function drawsOver(slot: "empty" | "error" | "card", failed: boolean, canRedraw: boolean): boolean {
  return slot !== "card" || (!failed && canRedraw);
}

/**
 * Counts the owner's changes so a reload never draws over one that is not saved:
 * change() on each edit, sent() when a save starts (it returns the count the save
 * carries), saved(n) when that save went through. canRedraw is true only when
 * every change is saved. Pure state, so the check can drive it without a DOM.
 */
export class ImageUpdateEdits {
  private edits = 0;
  private savedEdits = 0;
  private loads = 0;
  change(): void { this.edits += 1; }
  sent(): number { return this.edits; }
  saved(n: number): void { this.savedEdits = Math.max(this.savedEdits, n); }
  canRedraw(): boolean { return this.edits === this.savedEdits; }
  /**
   * Numbers a load. Only the newest one's answer is drawn (isLatest): answers can
   * arrive out of order, and an older one would show a choice the app no longer
   * holds, which the next Save would send back.
   */
  beginLoad(): number { return ++this.loads; }
  isLatest(load: number): boolean { return load === this.loads; }
  private pending: Record<string, string> | null = null;
  /**
   * Keeps the unsaved choices a page redraw carries until a load draws them,
   * whichever load that is: a newer load with no draft of its own still puts
   * them back, instead of drawing the saved ones over them.
   */
  holdDraft(draft: Record<string, string> | null): void { if (draft) this.pending = draft; }
  takeDraft(): Record<string, string> | null {
    const draft = this.pending;
    this.pending = null;
    return draft;
  }
}
