// Run after Vite builds the frontend with CHROME_BIN set:
// npm run test:dom
// CHROME_BIN=/path/to/chrome node scripts/docker_gate_dom_check.mjs [dist]
//
// The real app in Chromium, with the Wails bindings stubbed, checks three things:
//
//   1. controls: every control that starts or redeploys a container on this
//      computer follows the shared Docker status. Stopped or unknown, it is
//      disabled and the page says how to check or set up Docker, with a button to
//      the Runtime page. Ready, it works. Native apps are never blocked. The
//      controls are Import > Deploy, Fleet > Proxies > Run a copy and copy Start,
//      and Dashboard Start, Redeploy and Turn on isolation. A Docker change on
//      Fleet keeps what the user typed there, and opening Fleet reads Docker again.
//   2. activity: the last sample's failure is told apart from the Docker status
//      now, so a fresh "ready" does not keep the cached "Docker is not reachable".
//   3. read-order: a host check that answers "ready" after a newer reading said
//      "stopped" does not draw a ready checklist.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const [dist = "dist"] = process.argv.slice(2);
assert.ok(process.env.CHROME_BIN, "Set CHROME_BIN to your Chromium executable.");

function checkDockerGate(scenario) {
  const stopped = {
    kind: "existing-docker", available: false, reason: "not-running", message: "Docker Desktop is installed but not running.",
    nativeAvailable: true, nativeServices: ["MystNodes"], version: "", context: "", tools: null,
  };
  const ready = { ...stopped, available: true, reason: "running", message: "Docker's engine answered." };
  const unknown = { ...stopped, reason: "unreachable", message: "CashPilot could not reach Docker." };
  const docker = { slug: "honeygain", name: "Honeygain", category: "bandwidth", docker: { image: "honeygain/honeygain:1", env: [] } };
  const native = { slug: "mystnodes", name: "MystNodes", category: "bandwidth", docker: { image: "mysteriumnetwork/myst:1", env: [] } };
  const deployment = (service, runtime) => ({
    slug: service.slug, name: "cashpilot-" + service.slug, image: service.docker.image, status: "exited", runtime,
    containerId: "", cpuPercent: 0, memoryMb: 0, customImage: "", instances: 1,
  });
  const state = {
    config: { mode: "lead", firstRunComplete: true },
    runtime: stopped,
    notifications: [],
    guides: [],
    services: [docker, native],
    deployments: [deployment(docker, "existing-docker"), deployment(native, "native")],
    copies: { [docker.slug]: [{ slug: docker.slug, name: "cashpilot-honeygain-2", status: "exited", instance: 2, cpuPercent: 0, memoryMb: 0, containerId: "", image: docker.docker.image }] },
    drift: { [docker.slug]: ["missing NET_RAW"] },
    isolation: { mode: "legacy", setting: "auto", state: "reduced", outside: [docker.slug] },
    workerTargets: [],
  };
  const stoppedRow = { id: "a6", title: "Docker Desktop running", state: "needs-you", text: stopped.message, action: { kind: "start-docker", label: "Start Docker Desktop" } };
  const reading = (runtime) => ({
    facts: { supported: true }, runtime, runtimeNotice: null,
    steps: runtime.available ? [{ ...stoppedRow, state: "done", text: runtime.message, action: null }] : [stoppedRow],
  });
  const proxies = {
    proxies: [{
      id: 1, name: "Local proxy", kind: "socks5", host: "192.0.2.1", port: 1080, exitNode: "", secrets: {}, lastTest: null,
      cap: { enabled: false, cappedAt: "", capReason: "", usedBytes: 0, capBytes: 0, thresholdPct: 90, resetDay: 0 },
      copies: [{ slug: docker.slug, name: docker.name, status: "exited", exitIp: "", exitCountry: "", stoppedByCap: false }],
    }],
    candidates: [{ slug: docker.slug, name: docker.name, refusal: "" }, { slug: "earnapp", name: "EarnApp", refusal: "" }],
  };
  const events = {};
  const hostChecks = [];
  let activityRound = "no_runtime";
  window.confirm = () => true;
  window.runtime = { EventsOnMultiple(name, fn) { events[name] = fn; }, BrowserOpenURL() {} };
  window.go = { main: { App: new Proxy({
    async GetAppState() { return structuredClone(state); },
    async GetSettingsState() {
      const env = (key) => ({ key, label: key, description: "", value: "", defaultValue: "" });
      return { environment: [env("CASHPILOT_RUNTIME_PROVIDER"), env("CASHPILOT_ISOLATION")], collectors: [] };
    },
    async GetFleetState() {
      return { workers: 0, mobiles: 0, online: 0, services: 0, devices: [], workerSnippet: "", mobileSnippet: "", reach: { hubOff: true } };
    },
    async GetProxiesState() { return structuredClone(proxies); },
    async GetActivity() {
      return {
        enabled: true, days: 1, rawHours: 24, lastWindow: 0,
        round: { at: 1700000000, workerStatus: activityRound, leftoverProbes: [] },
        networks: { present: false, ageSeconds: null, rows: null }, networksEnabled: true, containers: [], alerts: [],
      };
    },
    async PreviewImport() {
      const row = (service) => ({ slug: service.slug, name: service.name, fields: [], selected: true, state: "ready" });
      return { source: "money4band", sourceName: "money4band", rows: [row(docker), row(native)] };
    },
    async ApplyImport() { return { saved: [docker.slug, native.slug] }; },
    async GetCredentials() { return { values: {}, savedSecrets: {} }; },
    async HostCheck() {
      return new Promise((resolve) => hostChecks.push(resolve));
    },
  }, { get: (app, name) => async (...args) => app[name]?.(...args) ?? null }) } };

  const q = (selector) => document.querySelector(selector);
  const all = (selector) => [...document.querySelectorAll(selector)];
  const pause = () => new Promise((resolve) => setTimeout(resolve, 10));
  async function until(predicate, description) {
    for (let i = 0; i < 100; i++) {
      if (predicate()) return;
      await pause();
    }
    throw Error("Waiting for " + description);
  }
  // A redraw that should happen: give it time, then read what is there.
  async function settle(predicate) {
    for (let i = 0; i < 100 && !predicate(); i++) await pause();
  }
  const view = async (name, ready) => {
    q(`[data-view="${name}"]`).click();
    await until(ready, name);
  };
  // A Docker reading arrives the way a background check delivers it.
  async function setRuntime(runtime) {
    state.runtime = runtime;
    events["runtime:host-checked"]();
    const badge = runtime.available ? "Runtime ready" : runtime.reason === "unreachable" ? "Runtime unknown" : "Native mode";
    await until(() => q(".topbar-runtime")?.textContent.includes(badge), badge);
    await pause();
  }
  // What a control offers: disabled or not, and the way to Docker beside it.
  const control = (selector) => {
    const buttons = all(selector);
    if (!buttons.length) return "missing";
    return buttons.every((b) => b.disabled) ? "disabled" : buttons.some((b) => b.disabled) ? "mixed" : "enabled";
  };
  const note = (scope) => {
    const line = q(scope)?.querySelector("[data-docker-blocked]");
    if (!line) return "";
    return line.querySelector('[data-view="runtime"]')?.textContent.trim() || "no button";
  };

  async function controls() {
    const out = {};
    await until(() => q('[data-view="settings"]'), "dashboard");
    const dashboard = () => ({
      start: control(`.attention [data-row-action="start"][data-slug="${docker.slug}"]:not([data-copy])`),
      tableStart: control(`.services-block [data-row-action="start"][data-slug="${docker.slug}"]:not([data-copy])`),
      copyStart: control(`[data-row-action="start"][data-slug="${docker.slug}"][data-copy="2"]`),
      nativeStart: control(`[data-row-action="start"][data-slug="${native.slug}"]`),
      redeploy: control(`[data-drift-action="redeploy"][data-slug="${docker.slug}"]`),
      isolation: control("[data-isolation-action]"),
      notes: all(".attention [data-docker-blocked]").length,
    });
    out.dashboard = { stopped: dashboard() };
    await setRuntime(ready);
    out.dashboard.ready = dashboard();
    await setRuntime(unknown);
    out.dashboard.unknown = dashboard();

    await setRuntime(stopped);
    await view("settings", () => q('[data-view="import"]'));
    await view("import", () => q("#import-paste"));
    q("#import-paste").value = "HONEYGAIN_EMAIL=a@example.com";
    q("#import-preview-btn").click();
    await until(() => q("#import-apply-btn"), "import preview");
    q("#import-apply-btn").click();
    await until(() => q(`[data-import-deploy="${docker.slug}"]`), "imported services");
    const imported = () => ({
      docker: control(`[data-import-deploy="${docker.slug}"]`),
      native: control(`[data-import-deploy="${native.slug}"]`),
      note: note("#import-saved"),
    });
    out.import = { stopped: imported() };
    await setRuntime(ready);
    out.import.ready = imported();
    await setRuntime(unknown);
    out.import.unknown = imported();

    await setRuntime(stopped);
    await view("fleet", () => q("[data-proxy-run-deploy]"));
    const fleet = () => ({
      run: control("[data-proxy-run-deploy]"),
      start: control("[data-proxy-copy-start]"),
      note: note(".proxies-panel"),
    });
    out.fleet = { stopped: fleet() };
    // What the user typed or chose on Fleet survives a Docker change.
    q("#fleet-name").value = "garage-pc";
    q("#fleet-endpoint").value = "192.0.2.9";
    q("[data-proxy-run-service]").value = "earnapp";
    const typed = () => [q("#fleet-name")?.value, q("#fleet-endpoint")?.value, q("[data-proxy-run-service]")?.value].join("|");
    await setRuntime(ready);
    await settle(() => !q("[data-proxy-run-deploy]")?.disabled);
    out.fleet.ready = fleet();
    out.fleet.typedAfterReady = typed();
    await setRuntime(unknown);
    await settle(() => q("[data-proxy-run-deploy]")?.disabled);
    out.fleet.unknown = fleet();
    out.fleet.typedAfterUnknown = typed();

    // Docker starts outside CashPilot, with no event: opening Fleet reads it again.
    await setRuntime(stopped);
    await view("activity", () => q(".activity-controls"));
    await pause();
    state.runtime = ready;
    await view("fleet", () => q("[data-proxy-run-deploy]"));
    await settle(() => !q("[data-proxy-run-deploy]")?.disabled);
    out.fleet.arrive = fleet();

    // A button to the Runtime page opens it.
    await setRuntime(stopped);
    await settle(() => q(".proxies-panel [data-docker-blocked] [data-view]"));
    q(".proxies-panel [data-docker-blocked] [data-view]")?.click();
    await settle(() => q("#docker-guide"));
    out.opensRuntime = !!q("#docker-guide");
    return out;
  }

  async function activity() {
    const out = {};
    await until(() => q('[data-view="activity"]'), "dashboard");
    const chip = () => q(".activity-controls")?.textContent.replace(/\s+/g, " ").trim() || "";
    await view("activity", () => q(".activity-controls"));
    const read = () => ({
      cachedFailure: chip().includes("cannot sample: Docker is not reachable"),
      readyNow: chip().includes("Docker is ready now"),
    });
    out.stopped = read();
    // A fresh check says ready; the last sample still says no_runtime.
    await setRuntime(ready);
    await settle(() => chip().includes("Docker is ready now") || !chip().includes("Docker is not reachable"));
    out.ready = read();
    // A later visit reads the same sample with Docker ready.
    await view("dashboard", () => q(".attention"));
    await view("activity", () => q(".activity-controls"));
    out.revisit = read();
    // A sample that ran says nothing about Docker.
    activityRound = "ok";
    await view("dashboard", () => q(".attention"));
    await view("activity", () => q(".activity-controls"));
    out.okSample = read();
    return out;
  }

  async function readOrder() {
    const out = {};
    for (const phase of ["entry", "retry"]) {
      state.runtime = ready;
      hostChecks.length = 0;
      events["runtime:host-checked"]?.();
      await until(() => q(".topbar-runtime")?.textContent.includes("Runtime ready"), "ready badge");
      q(".topbar-runtime").click();
      await until(() => hostChecks.length === 1, "first host check");
      if (phase === "retry") {
        hostChecks[0](reading(stopped));
        await until(() => q("#docker-guide-check"), "checklist");
        q("#docker-guide-check").click();
        await until(() => hostChecks.length === 2, "second host check");
      }
      const held = hostChecks[hostChecks.length - 1];
      const before = hostChecks.length;
      // An earnings refresh reads Docker stopped while the host check is out.
      state.runtime = stopped;
      events["earnings:changed"]();
      await until(() => q(".topbar-runtime")?.textContent.includes("Native mode"), "stopped badge");
      held(reading(ready));
      // The late ready answer may ask again; answer that with the truth.
      for (let i = 0; i < 20 && hostChecks.length === before; i++) await pause();
      for (let n = before; n < hostChecks.length; n++) hostChecks[n](reading(stopped));
      await pause();
      await pause();
      out[phase] = {
        badge: q(".topbar-runtime").textContent.trim(),
        row: q('[data-step="a6"]')?.dataset.state ?? "none",
        startDocker: !!q('[data-guide-action="start-docker"]'),
      };
      q('[data-view="dashboard"]').click();
      await until(() => q(".attention"), "dashboard");
    }
    return out;
  }

  document.addEventListener("DOMContentLoaded", async () => {
    try {
      const run = { controls, activity, "read-order": readOrder }[scenario];
      document.querySelector("#result").textContent = JSON.stringify(await run());
    } catch (error) {
      document.querySelector("#result").textContent = JSON.stringify({ error: String(error) });
    }
  });
}

