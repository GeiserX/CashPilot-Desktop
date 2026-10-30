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
  BackgroundHelperStatus,
  InstallBackgroundHelper,
  RemoveBackgroundHelper,
  CheckRuntime,
  CollectService,
  CompleteOnboarding,
  DeployService,
  ExportCompose,
  GetAppState,
  GetCopyLogs,
  GetCredentials,
  GetFleetState,
  GetLogs,
  GetRuntimeGuides,
  GetSettingsState,
  PreflightService,
  PlanServiceRemoval,
  RemoveFleetDevice,
  RefreshDeployments,
  RemoveService,
  SaveSettings,
  SaveCredentials,
  StartService,
  StartServiceCopy,
  StopService,
  StopServiceCopy,
} from "../wailsjs/go/main/App";
import { renderFleetSection } from "./render/fleet";
import { attentionItems, renderAttention } from "./render/attention";
import { renderEarningsBars } from "./render/chart";
import { renderServicesTable } from "./render/services";
import { renderCatalogTable } from "./render/catalog";
import { renderPreflight } from "./render/preflight";
import { escapeHtml, formatBalance } from "./render/format";
import { renderWizardServiceSetup } from "./render/wizard";
import { imageChoice } from "./render/image";
import { instancesChoice } from "./render/instances";
import { composeSelectionExport, deviceLinkText } from "./render/details";
import { totalText, totalCaption } from "./render/total";
import { deleteDataConfirmText, removalChoice, removeConfirmText, type RemovalChoice } from "./render/removal";
import type { AppState, BackgroundStatus, Deployment, FleetState, InstallGuide, PointsBalance, Service, SettingsState } from "./wails";

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
// The dashboard chart's window: the web console opens on 7 days.
let chartDays = 7;

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
  // Onboarding proceeds on EITHER runtime: Docker (runs every service) OR the
  // always-on native runtime (runs services that ship a native app, no Docker
  // needed). Native-only is a READY state, not a warning — only "neither" warns.
  const dockerReady = runtime.available;
  const runtimeReady = runtime.available || runtime.nativeAvailable;
  const runtimeTitle = dockerReady
    ? "Runtime ready"
    : runtime.nativeAvailable
      ? "Native mode ready — no Docker needed"
      : "Runtime setup needed";
  // Docker/neither show the backend's (Docker-centric) message, escaped; native-only
  // shows honest static copy instead of the alarming "choose a runtime" Docker text.
  const runtimeBody = !dockerReady && runtime.nativeAvailable
    ? "You can run services that ship a native app right away. A few container-only services would additionally need Docker."
    : escapeHtml(runtime.message);
  root.innerHTML = `
    ${titlebar()}
    <div class="gate">
      ${gateBar()}
      <main class="gate-main">
        <section class="gate-panel gate-panel-wide">
          ${gateSteps(2)}
          <h1 class="gate-title">Put your idle machine to work</h1>
          <p class="gate-lede">Share spare bandwidth, storage, CPU, RAM, or GPU with real networks, then track what each service earns from one place.</p>
          <div class="runtime-card ${runtimeReady ? "ok" : "warn"}">
            <strong>${runtimeTitle}</strong>
            <span>${runtimeBody}</span>
            ${runtime.context ? `<small>Docker context: ${escapeHtml(runtime.context)}</small>` : ""}
          </div>
          <div class="gate-actions">
            <button class="btn btn-primary btn-lg" id="continue-btn" ${runtimeReady ? "" : "disabled"}>Open the dashboard</button>
            <button class="btn btn-ghost btn-lg" id="refresh-runtime">Check again</button>
          </div>
          <div id="install-guides" class="guide-grid"></div>
        </section>
      </main>
    </div>
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

// First run: the same bar as the app, one column, one step at a time.
function gateBar() {
  return `<header class="gate-bar"><span class="topbar-brand">${officialLogoMark()}<span>CashPilot</span></span></header>`;
}

function gateSteps(active: 1 | 2) {
  const step = (n: 1 | 2, label: string) =>
    `<li class="gate-step ${n === active ? "active" : n < active ? "completed" : ""}"><span class="wizard-step-number">${n}</span>${label}</li>`;
  return `<ol class="gate-steps" aria-label="Step ${active} of 2">${step(1, "Welcome")}${step(2, "Runtime")}</ol>`;
}

function renderWelcome() {
  root.innerHTML = `
    ${titlebar()}
    <div class="gate">
      ${gateBar()}
      <main class="gate-main">
        <section class="gate-panel">
          ${gateSteps(1)}
          <h1 class="gate-title">Welcome to CashPilot</h1>
          <p class="gate-lede">Let your spare resources earn in the background while CashPilot handles setup, monitoring, and payouts from one place.</p>
          <div class="gate-actions">
            <button class="btn btn-primary btn-lg" id="get-started">Get started</button>
          </div>
        </section>
      </main>
    </div>
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
  const copies = current.copies || {};
  const containers = [...deployments, ...Object.values(copies).flat()];
  const upCount = containers.filter((item) => item.status === "running").length;
  const points = summary?.points || [];
  const attention = attentionItems({
    runtime: current.runtime,
    services,
    deployments,
    copies,
    earnings,
    outdated: current.outdatedServices,
    health: current.health,
  });

  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      <div class="main-content">
        ${topbar("dashboard", current)}
        <main class="page-content">
        ${pageHead("Dashboard", `
          <button class="btn btn-ghost btn-sm" id="refresh">Refresh</button>
          <button class="btn btn-primary btn-sm" id="open-wizard">Add service</button>
        `)}
        <!-- The money first and largest; the container count sits at the far
             end, smaller, because it answers a different question. -->
        <section class="stats-grid money-strip" aria-label="Earnings">
          <div class="stat-card stat-card-balance">
            <div class="stat-label">Balance</div>
            <div class="stat-value">${escapeHtml(totalText(summary, disp))}</div>
            <div class="stat-note">${escapeHtml(totalCaption(summary))}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">Today</div>
            <div class="stat-value">${escapeHtml(formatBalance(summary?.today ?? 0, disp))}</div>
            <div class="stat-change">${escapeHtml(changeCaption(summary?.todayChange ?? 0, "vs yesterday"))}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">This month</div>
            <div class="stat-value">${escapeHtml(formatBalance(summary?.month ?? 0, disp))}</div>
            <div class="stat-change">${escapeHtml(summary?.monthChange ? changeCaption(summary.monthChange, "vs last month") : "so far this month")}</div>
          </div>
          <div class="stat-card stat-card-ops">
            <div class="stat-label">Containers up</div>
            <div class="stat-value">${upCount}${containers.length !== upCount ? ` <span class="stat-qual">of ${containers.length}</span>` : ""}</div>
          </div>
        </section>

        ${renderAttention(attention, upCount, containers.length)}

        <section class="earnings" aria-labelledby="earnings-title">
          <div class="block-head">
            <h2 class="block-title" id="earnings-title">Earned per day</h2>
            <div class="tab-strip">
              ${[7, 30].map((days) => `<button class="tab-btn ${chartDays === days ? "active" : ""}" data-chart-days="${days}">${days} days</button>`).join("")}
            </div>
          </div>
          ${renderEarningsBars(summary?.daily || [], disp, chartDays)}
        </section>

        <section class="services-block" aria-labelledby="services-title">
          <div class="block-head">
            <h2 class="block-title" id="services-title">Services</h2>
            <span class="block-meta">Balances in ${escapeHtml(disp)}</span>
          </div>
          <div class="table-scroll">
            ${renderServicesTable({
              services,
              deployments,
              earnings,
              breakdown: summary?.breakdown,
              health: current.health,
              serviceDetails: current.serviceDetails,
              outdated: current.outdatedServices,
              producerStates: current.producerStates,
              copies,
              displayCurrency: disp,
            })}
          </div>
        </section>

        ${points.length ? renderPointsSection(points) : ""}

        ${current.fleet ? renderFleetSection(current.fleet) : ""}

        <pre id="service-output" class="output dashboard-output"></pre>
        </main>
        ${pageFoot()}
      </div>
    </div>
  `;
  wireChrome();
  wireShellNav();
  maybeResetScroll();

  document.querySelector("#refresh")?.addEventListener("click", refreshState);
  document.querySelector("#open-wizard")?.addEventListener("click", openWizard);
  document.querySelector("#open-wizard-empty")?.addEventListener("click", openWizard);
  document.querySelectorAll<HTMLButtonElement>("[data-chart-days]").forEach((button) => {
    button.addEventListener("click", () => {
      chartDays = Number(button.dataset.chartDays) || 30;
      render();
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-open-service]").forEach((button) => {
    button.addEventListener("click", () => openWizard(button.dataset.openService || ""));
  });
  document.querySelectorAll<HTMLButtonElement>("[data-row-action]").forEach((button) => {
    button.addEventListener("click", () => {
      const slug = button.dataset.slug || "";
      const action = button.dataset.rowAction || "";
      const copy = Number(button.dataset.copy || "1");
      void (copy > 1 ? runCopyAction(slug, copy, action) : runServiceAction(slug, action));
    });
  });
}

function metricCard(label: string, value: string, caption: string) {
  return `
    <div class="stat-card">
      <div class="stat-label">${escapeHtml(label)}</div>
      <div class="stat-value">${escapeHtml(value)}</div>
      <div class="stat-note">${escapeHtml(caption)}</div>
    </div>
  `;
}

// One bar, no sidebar: brand, the views, then the money and the alerts. The page
// title moves into the page itself (pageHead), like the web console.
function navButton(view: View, label: string, active: View) {
  return `<button class="sidebar-link ${active === view ? "active" : ""}" data-view="${view}"${active === view ? ' aria-current="page"' : ""}>${escapeHtml(label)}</button>`;
}

function pageHead(title: string, actions = "") {
  return `
    <div class="page-head">
      <h1 class="page-title">${escapeHtml(title)}</h1>
      ${actions ? `<div class="page-actions">${actions}</div>` : ""}
    </div>
  `;
}

function pageFoot() {
  return `
    <footer class="page-foot">
      <span>CashPilot Desktop v${__APP_VERSION__}</span>
      <button class="footer-link" data-url="https://github.com/GeiserX/CashPilot-Desktop">Source</button>
      <button class="footer-link" data-url="https://github.com/GeiserX/CashPilot-Desktop/issues">Report a problem</button>
      <button class="footer-link" data-url="https://github.com/sponsors/GeiserX">Sponsor</button>
    </footer>
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
        <g transform="translate(17.1,12.2) rotate(-17) scale(0.2)" fill="#0A0A1A" opacity="0.65">
          <path d="M37,1.2 C35.8,-1.8 32,-3.8 26,-3.9 L-22,-3.9 L-36.5,-4.8 L-37.6,-3.2 L-15,3.6 L24,3.8 C30.5,3.8 35,3 37,1.2 Z"/>
          <path d="M-25,-3.9 L-31.8,-18.4 L-37.2,-18.6 L-36.4,-4.6 Z"/>
          <path d="M-28.8,-18.6 L-42.5,-19.6 L-42.2,-17.6 L-31,-16.8 Z"/>
          <path d="M-10,-7.2 L-23,-7.2 C-25.4,-7.2 -25.4,-1.8 -23,-1.8 L-10,-1.8 C-8.4,-1.8 -8.4,-7.2 -10,-7.2 Z"/>
          <path d="M9,3 L-6,3.4 L-16.8,12.6 L-19.5,9.4 L-20.2,9.6 L-18.6,13.4 L-12.2,13.2 Z"/>
        </g>
      </g>
    </svg>
  `;
}


function topbar(active: View, current: AppState) {
  // The total was computed IN summary.displayCurrency, so it must be labelled
  // with that -- not with the configured preference, which can differ (the
  // summary is a snapshot and the setting can change under it). Falling back to
  // config only when the summary has no currency of its own.
  const disp = current.summary?.displayCurrency || current.config.displayCurrency || "USD";
  const notifications = current.notifications || [];
  const runtimeOk = current.runtime.available || current.runtime.nativeAvailable;
  return `
    <header class="topbar">
      <div class="topbar-brand">
        ${officialLogoMark()}
        <span>CashPilot</span>
      </div>
      <nav class="topnav" aria-label="Main">
        ${navButton("dashboard", "Dashboard", active)}
        ${navButton("fleet", "Fleet", active)}
        ${navButton("catalog", "Catalog", active)}
        ${navButton("settings", "Settings", active)}
        ${navButton("wizard", "Add services", active)}
      </nav>
      <div class="topbar-right">
        <span class="topbar-runtime ${runtimeOk ? "" : "text-warn"}" title="${escapeHtml(current.runtime.message || "")}"><span class="runtime-dot ${runtimeOk ? "ok" : "warn"}"></span>${current.runtime.available ? "Runtime ready" : current.runtime.nativeAvailable ? "Native mode" : "Runtime offline"}</span>
        <span class="topbar-earnings" title="Total balance across every service">${escapeHtml(totalText(current.summary, disp))}</span>
        <select class="currency-select" id="currency-select" title="Display currency">
          ${(current.currencies || ["USD", "EUR"]).map((currency) => `<option value="${currency}" ${currency === current.config.displayCurrency ? "selected" : ""}>${currency}</option>`).join("")}
        </select>
        <details class="notification-menu">
          <summary aria-label="Alerts">Alerts${notifications.length ? ` <span class="notify-badge">${notifications.length}</span>` : ""}</summary>
          <div class="notification-popover">
            <strong>Alerts</strong>
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
  const services = (current.services || []).filter((service) => {
    const matchesCategory = catalogFilter === "all" || service.category === catalogFilter;
    const haystack = `${service.name} ${service.shortDescription} ${service.description}`.toLowerCase();
    return matchesCategory && haystack.includes(catalogSearch.toLowerCase());
  });
  const categories = ["all", "bandwidth", "depin", "storage", "compute"];
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      <div class="main-content">
        ${topbar("catalog", current)}
        <main class="page-content">
          ${pageHead("Catalog")}
          <div class="filter-bar">
            <div class="filter-tabs">
              ${categories.map((category) => `<button class="filter-tab ${catalogFilter === category ? "active" : ""}" data-filter="${category}">${escapeHtml(category === "depin" ? "DePIN" : capitalize(category))}</button>`).join("")}
            </div>
            <input class="catalog-search" id="catalog-search" type="search" placeholder="Search services" aria-label="Search services" value="${escapeHtml(catalogSearch)}" />
          </div>
          <section class="catalog-block">
            <div class="table-scroll">
              ${renderCatalogTable(services, current.deployments || [], current.outdatedServices)}
            </div>
            <p class="referral-note">Visit and Sign up links to providers with a referral programme are referral links: the CashPilot maintainer may earn a commission from them.</p>
          </section>
        </main>
        ${pageFoot()}
      </div>
    </div>
  `;
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  document.querySelector<HTMLInputElement>("#catalog-search")?.addEventListener("input", (event) => {
    const input = event.target as HTMLInputElement;
    catalogSearch = input.value;
    const caret = input.selectionStart ?? catalogSearch.length;
    render();
    // render() replaces the page, input included: put the cursor back, or every
    // keystroke after the first lands nowhere.
    const next = document.querySelector<HTMLInputElement>("#catalog-search");
    next?.focus();
    next?.setSelectionRange(caret, caret);
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

async function renderSettings(current: AppState) {
  let settings: SettingsState;
  try {
    settings = await GetSettingsState();
  } catch (error) {
    showErrorToast({scope: "settings", error: String(error)});
    return;
  }
  // Background-helper state is derived live from the OS (not a stored preference):
  // installed = a login agent is registered, running = the service manager reports it
  // alive. Reject only if the app isn't ready yet, in which case the toggle is disabled.
  let background: BackgroundStatus | null = null;
  let backgroundError = "";
  try {
    background = await BackgroundHelperStatus();
  } catch (error) {
    backgroundError = String(error);
  }
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      <div class="main-content">
        ${topbar("settings", current)}
        <main class="page-content">
          ${pageHead("Settings")}
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

          ${renderBackgroundCard(background, backgroundError)}
        </main>
        ${pageFoot()}
      </div>
    </div>
  `;
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  document.querySelector("#save-settings")?.addEventListener("click", () => void saveSettingsFromForm());
  document.querySelector<HTMLInputElement>("#bg-helper-toggle")?.addEventListener("change", (event) => void toggleBackgroundHelper(event));
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

