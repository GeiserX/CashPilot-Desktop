// Run the real first-run wiring with Wails and the browser clock stubbed.
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
    this.children = [...html.matchAll(/<(button|details|div)\b([^>]*)>/g)];
    this.elements = new Map();
  }

  get innerHTML() { return this.html; }
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

async function openApp(key, initialRuntime, firstRunComplete, hostCheck) {
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
