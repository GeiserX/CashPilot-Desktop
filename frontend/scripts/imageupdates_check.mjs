#!/usr/bin/env node
// Browser-free checks on Settings > App image updates (render/imageupdates.ts):
// the three choices, the offer on an upgraded install, the history with its
// results, and the edit counter that keeps a choice not saved yet across a
// reload (main.ts redraws the card only when canRedraw is true).
//
//   node scripts/imageupdates_check.mjs      # against ./.harness-build

import { ImageUpdateEdits, draftOf, drawsOver, renderImageUpdatesCard, shortDigest } from "../.harness-build/render/imageupdates.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const view = (settings, extra = {}) => ({
  settings: { mode: "auto", hour: 4, services: {}, ...settings },
  settleSeconds: 120,
  apps: [{ slug: "honeygain", name: "Honeygain" }, { slug: "mysterium", name: "Mysterium <Node>" }],
  history: [],
  running: false,
  ...extra,
});

// The choices and the hour.
let html = renderImageUpdatesCard(view({ mode: "notify", hour: 6, services: { mysterium: "off" } }));
check("the card has its title", html.includes("App image updates"), html);
check("the choice for every app is selected", /<select id="image-updates-mode">[^]*?<option value="notify" selected>Notify only<\/option>/.test(html), html);
check("the hour is selected", html.includes('<option value="6" selected>06:00</option>'), html);
check("an app's own choice is selected", /data-image-updates-app="mysterium"[^]*?<option value="off" selected>Off<\/option>/.test(html), html);
check("an app without its own choice follows every app", /data-image-updates-app="honeygain"[^]*?<option value="" selected>Same as every app<\/option>/.test(html), html);
check("an app name is escaped", html.includes("Mysterium &lt;Node&gt;") && !html.includes("<Node>"), html);
check("the check period is stated", html.includes("120 s"), html);
check("no offer on a new install", !html.includes("data-image-updates-offer"), html);
check("no daily run yet says so", html.includes("No daily check yet"), html);

// An upgraded install: off, with the one-click offer.
html = renderImageUpdatesCard(view({ mode: "off", offer: true }));
check("the offer shows on an upgraded install", html.includes("data-image-updates-offer") && html.includes('data-image-updates-action="enable"'), html);
html = renderImageUpdatesCard(view({ mode: "notify", offer: true }));
check("the offer goes once something is on", !html.includes("data-image-updates-offer"), html);

// The history: results, digests, and an empty state.
html = renderImageUpdatesCard(view({ lastRun: "2026-11-28" }, {
  history: [
    { id: 2, at: "2026-11-28T04:02:00Z", slug: "honeygain", name: "Honeygain", result: "rolled_back", oldDigest: "sha256:abcdef0123456789", newDigest: "", detail: "The new cashpilot-honeygain crashed", oldImage: "", newImage: "", cleared: false },
    { id: 1, at: "2026-11-27T04:02:00Z", slug: "honeygain", name: "Honeygain", result: "updated", oldDigest: "", newDigest: "sha256:0123456789abcdef", detail: "", oldImage: "", newImage: "", cleared: true },
  ],
}));
check("an undone update reads Undone, in the error colour", html.includes('class="badge badge-stopped" data-result="rolled_back">Undone'), html);
check("an update reads Updated", html.includes('data-result="updated">Updated'), html);
check("digests are cut to 12", html.includes("<code>abcdef012345</code>") && html.includes("<code>0123456789ab</code>"), html);
check("no digest is an em dash, not a build", html.includes("<code>—</code>"), html);
check("the reason is shown", html.includes("The new cashpilot-honeygain crashed"), html);
check("the last run is shown", html.includes("Last daily check: 2026-11-28"), html);
html = renderImageUpdatesCard(view({}));
check("an empty history says so", html.includes("Nothing yet."), html);
check("a running check disables Check now", renderImageUpdatesCard(view({}, { running: true })).includes('data-image-updates-action="run" disabled'), "");
check("shortDigest of nothing is an em dash", shortDigest("") === "—", shortDigest(""));

// A failed read says so, never an empty history.
html = renderImageUpdatesCard(null, "backend not ready");
check("a failed read names the error", html.includes("backend not ready") && !html.includes("Nothing yet."), html);

// The edit counter: a reload redraws only when every change is saved.
const edits = new ImageUpdateEdits();
check("nothing changed: a reload redraws", edits.canRedraw(), "");
edits.change();
check("a change not saved stops a redraw", !edits.canRedraw(), "");
const sent = edits.sent();
edits.change();
edits.saved(sent);
check("a change made during the save still stops a redraw", !edits.canRedraw(), "");
edits.saved(edits.sent());
check("once every change is saved, a reload redraws", edits.canRedraw(), "");
edits.saved(sent);
check("an older save answering late does not undo a newer one", edits.canRedraw(), "");

// Only the newest load is drawn: an older answer arriving last is dropped.
const older = edits.beginLoad();
const newer = edits.beginLoad();
check("the newest load is drawn", edits.isLatest(newer), "");
check("an older load answering last is not drawn", !edits.isLatest(older), "");

// A page redraw's unsaved choices reach whichever load draws: a newer load with
// no draft of its own still puts them back, once.
const kept = new ImageUpdateEdits();
kept.holdDraft({ "image-updates-mode": "notify" });
const withDraft = kept.beginLoad();
kept.holdDraft(null);
const without = kept.beginLoad();
check("the older load with the draft is not drawn", !kept.isLatest(withDraft), "");
const drawn = kept.isLatest(without) ? kept.takeDraft() : null;
check("the newest load draws the held draft", drawn?.["image-updates-mode"] === "notify", JSON.stringify(drawn));
check("a draft is put back only once", kept.takeDraft() === null, "");

// A redraw of a slot with no choices on it (empty, or the error card) holds no
// draft, so the one held from before survives.
check("a slot with no choices makes no draft", draftOf([]) === null, "");
check("a card's choices make the draft", draftOf([["image-updates-mode", "off"]])?.["image-updates-mode"] === "off", "");

// What a load draws over: an error card gives way to the next answer, and a card
// stays when the reload failed or a change on it is not saved.
check("an empty slot takes an error", drawsOver("empty", true, false), "");
check("an error card gives way to a later answer, even with unsaved choices held", drawsOver("error", false, false), "");
check("a card stays when the reload failed", !drawsOver("card", true, true), "");
check("a card with an unsaved change stays", !drawsOver("card", false, false), "");
check("a card with every change saved is redrawn", drawsOver("card", false, true), "");

if (failures) {
  console.error(`\n${failures} of ${checks} image update checks failed`);
  process.exit(1);
}
console.log(`image update checks: ${checks} passed`);
