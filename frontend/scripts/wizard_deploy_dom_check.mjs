// Run after Vite builds the frontend with CHROME_BIN set:
// npm run test:dom
// CHROME_BIN=/path/to/chrome node scripts/wizard_deploy_dom_check.mjs wallet [dist]
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
  let finishHydration;
  let finishDeploy;
  let finishRefresh;
  let savedSettings = { [fieldKey]: "saved" };
  let savedPassword = wallet ? "previous password" : "";
  let redrawHydration;
  let workerDeploys = 0;
  let localAdminChecks = 0;
  let passwordSends = 0;
  let settings;
  let sentChoice;
  let sentValues;
  window.runtime = { EventsOnMultiple(name, fn) { events[name] = fn; } };
  window.go = { main: { App: new Proxy({
    async GetAppState() {
      if (wallet && settings && !finishRefresh) {
        return new Promise(resolve => { finishRefresh = () => resolve(structuredClone(state)); });
      }
      return structuredClone(state);
    },
    async GetCredentials() {
      const view = { values: { ...savedSettings }, savedSecrets: savedPassword ? { HOSTD_API_PASSWORD: true } : {} };
      if (wallet && !finishHydration) return new Promise(resolve => { finishHydration = () => resolve(view); });
      if (wallet && settings) redrawHydration = view;
      return view;
    },
    async SaveCredentials(slug, values) {
      savedSettings = { [fieldKey]: values[fieldKey] };
      if (values.HOSTD_API_PASSWORD?.trim()) savedPassword = values.HOSTD_API_PASSWORD;
    },
    async DeployAdminStep() {
      if (!wallet) {
        localAdminChecks++;
        throw Error("Unexpected local deploy");
      }
      return new Promise(resolve => { finishAdminCheck = resolve; });
    },
    async DeployServiceWallet(slug, values, image, instances, choice) {
      sentChoice = choice;
      sentValues = values;
      savedSettings = { [fieldKey]: values[fieldKey] };
      if (values.HOSTD_API_PASSWORD?.trim()) savedPassword = values.HOSTD_API_PASSWORD;
      settings = {
        storage: values.SIA_STORAGE_DIR, image, instances, mode: choice.mode,
        passwordSent: values.HOSTD_API_PASSWORD === "test password", phraseSent: choice.phrase === "test phrase",
      };
      return new Promise(resolve => { finishDeploy = () => resolve({ deployment: { slug } }); });
    },
    async DeployToWorker() {
      workerDeploys++;
      return { worker: "garage", containerId: "fixture" };
    },
  }, { get: (app, name) => async (...args) => {
    if (args.some(arg => arg?.HOSTD_API_PASSWORD === "test password")) passwordSends++;
    return app[name]?.(...args) ?? null;
  } }) } };

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
    const notice = () => q("[data-deploy-notice]")?.textContent;
    if (wallet) {
      await until(() => q(deploy) && finishHydration, "setup card while credentials are loading");
      click('[data-wallet-mode][value="import"]');
      const phrase = q("[data-wallet-phrase]");
      const passwordField = '[data-wizard-env="HOSTD_API_PASSWORD"][type="password"]';
      const password = q(passwordField);
      q(field).value = "";
      phrase.value = "test phrase";
      password.value = "test password";
      click(deploy);
      await until(() => status() === "missing", "missing storage directory");
      const rejectedBeforeAdminCheck = !finishAdminCheck && Boolean(q('[data-missing-field="SIA_STORAGE_DIR"]'));
      const rejectionSentNothing = passwordSends === 0 && !settings;
      const passwordClearedAfterRejection = password.value === "";
      const phraseClearedAfterRejection = phrase.value === "";
      finishHydration();
      await until(() => q(field).value === "saved", "delayed credentials response");
      const passwordEmptyAfterDelayedHydration = password.value === "";

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
      await until(() => finishDeploy, "wallet deploy request");
      const secretsClearedWhileDeployIsPending = !sentValues.HOSTD_API_PASSWORD && sentChoice.phrase === "";
      finishDeploy();
      click("#wizard-prev");
      await until(() => status() === "ok" && finishRefresh && q(field).value === "/fixture/storage",
        "wallet result and redrawn credentials during refresh");
      const secretsClearedWhileRefreshIsPending = !sentValues.HOSTD_API_PASSWORD && sentChoice.phrase === "";
      const redrawReportsOnlyPresence = redrawHydration.savedSecrets.HOSTD_API_PASSWORD === true &&
        !JSON.stringify(redrawHydration).includes(savedPassword);
      const passwordEmptyAfterRedraw = q(passwordField).value === "";
      const phraseEmptyAfterRedraw = q("[data-wallet-phrase]").value === "";
      const savedPasswordLabel = q(passwordField).placeholder === "Saved" &&
        q(passwordField).closest("label").textContent.includes("Leave blank to keep a saved value");
      finishRefresh();

      q(field).value = "/fixture/edited";
      click('[data-wizard-action="save"]');
      await until(() => q(`[data-output-slug="${slug}"]`).textContent === "Credentials saved.", "directory-only save");
      const directoryOnlySave = savedSettings[fieldKey] === "/fixture/edited" && savedPassword === "test password";
      const importedSettings = settings;
      finishAdminCheck = undefined;
      finishDeploy = undefined;
      click(deploy);
      await until(() => finishAdminCheck, "admin check with a blank saved password");
      finishAdminCheck({ needed: false });
      await until(() => finishDeploy, "redeploy with a blank saved password");
      finishDeploy();
      await until(() => status() === "ok", "redeploy result");
      const blankPasswordDeploy = settings.storage === "/fixture/edited" && !settings.passwordSent && savedPassword === "test password";
      return {
        settings: importedSettings, rejectedBeforeAdminCheck, rejectionSentNothing,
        passwordClearedAfterRejection, phraseClearedAfterRejection, passwordEmptyAfterDelayedHydration,
        passwordClearedAtClick, phraseClearedAtClick, phraseClearedAtEnd: sentChoice.phrase === "",
        passwordEmptyAfterRedraw, phraseEmptyAfterRedraw, savedPasswordLabel, directoryOnlySave, blankPasswordDeploy,
        secretsClearedWhileDeployIsPending, secretsClearedWhileRefreshIsPending, redrawReportsOnlyPresence,
        passwordSends,
      };
    }
    await until(() => q(deploy) && q(field).value === "saved", "setup card");
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
    await until(() => status() === "failed", "unavailable worker refusal");
    const beforeRedraw = {
      status: status(),
      workerRefused: notice().includes("The selected worker is no longer available. Choose a target and try again."),
    };
    click("#wizard-next");
    click("#wizard-prev");
    return { ...beforeRedraw, statusAfterRedraw: status(), workerDeploys, localAdminChecks };
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
    rejectionSentNothing: true,
    passwordClearedAfterRejection: true,
    phraseClearedAfterRejection: true,
    passwordEmptyAfterDelayedHydration: true,
    passwordClearedAtClick: true,
    phraseClearedAtClick: true,
    phraseClearedAtEnd: true,
    passwordEmptyAfterRedraw: true,
    phraseEmptyAfterRedraw: true,
    savedPasswordLabel: true,
    directoryOnlySave: true,
    blankPasswordDeploy: true,
    secretsClearedWhileDeployIsPending: true,
    secretsClearedWhileRefreshIsPending: true,
    redrawReportsOnlyPresence: true,
    passwordSends: 1,
  } : { status: "failed", workerRefused: true, statusAfterRedraw: "failed", workerDeploys: 1, localAdminChecks: 0 });
  console.log(`wizard deploy DOM: ${scenario} passed`);
} finally {
  rmSync(temp, { recursive: true, force: true });
}
