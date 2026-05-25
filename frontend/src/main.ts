import "./style.css";
import {
  CheckRuntime,
  CollectService,
  CompleteOnboarding,
  DeployService,
  GetAppState,
  GetCredentials,
  GetLogs,
  GetRuntimeGuides,
  ManagedRuntimePlan,
  RefreshDeployments,
  RemoveService,
  RestartService,
  SaveCredentials,
  StopService,
} from "../wailsjs/go/main/App";
import type { AppState, Deployment, InstallGuide, ManagedRuntimePlan as RuntimePlan, Service } from "./wails";

let state: AppState | null = null;
let selectedService: Service | null = null;

const root = document.querySelector<HTMLDivElement>("#app")!;

async function boot() {
  try {
    state = await GetAppState();
    render();
  } catch (error) {
    renderError(error);
  }
}

function render() {
  if (!state) return;
  if (!state.config.firstRunComplete) {
    renderOnboarding(state);
    return;
  }
  renderDashboard(state);
}

function renderOnboarding(current: AppState) {
  const runtime = current.runtime;
  root.innerHTML = `
    ${synthwaveBackground()}
    <main class="onboarding">
      <section class="onboarding-card">
        <p class="eyebrow">CashPilot Desktop</p>
        <h1>Passive income from your machine, with guardrails.</h1>
        <p class="subtitle">CashPilot now runs as a local Wails/Go app. Start with an existing Docker-compatible runtime, then move to the managed VM appliance when it lands.</p>
        <div class="runtime-card ${runtime.available ? "ok" : "warn"}">
          <strong>${runtime.available ? "Runtime ready" : "Runtime setup needed"}</strong>
          <span>${escapeHtml(runtime.message)}</span>
          ${runtime.context ? `<small>Docker context: ${escapeHtml(runtime.context)}</small>` : ""}
        </div>
        <div class="actions">
          <button class="primary" id="continue-btn" ${runtime.available ? "" : "disabled"}>Open Dashboard</button>
          <button class="secondary" id="refresh-runtime">Check Again</button>
        </div>
        <div id="install-guides" class="guide-grid"></div>
      </section>
    </main>
  `;
  document.querySelector("#continue-btn")?.addEventListener("click", async () => {
    await CompleteOnboarding();
    state = await GetAppState();
    render();
  });
  document.querySelector("#refresh-runtime")?.addEventListener("click", async () => {
    const updated = await CheckRuntime();
    state = {...current, runtime: updated, guides: await GetRuntimeGuides()};
    render();
  });
  renderGuides(current.guides || []);
}

function renderGuides(guides: InstallGuide[]) {
  const holder = document.querySelector<HTMLDivElement>("#install-guides");
  if (!holder) return;
  holder.innerHTML = guides.map((guide) => `
    <article class="guide">
      <h3>${escapeHtml(guide.name)}</h3>
      <p>${escapeHtml(guide.description)}</p>
      <a href="${escapeHtml(guide.url)}" target="_blank" rel="noreferrer">Open install guide</a>
      ${(guide.commands || []).map((cmd) => `<code>${escapeHtml(cmd)}</code>`).join("")}
      ${(guide.notes || []).map((note) => `<small>${escapeHtml(note)}</small>`).join("")}
    </article>
  `).join("");
}

