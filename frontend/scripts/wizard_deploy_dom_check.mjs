// Run after Vite builds the frontend:
// CHROME_BIN=/path/to/chrome node scripts/wizard_deploy_dom_check.mjs wallet [dist]
// Use "worker" for the disappeared-worker regression on the combined tree.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const [scenario, dist = "dist"] = process.argv.slice(2);
assert.ok(["wallet", "worker"].includes(scenario), "Choose wallet or worker.");
assert.ok(process.env.CHROME_BIN, "Set CHROME_BIN to your Chromium executable.");

function checkWizard(scenario) {
  const wallet = scenario === "wallet";
  const slug = wallet ? "sia-hostd" : "plain";
  const fieldKey = wallet ? "SIA_STORAGE_DIR" : "TOKEN";
  const env = [{ key: fieldKey, label: wallet ? "Storage directory" : "Token", required: true }];
  if (wallet) env.push(
    { key: "HOSTD_API_PASSWORD", label: "Dashboard password", required: true, secret: true },
    { key: "HOSTD_WALLET_SEED", required: true, seedFile: { path: "/wallet/seed" } },
  );
  const state = {
    config: { firstRunComplete: true },
    services: [{ slug, name: "Fixture", category: "storage", status: "active", docker: { image: "fixture/node:1", env } }],
    deployments: [],
    runtime: { available: true },
    scaling: { [slug]: { max: 3 } },
    workerTargets: wallet ? [] : [{ id: 23, name: "garage", online: true, services: [] }],
  };
  const events = {};
  let finishAdminCheck;
  let workerDeploys = 0;
  let settings;
  let sentChoice;
  window.runtime = { EventsOnMultiple(name, fn) { events[name] = fn; } };
  window.go = { main: { App: new Proxy({
    async GetAppState() { return structuredClone(state); },
    async GetCredentials() { return { [fieldKey]: "saved" }; },
    async DeployAdminStep() {
      if (!wallet) throw Error("Unexpected local deploy");
      return new Promise(resolve => { finishAdminCheck = resolve; });
    },
    async DeployServiceWallet(slug, values, image, instances, choice) {
      sentChoice = choice;
      settings = {
        storage: values.SIA_STORAGE_DIR, image, instances, mode: choice.mode,
        passwordSent: values.HOSTD_API_PASSWORD === "test password", phraseSent: choice.phrase === "test phrase",
      };
      return { deployment: { slug } };
    },
    async DeployToWorker() {
      workerDeploys++;
      return { worker: "garage", containerId: "fixture" };
    },
  }, { get: (app, name) => app[name] || (async () => null) }) } };

  const q = selector => document.querySelector(selector);
  const click = selector => q(selector).click();
  const pause = () => new Promise(resolve => setTimeout(resolve, 10));
  const status = () => q("[data-deploy-notice]")?.dataset.deployNotice;
  const deploy = '[data-wizard-action="deploy"]';
  async function until(predicate, description) {
    for (let i = 0; i < 80; i++) {
      if (predicate()) return;
      await pause();
    }
    throw Error("Waiting for " + description);
  }
  async function exercise() {
    await until(() => q('[data-view="catalog"]'), "dashboard");
    click('[data-view="catalog"]');
    click("[data-service]");
    const field = `[data-wizard-env="${fieldKey}"]`;
    await until(() => q(deploy) && q(field).value === "saved", "setup card");
    if (wallet) {
      click('[data-wallet-mode][value="import"]');
      const phrase = q("[data-wallet-phrase]");
      const password = q('[data-wizard-env="HOSTD_API_PASSWORD"][type="password"]');
      q(field).value = "";
      phrase.value = "test phrase";
      password.value = "test password";
      click(deploy);
      await until(() => status() === "missing", "missing storage directory");
      const rejectedBeforeAdminCheck = !finishAdminCheck && Boolean(q('[data-missing-field="SIA_STORAGE_DIR"]'));
      const passwordClearedAfterRejection = password.value === "";
      const phraseClearedAfterRejection = phrase.value === "";

      phrase.value = "test phrase";
      password.value = "test password";
      q("[data-wizard-image]").value = "fixture/node:2";
      q("[data-wizard-instances]").value = "2";
      q(field).value = "/fixture/storage";
      click(deploy);
      await until(() => finishAdminCheck, "admin check");
      const passwordClearedAtClick = password.value === "";
      const phraseClearedAtClick = phrase.value === "";
      click("#wizard-next");
      finishAdminCheck({ needed: false });
      await until(() => settings, "wallet deploy");
      click("#wizard-prev");
      await until(() => status() === "ok", "wallet result");
      return {
        settings, rejectedBeforeAdminCheck, passwordClearedAfterRejection, phraseClearedAfterRejection,
        passwordClearedAtClick, phraseClearedAtClick, phraseClearedAtEnd: sentChoice.phrase === "",
      };
    }
    const where = q("[data-wizard-where]");
    where.value = "23";
    where.dispatchEvent(new Event("change", { bubbles: true }));
    click(deploy);
    await until(() => status() === "ok", "first worker deploy");
    await pause();
    state.workerTargets = [];
    events["earnings:changed"]();
    await pause();
    click(deploy);
    await pause();
    const beforeRedraw = {
      status: status(),
      pickerRefused: where.validationMessage.includes("Choose a target"),
    };
    click("#wizard-next");
    click("#wizard-prev");
    return { ...beforeRedraw, statusAfterRedraw: status(), workerDeploys };
  }
  document.addEventListener("DOMContentLoaded", async () => {
    try {
      q("#result").textContent = JSON.stringify(await exercise());
    } catch (error) {
      q("#result").textContent = JSON.stringify({ error: String(error) });
    }
  });
}

const index = readFileSync(join(dist, "index.html"), "utf8");
const [, asset] = index.match(/src="\/(assets\/[^"]+\.js)"/);
const bundle = readFileSync(join(dist, asset), "utf8");
const temp = mkdtempSync(join(tmpdir(), "cashpilot-wizard-deploy-"));
try {
  const page = join(temp, "test.html");
  writeFileSync(page, `<!doctype html><div id="app"></div><pre id="result"></pre>
    <script>(${checkWizard})(${JSON.stringify(scenario)})</script>
    <script type="module">${bundle.replaceAll("</script", "<\\/script")}</script>`);
  const html = execFileSync(process.env.CHROME_BIN, [
    "--headless", "--disable-background-networking", "--no-first-run",
    "--user-data-dir=" + join(temp, "profile"), "--virtual-time-budget=4000",
    "--dump-dom", pathToFileURL(page).href,
  ], { encoding: "utf8", timeout: 60_000, maxBuffer: 4_000_000, stdio: ["ignore", "pipe", "ignore"] });
  const result = JSON.parse(html.match(/<pre id="result">(.*?)<\/pre>/s)?.[1] || "{}");
  assert.deepEqual(result, scenario === "wallet" ? {
    settings: {
      storage: "/fixture/storage", image: "fixture/node:2", instances: 2,
      mode: "import", passwordSent: true, phraseSent: true,
    },
    rejectedBeforeAdminCheck: true,
    passwordClearedAfterRejection: true,
    phraseClearedAfterRejection: true,
    passwordClearedAtClick: true,
    phraseClearedAtClick: true,
    phraseClearedAtEnd: true,
  } : { status: "failed", pickerRefused: true, statusAfterRedraw: "failed", workerDeploys: 1 });
  console.log(`wizard deploy DOM: ${scenario} passed`);
} finally {
  rmSync(temp, { recursive: true, force: true });
}
