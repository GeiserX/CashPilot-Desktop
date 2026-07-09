import "./style.css";
import {
  BrowserOpenURL,
  EventsOn,
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
  RemoveFleetDevice,
  RefreshDeployments,
  RemoveService,
  SaveSettings,
  SaveCredentials,
  StartService,
  StopService,
} from "../wailsjs/go/main/App";
import type { AppState, DailyPoint, Deployment, FleetState, HealthScore, InstallGuide, MystNode, PointsBalance, Service, ServiceEarning, SettingsState } from "./wails";

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
    wireBackendEvents();
  } catch (error) {
    renderError(error);
  }
}

let eventRefreshInFlight = false;

// The Go backend emits these after background collection cycles and deployment
// changes. Refresh state so passive earnings appear without the user navigating.
function wireBackendEvents() {
  EventsOn("earnings:changed", () => void onBackendEvent());
  EventsOn("deployment:changed", () => void onBackendEvent());
  // Background/startup failures the Go side reports via app:error had no listener,
  // so ~every background error was silently dropped. Surface them to the user.
  EventsOn("app:error", (payload) => showErrorToast(payload));
  EventsOn("app:notice", (payload) => showInfoToast(payload));
}

async function onBackendEvent() {
  if (eventRefreshInFlight) return;
  if (!state || !state.config.firstRunComplete) return;
  eventRefreshInFlight = true;
  try {
    state = await GetAppState();
    // Only re-render on the dashboard; other views own an in-progress form, so
    // update state silently and let them pick it up on their next natural render.
    if (activeView === "dashboard") render();
  } catch {
    // transient background refresh failure — ignore; next event/read recovers
  } finally {
    eventRefreshInFlight = false;
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
  const summary = current.summary;
  const disp = summary?.displayCurrency || current.config.displayCurrency || "USD";
  const runningCount = deployments.filter((dep) => dep.status === "running").length;
  const total = summary?.total ?? 0;
  const daily = summary?.daily || [];
  const breakdown = summary?.breakdown || [];
  const points = summary?.points || [];

  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      ${appSidebar("dashboard")}
      <div class="main-content">
        ${topbar("Dashboard", total, current)}
        <main class="page-content">
        <section class="stats-grid">
          ${metricCard("Total Balance", formatBalance(total, disp), summary?.ratesStale ? "Rates may be stale" : "Across convertible services")}
          ${metricCard("Today", formatBalance(summary?.today ?? 0, disp), changeCaption(summary?.todayChange ?? 0, "vs yesterday"))}
          ${metricCard("This Month", formatBalance(summary?.month ?? 0, disp), summary?.monthChange ? changeCaption(summary.monthChange, "vs last month") : "So far this month")}
          ${metricCard("Active Services", `${runningCount}`, "Containers currently running")}
        </section>

        <section class="card earnings-panel">
          <div class="card-header">
            <div>
              <span class="card-title">Earnings</span>
              <p class="muted compact-copy">Daily earnings in ${escapeHtml(disp)}.${summary?.ratesStale ? ` <span class="badge warn">rates stale</span>` : ""}</p>
            </div>
            <div class="tab-strip">
              <button class="tab-btn active">30 days</button>
            </div>
          </div>
          ${renderEarningsChart(daily, disp)}
          <div class="earnings-breakdown">
            ${breakdown.length ? breakdown.map((item) => renderEarningBreakdown(item, disp)).join("") : `<p class="muted">No earnings yet. Deploy a service, add credentials, then collect earnings.</p>`}
          </div>
        </section>

        ${points.length ? renderPointsSection(points) : ""}

        <section class="card dashboard-panel">
          <div class="card-header">
            <span class="card-title">Deployed Services</span>
            <div class="header-actions">
              <button class="secondary compact-btn" id="refresh">Refresh</button>
              <button class="primary compact-btn" id="open-wizard">+ Add Service</button>
            </div>
          </div>
          <div class="services-table-wrap">
            ${renderServicesTable(services, deployments, earnings, current.health, current.serviceDetails)}
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
          <button class="footer-link" data-url="https://github.com/GeiserX/CashPilot-Desktop" title="GitHub">GitHub</button>
          <button class="footer-link" data-url="https://github.com/sponsors/GeiserX" title="Sponsor">Sponsor</button>
        </div>
        <span>Desktop v${__APP_VERSION__}</span>
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
  const total = totalBalance(current);
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
        ${topbar("Service Catalog", total, current)}
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
  try {
    await SaveSettings(values);
    state = await GetAppState();
    render();
  } catch (error) {
    showErrorToast({scope: "settings", error: String(error)});
  }
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
  try {
    await AddFleetDevice(values);
    render();
  } catch (error) {
    showErrorToast({scope: "fleet", error: String(error)});
  }
}

async function removeFleetDevice(id: number) {
  if (!confirm("Remove this fleet device from CashPilot Desktop?")) return;
  await RemoveFleetDevice(id);
  render();
}

function totalBalance(current: AppState) {
  return current.summary?.total ?? 0;
}

function valueOf(selector: string) {
  return document.querySelector<HTMLInputElement | HTMLSelectElement>(selector)?.value || "";
}

function maybeResetScroll() {
  if (!resetScrollAfterRender) return;
  resetScrollAfterRender = false;
  requestAnimationFrame(() => window.scrollTo({top: 0, left: 0}));
}

function renderEarningsChart(points: DailyPoint[], displayCurrency: string) {
  const data = (points || []).filter((point) => Number.isFinite(point.amount));
  if (data.length === 0 || !data.some((point) => point.amount > 0)) {
    return `
      <div class="chart-empty">
        <strong>No earnings collected yet</strong>
        <span>Once collectors run, daily earnings in ${escapeHtml(displayCurrency)} appear here.</span>
      </div>
    `;
  }
  const width = 720;
  const height = 240;
  const max = Math.max(...data.map((point) => point.amount), 1);
  const step = data.length > 1 ? width / (data.length - 1) : width;
  const labelEvery = Math.max(1, Math.ceil(data.length / 6));
  const coords = data.map((point, index) => {
    const x = data.length > 1 ? index * step : width / 2;
    const y = height - (point.amount / max) * 180 - 30;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(" ");
  return `
    <div class="chart-shell">
      <svg class="earnings-chart" viewBox="0 0 ${width} ${height}" role="img" aria-label="Daily earnings chart">
        <defs>
          <linearGradient id="chart-fill" x1="0%" y1="0%" x2="0%" y2="100%">
            <stop offset="0%" stop-color="#fb7185" stop-opacity="0.32"/>
            <stop offset="100%" stop-color="#fb7185" stop-opacity="0"/>
          </linearGradient>
        </defs>
        ${[40, 80, 120, 160, 200].map((y) => `<line x1="0" y1="${y}" x2="${width}" y2="${y}" />`).join("")}
        <polyline points="${coords}" fill="none" stroke="#fb7185" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"/>
        <polygon points="0,${height} ${coords} ${width},${height}" fill="url(#chart-fill)"/>
        ${data.map((point, index) => {
          const x = data.length > 1 ? index * step : width / 2;
          const y = height - (point.amount / max) * 180 - 30;
          const label = index % labelEvery === 0 ? `<text x="${x.toFixed(1)}" y="232">${escapeHtml(point.day)}</text>` : "";
          return `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="4"><title>${escapeHtml(point.day)}: ${escapeHtml(formatBalance(point.amount, displayCurrency))}</title></circle>${label}`;
        }).join("")}
      </svg>
    </div>
  `;
}

function changeCaption(pct: number, suffix: string) {
  if (!pct || !Number.isFinite(pct)) return `No change ${suffix}`;
  const arrow = pct > 0 ? "▲" : "▼";
  return `${arrow} ${Math.abs(pct).toFixed(1)}% ${suffix}`;
}

function renderPointsSection(points: PointsBalance[]) {
  return `
    <section class="card points-panel">
      <div class="card-header">
        <div>
          <span class="card-title">Points / rewards</span>
          <p class="muted compact-copy">Not included in totals — these rewards have no market price yet.</p>
        </div>
      </div>
      <div class="earnings-breakdown">
        ${points.map((item) => `
          <div class="earning-chip points" title="${escapeHtml(formatBalance(item.balance, item.currency))}">
            <span>${escapeHtml(item.name || item.platform)}</span>
            <strong>${escapeHtml(formatBalance(item.balance, item.currency))}</strong>
            <small>${escapeHtml(item.currency)}</small>
          </div>
        `).join("")}
      </div>
    </section>
  `;
}

function renderEarningBreakdown(item: ServiceEarning, displayCurrency: string) {
  const native = `${item.balance.toFixed(2)} ${item.currency}`;
  // When a service is convertible but its display balance is 0 the live rate is
  // missing, so show the native `balance currency` instead of a misleading
  // display-currency 0.
  const primary = item.error
    ? "Needs attention"
    : item.convertible && item.balanceDisplay !== 0
      ? formatBalance(item.balanceDisplay, displayCurrency)
      : formatBalance(item.balance, item.currency);
  const cashout = item.cashout;
  const showBar = !item.error && cashout.comparable && cashout.minAmount > 0;
  const pct = Math.max(0, Math.min(100, cashout.percent || 0));
  const sub = item.error
    ? escapeHtml(item.error)
    : item.convertible
      ? `${escapeHtml(native)}${cashout.eligible ? " · ready to cash out" : ""}`
      : `${escapeHtml(item.currency)} · not converted`;
  return `
    <div class="earning-chip ${item.error ? "error" : ""}" title="${escapeHtml(native)}">
      <span>${escapeHtml(item.name || item.platform)}</span>
      <strong>${escapeHtml(primary)}</strong>
      <small>${sub}</small>
      ${showBar ? `
        <div class="payout-progress" title="${pct.toFixed(0)}% of ${escapeHtml(formatBalance(cashout.minAmount, cashout.currency))} minimum">
          <div class="payout-progress-bar" style="width: ${pct.toFixed(1)}%"></div>
        </div>
      ` : ""}
    </div>
  `;
}

// renderHealthBadge renders a compact, color-coded pill for a deployed service's
// rolling health: the 0-100 score plus uptime%. Colour tracks the score — green
// >= 80, amber 50-79, red < 50 — reusing the theme's own status variables. A
// service with no health entry yet (nothing scored) renders nothing rather than a
// misleading 0/NaN badge. The title surfaces the raw lifecycle counts behind it.
function renderHealthBadge(health: HealthScore | undefined): string {
  if (!health) return "";
  const score = Math.round(health.score);
  const uptime = Math.round(health.uptimePercent);
  const tone = score >= 80
    ? "color: var(--success); background: rgba(34, 197, 94, 0.12); border-color: rgba(34, 197, 94, 0.32);"
    : score >= 50
      ? "color: var(--warning); background: rgba(245, 158, 11, 0.14); border-color: rgba(245, 158, 11, 0.32);"
      : "color: var(--error); background: rgba(248, 113, 113, 0.14); border-color: rgba(248, 113, 113, 0.32);";
  const title = `Health ${score}/100 · ${uptime}% uptime · ${health.restarts} restarts · ${health.crashes} crashes · ${health.stops} stops`;
  return `<span class="badge" style="margin-left: 6px; text-transform: none; ${tone}" title="${escapeHtml(title)}">${score} · ${uptime}% up</span>`;
}

// renderMystNodes turns the Mysterium per-node earnings blob — a JSON array of
// MystNode the backend stashes under serviceDetails["mysterium"] — into a
// compact per-node list: each node's name (or a shortened identity), an
// online/offline dot in the theme's success/muted colours, and its 30-day and
// lifetime MYST. It returns "" when the blob is missing, unparseable, or not a
// non-empty array, so a Mysterium row with no per-node detail renders nothing
// extra rather than an empty header or a NaN.
function renderMystNodes(json: string | undefined): string {
  if (!json) return "";
  let nodes: MystNode[];
  try {
    const parsed: unknown = JSON.parse(json);
    if (!Array.isArray(parsed) || parsed.length === 0) return "";
    nodes = parsed as MystNode[];
  } catch {
    return "";
  }
  const items = nodes.map((node) => {
    const label = (node.name || "").trim() || shortenIdentity(node.identity);
    const dotColor = node.online ? "var(--success)" : "var(--text-muted)";
    const dot = `<span title="${node.online ? "online" : "offline"}" style="display:inline-block;width:8px;height:8px;border-radius:50%;flex:0 0 auto;background:${dotColor};"></span>`;
    return `
      <div style="display:flex;align-items:center;gap:0.6rem;font-size:0.82rem;padding:0.15rem 0;">
        <span style="display:flex;align-items:center;gap:0.4rem;flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--text-secondary);">${dot}${escapeHtml(label)}</span>
        <span style="flex:0 0 auto;color:var(--text-muted);" title="Last 30 days">${escapeHtml(formatMyst(node.earnings30dMyst))} · 30d</span>
        <span style="flex:0 0 auto;color:var(--text-secondary);" title="Lifetime">${escapeHtml(formatMyst(node.lifetimeMyst))} lifetime</span>
      </div>
    `;
  }).join("");
  return `
    <div style="display:flex;flex-direction:column;gap:0.1rem;">
      <span style="font-size:0.72rem;letter-spacing:0.06em;text-transform:uppercase;color:var(--text-muted);margin-bottom:0.2rem;">Per-node earnings</span>
      ${items}
    </div>
  `;
}

function renderServicesTable(services: Service[], deployments: Deployment[], earnings: {platform: string; balance: number; currency: string; error?: string}[], health: Record<string, HealthScore> | null, serviceDetails: Record<string, string> | null) {
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
          const nodeDetail = deployment.slug === "mysterium" ? renderMystNodes(serviceDetails?.["mysterium"]) : "";
          return `
            <tr>
              <td>
                <strong>${escapeHtml(service?.name || deployment.slug)}</strong>
                <small>${escapeHtml(deployment.image)}</small>
              </td>
              <td><span class="status-pill ${deployment.status === "running" ? "ok" : "warn"}">${escapeHtml(deployment.status)}</span>${renderHealthBadge(health?.[deployment.slug])}</td>
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
            ${nodeDetail ? `<tr><td colspan="6" style="padding-top:0.3rem;padding-left:0.8rem;">${nodeDetail}</td></tr>` : ""}
          `;
        }).join("")}
      </tbody>
    </table>
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
        ${topbar("Setup Wizard", totalBalance(current), current)}
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
  try {
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
  } catch (error) {
    if (output) output.textContent = String(error);
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

async function refreshState() {
  if (!state) return;
  const [runtime, deployments] = await Promise.all([CheckRuntime(), RefreshDeployments().catch(() => state?.deployments || [])]);
  state = {...await GetAppState(), runtime, deployments};
  render();
}

function setOutput(value: string) {
  const out = document.querySelector<HTMLPreElement>("#service-output");
  if (out) out.textContent = value;
}

// showToast renders a non-intrusive, auto-dismissing toast into the bottom-right stack.
// Backend app:error / app:notice events arrive outside a normal state render, so they
// cannot flow through the notification bell (which is rebuilt from GetAppState); this
// is their surface. title + message are escapeHtml'd before the innerHTML sink.
function showToast(className: string, borderVar: string, title: string, message: string) {
  if (!message) return;
  let stack = document.querySelector<HTMLDivElement>("#toast-stack");
  if (!stack) {
    stack = document.createElement("div");
    stack.id = "toast-stack";
    stack.style.cssText = "position:fixed;bottom:16px;right:16px;z-index:9999;display:grid;gap:8px;width:320px;max-width:calc(100vw - 32px);";
    document.body.appendChild(stack);
  }
  const toast = document.createElement("div");
  toast.className = className;
  toast.style.cssText = `border-left-color:var(${borderVar});background:var(--bg-secondary);box-shadow:0 18px 60px rgba(0,0,0,0.45);`;
  toast.innerHTML = `<span>${escapeHtml(title)}</span><small>${escapeHtml(message)}</small>`;
  stack.appendChild(toast);
  setTimeout(() => toast.remove(), 8000);
}

// showErrorToast surfaces a backend app:error (a background or startup failure that
// arrives outside a normal state render). It is also reused by the frontend action
// handlers to report a rejected bound call on views without an output pre.
function showErrorToast(payload: {scope?: string; error?: string} | string) {
  const scope = typeof payload === "object" && payload ? payload.scope ?? "" : "";
  const message = typeof payload === "object" && payload ? payload.error ?? "" : String(payload);
  showToast("notification-item error", "--error", scope ? `${scope} error` : "Error", message);
}

// showInfoToast surfaces a backend app:notice — an informational advisory (e.g. a
// "restart required" heads-up) styled distinctly from an error so a successful action
// is not reported as a failure.
function showInfoToast(payload: {scope?: string; message?: string} | string) {
  const scope = typeof payload === "object" && payload ? payload.scope ?? "" : "";
  const message = typeof payload === "object" && payload ? payload.message ?? "" : String(payload);
  showToast("notification-item", "--warning", scope ? `${scope} notice` : "Notice", message);
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
  // Sanitize to A-Z0-9 only: `code` is interpolated unescaped into a couple of
  // innerHTML sinks (the topbar and services-table balance cells), so stripping
  // everything else closes those injection points and keeps Intl happy.
  const code = (currency || "USD").toUpperCase().replace(/[^A-Z0-9]/g, "");
  const amount = Number.isFinite(value) ? value : 0;
  // Intl.NumberFormat throws RangeError on non-ISO codes (e.g. reward "points"
  // like MYST or GRASS); those fall back to a plain "1234.00 CODE" string.
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: code,
      maximumFractionDigits: 2,
    }).format(amount);
  } catch {
    return `${amount.toFixed(2)} ${code}`;
  }
}

// formatMyst renders a MYST amount to a few decimals. MYST is a reward token,
// not an ISO currency, so Intl currency formatting can't be used; non-finite
// values degrade to 0 rather than showing NaN.
function formatMyst(value: number) {
  const amount = Number.isFinite(value) ? value : 0;
  return `${amount.toFixed(4)} MYST`;
}

// shortenIdentity collapses a long Mysterium identity (a 0x… hash) to
// "first6…last4" so an unnamed node still shows something readable. Short or
// empty identities are returned as-is (or a neutral placeholder).
function shortenIdentity(identity: string) {
  const id = (identity || "").trim();
  if (!id) return "unknown node";
  if (id.length <= 12) return id;
  return `${id.slice(0, 6)}…${id.slice(-4)}`;
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
