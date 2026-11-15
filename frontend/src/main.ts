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
  DeleteActivityData,
  GetActivity,
  GetActivityDetail,
  RefreshActivityNetworks,
  SetActivityEnabled,
  InstallBackgroundHelper,
  ListCustomServices,
  ApplyImport,
  PreviewImport,
  RemoveBackgroundHelper,
  CheckRuntime,
  CollectService,
  CompleteOnboarding,
  ConfirmStateBackup,
  DeleteCustomService,
  DeleteProxy,
  DeployProxiedCopy,
  DeployService,
  DeployServiceLeavingRoute,
  DeployServiceRouted,
  EnableIsolation,
  ExportCompose,
  ExportStateBackup,
  GetAppState,
  GetCopyLogs,
  GetCredentials,
  GetEarningsSummary,
  GetFleetState,
  GetProxiesState,
  GetLogs,
  GetRuntimeGuides,
  GetSettingsState,
  GetUsage,
  IgnoreDrift,
  ListPayouts,
  PreflightService,
  PlanServiceRemoval,
  RemoveFleetDevice,
  RecordPayout,
  RefreshDeployments,
  RemovePayout,
  RemoveProxiedCopy,
  RemoveService,
  ResetProxyUsage,
  SaveSettings,
  SaveCredentials,
  SaveCustomService,
  SaveProxy,
  StartProxiedCopy,
  StartService,
  StartServiceCopy,
  StopProxiedCopy,
  StopService,
  StopServiceCopy,
  TestProxy,
  UndoStateBackup,
} from "../wailsjs/go/main/App";
import { renderFleetSection } from "./render/fleet";
import { fleetRedrawIsCurrent, keepsLiveProxyEditor, proxyValuesFrom, renderProxiesSection } from "./render/proxies";
import { renderActivityPage } from "./render/activity";
import { attentionItems, renderAttention } from "./render/attention";
import { backupExportForm, renderStateBackups } from "./render/backups";
import { renderEarningsBars } from "./render/chart";
import { renderUsagePanel } from "./render/usage";
import { renderServicesTable } from "./render/services";
import { renderCatalogTable } from "./render/catalog";
import { renderAddServiceButton, renderTopnav } from "./render/topbar";
import { renderPreflight } from "./render/preflight";
import { escapeHtml, formatBalance } from "./render/format";
import { renderWizardServiceSetup } from "./render/wizard";
import { imageChoice } from "./render/image";
import { instancesChoice } from "./render/instances";
import { routeChoice, routedDeployText, runRouteDeploy, wizardAfterNav } from "./render/route";
import { composeSelectionExport, deviceLinkText, payoutLedger } from "./render/details";
import { allTimeNote, totalText, totalCaption } from "./render/total";
import { deleteDataConfirmText, removalChoice, removeConfirmText, type RemovalChoice } from "./render/removal";
import { customDefinitionFrom, customEnvRow, keepsLiveEditor, renderCustomServicesSection } from "./render/custom";
import { IMPORT_MAX_BYTES, IMPORT_MAX_FILES, importChoices, importFilesProblem, renderImportInput, renderImportPreview, renderImportSaved } from "./render/importer";
import type { CustomEnv, CustomServiceView, ImportFile, ImportPreview, ImportRequest, ImportResult, ProxiesState, ProxyView } from "./wails";
import type { ActivityDetail, ActivityView, AppState, BackgroundStatus, Deployment, FleetState, InstallGuide, PointsBalance, Service, SettingsState, UsageView } from "./wails";

let state: AppState | null = null;
let selectedService: Service | null = null;
let onboardingStep: "welcome" | "runtime" = "welcome";
type View = "dashboard" | "wizard" | "catalog" | "settings" | "fleet" | "activity" | "import";

