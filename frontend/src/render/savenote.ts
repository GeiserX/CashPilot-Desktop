// The note next to Settings' Save Variables button: "Saving…" while it runs, then
// "Saved" or "Not saved" with the error. Before this, a save that worked redrew
// the page with no sign it had happened.
//
// renderSaveNote is pure. runSave is the click's flow, with the bindings and the
// page passed in, so a check runs the same flow the button runs.

import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node
import { ht } from "./i18n.js";

export type SaveNote =
  | { state: "saving" }
  | { state: "saved" }
  | { state: "failed"; error: string };

/** The note's inner HTML. Empty before the first save. */
export function renderSaveNote(note: SaveNote | null): string {
  if (!note) return "";
  if (note.state === "saving") return `<span class="save-note" data-save-note="saving">${ht("savenote.saving")}</span>`;
  if (note.state === "saved") return `<span class="save-note save-note-ok" data-save-note="saved">${ht("savenote.saved")}</span>`;
  return `<span class="save-note save-note-fail" data-save-note="failed">${ht("savenote.failed", { error: note.error })}</span>`;
}

/** The empty slot the note goes into: a live region, so a screen reader says it too. */
export function saveNoteSlot(id: string): string {
  return `<span class="save-note-slot" id="${escapeHtml(id)}" role="status" aria-live="polite"></span>`;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export interface SaveFlow {
  /** Send the values. Rejects as the binding does. */
  save(): Promise<void>;
  /** Redraw the page with the saved values. Its own failure is not a failed save. */
  refresh(): Promise<void>;
  /** Draw the note into the slot. Called after refresh, so the redrawn page gets it. */
  show(html: string): void;
  /** Disable the button while the save runs. */
  busy(on: boolean): void;
  /** A refresh that failed after a good save. */
  refreshFailed(error: unknown): void;
}

/** Save, then say so next to the button. Resolves to the final note. */
export async function runSave(flow: SaveFlow): Promise<SaveNote> {
  flow.busy(true);
  flow.show(renderSaveNote({ state: "saving" }));
  let note: SaveNote;
  try {
    await flow.save();
    note = { state: "saved" };
  } catch (error) {
    note = { state: "failed", error: errorText(error) };
  }
  if (note.state === "saved") {
    try {
      await flow.refresh();
    } catch (error) {
      flow.refreshFailed(error);
    }
  }
  flow.busy(false);
  flow.show(renderSaveNote(note));
  return note;
}
