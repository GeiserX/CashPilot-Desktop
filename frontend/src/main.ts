import "./style.css";
import {
  BrowserOpenURL,
  Quit,
  WindowMinimise,
  WindowToggleMaximise,
} from "../wailsjs/runtime/runtime";
import {
  AddFleetDevice,
  CheckRuntime,
  CollectService,
  CompleteOnboarding,
  DeployService,
  GetAppState,
  GetCredentials,
  GetFleetState,
  GetLogs,
  GetRuntimeGuides,
  GetSettingsState,
  ManagedRuntimePlan,
  RemoveFleetDevice,
  RefreshDeployments,
  RemoveService,
  RestartService,
  SaveSettings,
  SaveCredentials,
  StartService,
  StopService,
} from "../wailsjs/go/main/App";
import type { AppState, Deployment, FleetState, InstallGuide, ManagedRuntimePlan as RuntimePlan, Service, SettingsState } from "./wails";

let state: AppState | null = null;
let selectedService: Service | null = null;
let onboardingStep: "welcome" | "runtime" = "welcome";
type View = "dashboard" | "wizard" | "catalog" | "settings" | "fleet";

let activeView: View = "dashboard";
let wizardStep = 1;
let wizardCategories: string[] = [];
let wizardSelected: string[] = [];
let catalogFilter = "all";
let catalogSearch = "";
let resetScrollAfterRender = false;

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
  if (activeView === "wizard") {
    renderSetupWizard(state);
    return;
  }
  if (activeView === "catalog") {
    renderCatalog(state);
    return;
  }
  if (activeView === "settings") {
    void renderSettings(state);
    return;
  }
  if (activeView === "fleet") {
    void renderFleet(state);
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
        <h1>Put your idle machine to work</h1>
        <p class="subtitle">Share spare bandwidth, storage, CPU, RAM, or GPU with real networks, then track what each service earns from one place.</p>
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
  wireChrome();
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
        <p class="subtitle">Let your spare resources earn in the background while CashPilot handles setup, monitoring, and payouts from one place.</p>
        <button class="primary" id="get-started">Get Started</button>
      </section>
    </main>
  `;
  wireChrome();
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
      <button class="guide-link" data-url="${escapeHtml(guide.url)}">Open install guide</button>
      ${(guide.commands || []).map((cmd) => `<code>${escapeHtml(cmd)}</code>`).join("")}
      ${(guide.notes || []).map((note) => `<small>${escapeHtml(note)}</small>`).join("")}
    </article>
  `).join("");
  holder.querySelectorAll<HTMLButtonElement>("[data-url]").forEach((button) => {
    button.addEventListener("click", () => {
      const url = button.dataset.url;
      if (url) BrowserOpenURL(url);
    });
  });
}