function renderDashboard(current: AppState) {
  const services = current.services || [];
  const deployments = current.deployments || [];
  const earnings = current.earnings || [];
  const selected = selectedService || services.find((svc) => !svc.manualOnly) || services[0];
  selectedService = selected || null;

  root.innerHTML = `
    <div class="shell">
      <aside class="sidebar">
        <div>
          <p class="eyebrow">CashPilot Desktop</p>
          <h1>Local Service Manager</h1>
          <p class="muted">Wails + Go runtime orchestration. No Python sidecar, no server users.</p>
        </div>
        <div class="runtime-card compact ${current.runtime.available ? "ok" : "warn"}">
          <strong>${current.runtime.available ? "Runtime ready" : "Runtime offline"}</strong>
          <span>${escapeHtml(current.runtime.message)}</span>
        </div>
        <nav class="service-list">
          ${services.map((svc) => serviceButton(svc, deployments)).join("")}
        </nav>
      </aside>
      <main class="content">
        <section class="hero-panel">
          <div>
            <p class="eyebrow">Dashboard</p>
            <h2>${deployments.length} managed container${deployments.length === 1 ? "" : "s"}</h2>
            <p class="muted">Quiting the app leaves containers running. Stop or remove services explicitly from here.</p>
          </div>
          <button class="secondary" id="refresh">Refresh runtime</button>
        </section>
        <section class="grid two">
          <div class="panel">
            <h2>Running Services</h2>
            ${deployments.length ? deployments.map(renderDeployment).join("") : `<p class="muted">No CashPilot-managed containers yet.</p>`}
          </div>
          <div class="panel">
            <h2>Latest Earnings</h2>
            ${earnings.length ? earnings.map(renderEarning).join("") : `<p class="muted">No earnings collected yet.</p>`}
          </div>
        </section>
        ${selected ? renderServiceDetail(selected, deployments.find((dep) => dep.slug === selected.slug)) : ""}
        <section class="panel">
          <h2>Managed Runtime Roadmap</h2>
          <div id="runtime-plan" class="muted">Loading roadmap...</div>
        </section>
      </main>
    </div>
  `;

  document.querySelectorAll<HTMLButtonElement>("[data-service]").forEach((button) => {
    button.addEventListener("click", () => {
      selectedService = services.find((svc) => svc.slug === button.dataset.service) || null;
      render();
    });
  });
  document.querySelector("#refresh")?.addEventListener("click", refreshState);
  document.querySelector("#save-creds")?.addEventListener("click", saveCredentials);
  document.querySelector("#deploy")?.addEventListener("click", deploySelected);
  document.querySelector("#stop")?.addEventListener("click", () => actionSelected("stop"));
  document.querySelector("#restart")?.addEventListener("click", () => actionSelected("restart"));
  document.querySelector("#remove")?.addEventListener("click", () => actionSelected("remove"));
  document.querySelector("#logs")?.addEventListener("click", showLogs);
  document.querySelector("#collect")?.addEventListener("click", collectSelected);
  void renderRuntimePlan();
  void hydrateCredentialForm(selected);
}

function serviceButton(service: Service, deployments: Deployment[]) {
  const active = deployments.some((dep) => dep.slug === service.slug);
  return `
    <button class="${selectedService?.slug === service.slug ? "selected" : ""}" data-service="${service.slug}">
      <span>${escapeHtml(service.name)}</span>
      <small>${service.manualOnly ? "manual" : service.category}${active ? " / deployed" : ""}</small>
    </button>
  `;
}

function renderDeployment(dep: Deployment) {
  return `
    <div class="row">
      <div>
        <strong>${escapeHtml(dep.slug)}</strong>
        <small>${escapeHtml(dep.image)}</small>
      </div>
      <span class="pill">${escapeHtml(dep.status)}</span>
    </div>
  `;
}

function renderEarning(record: {platform: string; balance: number; currency: string; error?: string}) {
  return `
    <div class="row">
      <div>
        <strong>${escapeHtml(record.platform)}</strong>
        <small>${record.error ? escapeHtml(record.error) : "latest balance"}</small>
      </div>
      <span class="pill">${record.balance.toFixed(4)} ${escapeHtml(record.currency)}</span>
    </div>
  `;
}

function renderServiceDetail(service: Service, deployment?: Deployment) {
  const env = service.docker.env || [];
  return `
    <section class="panel service-detail">
      <div class="split">
        <div>
          <p class="eyebrow">${escapeHtml(service.category)} / ${service.manualOnly ? "manual tracking" : "docker managed"}</p>
          <h2>${escapeHtml(service.name)}</h2>
          <p class="muted">${escapeHtml(service.shortDescription || service.description)}</p>
        </div>
        <span class="pill">${deployment ? escapeHtml(deployment.status) : "not deployed"}</span>
      </div>
      ${service.manualOnly ? `<p class="tip">This service has no Docker image in the CashPilot catalog yet. Track it manually and use collectors where supported.</p>` : ""}
      <div class="credential-grid">
        ${env.map((item) => `
          <label>
            <span>${escapeHtml(item.label || item.key)}${item.required ? " *" : ""}</span>
            <input data-env="${item.key}" type="${item.secret ? "password" : "text"}" placeholder="${escapeHtml(stripHtml(item.description || item.key))}" value="${escapeHtml(item.default || "")}" />
          </label>
        `).join("")}
        ${service.slug === "earnfm" ? `
          <label><span>Earn.fm email for collector</span><input data-env="EARNFM_EMAIL" type="text" /></label>
          <label><span>Earn.fm password for collector</span><input data-env="EARNFM_PASSWORD" type="password" /></label>
        ` : ""}
      </div>
      <div class="actions left">
        <button class="secondary" id="save-creds">Save Credentials</button>
        <button class="primary" id="deploy" ${service.manualOnly ? "disabled" : ""}>Deploy</button>
        <button class="secondary" id="stop" ${deployment ? "" : "disabled"}>Stop</button>
        <button class="secondary" id="restart" ${deployment ? "" : "disabled"}>Restart</button>
        <button class="danger" id="remove" ${deployment ? "" : "disabled"}>Remove</button>
        <button class="secondary" id="logs" ${deployment ? "" : "disabled"}>Logs</button>
        <button class="secondary" id="collect">Collect Earnings</button>
      </div>
      <pre id="service-output" class="output"></pre>
    </section>
  `;
}