const blocked = (button) => ({ docker: "disabled", native: "enabled", note: button });
const expected = {
  controls: {
    dashboard: {
      stopped: { start: "disabled", tableStart: "disabled", copyStart: "disabled", nativeStart: "enabled", redeploy: "disabled", isolation: "disabled", notes: 4 },
      ready: { start: "enabled", tableStart: "enabled", copyStart: "enabled", nativeStart: "enabled", redeploy: "enabled", isolation: "enabled", notes: 0 },
      unknown: { start: "disabled", tableStart: "disabled", copyStart: "disabled", nativeStart: "enabled", redeploy: "disabled", isolation: "disabled", notes: 4 },
    },
    import: {
      stopped: blocked("Set up Docker"),
      ready: { docker: "enabled", native: "enabled", note: "" },
      unknown: blocked("Check Docker"),
    },
    fleet: {
      stopped: { run: "disabled", start: "disabled", note: "Set up Docker" },
      ready: { run: "enabled", start: "enabled", note: "" },
      typedAfterReady: "garage-pc|192.0.2.9|earnapp",
      unknown: { run: "disabled", start: "disabled", note: "Check Docker" },
      typedAfterUnknown: "garage-pc|192.0.2.9|earnapp",
      arrive: { run: "enabled", start: "enabled", note: "" },
    },
    opensRuntime: true,
  },
  activity: {
    stopped: { cachedFailure: true, readyNow: false },
    ready: { cachedFailure: false, readyNow: true },
    revisit: { cachedFailure: false, readyNow: true },
    okSample: { cachedFailure: false, readyNow: false },
  },
  "read-order": {
    entry: { badge: "Native mode", row: "needs-you", startDocker: true },
    retry: { badge: "Native mode", row: "needs-you", startDocker: true },
  },
};