function renderDashboard(current: AppState) {
  const services = current.services || [];
  const deployments = current.deployments || [];
  const earnings = current.earnings || [];
  const history = current.history || [];
  const runningCount = deployments.filter((dep) => dep.status === "running").length;
  const totalBalance = earnings.reduce((sum, record) => sum + (record.error ? 0 : record.balance), 0);
  const trackedCount = earnings.filter((record) => !record.error).length;

  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      ${appSidebar("dashboard")}
      <div class="main-content">
        ${topbar("Dashboard", totalBalance, current)}
        <main class="page-content">
        <section class="stats-grid">
          ${metricCard("Total Balance", formatBalance(totalBalance, "USD"), "Latest collected balance")}
          ${metricCard("Today", "$0.00", "Awaiting daily history")}
          ${metricCard("This Month", "$0.00", "Awaiting monthly history")}
          ${metricCard("Active Services", `${runningCount}`, "Containers currently running")}
        </section>

        <section class="card earnings-panel">
          <div class="card-header">
            <div>
              <span class="card-title">Earnings</span>
              <p class="muted compact-copy">What each service has earned so far.</p>
            </div>
            <div class="tab-strip">
              <button class="tab-btn active">7 days</button>
              <button class="tab-btn">30 days</button>
            </div>
          </div>
          ${renderEarningsChart(history, earnings)}
          <div class="earnings-breakdown">
            ${earnings.length ? earnings.map((record) => renderEarningBreakdown(record, services)).join("") : `<p class="muted">No earnings yet. Deploy a service, add credentials, then collect earnings.</p>`}
          </div>
        </section>

        <section class="card dashboard-panel">
          <div class="card-header">
            <span class="card-title">Deployed Services</span>
            <div class="header-actions">
              <button class="secondary compact-btn" id="refresh">Refresh</button>
              <button class="primary compact-btn" id="open-wizard">+ Add Service</button>
            </div>
          </div>
          <div class="services-table-wrap">
            ${renderServicesTable(services, deployments, earnings)}
          </div>
        </section>
        <pre id="service-output" class="output dashboard-output"></pre>
        </main>
      </div>
    </div>
  `;
  wireChrome();
  wireShellNav();
  maybeResetScroll();

  document.querySelector("#refresh")?.addEventListener("click", refreshState);
  document.querySelector("#refresh-services")?.addEventListener("click", refreshState);
  document.querySelector("#open-wizard")?.addEventListener("click", openWizard);
  document.querySelector("#open-wizard-empty")?.addEventListener("click", openWizard);
  document.querySelectorAll<HTMLButtonElement>("[data-row-action]").forEach((button) => {
    button.addEventListener("click", () => {
      const slug = button.dataset.slug || "";
      const action = button.dataset.rowAction || "";
      void runServiceAction(slug, action);
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-url]").forEach((button) => {
    button.addEventListener("click", () => {
      const url = button.dataset.url;
      if (url) BrowserOpenURL(url);
    });
  });
}

function metricCard(label: string, value: string, caption: string) {
  return `
    <article class="metric-card">
      <span>${escapeHtml(label)}</span>
      <strong>${escapeHtml(value)}</strong>
      <small>${escapeHtml(caption)}</small>
    </article>
  `;
}

function appSidebar(active: View) {
  return `
    <aside class="cp-sidebar">
      <div class="sidebar-brand">
        ${officialLogoMark()}
        CashPilot
      </div>
      <nav class="sidebar-nav">
        ${navButton("dashboard", "Dashboard", active)}
        ${navButton("wizard", "Setup Wizard", active)}
        ${navButton("catalog", "Service Catalog", active)}
        ${navButton("settings", "Settings", active)}
        ${navButton("fleet", "Fleet", active)}
      </nav>
      <div class="sidebar-footer">
        <div class="footer-links">
          <button class="footer-link" data-url="https://github.com/GeiserX/CashPilot" title="GitHub">GitHub</button>
          <button class="footer-link" data-url="https://github.com/sponsors/GeiserX" title="Sponsor">Sponsor</button>
        </div>
        <span>Desktop v0.5.0</span>
      </div>
    </aside>
  `;
}

function officialLogoMark() {
  return `
    <svg class="brand-logo" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" aria-hidden="true">
      <defs>
        <linearGradient id="brand-sun-gradient" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color="#FFD54F"/>
          <stop offset="35%" stop-color="#FF9800"/>
          <stop offset="65%" stop-color="#E91E63"/>
          <stop offset="100%" stop-color="#7B1FA2"/>
        </linearGradient>
        <clipPath id="brand-sun-clip"><circle cx="16" cy="16" r="11"/></clipPath>
      </defs>
      <circle cx="16" cy="16" r="11" fill="url(#brand-sun-gradient)"/>
      <g clip-path="url(#brand-sun-clip)">
        <rect x="4" y="16" width="24" height="1.2" fill="#0A0A1A" opacity="0.85"/>
        <rect x="4" y="18.5" width="24" height="1.5" fill="#0A0A1A" opacity="0.85"/>
        <rect x="4" y="21.5" width="24" height="2" fill="#0A0A1A" opacity="0.85"/>
        <rect x="4" y="25" width="24" height="3" fill="#0A0A1A" opacity="0.85"/>
        <g transform="translate(16,12) rotate(30) scale(0.4)">
          <path d="M0,-28 L2.5,-6 L30,2 L3,5 L4,12 L0,8 L-4,12 L-3,5 L-30,2 L-2.5,-6 Z" fill="#0A0A1A" opacity="0.65"/>
        </g>
      </g>
    </svg>
  `;
}

function navButton(view: View, label: string, active: string) {
  return `<button class="sidebar-link ${active === view ? "active" : ""}" data-view="${view}">${escapeHtml(label)}</button>`;
}

function topbar(title: string, totalBalance: number, current: AppState) {
  const notifications = current.notifications || [];
  return `
    <header class="topbar">
      <div class="topbar-left">
        <span class="topbar-title">${escapeHtml(title)}</span>
      </div>
      <div class="topbar-right">
        <span class="runtime-dot ${current.runtime.available ? "ok" : "warn"}"></span>
        <span class="topbar-runtime">${current.runtime.available ? "Runtime ready" : "Runtime offline"}</span>
        <span class="topbar-earnings">${formatBalance(totalBalance, current.config.displayCurrency || "USD")}</span>
        <select class="currency-select" id="currency-select" title="Display currency">
          ${(current.currencies || ["USD", "EUR"]).map((currency) => `<option value="${currency}" ${currency === current.config.displayCurrency ? "selected" : ""}>${currency}</option>`).join("")}
        </select>
        <details class="notification-menu">
          <summary aria-label="Notifications">Alerts <span class="notify-badge">${notifications.length}</span></summary>
          <div class="notification-popover">
            <strong>Notifications</strong>
            ${notifications.length ? notifications.map((item) => `
              <div class="notification-item ${escapeHtml(item.level)}">
                <span>${escapeHtml(item.title)}</span>
                <small>${escapeHtml(item.message)}</small>
              </div>
            `).join("") : `<p class="muted">No alerts right now.</p>`}
          </div>
        </details>
      </div>
    </header>
  `;
}

function wireShellNav() {
  document.querySelectorAll<HTMLButtonElement>("[data-view]").forEach((button) => {
    button.addEventListener("click", () => {
      const next = button.dataset.view;
      if (next === "dashboard" || next === "wizard" || next === "catalog" || next === "settings" || next === "fleet") {
        activeView = next;
        if (next === "wizard") {
          wizardStep = 1;
        }
        resetScrollAfterRender = true;
        render();
      }
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-url]").forEach((button) => {
    button.addEventListener("click", () => {
      const url = button.dataset.url;
      if (url) BrowserOpenURL(url);
    });
  });
  document.querySelector<HTMLSelectElement>("#currency-select")?.addEventListener("change", async (event) => {
    const currency = (event.target as HTMLSelectElement).value;
    await SaveSettings({displayCurrency: currency});
    state = await GetAppState();
    render();
  });
}

function renderCatalog(current: AppState) {
  const totalBalance = (current.earnings || []).reduce((sum, record) => sum + (record.error ? 0 : record.balance), 0);
  const services = (current.services || []).filter((service) => {
    const matchesCategory = catalogFilter === "all" || service.category === catalogFilter;
    const haystack = `${service.name} ${service.shortDescription} ${service.description}`.toLowerCase();
    return matchesCategory && haystack.includes(catalogSearch.toLowerCase());
  });
  const categories = ["all", "bandwidth", "depin", "storage", "compute"];
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      ${appSidebar("catalog")}
      <div class="main-content">
        ${topbar("Service Catalog", totalBalance, current)}
        <main class="page-content">
          <section class="card">
            <div class="card-header">
              <span class="card-title">Available Services</span>
              <input class="catalog-search" id="catalog-search" type="search" placeholder="Search services..." value="${escapeHtml(catalogSearch)}" />
            </div>
            <div class="filter-tabs">
              ${categories.map((category) => `<button class="filter-tab ${catalogFilter === category ? "active" : ""}" data-filter="${category}">${escapeHtml(capitalize(category))}</button>`).join("")}
            </div>
            <div class="catalog-grid">
              ${services.map((service) => renderCatalogCard(service, current.deployments || [])).join("")}
            </div>
          </section>
        </main>
      </div>
    </div>
  `;
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  document.querySelector<HTMLInputElement>("#catalog-search")?.addEventListener("input", (event) => {
    catalogSearch = (event.target as HTMLInputElement).value;
    render();
  });
  document.querySelectorAll<HTMLButtonElement>("[data-filter]").forEach((button) => {
    button.addEventListener("click", () => {
      catalogFilter = button.dataset.filter || "all";
      render();
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-service]").forEach((button) => {
    button.addEventListener("click", () => openWizard(button.dataset.service));
  });
  document.querySelectorAll<HTMLButtonElement>("[data-url]").forEach((button) => {
    button.addEventListener("click", () => {
      const url = button.dataset.url;
      if (url) BrowserOpenURL(url);
    });
  });
}

function renderCatalogCard(service: Service, deployments: Deployment[]) {
  const deployed = deployments.some((deployment) => deployment.slug === service.slug);
  const signupUrl = service.referral?.signupUrl || service.website;
  return `
    <article class="catalog-card">
      <div class="service-card-header">
        <div class="service-icon">${escapeHtml(service.name[0] || "?")}</div>
        <div>
          <strong>${escapeHtml(service.name)}</strong>
          <div class="badge-row">
            <span class="badge">${escapeHtml(service.category)}</span>
            <span class="badge ${deployed ? "success" : ""}">${deployed ? "Deployed" : service.manualOnly ? "Manual" : "Available"}</span>
          </div>
        </div>
      </div>
      <p>${escapeHtml(service.shortDescription || service.description)}</p>
      <div class="card-actions">
        ${service.manualOnly && signupUrl ? `<button class="primary compact-btn" data-url="${escapeHtml(signupUrl)}">Visit</button>` : `<button class="primary compact-btn" data-service="${service.slug}">${deployed ? "Manage" : "Deploy"}</button>`}
        ${signupUrl ? `<button class="secondary compact-btn" data-url="${escapeHtml(signupUrl)}">Sign Up</button>` : ""}
      </div>
    </article>
  `;
}

async function renderSettings(current: AppState) {
  const settings = await GetSettingsState();
  const total = totalBalance(current);
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      ${appSidebar("settings")}
      <div class="main-content">
        ${topbar("Settings", total, current)}
        <main class="page-content">
          <section class="card">
            <div class="card-header">
              <div>
                <span class="card-title">Environment Variables</span>
                <p class="muted compact-copy">Variables that affect this desktop node. Locked values are controlled by the app or OS.</p>
              </div>
              <button class="primary compact-btn" id="save-settings">Save Variables</button>
            </div>
            <div class="settings-list">
              ${settings.environment.map(renderEnvSetting).join("")}
            </div>
          </section>

          <section class="card">
            <div class="card-header">
              <div>
                <span class="card-title">Earnings Collection</span>
                <p class="muted compact-copy">Credentials for automated earnings tracking. Manual/mobile-first services can be tracked without deploying a container.</p>
              </div>
            </div>
            <div class="collector-grid">
              ${settings.collectors.map(renderCollectorSetting).join("")}
            </div>
          </section>
        </main>
      </div>
    </div>
  `;
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  document.querySelector("#save-settings")?.addEventListener("click", () => void saveSettingsFromForm());
  document.querySelectorAll<HTMLButtonElement>("[data-service]").forEach((button) => {
    button.addEventListener("click", () => openWizard(button.dataset.service));
  });
}

function renderEnvSetting(item: SettingsState["environment"][number]) {
  const editableKey = envInputName(item.key);
  return `
    <label class="setting-row">
      <span>
        <strong>${escapeHtml(item.label)}</strong>
        <small>${escapeHtml(item.key)} · ${escapeHtml(item.source)}</small>
      </span>
      <input data-setting="${editableKey}" value="${escapeHtml(item.value)}" ${item.readOnly ? "readonly" : ""} />
      <small>${escapeHtml(item.help)}</small>
    </label>
  `;
}

function renderCollectorSetting(item: SettingsState["collectors"][number]) {
  return `
    <button class="collector-row" data-service="${escapeHtml(item.slug)}">
      <span>${escapeHtml(item.name)}</span>
      <small>${escapeHtml(item.collector || "manual")}</small>
      <strong class="${item.configured ? "configured" : ""}">${item.configured ? "Configured" : "Not configured"}</strong>
    </button>
  `;
}

async function saveSettingsFromForm() {
  const values: Record<string, string> = {};
  document.querySelectorAll<HTMLInputElement>("[data-setting]").forEach((input) => {
    if (!input.readOnly) values[input.dataset.setting || ""] = input.value;
  });
  await SaveSettings(values);
  state = await GetAppState();
  render();
}

function envInputName(key: string) {
  const names: Record<string, string> = {
    CASHPILOT_HOSTNAME_PREFIX: "hostnamePrefix",
    CASHPILOT_COLLECT_INTERVAL: "collectIntervalMinutes",
    CASHPILOT_DISPLAY_CURRENCY: "displayCurrency",
    CASHPILOT_FLEET_BIND: "fleetBindAddress",
    CASHPILOT_FLEET_PORT: "fleetPort",
    TZ: "timezone",
  };
  return names[key] || key;
}

async function renderFleet(current: AppState) {
  const fleet = await GetFleetState();
  const total = totalBalance(current);
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      ${appSidebar("fleet")}
      <div class="main-content">
        ${topbar("Fleet Management", total, current)}
        <main class="page-content">
          <section class="stats-grid">
            ${metricCard("Workers", `${fleet.workers}`, "Desktop and server workers")}
            ${metricCard("Mobiles", `${fleet.mobiles}`, "Registered mobile devices")}
            ${metricCard("Online", `${fleet.online}`, "Devices currently reachable")}
            ${metricCard("Services", `${fleet.services}`, "Available providers")}
          </section>

          <section class="card">
            <div class="card-header">
              <span class="card-title">Add Worker or Mobile</span>
            </div>
            <p class="muted">Point CashPilot workers, mobile companions, or another machine on your LAN at this desktop API. The API listens for authenticated heartbeats and registers devices automatically.</p>
            <div class="connection-grid">
              ${detailStat("UI URL", fleet.uiUrl)}
              ${detailStat("Local API", fleet.localApiUrl)}
              ${detailStat("API Key", fleet.apiKey ? "Generated" : "Missing")}
              ${detailStat("Listener", fleet.apiListening ? "Listening" : "Offline")}
            </div>
            <div class="fleet-snippets">
              <div>
                <div class="snippet-header"><strong>Docker worker</strong><button class="secondary compact-btn" data-copy="${escapeHtml(fleet.workerSnippet)}">Copy</button></div>
                <code>${escapeHtml(fleet.workerSnippet)}</code>
              </div>
              <div>
                <div class="snippet-header"><strong>Mobile / companion</strong><button class="secondary compact-btn" data-copy="${escapeHtml(fleet.mobileSnippet)}">Copy</button></div>
                <code>${escapeHtml(fleet.mobileSnippet)}</code>
              </div>
            </div>
            <div class="fleet-form">
              <input id="fleet-name" placeholder="Device name" />
              <select id="fleet-kind">
                <option value="worker">Worker</option>
                <option value="mobile">Mobile</option>
              </select>
              <input id="fleet-endpoint" placeholder="Endpoint or device note" />
              <input id="fleet-services" placeholder="Services, comma-separated" />
              <button class="primary compact-btn" id="add-fleet-device">Register</button>
            </div>
          </section>

          <section class="card">
            <div class="card-header">
              <span class="card-title">Devices</span>
            </div>
            <div class="fleet-list">
              ${fleet.devices.map(renderFleetDevice).join("")}
            </div>
          </section>
        </main>
      </div>
    </div>
  `;
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  document.querySelector("#add-fleet-device")?.addEventListener("click", () => void addFleetDevice());
  document.querySelectorAll<HTMLButtonElement>("[data-remove-device]").forEach((button) => {
    button.addEventListener("click", () => void removeFleetDevice(Number(button.dataset.removeDevice || 0)));
  });
  document.querySelectorAll<HTMLButtonElement>("[data-copy]").forEach((button) => {
    button.addEventListener("click", async () => {
      await navigator.clipboard.writeText(button.dataset.copy || "");
      button.textContent = "Copied";
    });
  });
}

function renderFleetDevice(device: FleetState["devices"][number]) {
  return `
    <article class="fleet-device ${device.kind}">
      <div class="split">
        <div>
          <strong><span class="runtime-dot ${device.status === "online" ? "ok" : "warn"}"></span> ${escapeHtml(device.name)}</strong>
          <p class="muted compact-copy">${escapeHtml(device.endpoint || "No endpoint")} · ${escapeHtml(device.os || "unknown")} ${escapeHtml(device.arch || "")} · ${escapeHtml(device.lastSeen || "never seen")}</p>
        </div>
        <div class="device-actions">
          <span class="badge">${escapeHtml(device.kind)}</span>
          ${device.id > 0 ? `<button class="danger compact-btn" data-remove-device="${device.id}">Remove</button>` : ""}
        </div>
      </div>
      <div class="badge-row">${(device.services || []).map((service) => `<span class="badge success">${escapeHtml(service)}</span>`).join("") || `<span class="badge">No services yet</span>`}</div>
    </article>
  `;
}

async function addFleetDevice() {
  const values = {
    name: valueOf("#fleet-name"),
    kind: valueOf("#fleet-kind"),
    endpoint: valueOf("#fleet-endpoint"),
    services: valueOf("#fleet-services"),
  };
  await AddFleetDevice(values);
  render();
}

async function removeFleetDevice(id: number) {
  if (!confirm("Remove this fleet device from CashPilot Desktop?")) return;
  await RemoveFleetDevice(id);
  render();
}

function totalBalance(current: AppState) {
  return (current.earnings || []).reduce((sum, record) => sum + (record.error ? 0 : record.balance), 0);
}

function valueOf(selector: string) {
  return document.querySelector<HTMLInputElement | HTMLSelectElement>(selector)?.value || "";
}

function maybeResetScroll() {
  if (!resetScrollAfterRender) return;
  resetScrollAfterRender = false;
  requestAnimationFrame(() => window.scrollTo({top: 0, left: 0}));
}

function renderEarningsChart(history: {platform: string; balance: number; currency: string; error?: string; createdAt: string}[], latest: {platform: string; balance: number; currency: string; error?: string}[]) {
  const daily = new Map<string, number>();
  history.filter((record) => !record.error).forEach((record) => {
    const day = (record.createdAt || "").slice(5, 10) || "now";
    daily.set(day, (daily.get(day) || 0) + record.balance);
  });
  let points: Array<[string, number]> = [...daily.entries()].slice(-14);
  if (points.length === 0 && latest.length > 0) {
    points = latest.filter((record) => !record.error).map((record) => [record.platform, record.balance]);
  }
  if (points.length === 0) {
    return `
      <div class="chart-empty">
        <strong>No earnings collected yet</strong>
        <span>Once collectors run, this becomes the main view of what each service is earning.</span>
      </div>
    `;
  }
  const width = 720;
  const height = 240;
  const max = Math.max(...points.map(([, value]) => value), 1);
  const step = points.length > 1 ? width / (points.length - 1) : width;
  const coords = points.map(([, value], index) => {
    const x = points.length > 1 ? index * step : width / 2;
    const y = height - (value / max) * 180 - 30;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(" ");
  return `
    <div class="chart-shell">
      <svg class="earnings-chart" viewBox="0 0 ${width} ${height}" role="img" aria-label="Earnings chart">
        <defs>
          <linearGradient id="chart-fill" x1="0%" y1="0%" x2="0%" y2="100%">
            <stop offset="0%" stop-color="#fb7185" stop-opacity="0.32"/>
            <stop offset="100%" stop-color="#fb7185" stop-opacity="0"/>
          </linearGradient>
        </defs>
        ${[40, 80, 120, 160, 200].map((y) => `<line x1="0" y1="${y}" x2="${width}" y2="${y}" />`).join("")}
        <polyline points="${coords}" fill="none" stroke="#fb7185" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"/>
        <polygon points="0,${height} ${coords} ${width},${height}" fill="url(#chart-fill)"/>
        ${points.map(([label, value], index) => {
          const x = points.length > 1 ? index * step : width / 2;
          const y = height - (value / max) * 180 - 30;
          return `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="4" /><text x="${x.toFixed(1)}" y="232">${escapeHtml(label)}</text>`;
        }).join("")}
      </svg>
    </div>
  `;
}

function renderEarningBreakdown(record: {platform: string; balance: number; currency: string; error?: string}, services: Service[]) {
  const service = services.find((svc) => svc.slug === record.platform);
  return `
    <div class="earning-chip ${record.error ? "error" : ""}">
      <span>${escapeHtml(service?.name || record.platform)}</span>
      <strong>${record.error ? "Needs attention" : formatBalance(record.balance, record.currency)}</strong>
      <small>${record.error ? escapeHtml(record.error) : escapeHtml(record.currency)}</small>
    </div>
  `;
}

function renderServicesTable(services: Service[], deployments: Deployment[], earnings: {platform: string; balance: number; currency: string; error?: string}[]) {
  if (deployments.length === 0) {
    return `
      <div class="empty-state">
        <strong>No services deployed yet</strong>
        <span>Use the setup wizard to choose a provider, create an account, enter credentials, and deploy.</span>
        <button class="primary" id="open-wizard-empty">Setup Wizard</button>
      </div>
    `;
  }
  const earningBySlug = new Map(earnings.map((record) => [record.platform, record]));
  return `
    <table class="services-table">
      <thead>
        <tr>
          <th>Service</th>
          <th>Status</th>
          <th>Balance</th>
          <th>CPU</th>
          <th>Memory</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        ${deployments.map((deployment) => {
          const service = services.find((svc) => svc.slug === deployment.slug);
          const earning = earningBySlug.get(deployment.slug);
          return `
            <tr>
              <td>
                <strong>${escapeHtml(service?.name || deployment.slug)}</strong>
                <small>${escapeHtml(deployment.image)}</small>
              </td>
              <td><span class="status-pill ${deployment.status === "running" ? "ok" : "warn"}">${escapeHtml(deployment.status)}</span></td>
              <td>${earning && !earning.error ? formatBalance(earning.balance, earning.currency) : "<span class=\"muted\">--</span>"}</td>
              <td>${deployment.cpuPercent.toFixed(1)}%</td>
              <td>${deployment.memoryMb.toFixed(0)} MB</td>
              <td>
                <div class="table-actions">
                  <button class="secondary compact-btn" data-row-action="collect" data-slug="${deployment.slug}">Collect</button>
                  <button class="secondary compact-btn" data-row-action="logs" data-slug="${deployment.slug}">Logs</button>
                  ${deployment.status === "running"
                    ? `<button class="secondary compact-btn" data-row-action="stop" data-slug="${deployment.slug}">Stop</button>`
                    : `<button class="secondary compact-btn" data-row-action="start" data-slug="${deployment.slug}">Start</button>`}
                  <button class="danger compact-btn" data-row-action="remove" data-slug="${deployment.slug}">Remove</button>
                </div>
              </td>
            </tr>
          `;
        }).join("")}
      </tbody>
    </table>
  `;
}

function renderServiceCard(service: Service, deployed: boolean) {
  const signupUrl = service.referral?.signupUrl || service.website;
  return `
    <article class="service-card">
      <div>
        <strong>${escapeHtml(service.name)}</strong>
        <small>${escapeHtml(service.shortDescription || service.category)}</small>
      </div>
      <div class="service-card-actions">
        <span class="pill">${deployed ? "deployed" : service.manualOnly ? "manual" : "docker"}</span>
        ${signupUrl ? `<button class="text-link" data-url="${escapeHtml(signupUrl)}">Sign up</button>` : ""}
        <button class="secondary compact-btn" data-service="${service.slug}">Setup</button>
      </div>
    </article>
  `;
}

function renderCategoryCards(services: Service[]) {
  const counts = services.reduce<Record<string, {total: number; docker: number}>>((acc, service) => {
    const key = service.category || "other";
    acc[key] ||= {total: 0, docker: 0};
    acc[key].total += 1;
    if (!service.manualOnly) acc[key].docker += 1;
    return acc;
  }, {});
  return Object.entries(counts).map(([category, count]) => `
    <article class="category-card">
      <span>${escapeHtml(category)}</span>
      <strong>${count.total}</strong>
      <small>${count.docker} deployable</small>
    </article>
  `).join("");
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
  const envKeys = new Set(env.map((item) => item.key));
  const collectorFields = getCollectorFields(service.slug).filter((item) => !envKeys.has(item.key));
  const payment = [service.payment?.methods?.join(", "), service.payment?.minimumPayout ? `${service.payment.minimumPayout} min` : ""].filter(Boolean).join(" / ");
  const platforms = (service.platforms || []).join(", ");
  const requirements = [
    service.requirements?.residentialIp ? "Residential IP preferred" : "VPS/datacenter ok",
    service.requirements?.gpu ? "GPU required" : "",
    service.requirements?.minBandwidth ? `${service.requirements.minBandwidth} bandwidth` : "",
    service.requirements?.minStorage ? `${service.requirements.minStorage} storage` : "",
  ].filter(Boolean).join(" / ");
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
      <div class="provider-overview">
        ${detailStat("Category", service.category || "service")}
        ${detailStat("Estimate", `$${(service.earnings?.monthlyLow || 0).toFixed(0)}-${(service.earnings?.monthlyHigh || 0).toFixed(0)}/mo`)}
        ${detailStat("Payment", payment || "See provider")}
        ${detailStat("Collector", service.collector?.type || "manual")}
        ${detailStat("Runtime", service.manualOnly ? "External app" : service.docker.image)}
        ${detailStat("Requirements", requirements || "No special requirements")}
      </div>
      <p class="muted">${escapeHtml(stripHtml(service.description || service.shortDescription || ""))}</p>
      <div class="link-row">
        ${service.website ? `<button class="text-link" data-url="${escapeHtml(service.website)}">Open provider</button>` : ""}
        ${service.referral?.signupUrl ? `<button class="text-link" data-url="${escapeHtml(service.referral.signupUrl)}">Sign up</button>` : ""}
        ${service.cashout?.dashboardUrl ? `<button class="text-link" data-url="${escapeHtml(service.cashout.dashboardUrl)}">Dashboard / cashout</button>` : ""}
      </div>
      <div class="credential-grid">
        ${env.map((item) => `
          <label>
            <span>${escapeHtml(item.label || item.key)}${item.required ? " *" : ""}</span>
            <input data-env="${item.key}" type="${item.secret ? "password" : "text"}" placeholder="${escapeHtml(stripHtml(item.description || item.key))}" value="${escapeHtml(item.default || "")}" />
          </label>
        `).join("")}
        ${collectorFields.map((item) => `
          <label>
            <span>${escapeHtml(item.label)}${item.required ? " *" : ""}</span>
            <input data-env="${item.key}" type="${item.secret ? "password" : "text"}" placeholder="${escapeHtml(item.description)}" />
          </label>
        `).join("")}
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

function detailStat(label: string, value: string) {
  return `
    <div class="detail-stat">
      <span>${escapeHtml(label)}</span>
      <strong>${escapeHtml(value)}</strong>
    </div>
  `;
}

function openWizard(input?: string | Event) {
  const slug = typeof input === "string" ? input : "";
  activeView = "wizard";
  wizardStep = slug ? 3 : 1;
  wizardSelected = slug ? [slug] : [];
  const service = state?.services.find((svc) => svc.slug === slug);
  wizardCategories = service ? [service.category] : [];
  render();
}

function renderSetupWizard(current: AppState) {
  const services = current.services || [];
  const categories = ["bandwidth", "depin", "storage", "compute"].filter((category) => services.some((svc) => svc.category === category));
  if (wizardCategories.length === 0 && categories.length > 0) {
    wizardCategories = [categories[0]];
  }
  const filtered = services.filter((svc) => wizardCategories.includes(svc.category));
  const selectedServices = services.filter((svc) => wizardSelected.includes(svc.slug));
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      ${appSidebar("wizard")}
      <div class="main-content">
        ${topbar("Setup Wizard", (current.earnings || []).reduce((sum, record) => sum + (record.error ? 0 : record.balance), 0), current)}
      <main class="page-content setup-content">
        <div class="split">
          <div>
            <p class="eyebrow">Setup Wizard</p>
            <h1>Add earning services</h1>
            <p class="muted">Choose what to share, sign up through CashPilot, then deploy or track each provider.</p>
          </div>
          <button class="secondary" id="close-wizard">Back to Dashboard</button>
        </div>
        ${renderWizardProgress()}
        <section class="panel wizard-panel active">
          ${wizardStep === 1 ? renderWizardCategories(categories) : ""}
          ${wizardStep === 2 ? renderWizardServices(filtered) : ""}
          ${wizardStep === 3 ? renderWizardSetup(selectedServices) : ""}
          ${wizardStep === 4 ? renderWizardSummary(selectedServices) : ""}
        </section>
        <div class="wizard-footer">
          <button class="secondary" id="wizard-prev" ${wizardStep === 1 ? "disabled" : ""}>Back</button>
          <button class="primary" id="wizard-next">${wizardStep === 4 ? "Go to Dashboard" : wizardStep === 3 ? "Summary" : "Next"}</button>
        </div>
      </main>
      </div>
    </div>
  `;
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  document.querySelector("#close-wizard")?.addEventListener("click", closeWizard);
  document.querySelector("#wizard-prev")?.addEventListener("click", () => {
    if (wizardStep > 1) {
      wizardStep -= 1;
      render();
    }
  });
  document.querySelector("#wizard-next")?.addEventListener("click", () => {
    if (wizardStep === 1 && wizardCategories.length === 0) return;
    if (wizardStep === 2 && wizardSelected.length === 0) return;
    if (wizardStep === 4) {
      closeWizard();
      return;
    }
    wizardStep += 1;
    render();
  });
  document.querySelectorAll<HTMLButtonElement>("[data-category]").forEach((button) => {
    button.addEventListener("click", () => {
      const category = button.dataset.category || "";
      wizardCategories = wizardCategories.includes(category)
        ? wizardCategories.filter((item) => item !== category)
        : [...wizardCategories, category];
      wizardSelected = wizardSelected.filter((slug) => {
        const service = services.find((svc) => svc.slug === slug);
        return service ? wizardCategories.includes(service.category) : false;
      });
      render();
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-wizard-service]").forEach((button) => {
    button.addEventListener("click", () => {
      const slug = button.dataset.wizardService || "";
      wizardSelected = wizardSelected.includes(slug)
        ? wizardSelected.filter((item) => item !== slug)
        : [...wizardSelected, slug];
      render();
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-wizard-action]").forEach((button) => {
    button.addEventListener("click", () => {
      const slug = button.dataset.slug || "";
      const action = button.dataset.wizardAction || "";
      void runWizardAction(slug, action);
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-url]").forEach((button) => {
    button.addEventListener("click", () => {
      const url = button.dataset.url;
      if (url) BrowserOpenURL(url);
    });
  });
  selectedServices.forEach((service) => void hydrateWizardForm(service));
}

function renderWizardProgress() {
  const labels = ["Categories", "Services", "Setup", "Summary"];
  return `
    <div class="wizard-progress">
      ${labels.map((label, index) => {
        const step = index + 1;
        return `
          <div class="wizard-step ${wizardStep === step ? "active" : step < wizardStep ? "completed" : ""}">
            <span class="wizard-step-number">${step}</span>
            <span class="wizard-step-label">${label}</span>
          </div>
          ${step < labels.length ? `<div class="wizard-step-connector ${step < wizardStep ? "completed" : ""}"></div>` : ""}
        `;
      }).join("")}
    </div>
  `;
}

function renderWizardCategories(categories: string[]) {
  const copy: Record<string, string> = {
    bandwidth: "Share your internet connection",
    depin: "Run decentralized infrastructure",
    storage: "Rent out disk space",
    compute: "Share CPU or GPU power",
  };
  return `
    <h2>What would you like to share?</h2>
    <p class="muted">Pick one or more categories and CashPilot will show matching services.</p>
    <div class="wizard-category-grid">
      ${categories.map((category) => `
        <button class="wizard-category ${wizardCategories.includes(category) ? "selected" : ""}" data-category="${category}">
          <strong>${escapeHtml(capitalize(category))}</strong>
          <span>${escapeHtml(copy[category] || "Available providers")}</span>
        </button>
      `).join("")}
    </div>
  `;
}

function renderWizardServices(services: Service[]) {
  return `
    <h2>Select services</h2>
    <p class="muted">Choose providers to set up. Earnings vary by location, hardware, uptime, and demand, so CashPilot does not promise a fixed amount.</p>
    <div class="wizard-service-grid">
      ${services.map((service) => `
        <button class="wizard-service ${wizardSelected.includes(service.slug) ? "selected" : ""}" data-wizard-service="${service.slug}">
          <div class="service-icon">${escapeHtml(service.name[0] || "?")}</div>
          <div>
            <strong>${escapeHtml(service.name)}</strong>
            <span>${escapeHtml(service.shortDescription || service.category)}</span>
            <small>${service.manualOnly ? "External app / tracking" : "Docker managed"}${service.requirements?.residentialIp ? " / Residential IP" : ""}</small>
          </div>
        </button>
      `).join("") || `<p class="muted">No services match these categories.</p>`}
    </div>
  `;
}

function renderWizardSetup(services: Service[]) {
  if (services.length === 0) {
    return `<p class="muted">Go back and select at least one service.</p>`;
  }
  return `
    <h2>Configure and deploy</h2>
    <p class="muted">Create an account first if needed, then enter the credentials CashPilot needs to deploy or collect earnings.</p>
    <div class="wizard-setup-list">
      ${services.map(renderWizardServiceSetup).join("")}
    </div>
  `;
}

function renderWizardServiceSetup(service: Service) {
  const signupUrl = service.referral?.signupUrl || service.website;
  const dashboardUrl = service.cashout?.dashboardUrl || service.website;
  const fields = getServiceFields(service);
  return `
    <article class="wizard-setup-card" data-form-slug="${service.slug}">
      <div class="split">
        <div>
          <h3>${escapeHtml(service.name)}</h3>
          <p class="muted">${escapeHtml(service.shortDescription || service.description)}</p>
        </div>
        <span class="pill">${service.manualOnly ? "manual" : "docker"}</span>
      </div>
      <div class="signup-strip">
        ${signupUrl ? `<button class="primary" data-url="${escapeHtml(signupUrl)}">Create account</button>` : ""}
        ${dashboardUrl ? `<button class="secondary" data-url="${escapeHtml(dashboardUrl)}">Provider dashboard</button>` : ""}
        <button class="secondary" data-url="https://geiserx.github.io/CashPilot/guides/${escapeHtml(service.slug)}/">Setup guide</button>
      </div>
      ${service.manualOnly ? `<p class="tip">Install this provider's native app, then save collector credentials here so CashPilot can track earnings.</p>` : ""}
      <div class="credential-grid">
        ${fields.map((item) => `
          <label>
            <span>${escapeHtml(item.label)}${item.required ? " *" : ""}</span>
            <input data-wizard-env="${item.key}" type="${item.secret ? "password" : "text"}" placeholder="${escapeHtml(item.description)}" value="${escapeHtml(item.default || "")}" />
          </label>
        `).join("") || `<p class="muted">No credentials are required by the catalog for this service.</p>`}
      </div>
      <div class="actions left">
        <button class="secondary" data-wizard-action="save" data-slug="${service.slug}">Save Credentials</button>
        <button class="primary" data-wizard-action="deploy" data-slug="${service.slug}" ${service.manualOnly ? "disabled" : ""}>Deploy</button>
        <button class="secondary" data-wizard-action="collect" data-slug="${service.slug}">Collect Earnings</button>
      </div>
      <pre class="output wizard-output" data-output-slug="${service.slug}"></pre>
    </article>
  `;
}

function renderWizardSummary(services: Service[]) {
  return `
    <div class="wizard-summary">
      <h2>You're all set</h2>
      <p class="muted">Return to the dashboard to monitor balances, status, logs, and payout progress.</p>
      <div class="earnings-breakdown">
        ${services.map((service) => `<div class="earning-chip"><span>${escapeHtml(service.name)}</span><strong>${service.manualOnly ? "Tracking" : "Ready"}</strong><small>${escapeHtml(service.category)}</small></div>`).join("")}
      </div>
    </div>
  `;
}

function closeWizard() {
  activeView = "dashboard";
  wizardStep = 1;
  render();
}

type ServiceField = CollectorField & {default?: string};

function getServiceFields(service: Service): ServiceField[] {
  const env = (service.docker.env || []).map((item) => ({
    key: item.key,
    label: item.label || item.key,
    description: stripHtml(item.description || item.key),
    secret: item.secret,
    required: item.required,
    default: item.default,
  }));
  const envKeys = new Set(env.map((item) => item.key));
  return [
    ...env,
    ...getCollectorFields(service.slug).filter((item) => !envKeys.has(item.key)),
  ];
}

async function hydrateWizardForm(service: Service) {
  const creds = await GetCredentials(service.slug);
  document.querySelectorAll<HTMLInputElement>(`[data-form-slug="${service.slug}"] [data-wizard-env]`).forEach((input) => {
    const key = input.dataset.wizardEnv || "";
    if (creds[key]) input.value = creds[key];
  });
}

function readWizardForm(slug: string): Record<string, string> {
  const values: Record<string, string> = {};
  document.querySelectorAll<HTMLInputElement>(`[data-form-slug="${slug}"] [data-wizard-env]`).forEach((input) => {
    values[input.dataset.wizardEnv || ""] = input.value;
  });
  return values;
}

async function runWizardAction(slug: string, action: string) {
  const output = document.querySelector<HTMLPreElement>(`[data-output-slug="${slug}"]`);
  const values = readWizardForm(slug);
  if (action === "save") {
    await SaveCredentials(slug, values);
    if (output) output.textContent = "Credentials saved.";
    return;
  }
  if (action === "deploy") {
    if (state?.deployments?.some((deployment) => deployment.slug === slug) && !confirm(`Redeploy ${slug}? The existing container will be replaced, but its volumes are kept.`)) {
      return;
    }
    if (output) output.textContent = "Deploying...";
    await SaveCredentials(slug, values);
    await DeployService(slug, values);
    state = await GetAppState();
    if (output) output.textContent = "Deployed.";
    return;
  }
  if (action === "collect") {
    await SaveCredentials(slug, values);
    const record = await CollectService(slug);
    if (output) output.textContent = record.error ? record.error : `Collected ${formatBalance(record.balance, record.currency)}`;
    state = await GetAppState();
  }
}

async function runServiceAction(slug: string, action: string) {
  selectedService = state?.services.find((svc) => svc.slug === slug) || null;
  try {
    if (action === "remove" && !confirm(`Remove ${selectedService?.name || slug}? This deletes the managed container and its Docker volumes. Host bind-mount folders are left untouched.`)) {
      return;
    }
    if (action === "collect") {
      const record = await CollectService(slug);
      await refreshState();
      setOutput(record.error ? record.error : `Collected ${formatBalance(record.balance, record.currency)}`);
      return;
    }
    if (action === "logs") {
      setOutput(await GetLogs(slug, 200));
      return;
    }
    if (action === "stop") {
      await StopService(slug);
      setOutput(`${slug} stopped.`);
    }
    if (action === "start") {
      await StartService(slug);
      setOutput(`${slug} started.`);
    }
    if (action === "remove") {
      await RemoveService(slug);
      setOutput(`${slug} removed.`);
    }
    await refreshState();
    setOutput(`${slug} ${action} complete.`);
  } catch (error) {
    setOutput(String(error));
  }
}

type CollectorField = {
  key: string;
  label: string;
  description: string;
  secret?: boolean;
  required?: boolean;
};

function getCollectorFields(slug: string): CollectorField[] {
  const fields: Record<string, CollectorField[]> = {
    "anyone-protocol": [
      {key: "ANYONE_FINGERPRINTS", label: "Relay fingerprints", description: "Comma-separated relay fingerprints", required: true},
    ],
    bitping: [
      {key: "BITPING_EMAIL", label: "Bitping email", description: "Email used for app.bitping.com", required: true},
      {key: "BITPING_PASSWORD", label: "Bitping password", description: "Password used for app.bitping.com", secret: true, required: true},
    ],
    bytelixir: [
      {key: "BYTELIXIR_SESSION", label: "Bytelixir session", description: "bytelixir_session browser cookie", secret: true, required: true},
      {key: "BYTELIXIR_REMEMBER_WEB", label: "Remember cookie", description: "Optional remember_web cookie", secret: true},
      {key: "BYTELIXIR_XSRF_TOKEN", label: "XSRF token", description: "Optional XSRF-TOKEN cookie", secret: true},
    ],
    earnapp: [
      {key: "EARNAPP_OAUTH_TOKEN", label: "OAuth refresh token", description: "oauth-refresh-token browser cookie", secret: true, required: true},
      {key: "EARNAPP_BRD_SESS_ID", label: "Bright Data session", description: "Optional brd_sess_id cookie", secret: true},
    ],
    earnfm: [
      {key: "EARNFM_EMAIL", label: "Earn.fm email", description: "Email used for app.earn.fm", required: true},
      {key: "EARNFM_PASSWORD", label: "Earn.fm password", description: "Password used for app.earn.fm", secret: true, required: true},
    ],
    grass: [
      {key: "GRASS_ACCESS_TOKEN", label: "Grass access token", description: "accessToken from app.grass.io local storage", secret: true, required: true},
    ],
    mysterium: [
      {key: "MYSTNODES_EMAIL", label: "MystNodes email", description: "Email used for my.mystnodes.com", required: true},
      {key: "MYSTNODES_PASSWORD", label: "MystNodes password", description: "Password used for my.mystnodes.com", secret: true, required: true},
    ],
    packetstream: [
      {key: "PACKETSTREAM_AUTH_TOKEN", label: "PacketStream auth cookie", description: "auth cookie from app.packetstream.io", secret: true, required: true},
    ],
    salad: [
      {key: "SALAD_AUTH_COOKIE", label: "Salad auth cookie", description: "auth cookie from salad.com", secret: true, required: true},
    ],
    storj: [
      {key: "STORJ_API_URL", label: "Storj API URL", description: "Local node dashboard URL, default http://localhost:14002"},
    ],
  };
  return fields[slug] || [];
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
  wireChrome();
}

function titlebar() {
  return `
    <div class="titlebar">
      <div class="traffic-lights">
        <button class="traffic close" data-window-action="close" aria-label="Close"></button>
        <button class="traffic minimise" data-window-action="minimise" aria-label="Minimise"></button>
        <button class="traffic maximise" data-window-action="maximise" aria-label="Maximise"></button>
      </div>
      <span>CashPilot Desktop</span>
    </div>
  `;
}

function wireChrome() {
  document.querySelectorAll<HTMLButtonElement>("[data-window-action]").forEach((button) => {
    button.addEventListener("click", () => {
      const action = button.dataset.windowAction;
      if (action === "close") Quit();
      if (action === "minimise") WindowMinimise();
      if (action === "maximise") WindowToggleMaximise();
    });
  });
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

function capitalize(value: string) {
  return value ? value.charAt(0).toUpperCase() + value.slice(1) : "";
}

function formatBalance(value: number, currency: string) {
  const symbols: Record<string, string> = {USD: "$", EUR: "€", GBP: "£", JPY: "¥", CAD: "C$", AUD: "A$", BRL: "R$"};
  if (symbols[currency]) {
    return `${symbols[currency]}${value.toFixed(2)}`;
  }
  return `${value.toFixed(2)} ${currency}`;
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