// renderBackgroundCard renders the "Background Earning" settings card. The toggle
// reflects the live OS state (background.installed); when the status call rejected
// (app not ready), the toggle is disabled and the reason is shown.
function renderBackgroundCard(background: BackgroundStatus | null, backgroundError: string) {
  const unavailable = background === null;
  const on = Boolean(background?.installed);
  const running = Boolean(background?.running);
  let title: string;
  let detail: string;
  if (unavailable) {
    title = "Background earning unavailable";
    detail = backgroundError || "The background helper can't be reached right now.";
  } else if (on && running) {
    title = "Earning in the background";
    detail = "CashPilot keeps your earners running after you close the window, and starts them automatically when you sign in.";
  } else if (on) {
    title = "Starting in the background…";
    detail = "The helper is registered with your operating system and will start momentarily.";
  } else {
    title = "Off — earners stop when you quit";
    detail = "Turn on to keep earning after you close CashPilot. Registers a per-user helper the OS keeps alive and restarts — no admin required.";
  }
  const statusClass = unavailable ? "is-unavailable" : on && running ? "is-on" : "";
  return `
    <section class="card">
      <div class="card-header">
        <div>
          <span class="card-title">Background Earning</span>
          <p class="muted compact-copy">Keep earning when CashPilot is closed. Your operating system keeps a per-user helper running and restarts it automatically — no admin rights, no separate service to manage.</p>
        </div>
      </div>
      <div class="bg-toggle-row">
        <label class="switch" title="Keep earning in the background">
          <input type="checkbox" id="bg-helper-toggle" role="switch" ${on ? "checked" : ""} ${unavailable ? "disabled" : ""} aria-label="Keep earning in the background" />
          <span class="switch-track"><span class="switch-thumb"></span></span>
        </label>
        <div class="bg-toggle-status ${statusClass}">
          <strong>${escapeHtml(title)}</strong>
          <small>${escapeHtml(detail)}</small>
        </div>
      </div>
    </section>
  `;
}