async function hydrateCredentialForm(service: Service | null) {
  if (!service) return;
  const creds = await GetCredentials(service.slug);
  document.querySelectorAll<HTMLInputElement>("[data-env]").forEach((input) => {
    const key = input.dataset.env || "";
    if (creds[key]) input.value = creds[key];
  });
}

async function saveCredentials() {
  if (!selectedService) return;
  await SaveCredentials(selectedService.slug, readCredentialForm());
  setOutput("Credentials saved.");
}

async function deploySelected() {
  if (!selectedService) return;
  setOutput("Deploying...");
  await DeployService(selectedService.slug, readCredentialForm());
  await refreshState();
}

async function actionSelected(action: "stop" | "restart" | "remove") {
  if (!selectedService) return;
  if (action === "stop") await StopService(selectedService.slug);
  if (action === "restart") await RestartService(selectedService.slug);
  if (action === "remove") await RemoveService(selectedService.slug);
  await refreshState();
}

async function showLogs() {
  if (!selectedService) return;
  setOutput(await GetLogs(selectedService.slug, 200));
}

async function collectSelected() {
  if (!selectedService) return;
  const record = await CollectService(selectedService.slug);
  setOutput(record.error ? record.error : `Collected ${record.balance} ${record.currency}`);
  await refreshState();
}

function readCredentialForm(): Record<string, string> {
  const values: Record<string, string> = {};
  document.querySelectorAll<HTMLInputElement>("[data-env]").forEach((input) => {
    values[input.dataset.env || ""] = input.value;
  });
  return values;
}

async function refreshState() {
  if (!state) return;
  const [runtime, deployments] = await Promise.all([CheckRuntime(), RefreshDeployments().catch(() => state?.deployments || [])]);
  state = {...await GetAppState(), runtime, deployments};
  render();
}

async function renderRuntimePlan() {
  const plan: RuntimePlan = await ManagedRuntimePlan();
  const node = document.querySelector("#runtime-plan");
  if (!node) return;
  node.innerHTML = `
    <p>${escapeHtml(plan.summary)}</p>
    <div class="guide-grid">${plan.phases.map((phase) => `<article class="guide"><p>${escapeHtml(phase)}</p></article>`).join("")}</div>
  `;
}

function setOutput(value: string) {
  const out = document.querySelector<HTMLPreElement>("#service-output");
  if (out) out.textContent = value;
}

function renderError(error: unknown) {
  root.innerHTML = `<main class="center"><section class="panel"><h1>CashPilot failed to start</h1><pre>${escapeHtml(String(error))}</pre></section></main>`;
}

function escapeHtml(value: string | undefined | null) {
  return String(value || "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;",
  }[ch] || ch));
}

function stripHtml(value: string) {
  return value.replace(/<[^>]+>/g, "");
}

function synthwaveBackground() {
  return `
    <svg class="synthwave-bg" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 800" preserveAspectRatio="xMidYMid slice">
      <defs>
        <linearGradient id="sky" x1="0%" y1="0%" x2="0%" y2="100%">
          <stop offset="0%" stop-color="#06060f"/><stop offset="35%" stop-color="#1a003a"/><stop offset="100%" stop-color="#06060f"/>
        </linearGradient>
        <linearGradient id="sun" x1="0%" y1="0%" x2="0%" y2="100%">
          <stop offset="0%" stop-color="#fde68a"/><stop offset="45%" stop-color="#fb923c"/><stop offset="75%" stop-color="#e11d48"/><stop offset="100%" stop-color="#6d28d9"/>
        </linearGradient>
        <clipPath id="sun-clip"><circle cx="600" cy="240" r="120"/></clipPath>
      </defs>
      <rect width="1200" height="800" fill="url(#sky)"/>
      <rect x="0" y="237" width="1200" height="3" fill="#fb7185" opacity="0.8"/>
      <circle cx="600" cy="240" r="120" fill="url(#sun)"/>
      <g clip-path="url(#sun-clip)">${[240,249,260,273,288,306,328].map((y, i) => `<rect x="475" y="${y}" width="250" height="${3 + i * 2}" fill="#06060f"/>`).join("")}</g>
      <g stroke="#8b5cf6" opacity="0.4">${[280,330,390,460,540,635,740].map((y) => `<line x1="0" y1="${y}" x2="1200" y2="${y}" stroke-width="0.6"/>`).join("")}</g>
      <g stroke="#fb7185" opacity="0.15">${[-50,150,300,450,600,750,900,1050,1250].map((x) => `<line x1="600" y1="242" x2="${x}" y2="800" stroke-width="0.6"/>`).join("")}</g>
    </svg>
  `;
}

void boot();
