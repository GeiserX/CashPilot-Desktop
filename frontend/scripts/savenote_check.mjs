#!/usr/bin/env node
// Browser-free checks on Settings' Save Variables note (src/render/savenote.ts).
//
//   1. A SAVE THAT WORKS SAYS SO. The note reads Saving… while it runs, then
//      Saved, drawn after the page is redrawn so the redraw does not wipe it.
//   2. A SAVE THAT FAILS SAYS WHY. The note reads Not saved with the error text,
//      escaped, and the page is not redrawn over the user's typed values.
//   3. THE BUTTON IS BUSY WHILE IT RUNS, and free again after.
//   4. A REFRESH THAT FAILS AFTER A GOOD SAVE still says Saved, and reports the
//      refresh failure on its own.
//   5. THE SLOT IS A LIVE REGION, so a screen reader reads the note.
//
//   node scripts/savenote_check.mjs      # against ./.harness-build

import { renderSaveNote, runSave, saveNoteSlot } from "../.harness-build/render/savenote.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

// The Settings page as main.ts wires it: a slot, a button, and a redraw that
// replaces both (the slot comes back empty, the button enabled).
function page({ save, refresh }) {
  const log = [];
  const view = { slot: "", disabled: false, redraws: 0, refreshErrors: [] };
  const flow = {
    save: async () => { log.push("save"); await save(); },
    refresh: async () => {
      log.push("refresh");
      await refresh();
      view.slot = "";
      view.disabled = false;
      view.redraws++;
    },
    show: (html) => { view.slot = html; log.push(`show:${/data-save-note="(\w+)"/.exec(html)?.[1] ?? "empty"}`); },
    busy: (on) => { view.disabled = on; log.push(`busy:${on}`); },
    refreshFailed: (error) => { view.refreshErrors.push(String(error)); },
  };
  return { flow, log, view };
}

// RULE 1 and RULE 3
{
  let disabledDuringSave = null;
  let shownDuringSave = "";
  const p = page({ save: async () => { disabledDuringSave = p.view.disabled; shownDuringSave = p.view.slot; }, refresh: async () => {} });
  const note = await runSave(p.flow);
  check("RULE 1: a good save ends Saved", note.state === "saved", JSON.stringify(note));
  check("RULE 1: the note reads Saving… while it runs", shownDuringSave.includes("Saving"), shownDuringSave);
  check("RULE 1: the note reads Saved at the end", p.view.slot.includes(">Saved<") && p.view.slot.includes('data-save-note="saved"'), p.view.slot);
  check("RULE 1: the note is drawn after the redraw, so it stays", p.log.indexOf("refresh") >= 0 && p.log.lastIndexOf("show:saved") > p.log.indexOf("refresh"), JSON.stringify(p.log));
  check("RULE 1: the page is redrawn with the saved values", p.view.redraws === 1, String(p.view.redraws));
  check("RULE 3: the button is disabled while the save runs", disabledDuringSave === true, String(disabledDuringSave));
  check("RULE 3: the button is free again after", p.view.disabled === false, String(p.view.disabled));
}

// RULE 2
{
  const p = page({ save: async () => { throw new Error(`settings: CASHPILOT_COLLECT_INTERVAL must be a number <b>`); }, refresh: async () => {} });
  const note = await runSave(p.flow);
  check("RULE 2: a failed save ends failed", note.state === "failed", JSON.stringify(note));
  check("RULE 2: the note says Not saved", p.view.slot.includes("Not saved"), p.view.slot);
  check("RULE 2: the note carries the error text", p.view.slot.includes("CASHPILOT_COLLECT_INTERVAL must be a number"), p.view.slot);
  check("RULE 2: the error text is escaped", !p.view.slot.includes("<b>") && p.view.slot.includes("&lt;b&gt;"), p.view.slot);
  check("RULE 2: the page is not redrawn over the typed values", p.view.redraws === 0 && !p.log.includes("refresh"), JSON.stringify(p.log));
  check("RULE 3: the button is free again after a failure", p.view.disabled === false, String(p.view.disabled));
  check("RULE 2: a plain string error is shown too", renderSaveNote({ state: "failed", error: "store: locked" }).includes("store: locked"));
}

// RULE 4
{
  const p = page({ save: async () => {}, refresh: async () => { throw new Error("state: still loading"); } });
  const note = await runSave(p.flow);
  check("RULE 4: a refresh failure after a good save still says Saved", note.state === "saved" && p.view.slot.includes(">Saved<"), p.view.slot);
  check("RULE 4: the refresh failure is reported on its own", p.view.refreshErrors.length === 1 && p.view.refreshErrors[0].includes("still loading"), JSON.stringify(p.view.refreshErrors));
}

// RULE 5
{
  const slot = saveNoteSlot("save-settings-note");
  check("RULE 5: the slot has the id main.ts fills", slot.includes('id="save-settings-note"'), slot);
  check("RULE 5: the slot is a polite live region", slot.includes('role="status"') && slot.includes('aria-live="polite"'), slot);
  check("RULE 5: no note before the first save", renderSaveNote(null) === "");
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} save note checks FAILED`);
  process.exit(1);
}
console.log(`save note checks: ${checks} passed`);