// toggleBackgroundHelper installs or removes the OS login agent when the switch is
// flipped, then re-renders Settings so the card reflects the true OS state. On failure
// the switch is reverted and the error surfaced.
async function toggleBackgroundHelper(event: Event) {
  const input = event.target as HTMLInputElement;
  const enable = input.checked;
  input.disabled = true;
  try {
    if (enable) {
      await InstallBackgroundHelper();
      showInfoToast({scope: "background", message: "Earning will continue after you close CashPilot."});
    } else {
      await RemoveBackgroundHelper();
      showInfoToast({scope: "background", message: "Background earning turned off."});
    }
    // Re-render the active (Settings) view so the card reflects fresh OS state;
    // render() null-guards module state, then re-invokes renderSettings.
    render();
  } catch (error) {
    input.checked = !enable;
    input.disabled = false;
    showErrorToast({scope: "background", error: String(error)});
  }
}

function envInputName(key: string) {
  const names: Record<string, string> = {
    CASHPILOT_HOSTNAME_PREFIX: "hostnamePrefix",
    CASHPILOT_COLLECT_INTERVAL: "collectIntervalMinutes",
    CASHPILOT_DISPLAY_CURRENCY: "displayCurrency",
    CASHPILOT_FLEET_BIND: "fleetBindAddress",
    CASHPILOT_FLEET_PORT: "fleetPort",
    CASHPILOT_FLEET_SERVER: "fleetServerEnabled",
    CASHPILOT_UPSTREAM_URL: "upstreamUrl",
    CASHPILOT_UPSTREAM_KEY: "upstreamKey",
    TZ: "timezone",
  };
  return names[key] || key;
}

