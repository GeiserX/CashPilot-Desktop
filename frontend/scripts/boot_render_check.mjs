#!/usr/bin/env node
// Browser-free checks on the first draw of the window (src/render/boot.ts).
//
// The window can load before the Go side has finished starting. Every binding then
// rejects with "app is still starting". These checks hold that:
//
//   1. A SLOW START WAITS. GetAppState rejects with the still-starting error twice,
//      then answers: the starting page shows, then the first screen, and the failed
//      page never shows.
//   2. A REAL ERROR FAILS AT ONCE, with the error text and a Try again button, and
//      Try again runs the boot again, which can then reach the first screen.
//   3. A START THAT NEVER ENDS FAILS after the wait, with a plain message, not a
//      bare "app is still starting".
//   4. THE PAGES KEEP THE WINDOW'S TITLE BAR, so the window can be moved and closed,
//      and escape the error text, which lands in an innerHTML sink.
//
//   node scripts/boot_render_check.mjs      # against ./.harness-build

import {
  BOOT_WAIT_MS,
  STILL_STARTING,
  bootWhenReady,
  isStillStarting,
  renderFailed,
  renderStarting,
  wireBootRetry,
} from "../.harness-build/render/boot.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const TITLEBAR = `<div class="titlebar"><button data-window-action="close"></button></div>`;
// A Wails binding rejects with an Error whose message is the Go error's text.
const stillStarting = () => new Error(STILL_STARTING);

// A page the size of #app: holds the last HTML drawn and finds its Try again buttons.
function fakeRoot() {
  return {
    html: "",
    querySelectorAll(selector) {
      if (selector !== "[data-boot-retry]") return [];
      const count = this.html.split("data-boot-retry").length - 1;
      return Array.from({ length: count }, () => {
        const button = {
          disabled: false,
          listeners: [],
          addEventListener(type, fn) { if (type === "click") this.listeners.push(fn); },
          click() { this.listeners.forEach((fn) => fn()); },
        };
        root.buttons.push(button);
        return button;
      });
    },
    buttons: [],
  };
}
let root = fakeRoot();

// The boot of main.ts, with the bindings faked: getAppState answers from a script of
// outcomes, and a fake clock moves on with every sleep.
function makeBoot(outcomes) {
  const log = [];
  let clock = 0;
  let calls = 0;
  async function boot() {
    return bootWhenReady({
      load: async () => {
        const next = outcomes[Math.min(calls, outcomes.length - 1)];
        calls++;
        if (next !== "ok") throw next;
        root.html = "FIRST SCREEN";
        log.push("first");
      },
      showStarting: () => {
        root.html = renderStarting(TITLEBAR);
        log.push("starting");
      },
      showFailed: (error) => {
        root.html = renderFailed(TITLEBAR, error);
        root.buttons = [];
        wireBootRetry(root, () => void boot());
        log.push("failed");
      },
      sleep: async (ms) => { clock += ms; },
      now: () => clock,
    });
  }
  return { boot, log, calls: () => calls, clock: () => clock };
}

// ---------------------------------------------------------------------------
// RULE 1: a slow start waits, then shows the first screen.
// ---------------------------------------------------------------------------
{
  root = fakeRoot();
  const run = makeBoot([stillStarting(), stillStarting(), "ok"]);
  const started = await run.boot();
  check("RULE 1: the boot ends on the first screen", started === true && root.html === "FIRST SCREEN", root.html);
  check("RULE 1: the starting page shows once, then the first screen", JSON.stringify(run.log) === JSON.stringify(["starting", "first"]), JSON.stringify(run.log));
  check("RULE 1: the failed page never shows", !run.log.includes("failed"), JSON.stringify(run.log));
  check("RULE 1: GetAppState is asked three times", run.calls() === 3, String(run.calls()));
  check("RULE 1: the still-starting error is recognised", isStillStarting(stillStarting()) && isStillStarting(STILL_STARTING));
  check("RULE 1: another error is not", !isStillStarting(new Error("store: database is locked")) && !isStillStarting(new Error(`x ${STILL_STARTING}`)));
}

// ---------------------------------------------------------------------------
// RULE 2: a real error fails at once, and Try again boots again.
// ---------------------------------------------------------------------------
{
  root = fakeRoot();
  const run = makeBoot([new Error("store: database is locked"), "ok"]);
  const started = await run.boot();
  check("RULE 2: a real error shows the failed page at once", started === false && JSON.stringify(run.log) === JSON.stringify(["failed"]), JSON.stringify(run.log));
  check("RULE 2: no wait before a real error", run.clock() === 0, String(run.clock()));
  check("RULE 2: the failed page names the error", root.html.includes("CashPilot failed to start") && root.html.includes("store: database is locked"), root.html);
  check("RULE 2: the failed page has a Try again button", /<button[^>]*data-boot-retry[^>]*>Try again<\/button>/.test(root.html), root.html);
  check("RULE 2: the Try again button is wired", root.buttons.length === 1 && root.buttons[0].listeners.length === 1, String(root.buttons.length));
  const button = root.buttons[0];
  button?.click();
  check("RULE 2: Try again disables itself so a double click boots once", button?.disabled === true);
  // Let the second boot finish.
  for (let i = 0; i < 5; i++) await Promise.resolve();
  check("RULE 2: Try again runs the boot again", run.calls() === 2, String(run.calls()));
  check("RULE 2: the second boot reaches the first screen", root.html === "FIRST SCREEN" && run.log.at(-1) === "first", JSON.stringify(run.log));
}

// ---------------------------------------------------------------------------
// RULE 3: a start that never ends fails after the wait.
// ---------------------------------------------------------------------------
{
  root = fakeRoot();
  const run = makeBoot([stillStarting()]);
  const started = await run.boot();
  check("RULE 3: a start that never ends shows the failed page", started === false && run.log.at(-1) === "failed", JSON.stringify(run.log));
  check("RULE 3: it shows the starting page first, once", JSON.stringify(run.log) === JSON.stringify(["starting", "failed"]), JSON.stringify(run.log));
  check("RULE 3: it waits the full time", run.clock() >= BOOT_WAIT_MS, String(run.clock()));
  check("RULE 3: it does not wait much longer", run.clock() < BOOT_WAIT_MS + 5_000, String(run.clock()));
  check("RULE 3: it asks with a backoff, not in a tight loop", run.calls() < 50, String(run.calls()));
  check("RULE 3: it says the start did not finish, in plain words", root.html.includes("did not finish starting within 60 seconds") && !root.html.includes(STILL_STARTING), root.html);
  check("RULE 3: it keeps the Try again button", root.html.includes("data-boot-retry"), root.html);
}

// ---------------------------------------------------------------------------
// RULE 4: the title bar stays, and the error text is escaped.
// ---------------------------------------------------------------------------
{
  check("RULE 4: the starting page keeps the title bar", renderStarting(TITLEBAR).startsWith(TITLEBAR));
  check("RULE 4: the starting page says it is starting", renderStarting(TITLEBAR).includes("Starting CashPilot"));
  check("RULE 4: the starting page is not a failure", !renderStarting(TITLEBAR).includes("failed"));
  const failed = renderFailed(TITLEBAR, new Error(`<img src=x onerror=alert(1)>`));
  check("RULE 4: the failed page keeps the title bar", failed.startsWith(TITLEBAR));
  check("RULE 4: the error text is escaped", !failed.includes("<img") && failed.includes("&lt;img"), failed);
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} boot render checks FAILED`);
  process.exit(1);
}
console.log(`boot render checks: ${checks} passed`);
