#!/usr/bin/env node
// Browser-free checks on the Copy buttons' feedback (Settings secret Copy and
// the Fleet snippets' Copy): "Copied" after a copy, then "Copy" again.
//
//   node scripts/copy_check.mjs      # against ./.harness-build

import { COPIED_MS, flashCopied, wireCopyText } from "../.harness-build/render/copy.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const timers = [];
const schedule = (reset, ms) => timers.push({reset, ms});
const button = {textContent: "Copy"};

flashCopied(button, schedule);
check("a copy says Copied", button.textContent === "Copied", button.textContent);
check("one reset is scheduled", timers.length === 1, String(timers.length));
check("the reset comes after about 2 seconds", COPIED_MS >= 1500 && COPIED_MS <= 3000 && timers[0]?.ms === COPIED_MS, String(timers[0]?.ms));
timers[0]?.reset();
check("after the wait the button says Copy again", button.textContent === "Copy", button.textContent);

// wireCopyText: a [data-copy-text] button copies its own value, once per click,
// even when the page is wired twice.
const listeners = [];
const codeButton = {
  dataset: {copyText: "2QLQV97F"},
  textContent: "Copy",
  addEventListener: (event, fn) => listeners.push({event, fn}),
};
const scope = {querySelectorAll: () => [codeButton]};
const written = [];
const failed = [];
wireCopyText(scope, async (text) => { written.push(text); }, (error) => failed.push(error));
wireCopyText(scope, async (text) => { written.push(text); }, (error) => failed.push(error));
check("a second wiring adds no second listener", listeners.length === 1, String(listeners.length));
await listeners[0]?.fn();
check("the click copies the button's value", written.length === 1 && written[0] === "2QLQV97F", JSON.stringify(written));
check("the click says Copied", codeButton.textContent === "Copied", codeButton.textContent);

const denied = {dataset: {copyText: "X"}, textContent: "Copy", addEventListener: (event, fn) => listeners.push({event, fn})};
wireCopyText({querySelectorAll: () => [denied]}, async () => { throw new Error("denied"); }, (error) => failed.push(error));
await listeners[1]?.fn();
check("a refused clipboard is reported, not hidden", failed.length === 1 && denied.textContent === "Copy", String(failed.length));

if (failures > 0) {
  console.error(`\n${failures} of ${checks} copy checks FAILED`);
  process.exit(1);
}
console.log(`copy_check: ${checks} checks passed`);