async function renderFleet(current: AppState) {
  let fleet: FleetState;
  try {
    fleet = await GetFleetState();
  } catch (error) {
    showErrorToast({scope: "fleet", error: String(error)});
    return;
  }
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      <div class="main-content">
        ${topbar("fleet", current)}
        <main class="page-content">
          ${pageHead("Fleet")}
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
        ${pageFoot()}
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
      try {
        await navigator.clipboard.writeText(button.dataset.copy || "");
        button.textContent = "Copied";
      } catch (error) {
        showErrorToast({scope: "clipboard", error: String(error)});
      }
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
  try {
    await RemoveFleetDevice(id);
    render();
  } catch (error) {
    showErrorToast({scope: "fleet", error: String(error)});
  }
}

function valueOf(selector: string) {
  return document.querySelector<HTMLInputElement | HTMLSelectElement>(selector)?.value || "";
}

function maybeResetScroll() {
  if (!resetScrollAfterRender) return;
  resetScrollAfterRender = false;
  requestAnimationFrame(() => window.scrollTo({top: 0, left: 0}));
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
      <div class="main-content">
        ${topbar("wizard", current)}
      <main class="page-content setup-content">
        ${pageHead("Add services", `<button class="btn btn-ghost btn-sm" id="close-wizard">Back to dashboard</button>`)}
        <p class="page-lede">Choose what to share, sign up through CashPilot, then deploy or track each provider.</p>
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
        ${pageFoot()}
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
  document.querySelectorAll<HTMLButtonElement>("[data-compose-export]").forEach((button) => {
    button.addEventListener("click", () => {
      const slug = button.dataset.composeExport || "";
      const arch = document.querySelector<HTMLSelectElement>(`[data-compose-arch="${slug}"]`)?.value || "";
      void exportCompose([slug], arch, slug);
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-compose-export-selection]").forEach((button) => {
    button.addEventListener("click", () => {
      const slugs = (button.dataset.composeExportSelection || "").split(" ").filter(Boolean);
      const arch = document.querySelector<HTMLSelectElement>(`[data-compose-arch="selection"]`)?.value || "";
      void exportCompose(slugs, arch, "compose-selection");
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-url]").forEach((button) => {
    button.addEventListener("click", () => {
      const url = button.dataset.url;
      if (url) BrowserOpenURL(url);
    });
  });
  selectedServices.forEach((service) => void hydrateWizardForm(service));
  selectedServices.forEach((service) => void hydrateWizardPreflight(service));
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
        <button class="wizard-service ${wizardSelected.includes(service.slug) ? "selected" : ""}" data-wizard-service="${escapeHtml(service.slug)}">
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
    ${composeSelectionExport(services)}
    <div class="wizard-setup-list">
      ${services.map((service) => renderWizardServiceSetup(service, state?.collectorFields, state?.hostname, state?.summary?.breakdown, deploymentOf(service.slug)?.customImage, state?.scaling?.[service.slug], deploymentOf(service.slug)?.instances)).join("")}
    </div>
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

function deploymentOf(slug: string): Deployment | undefined {
  return state?.deployments?.find((deployment) => deployment.slug === slug);
}

// The Image box's answer for a deploy: null keeps the recorded image (also when the
// card has no box at all), "" goes back to the catalog's.
function readWizardImage(slug: string): string | null {
  const input = document.querySelector<HTMLInputElement>(`[data-form-slug="${slug}"] [data-wizard-image]`);
  return input ? imageChoice(input.value, input.dataset.initial || "") : null;
}

// The Instances box's answer for a deploy: null keeps the recorded count (also when
// the card has no box, or the box is locked).
function readWizardInstances(slug: string): number | null {
  const input = document.querySelector<HTMLInputElement>(`[data-form-slug="${slug}"] [data-wizard-instances]`);
  return input ? instancesChoice(input.value, input.dataset.initial || "", input.disabled) : null;
}

function closeWizard() {
  activeView = "dashboard";
  wizardStep = 1;
  render();
}

async function hydrateWizardForm(service: Service) {
  let creds: Record<string, string> = {};
  try {
    creds = await GetCredentials(service.slug);
  } catch (error) {
    showErrorToast({scope: "credentials", error: String(error)});
  }
  document.querySelectorAll<HTMLInputElement>(`[data-form-slug="${service.slug}"] [data-wizard-env]`).forEach((input) => {
    const key = input.dataset.wizardEnv || "";
    if (creds[key]) input.value = creds[key];
  });
}

// The pre-deploy check, filled in beside the Deploy button once the backend has
// answered. It runs after the card is on screen because it talks to the container
// runtime, and a slow or unreachable runtime must not hold up the wizard. A failed
// check leaves the slot empty: a panel that cannot say anything must say nothing,
// never a reassuring blank.
async function hydrateWizardPreflight(service: Service) {
  const slot = document.querySelector<HTMLDivElement>(`[data-preflight-slug="${service.slug}"]`);
  if (!slot) return;
  try {
    slot.innerHTML = renderPreflight(await PreflightService(service.slug));
  } catch {
    slot.innerHTML = "";
  }
}

function readWizardForm(slug: string): Record<string, string> {
  const values: Record<string, string> = {};
  document.querySelectorAll<HTMLInputElement>(`[data-form-slug="${slug}"] [data-wizard-env]`).forEach((input) => {
    values[input.dataset.wizardEnv || ""] = input.value;
  });
  return values;
}

// A typed value the catalog constrains (the input carries its pattern) is checked
// in the browser first, so a password pasted where a UUID API key belongs is
// pointed at in the form instead of coming back as a backend error after a save.
// The backend check stays authoritative: this is the early warning, not the gate.
function firstInvalidWizardInput(slug: string): HTMLInputElement | null {
  for (const input of document.querySelectorAll<HTMLInputElement>(`[data-form-slug="${slug}"] [data-wizard-env]`)) {
    if (input.pattern && input.value.trim() && !input.checkValidity()) return input;
  }
  return null;
}

async function runWizardAction(slug: string, action: string) {
  const output = document.querySelector<HTMLPreElement>(`[data-output-slug="${slug}"]`);
  const invalid = firstInvalidWizardInput(slug);
  if (invalid && (action === "save" || action === "deploy")) {
    invalid.reportValidity();
    if (output) output.textContent = `${invalid.dataset.wizardEnv} does not have the shape this provider expects.`;
    return;
  }
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
      const deployment = await DeployService(slug, values, readWizardImage(slug), readWizardInstances(slug));
      state = await GetAppState();
      // The box now holds what runs, so a second Deploy without an edit keeps it.
      const imageInput = document.querySelector<HTMLInputElement>(`[data-form-slug="${slug}"] [data-wizard-image]`);
      if (imageInput) {
        imageInput.value = deployment.customImage || "";
        imageInput.dataset.initial = deployment.customImage || "";
      }
      const instancesInput = document.querySelector<HTMLInputElement>(`[data-form-slug="${slug}"] [data-wizard-instances]`);
      if (instancesInput) {
        instancesInput.value = String(deployment.instances || 1);
        instancesInput.dataset.initial = String(deployment.instances || 1);
      }
      const link = deviceLinkText(state?.services?.find((service) => service.slug === slug), values, state?.addedDevices?.[slug] || "");
      if (output) output.textContent = link ? `Deployed.\n\n${link}` : "Deployed.";
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

// Save a compose file for one service. The backend generates it, asks where to put
// it, and answers with the path — or with "" when the save dialog was dismissed,
// which is not an error and must not read like one.
async function exportCompose(slugs: string[], arch: string, outputSlug: string) {
  const output = document.querySelector<HTMLPreElement>(`[data-output-slug="${outputSlug}"]`);
  try {
    const path = await ExportCompose(slugs, arch);
    if (output) output.textContent = path ? `Saved ${path}` : "";
  } catch (error) {
    if (output) output.textContent = String(error);
  }
}

// askAboutRemoval puts the two questions to the user: remove the service, and then
// separately, delete the data it saved. The second one names each volume and what it
// holds, because the data behind them - a node identity, a keystore - has no backup
// anywhere and cannot be recreated.
//
// When the plan cannot be read (the container is already gone, or the daemon is down)
// the removal still goes ahead, but only ever as the safe kind: nothing can say what
// the data is, so nothing is deleted.
async function askAboutRemoval(slug: string, serviceName: string): Promise<RemovalChoice | null> {
  let plan;
  try {
    plan = await PlanServiceRemoval(slug);
  } catch {
    return confirm(`Remove ${serviceName}? This deletes its container and keeps any data it saved.`) ? {deleteData: false, allowCritical: false} : null;
  }
  if (!confirm(removeConfirmText(serviceName, plan))) return null;
  const dataQuestion = deleteDataConfirmText(serviceName, plan);
  if (!dataQuestion) return {deleteData: false, allowCritical: false};
  return removalChoice(plan, confirm(dataQuestion));
}

async function runServiceAction(slug: string, action: string) {
  selectedService = state?.services.find((svc) => svc.slug === slug) || null;
  try {
    let removal: RemovalChoice | null = null;
    if (action === "remove") {
      removal = await askAboutRemoval(slug, selectedService?.name || slug);
      if (!removal) return;
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
    if (action === "remove" && removal) {
      await RemoveService(slug, removal.deleteData, removal.allowCritical);
      // Refresh first, then write the message. The generic "complete" line below used
      // to land on top of this one, so the user never got to read which of the two
      // removals actually happened - the one fact they need after an irreversible act.
      await refreshState();
      setOutput(removal.deleteData
        ? `${slug} and its saved data removed.`
        : `${slug} removed. Its saved data is still on this computer. To delete it later, set ${slug} up again and remove it with its data.`);
      return;
    }
    await refreshState();
    setOutput(`${slug} ${action} complete.`);
  } catch (error) {
    setOutput(String(error));
  }
}

// Logs, Stop and Start on copy n (2 and up) of a service. Copy 1 is the service and
// goes through runServiceAction.
async function runCopyAction(slug: string, n: number, action: string) {
  try {
    if (action === "logs") {
      setOutput(await GetCopyLogs(slug, n, 200));
      return;
    }
    if (action === "stop") await StopServiceCopy(slug, n);
    if (action === "start") await StartServiceCopy(slug, n);
    await refreshState();
    setOutput(`${slug} #${n} ${action === "stop" ? "stopped" : "started"}.`);
  } catch (error) {
    setOutput(String(error));
  }
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

function capitalize(value: string) {
  return value ? value.charAt(0).toUpperCase() + value.slice(1) : "";
}

void boot();
