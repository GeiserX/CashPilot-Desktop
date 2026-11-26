// Run the real runtime status wiring with Wails and the browser clock stubbed.
import assert from "node:assert/strict";
import { setImmediate } from "node:timers/promises";
import { build } from "vite";

const { output: [entry] } = await build({
  logLevel: "error",
  build: { ssr: "src/main.ts", write: false },
});
const appModule = `data:text/javascript;base64,${Buffer.from(entry.code).toString("base64")}`;

// The render harness only needs slots, attributes, and click listeners.
class Element {
  style = {};
  classList = { toggle() {} };
  listeners = new Map();

  constructor(attributes = new Map(), html = "") {
    this.attributes = attributes;
    this.disabled = attributes.has("disabled");
    this.dataset = Object.fromEntries([...attributes].filter(([key]) => key.startsWith("data-"))
      .map(([key, value]) => [key.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase()), value]));
    this.innerHTML = html;
  }

  set innerHTML(html) {
    this.html = html;
    this.children = [...html.matchAll(/<(button|details|div|p|section|small)\b([^>]*)>/g)];
    this.elements = new Map();
  }

  get innerHTML() { return this.html; }
  set outerHTML(html) {
    const [, , content] = html.match(/^<(p|small)\b[^>]*>([\s\S]*)<\/\1>$/);
    this.innerHTML = content;
  }
  get isConnected() { return true; }
  appendChild() {}
  addEventListener(event, fn) { this.listeners.set(event, fn); }
  click() {
    assert.equal(this.disabled, false, "cannot click a disabled button");
    assert.ok(this.listeners.has("click"), "the app wired the button");
    this.listeners.get("click")();
  }

  querySelectorAll(selector) {
    const [, attribute, value] = selector.match(/^\[([\w-]+)(?:="([^"]*)")?\]$/) ?? [];
    return this.children.flatMap(([tag, name, raw], index) => {
      const attrs = new Map([...raw.matchAll(/([\w-]+)(?:="([^"]*)")?/g)]
        .map(([, key, value]) => [key, value ?? ""]));
      const matches = selector.startsWith("#") ? attrs.get("id") === selector.slice(1)
        : selector.startsWith(".") ? attrs.get("class")?.split(" ").includes(selector.slice(1))
          : attrs.has(attribute) && (value === undefined || attrs.get(attribute) === value);
      if (!matches) return [];
      if (!this.elements.has(index)) {
        const offset = this.children[index].index + tag.length;
        const tail = this.html.slice(offset);
        let depth = 1;
        const end = [...tail.matchAll(new RegExp(`</?${name}\\b[^>]*>`, "g"))].find(([part]) => {
          depth += part.startsWith("</") ? -1 : 1;
          return depth === 0;
        });
        this.elements.set(index, new Element(attrs, tail.slice(0, end?.index ?? 0)));
      }
      return [this.elements.get(index)];
    });
  }

  querySelector(selector) { return this.querySelectorAll(selector)[0] ?? null; }
}

const stopped = {
  available: false, reason: "not-running", nativeAvailable: false, nativeServices: [],
  message: "Docker Desktop is installed but not running.",
};
const ready = { ...stopped, available: true, reason: "running", message: "Docker's engine answered." };
const stoppedRow = {
  id: "a6", title: "Docker Desktop running", state: "needs-you", text: stopped.message,
  action: { kind: "start-docker", label: "Start Docker Desktop" },
};
const reading = (runtime, steps) => ({ facts: { supported: true }, runtime, steps, runtimeNotice: null });
const setup = reading(stopped, [stoppedRow]);
const answered = reading(ready, [
  { ...stoppedRow, state: "done", text: ready.message },
  { id: "a9", title: "Linux containers", state: "done", text: "Docker runs Linux containers." },
]);
const emptyFleet = {
  workers: 0, mobiles: 0, online: 0, services: 0, devices: [],
  workerSnippet: "", mobileSnippet: "", reach: { hubOff: true },
};

async function openApp(key, initialRuntime, firstRunComplete, hostCheck, bindings = {}) {
  const root = new Element();
  const events = new Map();
  let now = 0;
  globalThis.document = {
    body: new Element(),
    createElement: () => new Element(),
    querySelector: (selector) => selector === "#app" ? root : root.querySelector(selector),
    querySelectorAll: (selector) => root.querySelectorAll(selector),
  };
  Object.defineProperty(globalThis, "navigator", { value: { userAgent: "Windows NT 10.0" }, configurable: true });
  Object.defineProperty(globalThis, "performance", { value: { now: () => now }, configurable: true });
  globalThis.requestAnimationFrame = () => {};
  // Advance the Docker wait without sleeping. Toast dismissal is outside this check.
  globalThis.setTimeout = (fn, ms) => { if (ms <= 1000) { now += ms; queueMicrotask(fn); } };
  globalThis.window = {
    runtime: { EventsOnMultiple: (event, fn) => events.set(event, fn) },
    go: { main: { App: {
      GetAppState: async () => ({ config: { mode: "lead", firstRunComplete }, runtime: initialRuntime, guides: [] }),
      GetRuntimeGuides: async () => [],
      SetupResume: async () => null,
      StartDockerDesktop: async () => {},
      DockerDesktopEngineReady: async () => true,
      HostCheck: hostCheck,
      ...bindings,
    } } },
  };
  await import(`${appModule}#${key}`);
  await setImmediate();
  return { root, events };
}

for (const outcome of ["failed", "stopped"]) {
  let checks = 0;
  const { root, events } = await openApp(outcome, stopped, false, async () => {
    if (++checks === 1) return setup;
    if (checks === 2) return answered;
    if (outcome === "failed") throw new Error("Host check failed");
    return setup;
  });
  const card = () => root.querySelector("#runtime-card-slot").innerHTML;
  const guide = () => root.querySelector("#docker-guide");
  const dashboard = () => root.querySelector("#continue-btn");
  guide().querySelector('[data-guide-action="start-docker"]').click();
  await setImmediate();
  assert.match(card(), /Runtime ready/);
  assert.match(guide().innerHTML, /Docker Desktop is ready on this PC/);
  assert.equal(dashboard().disabled, false);

  if (outcome === "stopped") {
    // A setup-resume event arrives while the checklist shows Docker ready.
    events.get("setup:resume")();
    await setImmediate();
  }
  const retry = guide().querySelector("#docker-guide-check") ?? root.querySelector("#refresh-runtime");
  retry.click();
  await setImmediate();

  const actual = {
    ready: /Runtime ready/.test(card()),
    cardStatus: outcome === "failed" ? /Docker status unknown/.test(card()) : /not running/.test(card()),
    guideStatus: outcome === "failed"
      ? /current status is unknown/.test(guide().innerHTML) && !/data-step=/.test(guide().innerHTML)
      : /data-step="a6" data-state="needs-you"/.test(guide().innerHTML),
    checklistRetry: retry.attributes.get("id") === "docker-guide-check",
    greenChecklist: /docker-guide-summary ok|docker-guide-wait ok/.test(guide().innerHTML),
    dashboardDisabled: dashboard().disabled,
  };
  try {
    assert.deepEqual(actual, {
      ready: false, cardStatus: true, guideStatus: true, checklistRetry: true,
      greenChecklist: false, dashboardDisabled: true,
    });
    console.log(`runtime readiness: answered then ${outcome} retry passed`);
  } catch (error) {
    console.error(`runtime readiness: answered then ${outcome} retry failed\n${error.message}`);
    process.exitCode = 1;
  }
}

{
  let checks = 0;
  const { root, events } = await openApp("later", ready, true, async () => {
    if (++checks === 1) return answered;
    if (checks === 2) throw new Error("Host check failed");
    return setup;
  });
  const badge = () => root.querySelector(".topbar-runtime").innerHTML;
  const card = () => root.querySelector("#runtime-card-slot").innerHTML;
  const guide = () => root.querySelector("#docker-guide");
  const retry = () => guide().querySelector("#docker-guide-check").click();
  root.querySelector(".topbar-runtime").click();
  await setImmediate();
  assert.match(badge(), /Runtime ready/);
  assert.match(card(), /Runtime ready/);
  assert.match(guide().innerHTML, /Docker Desktop is ready on this PC/);

  retry();
  await setImmediate();
  events.get("runtime:host-checked")();
  await setImmediate();
  assert.match(badge(), /Runtime unknown/);
  assert.match(card(), /unknown/i);
  assert.match(root.querySelector(".notification-menu").innerHTML, /Docker status unknown/);
  assert.match(guide().innerHTML, /current status is unknown/);
  assert.doesNotMatch(guide().innerHTML, /data-step=|docker-guide-summary ok/);

  retry();
  await setImmediate();
  assert.match(badge(), /Runtime offline/);
  assert.match(card(), /not running/);
  assert.match(guide().innerHTML, /data-step="a6" data-state="needs-you"/);
  assert.doesNotMatch(root.querySelector(".notification-menu").innerHTML, /Docker status unknown/);
  console.log("runtime readiness: Runtime page and Alerts follow the checklist");
}

async function checkCase(name, run) {
  try {
    await run();
    console.log(`runtime readiness: ${name} passed`);
  } catch (error) {
    console.error(`runtime readiness: ${name} failed\n${error.message}`);
    process.exitCode = 1;
  }
}

for (const nativeAvailable of [false, true]) {
  const name = `Docker stops before first run opens Dashboard (native apps: ${nativeAvailable})`;
  await checkCase(name, async () => {
    const native = { nativeAvailable, nativeServices: nativeAvailable ? ["MystNodes"] : [] };
    let firstRunComplete = false;
    let runtime = { ...ready, ...native };
    const appReads = [];
    const { root } = await openApp(name, runtime, false, async () => answered, {
      CompleteOnboarding: async () => { firstRunComplete = true; },
      GetAppState: async () => {
        appReads.push({ firstRunComplete, reason: runtime.reason });
        const notifications = firstRunComplete
          ? [{ runtime: true, level: "warning", title: "Docker offline", message: runtime.message }]
          : [];
        return { config: { mode: "lead", firstRunComplete }, runtime, notifications, guides: [] };
      },
      GetUsage: async () => null,
    });

    assert.match(root.querySelector("#runtime-card-slot").innerHTML, /Runtime ready/);
    runtime = { ...stopped, ...native };
    root.querySelector("#continue-btn").click();
    await setImmediate();

    assert.deepEqual(appReads, [
      { firstRunComplete: false, reason: "running" },
      { firstRunComplete: true, reason: "not-running" },
    ]);
    const attention = root.querySelector(".attention").innerHTML;
    assert.deepEqual({
      stoppedBadge: (nativeAvailable ? /Native mode/ : /Runtime offline/).test(root.querySelector(".topbar-runtime").innerHTML),
      stoppedMessage: attention.includes(stopped.message),
      setupPrompt: /<button[^>]*data-view="runtime"[^>]*>Set up Docker<\/button>/.test(attention),
      allClear: /Nothing needs you/.test(attention),
    }, { stoppedBadge: true, stoppedMessage: true, setupPrompt: true, allClear: false });
    assert.match(root.querySelector(".notification-menu").innerHTML, /Docker offline/);
  });
}

await checkCase("first-run checklist Skip uses the fresh Dashboard reading", async () => {
  const native = { nativeAvailable: true, nativeServices: ["MystNodes"] };
  const nativeOnly = { ...stopped, ...native };
  let firstRunComplete = false;
  const { root } = await openApp("skip", nativeOnly, false, async () => ({ ...setup, runtime: nativeOnly }), {
    CancelSetup: async () => {},
    CompleteOnboarding: async () => { firstRunComplete = true; },
    GetAppState: async () => ({
      config: { mode: "lead", firstRunComplete },
      runtime: firstRunComplete ? { ...ready, ...native } : nativeOnly,
      guides: [],
    }),
    GetUsage: async () => null,
  });
  const guide = () => root.querySelector("#docker-guide");
  guide().querySelector('[data-guide-action="skip"]').click();
  guide().querySelector('[data-guide-action="skip-go"]').click();
  await setImmediate();
  assert.match(root.querySelector(".topbar-runtime").innerHTML, /Runtime ready/);
  assert.match(root.querySelector(".attention").innerHTML, /Nothing needs you/);
});

await checkCase("a first-run retry finishing in Settings preserves the newer reading", async () => {
  const retry = Promise.withResolvers();
  let firstRunComplete = false;
  let appReads = 0;
  const { root } = await openApp("first-run-to-settings", ready, false, () => retry.promise, {
    CompleteOnboarding: async () => { firstRunComplete = true; },
    GetAppState: async () => ({
      config: { mode: "lead", firstRunComplete },
      runtime: ++appReads === 1 ? ready : stopped,
      guides: [],
    }),
    GetUsage: async () => null,
    GetSettingsState: async () => ({ environment: [], collectors: [] }),
    BackgroundHelperStatus: async () => ({ supported: false }),
    UnfinishedRestores: async () => [],
    GetModeState: async () => null,
  });
  root.querySelector("#refresh-runtime").click();
  root.querySelector("#continue-btn").click();
  await setImmediate();
  root.querySelector('[data-view="settings"]').click();
  await setImmediate();
  assert.equal(appReads, 3);

  retry.resolve(answered);
  await setImmediate();
  assert.match(root.querySelector(".topbar-runtime").innerHTML, /Runtime offline/);
  assert.match(root.querySelector("#runtime-settings-slot").innerHTML, /Set up Docker/);
});

await checkCase("a check replaced three times in a row gives up and keeps the newer reading", async () => {
  const held = [];
  let runtime = ready;
  const { root, events } = await openApp("give-up", ready, true, async () => {
    if (held.length === 0) { held.push(null); return answered; }
    const read = Promise.withResolvers();
    held.push(read);
    return read.promise;
  }, {
    GetAppState: async () => ({ config: { mode: "lead", firstRunComplete: true }, runtime, guides: [] }),
  });
  const badge = () => root.querySelector(".topbar-runtime").innerHTML;
  const guide = () => root.querySelector("#docker-guide");
  root.querySelector(".topbar-runtime").click();
  await setImmediate();
  assert.match(guide().innerHTML, /Docker Desktop is ready on this PC/);

  guide().querySelector("#docker-guide-check").click();
  runtime = stopped;
  for (let read = 1; read <= 3; read++) {
    await setImmediate();
    assert.equal(held.length, 1 + read, `the replaced check reads the PC again (read ${read})`);
    // An earnings refresh reads Docker stopped while this host read is out.
    events.get("earnings:changed")();
    await setImmediate();
    assert.match(badge(), /Runtime offline/);
    held[read].resolve(answered);
  }
  await setImmediate();
  assert.equal(held.length, 4, "the check stops after three replaced readings");
  assert.match(badge(), /Runtime offline/, "the top bar keeps the newer reading");
  assert.match(guide().innerHTML, /current status is unknown/);
  assert.match(guide().innerHTML, /Click Check again to retry/);
  assert.doesNotMatch(guide().innerHTML, /data-step=|docker-guide-summary ok/);
});

for (const view of ["fleet", "activity"]) {
  await checkCase(`${view} load preserves a failed retry`, async () => {
    const host = Promise.withResolvers();
    const page = Promise.withResolvers();
    let checks = 0;
    let loads = 0;
    let appReads = 0;
    const binding = view === "fleet" ? "GetFleetState" : "GetActivity";
    const { root } = await openApp(view, ready, true, async () => ++checks === 1 ? answered : host.promise, {
      [binding]: () => { loads++; return page.promise; },
      GetProxiesState: async () => null,
      // Opening the view reads Docker again; that read is still out when the retry fails.
      GetAppState: () => ++appReads === 1
        ? Promise.resolve({ config: { mode: "lead", firstRunComplete: true }, runtime: ready, guides: [] })
        : new Promise(() => {}),
    });
    root.querySelector(".topbar-runtime").click();
    await setImmediate();
    assert.match(root.querySelector(".topbar-runtime").innerHTML, /Runtime ready/);
    root.querySelector("#docker-guide").querySelector("#docker-guide-check").click();
    root.querySelector(`[data-view="${view}"]`).click();
    assert.equal(loads, 1);
    assert.equal(appReads, 2, "opening the view reads Docker again");

    host.reject(new Error("Host check failed"));
    await setImmediate();
    const status = () => ({
      unknownBadge: /Runtime unknown/.test(root.querySelector(".topbar-runtime").innerHTML),
      unknownAlert: /Docker status unknown/.test(root.querySelector(".notification-menu").innerHTML),
    });
    const unknown = { unknownBadge: true, unknownAlert: true };
    assert.deepEqual(status(), unknown, "the failed check publishes unknown before the page loads");

    page.resolve(view === "fleet" ? emptyFleet : { days: 1, enabled: false, containers: [] });
    await setImmediate();
    assert.match(root.innerHTML, view === "fleet" ? /Add Worker or Mobile/ : /Activity is off/);
    assert.deepEqual(status(), unknown, "the completed page load preserves the badge and alert");
  });
}

for (const retryOrder of ["none", "retry-first", "settings-first"]) {
  const name = `Settings check reaches Fleet (completion order: ${retryOrder})`;
  await checkCase(name, async () => {
    const settingsCheck = Promise.withResolvers();
    const retry = Promise.withResolvers();
    const snapshot = (runtime) => ({ config: { mode: "lead", firstRunComplete: true }, runtime, guides: [] });
    let appReads = 0;
    let hostReads = 0;
    // A host read after the retry finds Docker as the Settings check left it: stopped.
    const { root } = await openApp(name, ready, true, async () => ++hostReads === 1 ? answered : hostReads === 2 ? retry.promise : setup, {
      GetAppState: async () => ++appReads === 1 ? snapshot(ready) : settingsCheck.promise,
      GetSettingsState: async () => ({ environment: [], collectors: [] }),
      BackgroundHelperStatus: async () => ({ supported: false }),
      UnfinishedRestores: async () => [],
      GetModeState: async () => null,
      GetFleetState: async () => emptyFleet,
      GetProxiesState: async () => null,
    });
    const badge = () => root.querySelector(".topbar-runtime").innerHTML;
    if (retryOrder !== "none") {
      root.querySelector(".topbar-runtime").click();
      await setImmediate();
      root.querySelector("#docker-guide").querySelector("#docker-guide-check").click();
      assert.equal(hostReads, 2);
    }
    root.querySelector('[data-view="settings"]').click();
    await setImmediate();
    assert.equal(appReads, 2);
    root.querySelector('[data-view="fleet"]').click();
    await setImmediate();
    assert.match(badge(), /Runtime ready/);

    if (retryOrder === "retry-first") {
      retry.reject(new Error("Host check failed"));
      await setImmediate();
      assert.match(badge(), /Runtime unknown/, "the retry can publish while the Settings check is pending");
      assert.match(root.querySelector(".notification-menu").innerHTML, /Docker status unknown/);
    }
    settingsCheck.resolve(snapshot(stopped));
    await setImmediate();
    assert.match(badge(), /Runtime offline/);
    assert.match(root.innerHTML, /Add Worker or Mobile/);
    assert.doesNotMatch(root.querySelector(".notification-menu").innerHTML, /Docker status unknown/);

    if (retryOrder === "settings-first") {
      retry.resolve(answered);
      await setImmediate();
      assert.match(badge(), /Runtime offline/, "a retry cannot replace the completed Settings check");
      assert.equal(hostReads, 3, "the replaced retry reads the PC again instead of returning its old rows");
    }
  });
}

await checkCase("Dashboard and Settings fence lines follow the runtime reading", async () => {
  let snapshot = stopped;
  let fail = false;
  const { root, events } = await openApp("fence", stopped, true, async () => setup, {
    GetAppState: async () => {
      if (fail) throw new Error("App state check failed");
      return {
        config: { mode: "lead", firstRunComplete: true },
        runtime: snapshot,
        guides: [],
        isolation: { state: "n/a", setting: "auto" },
      };
    },
    GetSettingsState: async () => ({
      environment: [{ key: "CASHPILOT_ISOLATION", label: "LAN isolation", value: "auto" }],
      collectors: [],
    }),
    BackgroundHelperStatus: async () => ({ supported: false }),
    UnfinishedRestores: async () => [],
    GetModeState: async () => null,
    GetUsage: async () => null,
  });
  const line = () => root.querySelector("[data-fence-status]").innerHTML;
  const checkStatus = async () => {
    events.get("runtime:host-checked")();
    await setImmediate();
  };
  const expectUnknown = () => {
    assert.match(root.querySelector(".topbar-runtime").innerHTML, /Runtime unknown/);
    assert.match(line(), /Docker status is unknown/);
    assert.doesNotMatch(line(), /Docker is not running/);
  };

  assert.match(line(), /Docker is not running/);
  fail = true;
  await checkStatus();
  expectUnknown();

  root.querySelector('[data-view="settings"]').click();
  await setImmediate();
  expectUnknown();

  fail = false;
  snapshot = ready;
  await checkStatus();
  assert.match(root.querySelector(".topbar-runtime").innerHTML, /Runtime ready/);
  assert.match(line(), /Not checked yet/);
  assert.doesNotMatch(line(), /Docker is not running/);

  snapshot = stopped;
  await checkStatus();
  assert.match(line(), /Docker is not running/);
});

await checkCase("positive pipe poll followed by an inconclusive check", async () => {
  const unknown = { ...stopped, reason: "unreachable", message: "CashPilot could not check Docker on this computer." };
  const inconclusive = reading(unknown, [{ ...stoppedRow, state: "unknown", text: unknown.message, action: null }]);
  let checks = 0;
  let polls = 0;
  const { root } = await openApp("inconclusive", stopped, false, async () => ++checks === 1 ? setup : inconclusive, {
    DockerDesktopEngineReady: async () => { polls++; return true; },
  });
  const guide = () => root.querySelector("#docker-guide");
  guide().querySelector('[data-guide-action="start-docker"]').click();
  await setImmediate();
  assert.equal(polls, 1);
  assert.equal(checks, 2);
  assert.match(root.querySelector("#runtime-card-slot").innerHTML, /Docker status unknown/);
  assert.equal(root.querySelector("#continue-btn").disabled, true);
  assert.match(guide().innerHTML, /data-step="a6" data-state="unknown"/);
  assert.doesNotMatch(guide().innerHTML, /docker-guide-wait ok/);
  assert.match(guide().innerHTML, /connected to Docker Desktop earlier/);
});

await checkCase("remote engine readiness leaves an inconclusive local pipe answer as history", async () => {
  const remote = { ...ready, context: "remote" };
  const localUnknown = { ...stoppedRow, state: "unknown", text: "CashPilot could not read whether Docker Desktop is running.", action: null };
  const locallyAnswered = reading({ ...stopped, context: "remote" }, answered.steps);
  let checks = 0;
  let polls = 0;
  const results = [setup, reading(remote, [localUnknown]), locallyAnswered];
  const { root } = await openApp("remote", stopped, true, async () => results[checks++], {
    DockerDesktopEngineReady: async () => { polls++; return true; },
  });
  const guide = () => root.querySelector("#docker-guide");
  root.querySelector(".topbar-runtime").click();
  await setImmediate();
  guide().querySelector('[data-guide-action="start-docker"]').click();
  await setImmediate();

  assert.equal(checks, 2);
  assert.equal(polls, 1);
  assert.match(root.querySelector(".topbar-runtime").innerHTML, /Runtime ready/);
  assert.match(guide().innerHTML, /data-step="a6" data-state="unknown"/);
  assert.doesNotMatch(guide().innerHTML, /docker-guide-wait ok|docker-guide-summary ok/);
  assert.match(guide().innerHTML, /connected to Docker Desktop earlier/);

  guide().querySelector("#docker-guide-check").click();
  await setImmediate();
  assert.match(root.querySelector(".topbar-runtime").innerHTML, /Runtime offline/);
  assert.match(guide().innerHTML, /data-step="a6" data-state="done"/);
  assert.match(guide().innerHTML, /docker-guide-wait ok/);
  assert.match(guide().innerHTML, /Docker Desktop is ready on this PC/);
});
