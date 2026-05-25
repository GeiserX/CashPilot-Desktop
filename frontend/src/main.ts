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
let onboardingStep: "welcome" | "runtime" = "welcome";

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
  if (onboardingStep === "welcome") {
    renderWelcome();
    return;
  }
  const runtime = current.runtime;
  root.innerHTML = `
    ${titlebar()}
    ${synthwaveBackground()}
    <main class="onboarding">
      <section class="onboarding-card">
        <p class="eyebrow">CashPilot Desktop</p>
        <h1>Passive income for you</h1>
        <p class="subtitle">Share spare bandwidth, CPU, RAM, storage, or GPU with projects that need it. CashPilot helps you install, run, and monitor the services from one place.</p>
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

function renderWelcome() {
  root.innerHTML = `
    ${titlebar()}
    ${synthwaveBackground()}
    <main class="welcome-screen">
      <section class="welcome-copy">
        <p class="eyebrow">CashPilot Desktop</p>
        <h1>Welcome to CashPilot</h1>
        <p class="subtitle">Turn spare machine resources into side income while supporting bandwidth, storage, compute, and DePIN networks.</p>
        <button class="primary" id="get-started">Get Started</button>
      </section>
    </main>
  `;
  document.querySelector("#get-started")?.addEventListener("click", () => {
    onboardingStep = "runtime";
    render();
  });
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
    ${titlebar()}
    <div class="shell">
      <aside class="sidebar">
        <div>
          <p class="eyebrow">CashPilot Desktop</p>
          <h1>Local Service Manager</h1>
          <p class="muted">Deploy earning services, monitor containers, and track balances from this machine.</p>
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
  root.innerHTML = `${titlebar()}<main class="center"><section class="panel"><h1>CashPilot failed to start</h1><pre>${escapeHtml(String(error))}</pre></section></main>`;
}

function titlebar() {
  return `<div class="titlebar"><span>CashPilot Desktop</span></div>`;
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
          <stop offset="0%" stop-color="#06060f"/><stop offset="25%" stop-color="#0d0022"/><stop offset="40%" stop-color="#1a003a"/><stop offset="50%" stop-color="#2d0055"/><stop offset="100%" stop-color="#06060f"/>
        </linearGradient>
        <linearGradient id="sun" x1="0%" y1="0%" x2="0%" y2="100%">
          <stop offset="0%" stop-color="#fde68a"/><stop offset="25%" stop-color="#fbbf24"/><stop offset="45%" stop-color="#fb923c"/><stop offset="65%" stop-color="#e11d48"/><stop offset="85%" stop-color="#9f1239"/><stop offset="100%" stop-color="#6d28d9"/>
        </linearGradient>
        <radialGradient id="halo" cx="50%" cy="50%">
          <stop offset="35%" stop-color="#fb923c" stop-opacity="0.2"/>
          <stop offset="65%" stop-color="#e11d48" stop-opacity="0.08"/>
          <stop offset="100%" stop-color="transparent"/>
        </radialGradient>
        <linearGradient id="horizon" x1="0%" y1="0%" x2="100%" y2="0%">
          <stop offset="0%" stop-color="#fb7185" stop-opacity="0.65"/>
          <stop offset="38%" stop-color="#fb923c" stop-opacity="0.88"/>
          <stop offset="50%" stop-color="#fb923c" stop-opacity="0.95"/>
          <stop offset="62%" stop-color="#fb923c" stop-opacity="0.88"/>
          <stop offset="100%" stop-color="#fb7185" stop-opacity="0.65"/>
        </linearGradient>
        <linearGradient id="reflect" x1="0%" y1="0%" x2="0%" y2="100%">
          <stop offset="0%" stop-color="#fb923c" stop-opacity="0.12"/>
          <stop offset="100%" stop-color="transparent"/>
        </linearGradient>
        <filter id="star-glow"><feGaussianBlur stdDeviation="1.2" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>
        <clipPath id="sun-clip"><circle cx="600" cy="280" r="130"/></clipPath>
      </defs>
      <rect width="1200" height="800" fill="url(#sky)"/>
      <g fill="#fff" filter="url(#star-glow)">
        ${[
          [80,30,1.2,.8],[220,70,.7,.5],[370,20,1,.7],[520,55,.8,.4],[680,35,1.1,.6],[840,60,.6,.4],[980,25,1.2,.7],[1120,50,.9,.6],
          [120,115,.7,.35],[310,125,.8,.3],[455,100,.6,.35],[760,112,.8,.32],[1030,118,.7,.3],[1160,100,.6,.35],
          [55,175,.6,.25],[175,200,.9,.32],[1060,190,.7,.25]
        ].map(([cx, cy, r, opacity]) => `<circle cx="${cx}" cy="${cy}" r="${r}" opacity="${opacity}"/>`).join("")}
      </g>
      <rect x="0" y="0" width="1200" height="2" fill="#fb7185" opacity="0.5"/>
      <rect x="0" y="798" width="1200" height="2" fill="#22d3ee" opacity="0.3"/>
      <rect x="0" y="277" width="1200" height="3" fill="url(#horizon)"/>
      <rect x="0" y="276" width="1200" height="1" fill="#fde68a" opacity="0.26"/>
      <circle cx="600" cy="280" r="200" fill="url(#halo)"/>
      <circle cx="600" cy="280" r="130" fill="url(#sun)"/>
      <g clip-path="url(#sun-clip)">
        ${[280,290,302,316,333,354,380].map((y, i) => `<rect x="465" y="${y}" width="270" height="${[4,6,8,10,13,17,22][i]}" fill="#06060f"/>`).join("")}
        <g transform="translate(600,235) rotate(27)">
          <path d="M0,-30 L3,-6 L32,2.5 L3,5.5 L4.5,13 L0,9 L-4.5,13 L-3,5.5 L-32,2.5 L-3,-6 Z" fill="#06060f" opacity="0.7"/>
        </g>
      </g>
      <ellipse cx="600" cy="320" rx="90" ry="25" fill="url(#reflect)"/>
      <g stroke="#8b5cf6" opacity="0.4">${[320,365,420,485,560,650,750].map((y, i) => `<line x1="0" y1="${y}" x2="1200" y2="${y}" stroke-width="${i < 2 ? 0.7 : 0.5}"/>`).join("")}</g>
      <g stroke="#fb7185" opacity="0.15">${[-50,150,300,450,600,750,900,1050,1250].map((x) => `<line x1="600" y1="282" x2="${x}" y2="800" stroke-width="0.6"/>`).join("")}</g>
    </svg>
  `;
}

void boot();
