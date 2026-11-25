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
//   3. A START STILL NOT DONE AFTER THE WAIT shows the failed page, with a plain
//      message, not a bare "app is still starting".
//   4. THE PAGES KEEP THE WINDOW'S TITLE BAR, so the window can be moved and closed,
//      and escape the error text, which lands in an innerHTML sink.
//   5. A SLOW START THAT ENDS AFTER THE WAIT MOVES ON BY ITSELF. Behind the failed
//      page the boot keeps asking, and draws the first screen when the start
//      finishes, with no click. Try again on that page stops the older boot, so
//      two never ask at once.
//
// The boot here is createBoot, the same function main.ts runs: only the bindings,
// the clock and #app are faked.
//
//   node scripts/boot_render_check.mjs      # against ./.harness-build

import {
  BOOT_WAIT_MS,
  STILL_STARTING,
  createBoot,
  isStillStarting,
  renderFailed,
  renderStarting,
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

// #app: records every page drawn into it and finds its Try again buttons.
function fakeRoot(log) {
  let html = "";
  const root = {
    buttons: [],
    pages: [],
    get innerHTML() { return html; },
    set innerHTML(value) {
      html = value;
      root.buttons = [];
      root.pages.push(value);
      const kind = /data-boot="(\w+)"/.exec(value)?.[1];
      if (kind) log.push(kind);
    },
    querySelectorAll(selector) {
      if (selector !== "[data-boot-retry]") return [];
      const count = html.split("data-boot-retry").length - 1;
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
  };
  return root;
}

// main.ts's boot with the bindings faked: next(call, clock) says what GetAppState
// does on each call ("ok" or an error to reject with), and a fake clock moves on
// with every sleep. sleep can be replaced to let a test step the boot by hand.
function makeBoot(next, opts = {}) {
  const log = [];
  const root = fakeRoot(log);
  let clock = 0;
  let calls = 0;
  let wired = 0;
  const boot = createBoot({
    root,
    chrome: (body) => `${body}${TITLEBAR}`,
    wireChrome: () => { wired++; },
    load: async () => {
      const outcome = next(calls, clock);
      calls++;
      if (outcome !== "ok") throw outcome;
      root.innerHTML = "FIRST SCREEN";
      log.push("first");
    },
    sleep: opts.sleep ?? (async (ms) => { clock += ms; }),
    now: () => clock,
  });
  return { boot, log, root, calls: () => calls, clock: () => clock, wired: () => wired, tick: (ms) => { clock += ms; } };
}
const script = (outcomes) => (call) => outcomes[Math.min(call, outcomes.length - 1)];

// ---------------------------------------------------------------------------
// RULE 1: a slow start waits, then shows the first screen.
// ---------------------------------------------------------------------------
{
  const run = makeBoot(script([stillStarting(), stillStarting(), "ok"]));
  const started = await run.boot();
  check("RULE 1: the boot ends on the first screen", started === true && run.root.innerHTML === "FIRST SCREEN", run.root.innerHTML);
  check("RULE 1: the starting page shows once, then the first screen", JSON.stringify(run.log) === JSON.stringify(["starting", "first"]), JSON.stringify(run.log));
  check("RULE 1: the failed page never shows", !run.log.includes("failed"), JSON.stringify(run.log));
  check("RULE 1: GetAppState is asked three times", run.calls() === 3, String(run.calls()));
  check("RULE 1: the starting page has the title bar, wired", run.root.pages[0].endsWith(TITLEBAR) && run.wired() === 1, `${run.wired()}`);
  check("RULE 1: the still-starting error is recognised", isStillStarting(stillStarting()) && isStillStarting(STILL_STARTING));
  check("RULE 1: another error is not", !isStillStarting(new Error("store: database is locked")) && !isStillStarting(new Error(`x ${STILL_STARTING}`)));
}

// ---------------------------------------------------------------------------
// RULE 2: a real error fails at once, and Try again boots again.
// ---------------------------------------------------------------------------
{
  const run = makeBoot(script([new Error("store: database is locked"), "ok"]));
  const started = await run.boot();
  check("RULE 2: a real error shows the failed page at once", started === false && JSON.stringify(run.log) === JSON.stringify(["failed"]), JSON.stringify(run.log));
  check("RULE 2: no wait before a real error", run.clock() === 0, String(run.clock()));
  check("RULE 2: the failed page names the error", run.root.innerHTML.includes("CashPilot failed to start") && run.root.innerHTML.includes("store: database is locked"), run.root.innerHTML);
  check("RULE 2: the failed page has a Try again button", /<button[^>]*data-boot-retry[^>]*>Try again<\/button>/.test(run.root.innerHTML), run.root.innerHTML);
  check("RULE 2: the failed page has the title bar, wired", run.root.innerHTML.endsWith(TITLEBAR) && run.wired() === 1, String(run.wired()));
  check("RULE 2: the Try again button is wired", run.root.buttons.length === 1 && run.root.buttons[0].listeners.length === 1, String(run.root.buttons.length));
  const button = run.root.buttons[0];
  button?.click();
  check("RULE 2: Try again disables itself so a double click boots once", button?.disabled === true);
  // Let the second boot finish.
  for (let i = 0; i < 5; i++) await Promise.resolve();
  check("RULE 2: Try again runs the boot again", run.calls() === 2, String(run.calls()));
  check("RULE 2: the second boot reaches the first screen", run.root.innerHTML === "FIRST SCREEN" && run.log.at(-1) === "first", JSON.stringify(run.log));
}

// ---------------------------------------------------------------------------
// RULE 3: a start still not done after the wait shows the failed page.
// ---------------------------------------------------------------------------
{
  // Still starting until 90 s in, then ready.
  const run = makeBoot((_call, clock) => (clock < 90_000 ? stillStarting() : "ok"));
  const started = await run.boot();
  const failedPage = run.root.pages.find((p) => p.includes('data-boot="failed"')) || "";
  const failedAt = run.log.indexOf("failed");
  check("RULE 3: a start past the wait shows the failed page", failedAt > 0, JSON.stringify(run.log));
  check("RULE 3: it shows the starting page first, once", run.log[0] === "starting" && run.log.filter((k) => k === "starting").length === 1, JSON.stringify(run.log));
  check("RULE 3: it asks with a backoff, not in a tight loop", run.calls() < 80, String(run.calls()));
  check("RULE 3: it says the start did not finish, in plain words", failedPage.includes("did not finish starting within 60 seconds") && !failedPage.includes(STILL_STARTING), failedPage);
  check("RULE 3: it says it keeps checking", failedPage.includes("It keeps checking"), failedPage);
  check("RULE 3: it keeps the Try again button", failedPage.includes("data-boot-retry"), failedPage);

  // RULE 5: and then moves on by itself.
  check("RULE 5: the boot keeps asking behind the failed page and reaches the first screen", started === true && run.root.innerHTML === "FIRST SCREEN", JSON.stringify(run.log));
  check("RULE 5: the failed page shows once, then the first screen, with no click", JSON.stringify(run.log) === JSON.stringify(["starting", "failed", "first"]), JSON.stringify(run.log));
  check("RULE 5: it reaches the first screen soon after the start finishes", run.clock() >= 90_000 && run.clock() < 90_000 + 5_000, String(run.clock()));
}
{
  // Timing: the failed page comes at the wait, not much later.
  let failedClock = -1;
  const run = makeBoot((_call, clock) => (clock < 70_000 ? stillStarting() : "ok"));
  const realSet = Object.getOwnPropertyDescriptor(run.root, "innerHTML").set;
  Object.defineProperty(run.root, "innerHTML", {
    get: Object.getOwnPropertyDescriptor(run.root, "innerHTML").get,
    set(v) { if (v.includes('data-boot="failed"') && failedClock < 0) failedClock = run.clock(); realSet.call(run.root, v); },
  });
  await run.boot();
  check("RULE 3: it waits the full time before the failed page", failedClock >= BOOT_WAIT_MS, String(failedClock));
  check("RULE 3: it does not wait much longer", failedClock < BOOT_WAIT_MS + 5_000, String(failedClock));
}

// ---------------------------------------------------------------------------
// RULE 5: Try again behind a slow start stops the older boot.
// ---------------------------------------------------------------------------
{
  // Sleep yields to the event loop, so the boot runs in the background as in
  // the window, while the test clicks.
  let ready = false;
  const turn = () => new Promise((resolve) => setImmediate(resolve));
  let run;
  run = makeBoot(() => (ready ? "ok" : stillStarting()), { sleep: async (ms) => { run.tick(ms); await turn(); } });
  const first = run.boot();
  for (let i = 0; i < 200 && !run.log.includes("failed"); i++) await turn();
  check("RULE 5: the failed page shows while the start goes on", run.log.includes("failed"), JSON.stringify(run.log));
  run.root.buttons[0]?.click();
  const callsAfterClick = run.calls();
  for (let i = 0; i < 20; i++) await turn();
  check("RULE 5: the older boot stops after Try again", (await Promise.race([first, turn().then(() => "running")])) === false, "older boot still running");
  ready = true;
  for (let i = 0; i < 20 && run.log.at(-1) !== "first"; i++) await turn();
  check("RULE 5: the newer boot reaches the first screen", run.root.innerHTML === "FIRST SCREEN", JSON.stringify(run.log));
  check("RULE 5: the first screen is drawn once", run.log.filter((k) => k === "first").length === 1, JSON.stringify(run.log));
  check("RULE 5: Try again asked again", run.calls() > callsAfterClick, `${run.calls()} vs ${callsAfterClick}`);
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