let activeView: View = "dashboard";
let wizardStep = 1;
let wizardCategories: string[] = [];
let wizardSelected: string[] = [];
// The Route box's pick per slug (a proxy id, null for this computer's own IP), kept so
// a redraw of the wizard does not put an unsaved proxy choice back to Direct. Cleared
// when the wizard opens or closes, and on a tab or Add service click (wizardAfterNav).
let wizardRouteDrafts: Record<string, number | null> = {};
let catalogFilter = "all";
let catalogSearch = "";
// Custom services on the Catalog page: the list as last loaded (null before the first
// load), the one open in the editor ("new" for the add form), and a load failure.
let customViews: CustomServiceView[] | null = null;
// Import from another tool. importRequest is what was last previewed: a save sends the
// same files again, so a masked value never has to come back from the page.
let importSource = "auto";
let importRequest: ImportRequest | null = null;
let importPreview: ImportPreview | null = null;
let importResult: ImportResult | null = null;
let importDeployed: string[] = [];
// The device-link step of each deployed import, shown under it (slug -> text).
let importLinks: Record<string, string> = {};
let importDeploying = new Set<string>();
// Bumped by each preview, so a slower earlier one cannot land over a later one.
let importPreviewSeq = 0;
// The preview the page last drew: a redraw of the same preview keeps the ticks and
// typed values, which live only in the page until Save.
let renderedImportPreview: ImportPreview | null = null;
let customEditing: CustomServiceView | "new" | null = null;
let customLoadError = "";
// The proxy open in the Fleet page's editor ("new" for the add form), and the last
// proxies state, kept so the editor can find the proxy it edits.
let proxyEditing: ProxyView | "new" | null = null;
let proxiesState: ProxiesState | null = null;
let resetScrollAfterRender = false;
// The dashboard chart's window: the web console opens on 7 days.
let chartDays = 7;
// The usage graphs under it share chartDays. usageContainer is the picked
// container, "" for all of them; usageView is the last answer, kept so a
// dashboard re-render does not blank the charts while the next one loads.
let usageContainer = "";
let usageView: UsageView | null = null;
let usageRequest = 0;
// The Activity page's window (1, 7 or 30 days) and its expanded rows, each with
// its detail once loaded (null while it loads).
let activityDays = 1;
// Moves on with every window change, switch or delete, so a page answer that
// arrives after a newer one is dropped instead of drawn over it.
let activityRequest = 0;
// Moves on with every page fetch, so of two fetches in one generation only the
// newer one is drawn.
let activityPage = 0;
// The Activity view last drawn: a late detail answer redraws this one, never
// the older view its click came from.
let activityShown: ActivityView | null = null;
let activityOpen: Record<string, ActivityDetail | null> = {};
// Deletes still running (switch-off or Delete data): rows do not open meanwhile.
let activityDeleting = 0;

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
  if (activeView === "activity") {
    void renderActivity(state);
    return;
  }
  if (activeView === "import") {
    renderImport(state);
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

// Fetch the usage graphs for the current window and container and redraw only
// their panel, so a slow answer never re-renders the dashboard under the user.
// A stale answer (the window or the pick changed meanwhile) is dropped.
async function loadUsage() {
  const request = ++usageRequest;
  try {
    const view = await GetUsage(chartDays, usageContainer);
    if (request !== usageRequest) return;
    usageView = view;
    usageContainer = view.container;
  } catch {
    // The app is still starting or the store is busy: keep what is drawn.
    return;
  }
  const panel = document.querySelector<HTMLDivElement>("#usage-panel");
  if (!panel) return;
  panel.innerHTML = renderUsagePanel(usageView);
  wireUsagePanel();
}

function wireUsagePanel() {
  document.querySelector<HTMLSelectElement>("#usage-container")?.addEventListener("change", (event) => {
    usageContainer = (event.target as HTMLSelectElement).value;
    void loadUsage();
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
    drift: current.drift,
    producerStates: current.producerStates,
    isolation: current.isolation,
  });

  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      <div class="main-content">
        ${topbar("dashboard", current)}
        <main class="page-content">
        ${pageHead("Dashboard", `
          <button class="btn btn-ghost btn-sm" id="refresh">Refresh</button>
        `)}
        <!-- The money first and largest; the container count sits at the far
             end, smaller, because it answers a different question. -->
        <section class="stats-grid money-strip" aria-label="Earnings">
          <div class="stat-card stat-card-balance">
            <div class="stat-label">Balance</div>
            <div class="stat-value">${escapeHtml(totalText(summary, disp))}</div>
            <div class="stat-note">${escapeHtml(totalCaption(summary))}</div>
            ${allTimeNote(summary, disp) ? `<div class="stat-note">${escapeHtml(allTimeNote(summary, disp))}</div>` : ""}
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

        ${renderStateBackups(current.stateBackups)}

        <section class="earnings" aria-labelledby="earnings-title">
          <div class="block-head">
            <h2 class="block-title" id="earnings-title">Earned per day</h2>
            <div class="tab-strip">
              ${[7, 30].map((days) => `<button class="tab-btn ${chartDays === days ? "active" : ""}" data-chart-days="${days}">${days} days</button>`).join("")}
            </div>
          </div>
          ${renderEarningsBars(summary?.daily || [], disp, chartDays)}
        </section>

        <section class="usage" aria-labelledby="usage-title">
          <div class="block-head">
            <h2 class="block-title" id="usage-title">Resource use</h2>
            <span class="block-meta">This machine, last ${chartDays} days</span>
          </div>
          <div id="usage-panel">${renderUsagePanel(usageView && usageView.days === chartDays ? usageView : null)}</div>
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
              drift: current.drift,
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
  document.querySelector("#open-wizard-empty")?.addEventListener("click", openWizard);
  document.querySelectorAll<HTMLButtonElement>("[data-chart-days]").forEach((button) => {
    button.addEventListener("click", () => {
      chartDays = Number(button.dataset.chartDays) || 30;
      render();
    });
  });
  wireUsagePanel();
  void loadUsage();
  document.querySelectorAll<HTMLButtonElement>("[data-open-service]").forEach((button) => {
    button.addEventListener("click", () => openWizard(button.dataset.openService || ""));
  });
  document.querySelectorAll<HTMLButtonElement>("[data-drift-action]").forEach((button) => {
    button.addEventListener("click", () => void runDriftAction(button.dataset.slug || "", button.dataset.driftAction || ""));
  });
  document.querySelectorAll<HTMLButtonElement>("[data-isolation-action]").forEach((button) => {
    button.addEventListener("click", () => void runEnableIsolation());
  });
  document.querySelectorAll<HTMLButtonElement>("[data-url]").forEach((button) => {
    button.addEventListener("click", () => {
      const url = button.dataset.url;
      if (url) BrowserOpenURL(url);
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-row-action]").forEach((button) => {
    button.addEventListener("click", () => {
      const slug = button.dataset.slug || "";
      const action = button.dataset.rowAction || "";
      const copy = Number(button.dataset.copy || "1");
      void (copy > 1 ? runCopyAction(slug, copy, action) : runServiceAction(slug, action));
    });
  });
  wireStateBackups();
}

// The "Back these up" card: confirm, undo, and the export form under a row.
function wireStateBackups() {
  document.querySelectorAll<HTMLButtonElement>("[data-backup-action]").forEach((button) => {
    button.addEventListener("click", async () => {
      const slug = button.dataset.slug || "";
      const action = button.dataset.backupAction;
      if (action === "export") {
        const row = button.closest<HTMLElement>("[data-backup-row]");
        const next = row?.nextElementSibling;
        if (next?.classList.contains("backup-form")) {
          next.remove();
          return;
        }
        row?.insertAdjacentHTML("afterend", backupExportForm(slug));
        wireBackupForm(row?.nextElementSibling as HTMLFormElement);
        return;
      }
      try {
        await (action === "undo" ? UndoStateBackup(slug) : ConfirmStateBackup(slug));
        await refreshState();
      } catch (error) {
        showErrorToast(String(error));
      }
    });
  });
}

function wireBackupForm(form: HTMLFormElement | null) {
  if (!form) return;
  const secret = form.querySelector<HTMLInputElement>("input[name=secret]")!;
  const repeat = form.querySelector<HTMLInputElement>("input[name=secret_repeat]")!;
  secret.focus();
  // A public key is pasted, not typed, so it has no repeat field.
  form.addEventListener("change", (event) => {
    const target = event.target as HTMLInputElement;
    if (target.name !== "mode") return;
    const passphrase = target.value === "passphrase";
    repeat.hidden = !passphrase;
    repeat.required = passphrase;
    repeat.value = "";
  });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const recipient = form.querySelector<HTMLInputElement>("input[name=mode]:checked")?.value === "recipient";
    // A typo here makes a file nobody can open, marked as a confirmed backup.
    if (!recipient && secret.value !== repeat.value) {
      showErrorToast("The two passphrases differ");
      repeat.value = "";
      return;
    }
    const value = secret.value;
    const again = repeat.value;
    secret.value = "";
    repeat.value = "";
    const button = form.querySelector<HTMLButtonElement>("button[type=submit]")!;
    button.disabled = true;
    button.textContent = "Reading and encrypting\u2026";
    try {
      const path = await ExportStateBackup(form.dataset.slug || "", recipient ? "" : value, recipient ? "" : again, recipient ? value : "");
      if (path) {
        showInfoToast(`Backup saved: ${path}`);
        await refreshState();
        return;
      }
    } catch (error) {
      showErrorToast(String(error));
    }
    button.disabled = false;
    button.textContent = "Encrypt and save";
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
      ${renderTopnav(active)}
      <div class="topbar-right">
        ${renderAddServiceButton(active)}
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
      if (next === "dashboard" || next === "wizard" || next === "catalog" || next === "settings" || next === "fleet" || next === "activity" || next === "import") {
        activeView = next;
        ({ step: wizardStep, drafts: wizardRouteDrafts } = wizardAfterNav(next, wizardStep));
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
  if ((current.services || []).some((service) => service.custom)) categories.push("custom");
  // The open editor's typed values live only in the DOM. Search, the filter tabs and
  // any refresh redraw this page, so the live form is put back over the redrawn copy.
  const liveEditor = document.querySelector<HTMLFormElement>("[data-custom-editor]");
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      <div class="main-content">
        ${topbar("catalog", current)}
        <main class="page-content">
          ${pageHead("Catalog")}
          ${renderCustomServicesSection(customViews, current.deployments || [], customEditing, customLoadError)}
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
            <p class="referral-note">Visit and Sign up links to providers with a referral programme are referral links: the CashPilot maintainer may earn a commission from them. Using them costs you nothing, sometimes gets you a sign-up bonus, and is the simplest way to support CashPilot.</p>
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
  wireCustomServices();
  const freshEditor = document.querySelector<HTMLFormElement>("[data-custom-editor]");
  if (liveEditor && freshEditor && keepsLiveEditor(liveEditor.dataset.customEditor, freshEditor.dataset.customEditor)) {
    freshEditor.replaceWith(liveEditor);
  }
  if (customViews === null && !customLoadError) void loadCustomViews();
}

async function loadCustomViews() {
  try {
    customViews = await ListCustomServices();
    customLoadError = "";
  } catch (error) {
    customViews = [];
    customLoadError = `Your custom services could not be read: ${String(error)}`;
  }
  if (activeView === "catalog") render();
}

// The custom-service list and editor above the catalog. The editor's typed values
// live only in the DOM until Save; renderCatalog carries the live form over a redraw.
function wireCustomServices() {
  document.querySelector<HTMLButtonElement>("[data-custom-new]")?.addEventListener("click", () => {
    customEditing = "new";
    render();
  });
  document.querySelectorAll<HTMLButtonElement>("[data-custom-edit]").forEach((button) => {
    button.addEventListener("click", () => {
      customEditing = customViews?.find((view) => view.slug === button.dataset.customEdit) || null;
      render();
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-custom-delete]").forEach((button) => {
    button.addEventListener("click", () => void deleteCustomService(button.dataset.customDelete || ""));
  });
  const editor = document.querySelector<HTMLFormElement>("[data-custom-editor]");
  if (!editor) return;
  const wireRow = (row: HTMLElement) => {
    row.querySelector("[data-custom-env-remove]")?.addEventListener("click", () => row.remove());
    const secret = row.querySelector<HTMLInputElement>("[data-custom-env-secret]");
    const value = row.querySelector<HTMLInputElement>("[data-custom-env-value]");
    secret?.addEventListener("change", () => {
      if (value) value.type = secret.checked ? "password" : "text";
    });
  };
  editor.querySelectorAll<HTMLElement>("[data-custom-env-row]").forEach(wireRow);
  editor.querySelector("[data-custom-env-add]")?.addEventListener("click", () => {
    const list = editor.querySelector<HTMLElement>("[data-custom-env]");
    if (!list) return;
    list.insertAdjacentHTML("beforeend", customEnvRow());
    const rows = list.querySelectorAll<HTMLElement>("[data-custom-env-row]");
    wireRow(rows[rows.length - 1]);
  });
  editor.querySelector("[data-custom-cancel]")?.addEventListener("click", () => {
    customEditing = null;
    render();
  });
  editor.querySelector("[data-custom-save]")?.addEventListener("click", () => void saveCustomService(editor));
}

async function saveCustomService(editor: HTMLFormElement) {
  const output = editor.querySelector<HTMLPreElement>("[data-custom-output]");
  const fields: Record<string, string> = {};
  editor.querySelectorAll<HTMLInputElement>("[data-custom-field]").forEach((input) => {
    fields[input.dataset.customField || ""] = input.value;
  });
  const rows: CustomEnv[] = [...editor.querySelectorAll<HTMLElement>("[data-custom-env-row]")].map((row) => ({
    key: row.querySelector<HTMLInputElement>("[data-custom-env-key]")?.value || "",
    value: row.querySelector<HTMLInputElement>("[data-custom-env-value]")?.value || "",
    secret: Boolean(row.querySelector<HTMLInputElement>("[data-custom-env-secret]")?.checked),
  }));
  try {
    const saved = await SaveCustomService(editor.dataset.customEditor || "", customDefinitionFrom(fields, rows));
    // From here the service exists: if the refresh below fails, a retry from this
    // form must update it, not add a second one.
    editor.dataset.customEditor = saved.slug;
    customEditing = saved;
    customViews = await ListCustomServices();
    state = await GetAppState();
    customEditing = null;
    render();
    const deployed = state?.deployments?.some((dep) => dep.slug === saved.slug);
    showInfoToast({scope: "custom service", message: `${saved.name} saved.${saved.warnings?.length ? ` ${saved.warnings.join(" ")}` : ""}${deployed ? " Redeploy it to apply the change." : ""}`});
  } catch (error) {
    if (output) output.textContent = String(error);
  }
}

async function deleteCustomService(slug: string) {
  const view = customViews?.find((item) => item.slug === slug);
  if (!view) return;
  const volume = view.volume ? ` Its data volume ${view.volume}, if you kept it when removing it, stays on this computer until you delete it with docker volume rm.` : "";
  if (!confirm(`Delete ${view.name}?${volume}`)) return;
  try {
    await DeleteCustomService(slug);
    customViews = await ListCustomServices();
    state = await GetAppState();
    render();
  } catch (error) {
    showErrorToast({scope: "custom service", error: String(error)});
  }
}

// The import page: the files go to PreviewImport for a dry run, the ticked rows to
// ApplyImport, and each saved service deploys through the normal DeployService.
type ImportForm = { picks: [string, boolean][]; extras: [string, string][]; paste: string };

function readImportForm(): ImportForm {
  return {
    picks: Array.from(document.querySelectorAll<HTMLInputElement>(".import-pick")).map((el) => [el.dataset.importSlug || "", el.checked]),
    extras: Array.from(document.querySelectorAll<HTMLInputElement>(".import-extra")).map((el) => [`${el.dataset.importSlug}|${el.dataset.importKey}`, el.value]),
    paste: document.querySelector<HTMLTextAreaElement>("#import-paste")?.value || "",
  };
}

function restoreImportForm(form: ImportForm) {
  const picks = new Map(form.picks);
  const extras = new Map(form.extras);
  document.querySelectorAll<HTMLInputElement>(".import-pick").forEach((el) => {
    const checked = picks.get(el.dataset.importSlug || "");
    if (checked !== undefined && !el.disabled) el.checked = checked;
  });
  document.querySelectorAll<HTMLInputElement>(".import-extra").forEach((el) => {
    const value = extras.get(`${el.dataset.importSlug}|${el.dataset.importKey}`);
    if (value !== undefined) el.value = value;
  });
  const paste = document.querySelector<HTMLTextAreaElement>("#import-paste");
  if (paste && form.paste) paste.value = form.paste;
}

function renderImportIfShown() {
  if (state && activeView === "import") renderImport(state);
}

function renderImport(current: AppState) {
  const keep = renderedImportPreview === importPreview ? readImportForm() : null;
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      <div class="main-content">
        ${topbar("settings", current)}
        <main class="page-content">
          ${pageHead("Import from another tool", `<button class="compact-btn" data-view="settings">Back to Settings</button>`)}
          ${renderImportInput(importSource)}
          ${importPreview ? renderImportPreview(importPreview) : ""}
          ${importResult && importResult.saved.length ? renderImportSaved(importResult, importPreview, importDeployed, importLinks) : ""}
        </main>
        ${pageFoot()}
      </div>
    </div>
  `;
  renderedImportPreview = importPreview;
  if (keep) restoreImportForm(keep);
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  document.querySelector<HTMLSelectElement>("#import-source")?.addEventListener("change", (event) => {
    importSource = (event.target as HTMLSelectElement).value;
  });
  document.querySelector("#import-preview-btn")?.addEventListener("click", () => void runImportPreview());
  document.querySelector("#import-apply-btn")?.addEventListener("click", () => void runImportApply());
  document.querySelectorAll<HTMLButtonElement>("[data-import-deploy]").forEach((button) => {
    button.addEventListener("click", () => void runImportDeploy(button.dataset.importDeploy || ""));
  });
}

async function readImportFiles(): Promise<ImportFile[]> {
  const files: ImportFile[] = [];
  const picked = Array.from(document.querySelector<HTMLInputElement>("#import-files")?.files || []);
  // Refuse by count and size before reading, so a huge file is never loaded.
  if (picked.length > IMPORT_MAX_FILES) throw new Error(`At most ${IMPORT_MAX_FILES} files at a time.`);
  if (picked.reduce((n, f) => n + f.size, 0) > IMPORT_MAX_BYTES) {
    throw new Error(`The files add up to more than ${IMPORT_MAX_BYTES / 1024} KiB.`);
  }
  // Strict UTF-8: file.text() would turn bad bytes into U+FFFD, which the
  // backend's UTF-8 check could then not see.
  const decoder = new TextDecoder("utf-8", { fatal: true });
  for (const file of picked) {
    // Refuse before reading a file that alone is over the limit.
    if (file.size > IMPORT_MAX_BYTES) throw new Error(`${file.name} is larger than 64 KiB; this is not a config file.`);
    let content: string;
    try {
      content = decoder.decode(await file.arrayBuffer());
    } catch {
      throw new Error(`${file.name} is not UTF-8 text.`);
    }
    files.push({ name: file.name, content });
  }
  const pasted = document.querySelector<HTMLTextAreaElement>("#import-paste")?.value || "";
  if (pasted.trim()) files.push({ name: "pasted text", content: pasted });
  // A redraw empties the file picker, so a second preview (another tool picked,
  // say) reuses the files last previewed when nothing new was added.
  if (!files.length && importRequest) files.push(...importRequest.files);
  const problem = importFilesProblem(files);
  if (problem) throw new Error(problem);
  return files;
}

async function runImportPreview() {
  const seq = ++importPreviewSeq;
  let files: ImportFile[];
  try {
    files = await readImportFiles();
  } catch (error) {
    showErrorToast({ scope: "import", error: error instanceof Error ? error.message : String(error) });
    return;
  }
  try {
    const preview = await PreviewImport({ source: importSource, files });
    if (seq !== importPreviewSeq) return;
    importRequest = { source: preview.source, files };
    importPreview = preview;
    importResult = null;
    importDeployed = [];
    importLinks = {};
    renderImportIfShown();
  } catch (error) {
    showErrorToast({ scope: "import", error: String(error) });
  }
}

async function runImportApply() {
  if (!importRequest) return;
  const picks = Array.from(document.querySelectorAll<HTMLInputElement>(".import-pick")).map((el) => ({
    slug: el.dataset.importSlug || "", checked: el.checked,
  }));
  const inputs = Array.from(document.querySelectorAll<HTMLInputElement>(".import-extra"));
  const { slugs, extra } = importChoices(picks, inputs.map((el) => ({
    slug: el.dataset.importSlug || "", key: el.dataset.importKey || "", value: el.value,
  })));
  if (!slugs.length) {
    showErrorToast({ scope: "import", error: "Tick at least one service." });
    return;
  }
  for (const input of inputs) {
    // Check what is sent: importChoices trims, so the pattern sees the trimmed value.
    input.value = input.value.trim();
    if (slugs.includes(input.dataset.importSlug || "") && input.value && !input.checkValidity()) {
      input.reportValidity();
      return;
    }
  }
  try {
    importResult = await ApplyImport({ ...importRequest, slugs, extra });
    importDeployed = [];
    state = await GetAppState();
    importLinks = {};
    showInfoToast({ scope: "import", message: `Saved ${importResult.saved.length} service(s).` });
    renderImportIfShown();
  } catch (error) {
    showErrorToast({ scope: "import", error: String(error) });
  }
}

async function runImportDeploy(slug: string) {
  // A second click while this deploy runs does nothing.
  if (!slug || importDeploying.has(slug)) return;
  importDeploying.add(slug);
  try {
    const name = importPreview?.rows.find((row) => row.slug === slug)?.name || slug;
    // The same pre-flight report the wizard shows, when there is one.
    const report = await PreflightService(slug).catch(() => null);
    const preflight = report?.summary ? `\n\n${report.summary}` : "";
    if (!confirm(`Deploy ${name} on this computer? Stop the old tool here first: two copies behind one connection earn as one.${preflight}`)) return;
    await DeployService(slug, {}, null, null);
    importDeployed = [...importDeployed, slug];
    state = await GetAppState();
    // The saved values hold the device id; the step is the one the wizard shows.
    const creds = await GetCredentials(slug).catch(() => ({} as Record<string, string>));
    const link = deviceLinkText(state?.services?.find((service) => service.slug === slug), creds, state?.addedDevices?.[slug] || "", state?.retryingDevices?.[slug] || "");
    if (link) importLinks = { ...importLinks, [slug]: link };
    renderImportIfShown();
  } catch (error) {
    showErrorToast({ scope: "deploy", error: String(error) });
  } finally {
    importDeploying.delete(slug);
  }
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

          <section class="card">
            <div class="card-header">
              <div>
                <span class="card-title">Import from another tool</span>
                <p class="muted compact-copy">Coming from money4band, CashFactory or InternetIncome? Bring your service logins over instead of typing them again.</p>
              </div>
              <button class="compact-btn" data-view="import">Import</button>
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
    CASHPILOT_DRIFT_WARNINGS: "driftWarnings",
    CASHPILOT_ISOLATION: "isolation",
    CASHPILOT_UPSTREAM_URL: "upstreamUrl",
    CASHPILOT_UPSTREAM_KEY: "upstreamKey",
    TZ: "timezone",
  };
  return names[key] || key;
}

async function renderActivity(current: AppState, view?: ActivityView) {
  const request = activityRequest;
  const page = view ? activityPage : ++activityPage;
  let data: ActivityView;
  try {
    data = view || (await GetActivity(activityDays));
  } catch (error) {
    if (page !== activityPage || request !== activityRequest || activeView !== "activity") return;
    showErrorToast({scope: "activity", error: String(error)});
    return;
  }
  if (page !== activityPage || activeView !== "activity" || request !== activityRequest) return;
  activityShown = data;
  root.innerHTML = `
    ${titlebar()}
    <div class="app-layout">
      <div class="main-content">
        ${topbar("activity", current)}
        <main class="page-content">
          ${pageHead("Activity")}
          ${renderActivityPage(data, activityOpen)}
        </main>
        ${pageFoot()}
      </div>
    </div>
  `;
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  // The actions answer with the 24-hour view; render the window the owner chose.
  const run = async (action: () => Promise<ActivityView>) => {
    try {
      await action();
      await renderActivity(current);
    } catch (error) {
      showErrorToast({scope: "activity", error: String(error)});
      void renderActivity(current);
    }
  };
  document.querySelector<HTMLInputElement>("#activity-toggle")?.addEventListener("change", (event) => {
    const on = (event.target as HTMLInputElement).checked;
    activityRequest++;
    if (on) {
      void run(() => SetActivityEnabled(on));
      return;
    }
    // Off deletes the addresses: drop the open rows' copies, refuse to open a
    // row until the delete is done, then drop anything opened meanwhile.
    activityOpen = {};
    activityDeleting++;
    // Take the open rows' addresses off the screen now, not when the delete returns.
    void renderActivity(current, {...data, enabled: false});
    void run(() => SetActivityEnabled(on)).finally(() => {
      activityDeleting--;
      activityOpen = {};
    });
  });
  document.querySelector<HTMLSelectElement>("#activity-days")?.addEventListener("change", (event) => {
    activityDays = Number((event.target as HTMLSelectElement).value) || 1;
    activityRequest++;
    activityOpen = {};
    void renderActivity(current);
  });
  document.querySelector("#activity-delete")?.addEventListener("click", () => {
    if (!window.confirm("Delete every Activity reading on this machine: counts, totals, addresses and alerts?")) return;
    activityRequest++;
    activityOpen = {};
    // As on switch-off: no row opens until the delete is done, and the open
    // rows' addresses leave the screen now.
    activityDeleting++;
    void renderActivity(current, data);
    void run(() => DeleteActivityData()).finally(() => {
      activityDeleting--;
      activityOpen = {};
    });
  });
  document.querySelector("#activity-networks")?.addEventListener("click", () => {
    showInfoToast({scope: "activity", message: "Downloading the network table (about 11 MB)."});
    void run(() => RefreshActivityNetworks());
  });
  document.querySelectorAll<HTMLButtonElement>("[data-activity-expand]").forEach((button) => {
    button.addEventListener("click", async () => {
      const name = button.dataset.activityExpand || "";
      if (activityDeleting > 0) return;
      if (Object.hasOwn(activityOpen, name)) {
        delete activityOpen[name];
        void renderActivity(current, data);
        return;
      }
      const open = activityOpen;
      open[name] = null;
      void renderActivity(current, data);
      let detail: ActivityDetail;
      try {
        detail = await GetActivityDetail(name, activityDays);
      } catch (error) {
        if (open !== activityOpen || open[name] !== null) return;
        delete open[name];
        showErrorToast({scope: "activity", error: String(error)});
        if (activeView === "activity" && activityShown) void renderActivity(current, activityShown);
        return;
      }
      // The window changed, the data was deleted or the row was closed meanwhile:
      // this answer belongs to a page that is gone.
      if (open !== activityOpen || open[name] !== null) return;
      open[name] = detail;
      // Redraw the newest view, not the one this click came from.
      if (activeView === "activity" && activityShown) void renderActivity(current, activityShown);
    });
  });
}

// Counts Fleet redraws, so one that finishes after a newer one never writes the page.
let fleetRequest = 0;

async function renderFleet(current: AppState) {
  const request = ++fleetRequest;
  let fleet: FleetState;
  try {
    fleet = await GetFleetState();
  } catch (error) {
    showErrorToast({scope: "fleet", error: String(error)});
    return;
  }
  // Kept local until this redraw is known to be current: a stale one must not replace
  // the state wireProxies opens editors from.
  let proxies: ProxiesState | null;
  try {
    proxies = await GetProxiesState();
  } catch (error) {
    proxies = null;
    showErrorToast({scope: "proxies", error: String(error)});
  }
  if (!fleetRedrawIsCurrent(request, fleetRequest, activeView)) return;
  proxiesState = proxies;
  // The open proxy editor's typed values live only in the DOM; see keepsLiveProxyEditor.
  const liveEditor = document.querySelector<HTMLFormElement>("[data-proxy-editor]");
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

          ${proxiesState ? renderProxiesSection(proxiesState, proxyEditing) : ""}
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
  wireProxies();
  const freshEditor = document.querySelector<HTMLFormElement>("[data-proxy-editor]");
  if (liveEditor && freshEditor && keepsLiveProxyEditor(liveEditor.dataset.proxyEditor, freshEditor.dataset.proxyEditor)) {
    freshEditor.replaceWith(liveEditor);
  }
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

// The Proxies section's buttons. Every action re-renders the Fleet page, which reads
// the proxies again, so what is shown is always what the backend now holds.
function wireProxies() {
  const on = (selector: string, handler: (button: HTMLButtonElement) => Promise<void> | void) => {
    document.querySelectorAll<HTMLButtonElement>(selector).forEach((button) => {
      button.addEventListener("click", () => void runProxyAction(button, handler));
    });
  };
  const proxyById = (id: number) => proxiesState?.proxies.find((p) => p.id === id) || null;
  on("[data-proxy-new]", () => { proxyEditing = "new"; });
  on("[data-proxy-edit]", (b) => { proxyEditing = proxyById(Number(b.dataset.proxyEdit)); });
  on("[data-proxy-cancel]", () => { proxyEditing = null; });
  on("[data-proxy-save]", async () => {
    const form = document.querySelector<HTMLFormElement>("[data-proxy-editor]");
    if (!form) return;
    const fields: Record<string, string> = {};
    form.querySelectorAll<HTMLInputElement | HTMLSelectElement>("[data-proxy-field]").forEach((el) => {
      const name = el.dataset.proxyField || "";
      fields[name] = el instanceof HTMLInputElement && el.type === "checkbox" ? String(el.checked) : el.value;
    });
    await SaveProxy(Number(form.dataset.proxyEditor || 0), proxyValuesFrom(fields));
    proxyEditing = null;
  });
  on("[data-proxy-test]", async (b) => {
    const result = await TestProxy(Number(b.dataset.proxyTest));
    showInfoToast({scope: "proxy test", message: `Exit IP ${result.exitIp}${result.country ? ` (${result.country})` : ""}, in ${result.ms} ms.`});
  });
  on("[data-proxy-delete]", async (b) => {
    if (!confirm("Delete this proxy?")) return;
    await DeleteProxy(Number(b.dataset.proxyDelete));
  });
  on("[data-proxy-reset]", async (b) => {
    const started = await ResetProxyUsage(Number(b.dataset.proxyReset));
    showInfoToast({scope: "proxy", message: started.length ? `New period started; started ${started.join(", ")}.` : "New period started."});
  });
  on("[data-proxy-copy-stop]", (b) => StopProxiedCopy(b.dataset.slug || "", Number(b.dataset.proxyCopyStop)));
  on("[data-proxy-copy-start]", (b) => StartProxiedCopy(b.dataset.slug || "", Number(b.dataset.proxyCopyStart)));
  on("[data-proxy-copy-remove]", async (b) => {
    if (!confirm("Remove this proxied copy?")) return;
    await RemoveProxiedCopy(b.dataset.slug || "", Number(b.dataset.proxyCopyRemove));
  });
  on("[data-proxy-run-deploy]", async () => {
    const slug = valueOf("[data-proxy-run-service]");
    const id = Number(valueOf("[data-proxy-run-proxy]"));
    if (!slug || !id) return;
    const result = await DeployProxiedCopy(slug, id);
    showInfoToast({scope: "proxy", message: `Running through the proxy, exit IP ${result.exitIp || "not seen yet"}.${result.warning ? ` ${result.warning}` : ""}`});
  });
}

async function runProxyAction(button: HTMLButtonElement, handler: (button: HTMLButtonElement) => Promise<void> | void) {
  button.disabled = true;
  try {
    await handler(button);
    render();
  } catch (error) {
    showErrorToast({scope: "proxies", error: String(error)});
    button.disabled = false;
  }
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
  wizardRouteDrafts = {};
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
  document.querySelectorAll<HTMLSelectElement>("[data-wizard-route]").forEach((select) => {
    select.addEventListener("change", () => {
      wizardRouteDrafts[select.dataset.wizardRoute || ""] = routeChoice(select.value);
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
  selectedServices.forEach((service) => void hydratePayouts(service));
}

// The payout ledger under a service's card: load it, then wire its record form
// and Remove buttons. A record or a removal refreshes the summary in place (not a
// full render, which would drop credentials typed but not saved yet).
async function hydratePayouts(service: Service) {
  const box = document.querySelector<HTMLElement>(`[data-payouts-slug="${CSS.escape(service.slug)}"]`);
  if (!box) return;
  const payouts = await ListPayouts(service.slug).catch(() => null);
  box.innerHTML = payoutLedger(service, payouts);
  box.querySelector<HTMLButtonElement>("[data-record-payout]")?.addEventListener("click", () => void recordPayout(service, box));
  box.querySelectorAll<HTMLButtonElement>("[data-remove-payout]").forEach((button) => {
    button.addEventListener("click", () => void removePayout(service, Number(button.dataset.removePayout)));
  });
}

async function recordPayout(service: Service, box: HTMLElement) {
  const field = (name: string) => box.querySelector<HTMLInputElement>(`[data-payout-field="${name}"]`)?.value.trim() || "";
  const amount = Number(field("amount"));
  if (!(amount > 0)) {
    showErrorToast({scope: "cash-out", error: "Enter the amount that was cashed out."});
    return;
  }
  try {
    const payout = await RecordPayout(service.slug, amount, field("currency"), field("method"), field("date"));
    if (payout.fxRateUsd === null || payout.fxRateUsd === undefined) {
      showInfoToast({scope: "cash-out", message: "Recorded. There is no exchange rate for that day, so it is left out of the total."});
    }
    await afterPayoutChange(service);
  } catch (error) {
    showErrorToast({scope: "cash-out", error: String(error)});
  }
}

async function removePayout(service: Service, id: number) {
  if (!confirm("Remove this cash-out? It will no longer count toward what you earned all time.")) return;
  try {
    await RemovePayout(id);
    await afterPayoutChange(service);
  } catch (error) {
    showErrorToast({scope: "cash-out", error: String(error)});
  }
}

async function afterPayoutChange(service: Service) {
  await hydratePayouts(service);
  const summary = await GetEarningsSummary().catch(() => null);
  if (state && summary) state = {...state, summary};
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
      ${services.map((service) => renderWizardServiceSetup(service, state?.collectorFields, state?.hostname, state?.summary?.breakdown, deploymentOf(service.slug)?.customImage, state?.scaling?.[service.slug], deploymentOf(service.slug)?.instances, state?.route, wizardRouteDrafts[service.slug])).join("")}
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

// The Route box's answer: a proxy id, or null for this computer's own IP.
function readWizardRoute(slug: string): number | null {
  const select = document.querySelector<HTMLSelectElement>(`[data-form-slug="${slug}"] [data-wizard-route]`);
  return select ? routeChoice(select.value) : null;
}

function closeWizard() {
  activeView = "dashboard";
  wizardStep = 1;
  wizardRouteDrafts = {};
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
      // A custom service has no credentials: it runs its saved definition.
      const custom = state?.services?.some((service) => service.slug === slug && service.custom);
      const image = readWizardImage(slug);
      const instances = readWizardInstances(slug);
      const outcome = await runRouteDeploy(slug, readWizardRoute(slug), image, instances, state?.route, {
        save: async () => {
          if (!custom) await SaveCredentials(slug, values);
        },
        confirm: (message) => confirm(message),
        routed: (proxyId) => DeployServiceRouted(slug, values, proxyId),
        direct: (leaveRoute) => leaveRoute
          ? DeployServiceLeavingRoute(slug, values, image, instances)
          : DeployService(slug, values, image, instances),
      });
      if (outcome.kind === "refused" || outcome.kind === "cancelled") {
        if (output) output.textContent = outcome.kind === "refused" ? outcome.message : "Nothing was deployed.";
        return;
      }
      if (outcome.kind === "routed") {
        state = await GetAppState();
        const link = deviceLinkText(state?.services?.find((service) => service.slug === slug), values, state?.addedDevices?.[slug] || "", state?.retryingDevices?.[slug] || "");
        if (output) output.textContent = routedDeployText(outcome.result, link);
        return;
      }
      const deployment = outcome.deployment;
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
      const link = deviceLinkText(state?.services?.find((service) => service.slug === slug), values, state?.addedDevices?.[slug] || "", state?.retryingDevices?.[slug] || "");
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

// The two buttons on a "Needs fixing" row. Redeploy sends no credentials, no image and
// no copy count, so the deploy keeps the saved credentials, the image in use and the
// number of copies, and rebuilds the rest from the current catalog.
async function runDriftAction(slug: string, action: string) {
  const name = state?.services.find((svc) => svc.slug === slug)?.name || slug;
  try {
    if (action === "redeploy") {
      if (!confirm(`Redeploy ${name}? It keeps the settings it was deployed with and its saved data.`)) return;
      setOutput(`Redeploying ${name}...`);
      await DeployService(slug, {}, null, null);
      await refreshState();
      setOutput(`${name} redeployed.`);
      return;
    }
    if (action === "ignore") {
      await IgnoreDrift(slug);
      await refreshState();
      setOutput(`${name}: warning ignored until the catalog changes it again.`);
    }
  } catch (error) {
    setOutput(String(error));
  }
}

// "Turn on isolation": the fence goes up, then each running earner is redeployed onto
// the fenced network with the credentials, image and copies it already has.
async function runEnableIsolation() {
  const names = state?.isolation?.outside || [];
  if (!confirm(`Turn on LAN isolation? CashPilot redeploys ${names.length === 1 ? "1 running service" : `${names.length} running services`} onto its fenced network. Each keeps its settings and saved data, and is offline for a few seconds.`)) return;
  try {
    setOutput("Turning on LAN isolation...");
    const result = await EnableIsolation();
    await refreshState();
    const failed = Object.entries(result.failed || {});
    const moved = result.moved || [];
    // The status after the moves, not the switch: the last check can find the fence
    // down, or Settings turned off meanwhile.
    const after = result.status;
    // The fence being up does not mean every service is behind it: say the fence is
    // applied, and name any service that did not move as still able to reach the LAN.
    const head = after && after.mode === "on" && after.state === "applied"
      ? "The LAN fence is up."
      : `Isolation is not in place (mode ${after?.mode || "unknown"}, fence ${after?.state || "unknown"}${after?.error ? `: ${after.error}` : ""}).`;
    setOutput(failed.length
      ? `${head} Moved behind it: ${moved.join(", ") || "none"}. Not moved, still able to reach your LAN: ${failed.map(([slug, why]) => `${slug} (${why})`).join("; ")}.`
      : `${head} Moved behind it: ${moved.join(", ") || "none"}.`);
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