const index = readFileSync(join(dist, "index.html"), "utf8");
const [, asset] = index.match(/src="\/(assets\/[^"]+\.js)"/);
const bundle = readFileSync(join(dist, asset), "utf8");
const css = readdirSync(join(dist, "assets")).filter((name) => name.endsWith(".css"))
  .map((name) => readFileSync(join(dist, "assets", name), "utf8")).join("\n");
let failed = false;
for (const scenario of Object.keys(expected)) {
  const temp = mkdtempSync(join(tmpdir(), "cashpilot-docker-gate-"));
  try {
    const page = join(temp, "test.html");
    writeFileSync(page, `<!doctype html><style>${css}</style><div id="app"></div><pre id="result"></pre>
      <script>(${checkDockerGate})(${JSON.stringify(scenario)})</script>
      <script type="module">${bundle.replaceAll("</script", "<\\/script")}</script>`);
    const html = execFileSync(process.env.CHROME_BIN, [
      "--headless", "--disable-background-networking", "--no-first-run",
      "--user-data-dir=" + join(temp, "profile"), "--virtual-time-budget=20000",
      "--dump-dom", pathToFileURL(page).href,
    ], { encoding: "utf8", timeout: 120_000, maxBuffer: 8_000_000, stdio: ["ignore", "pipe", "ignore"] });
    const result = JSON.parse(html.match(/<pre id="result">(.*?)<\/pre>/s)?.[1] || "{}");
    try {
      assert.deepEqual(result, expected[scenario]);
      console.log(`docker gate DOM: ${scenario} passed`);
    } catch (error) {
      failed = true;
      console.error(`docker gate DOM: ${scenario} failed\n${error.message}`);
    }
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
}
if (failed) process.exit(1);
