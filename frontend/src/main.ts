import "./style.css";
import {
  BrowserOpenURL,
  EventsOn,
  Quit,
  WindowMinimise,
  WindowToggleMaximise,
} from "../wailsjs/runtime/runtime";
import {
  SecureVMInstall,
  SecureVMOpenScreen,
  SecureVMPlace,
  SecureVMStart,
  SecureVMStatus,
  SecureVMStop,
  SecureVMUndo,
  SecureVMVerify,
  AddFleetDevice,
  CommandWorkerCopy,
  DeployToWorker,
  BackgroundHelperStatus,
  DeleteActivityData,
  GetActivity,
  GetActivityDetail,
  RefreshActivityNetworks,
  SetActivityEnabled,
  SetServerCommands,
  AllowServerCommandsThroughFirewall,
  RetryServerCommandsFirewallRemoval,
  InstallBackgroundHelper,
  ListCustomServices,
  ApplyImport,
  PreviewImport,
  RemoveBackgroundHelper,
  ChooseRestoreFile,
  CollectService,
  CompleteOnboarding,
  ConfirmStateBackup,
  DeleteCustomService,
  DeleteProxy,
  DeployProxiedCopy,
  DeployService,
  DeployServiceLeavingRoute,
  DeployServiceRouted,
  DeployServiceWallet,
  RevealWalletSeed,
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
  GetSettingsState,
  GetUsage,
  HostCheck,
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
  UnfinishedRestores,
  RestoreStateBackup,
  DiscardPartialRestore,
  AcceptSessionRisk,
  RevokeSessionRisk,
  ReminderAction,
  DeployAdminStep,
  DeployServiceWithAdminApproval,
  GetModeState,
  JoinCashPilot,
  StartAsLead,
  SwitchToLead,
  StartDockerDesktop,
  DockerDesktopEngineReady,
  OpenWindowsUpdate,
  RestartToFirmware,
  SetupResume,
  CancelSetup,
  RunSetupStep,
  RestartNow,
  SignOutNow,
  GetLanguageState,
  SetLanguage,
} from "../wailsjs/go/main/App";
import { GetImageUpdates, RunImageUpdates, SaveImageUpdates } from "../wailsjs/go/main/App";
import { currentLocale, ht, setLocale, t, textDirection } from "./render/i18n";
import { renderLanguageCard } from "./render/language";
import { renderFleetSection } from "./render/fleet";
import { renderServerCommands } from "./render/server_commands";
import { wireServerCommandsFirewall } from "./render/server_commands_wire";
import { renderFleetSnippets, type FleetSnippet } from "./render/fleet_snippets";
import { flashCopied, wireCopyText } from "./render/copy";
import { createBoot } from "./render/boot";
import { firstRunSteps, renderGateSteps, renderJoinOutcome, renderJoinedBanner, renderModeCard, type Mode, type ModeCardView } from "./render/mode";
import { gatePage as chromeGatePage, withChrome as chromeWithChrome, windowPlatform, type WindowPlatform } from "./render/chrome";
import { firstRunScreen } from "./render/firstrun";
import { fleetRedrawIsCurrent, keepsLiveProxyEditor, proxyValuesFrom, renderProxiesSection } from "./render/proxies";
import { renderActivityPage } from "./render/activity";
import { attentionItems, renderAttention } from "./render/attention";
import { renderReminders, withReminders } from "./render/reminders";
import { ImageUpdateEdits, draftOf, drawsOver, renderImageUpdatesCard } from "./render/imageupdates";
import { stallItems } from "./render/stall";
import { adminConsentScreen, cancelledText, deployErrorText, deployNotice, renderDeployFeedback, runCheckedDeploy, runDeployWithStatus, type DeployAnswer, type DeployFeedback, type DeployStatus } from "./render/deploy_consent";
import { serviceFormFields } from "./render/fields";
import { renderStallServerNote } from "./render/stall_server";
import { backupExportForm, renderStateBackups } from "./render/backups";
import { renderRestoreCard, renderRestorePreview, restoreResultText } from "./render/restore";
import { renderEnvSetting, settingField, settingsSaveValues, unsavedDrafts, type SettingField } from "./render/settings";
import { runSave, saveNoteSlot } from "./render/savenote";
import { renderEarningsBars } from "./render/chart";
import { renderUsagePanel } from "./render/usage";
import { renderServicesTable } from "./render/services";
import { catalogFilterTabs, kindLabel, renderCatalogTable, wizardCategoryChoices, wizardCategoryCopy } from "./render/catalog";
import { wireCatalogRows } from "./render/catalog_open";
import { wireOpenLinks } from "./render/open_links";
import { renderAddServiceButton, renderTopnav } from "./render/topbar";
import { localDockerGap, renderPreflight } from "./render/preflight";
import { escapeHtml, formatBalance } from "./render/format";
import { renderWizardServiceSetup } from "./render/wizard";
import { phraseWords, pickPositions, renderPhrasePanel, renderRevealPanel, seedFileVar, walletChoiceRefusal, walletDeployAnswer, wordsMatch, type PhrasePanelView, type RevealView } from "./render/wallet";
import { renderSessionConsentCard, renderSessionSlot } from "./render/session";
import { oneAtATime, wireSessionConsent as wireSessionConsentGate } from "./render/session_wire";
import { imageChoice } from "./render/image";
import { runtimeCard } from "./render/onboarding";
import { checkAgainButton, setupFinished } from "./render/dockerguide";
import { DockerGuide, type DockerGuideDeps } from "./render/dockerguide_wire";
import { renderSecureVM, unreadSecureVM } from "./render/securevm";
import type { SecureVMRemoveResult, SecureVMStatus as SecureVMReading } from "./wails";
import { guidesBesideChecklist, loadRuntimePage, unknownRuntime, unknownRuntimeNotice, withRuntimeReading } from "./render/runtimepage";
import { dockerBlockedNote, renderRuntimeSettings, runtimeProviderNote, runtimeState } from "./render/runtime_state";
import { instancesChoice } from "./render/instances";
import { routeChoice, routedDeployText, runRouteDeploy, wizardAfterNav } from "./render/route";
import { runWorkerDeploy, whereChoice, workerDeployText } from "./render/where";
import { copyCommandQuestion, workerCopiesSection } from "./render/workercopies";
import { composeSelectionExport, deviceLinkText, payoutLedger, renderPayoutFacts } from "./render/details";
import { allTimeNote, totalText, totalCaption } from "./render/total";
import { renderFleetConnect } from "./render/fleet_reach";
import { renderFenceStatusLine } from "./render/fence_status";
import { deleteDataConfirmText, removalChoice, removeConfirmText, type RemovalChoice } from "./render/removal";
import { customDefinitionFrom, customEnvRow, keepsLiveEditor, renderCustomServicesSection } from "./render/custom";
import { renderLogLines } from "./render/logs";
import { IMPORT_MAX_BYTES, IMPORT_MAX_FILES, importChoices, importFilesProblem, renderImportInput, renderImportPreview, renderImportSaved } from "./render/importer";
import type { CustomEnv, LanguageState, CustomServiceView, ImportFile, ImportPreview, ImportRequest, ImportResult, ProxiesState, ProxyView } from "./wails";
import type { ActivityDetail, AdminStep, ActivityView, AppState, BackgroundStatus, Deployment, FleetState, HostCheckResult, InstallGuide, JoinResult, LeaveResult, ModeState, NotificationItem, PointsBalance, PreflightReport, RouteState, RuntimeStatus, Service, SessionConsent, SettingsState, SetupResumeView, UnfinishedRestore, UsageView, WalletChoice, WorkerTarget } from "./wails";

let state: AppState | null = null;
// The language picker's state, from Go. null until the first read.
let languageState: LanguageState | null = null;
let selectedService: Service | null = null;
let onboardingStep: "welcome" | "mode" | "join" | "runtime" = "welcome";
// The path the first run is on: "lead" (Run everything here), "follower" (Join my
// CashPilot), or "" before the choice. It decides which steps the bar shows.
let firstRunPath: Mode = "";
// A first run that chose a mode on an earlier start carries on from the runtime step.
let onboardingResumeChecked = false;
// First run's reading of this PC for the runtime step, once it has landed (or
// failed), and whether "Check again" is reading now.
let firstRunHost: HostCheckResult | null = null;
let firstRunHasChecklist = false;
let firstRunHostTried = false;
let firstRunChecking = false;
// A guided setup a restart interrupted (render/dockerguide.ts), and the checklist
// on screen, if any.
let setupResume: SetupResumeView | null = null;
let dockerGuide: DockerGuide | null = null;
// The Settings card "How this computer runs": its screen, its state, and what the
// last switch to Run everything here said to do on the server.
let modeCardView: ModeCardView = "summary";
let modeState: ModeState | null = null;
let modeLeft: LeaveResult | null = null;
type View = "dashboard" | "wizard" | "catalog" | "settings" | "fleet" | "activity" | "import" | "runtime" | "security";

let activeView: View = "dashboard";
let wizardStep = 1;
let wizardCategories: string[] = [];
let wizardSelected: string[] = [];
// The Route box's pick per slug (a proxy id, null for this computer's own IP), kept so
// a redraw of the wizard does not put an unsaved proxy choice back to Direct. Cleared
// when the wizard opens or closes, and on a tab or Add service click (wizardAfterNav).
let wizardRouteDrafts: Record<string, number | null> = {};
// The Where box's pick per slug (a worker id, null for this computer), kept and
// cleared as the Route box's picks are (render/where.ts).
let wizardWhereDrafts: Record<string, number | null> = {};
const wizardPreflightDraws = new Map<string, () => void>();
const wizardDeployFeedback: Record<string, DeployFeedback> = {};
const wizardDeploying = new Map<string, WizardDeploySettings>();
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
let importDeploys: Record<string, DeployFeedback> = {};
const driftDeploys: Record<string, DeployStatus> = {};
let proxyDeployStatus: DeployStatus | undefined;
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
// The Docker status Import, Fleet and Activity were last drawn with. Their Docker
// controls and lines follow it, so a change redraws them (updateRuntimeSurfaces).
let dockerDrawn = "";

function dockerKey(runtime: RuntimeStatus) {
  return `${runtime.available}|${runtimeState(runtime).kind}|${(runtime.nativeServices || []).join(",")}`;
}
let activityOpen: Record<string, ActivityDetail | null> = {};
// Deletes still running (switch-off or Delete data): rows do not open meanwhile.
let activityDeleting = 0;
// Moves on with every Settings draw, so an answer that arrives after the user left
// Settings, or after a newer draw began, is dropped instead of drawn over the page.
let settingsRequest = 0;

const root = document.querySelector<HTMLDivElement>("#app")!;

// The title bar draws the window controls of this OS (render/chrome.ts): the
// webview's user agent says which.
const chromePlatform: WindowPlatform = windowPlatform(navigator.userAgent);
const withChrome = (body: string) => chromeWithChrome(body, chromePlatform);
const gatePage = (bar: string, panel: string, opts: {wide?: boolean} = {}) => chromeGatePage(bar, panel, {...opts, platform: chromePlatform});

// The window can load before the Go side has finished starting (render/boot.ts):
// wait for it on a calm page, and keep a Try again on the failed page.
const boot = createBoot({
  root,
  chrome: withChrome,
  wireChrome,
  load: async () => {
    wireBackendEvents();
    await loadLanguage();
    await loadAppState();
    await applySetupResume();
    render();
    if (hostReadingPending) {
      hostReadingPending = false;
      void refreshRuntimeSnapshot();
    }
  },
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  now: () => Date.now(),
});

let backendEventsWired = false;

let eventRefreshInFlight = false;
let hostReadingPending = false;
let runtimeCheckStarted = 0;
let runtimeCheckCompleted = 0;

// A newer check supersedes an older one once it finishes, even if it failed.
function completeRuntimeCheck(request: number) {
  if (request < runtimeCheckCompleted) return false;
  runtimeCheckCompleted = request;
  return true;
}

// The language comes from Go: the saved choice, else the OS language, else the
// webview's. A failed read keeps English, which is bundled.
async function loadLanguage() {
  try {
    applyLanguage(await GetLanguageState(webviewLanguages()));
  } catch {
    applyLanguage(null);
  }
}

function webviewLanguages(): string[] {
  return [...(navigator.languages ?? [])];
}

function applyLanguage(next: LanguageState | null) {
  languageState = next;
  setLocale(next?.active ?? "en", next?.messages ?? {});
  const html = document.documentElement;
  if (html) {
    html.lang = currentLocale();
    html.dir = textDirection();
  }
}

// Picks reach Go one at a time and in order, so the saved choice is always
// the last one made. A pick that a later one has superseded is not drawn.
let languageRequest = 0;
let languageQueue: Promise<void> = Promise.resolve();

function chooseLanguage(code: string) {
  const request = ++languageRequest;
  languageQueue = languageQueue.then(() => pickLanguage(code, request));
  return languageQueue;
}

async function pickLanguage(code: string, request: number) {
  let next: LanguageState;
  try {
    next = await SetLanguage(code, webviewLanguages());
  } catch (error) {
    showErrorToast({scope: "language", error: String(error)});
    // Draw what Go holds now, so the picker and the page match the saved choice.
    if (request === languageRequest) {
      await loadLanguage();
      render();
    }
    return;
  }
  if (request !== languageRequest) return;
  applyLanguage(next);
  // The catalog text and the reminders come from Go in the language Go holds,
  // so read the state again: without this they stay in the old language until
  // the next background refresh. A failed read still switches the page.
  try {
    await loadAppState();
  } catch {
    // The next state read brings the catalog text in the new language.
  }
  if (request !== languageRequest) return;
  // Everything on screen is drawn from the new language at once.
  render();
}

// The Go backend emits these after background collection cycles and deployment
// changes. Refresh state so passive earnings appear without the user navigating.
function wireBackendEvents() {
  // Once: a Try again after a failed start runs boot again.
  if (backendEventsWired) return;
  backendEventsWired = true;
  EventsOn("earnings:changed", () => void onBackendEvent());
  EventsOn("deployment:changed", () => void onBackendEvent());
  EventsOn("runtime:host-checked", () => {
    if (state) void refreshRuntimeSnapshot();
    else hostReadingPending = true;
  });
  // Background/startup failures the Go side reports via app:error had no listener,
  // so ~every background error was silently dropped. Surface them to the user.
  // A finished run can change the bell too (an undone or a new build), so the
  // app state is read again on every page.
  EventsOn("imageupdates:changed", () => {
    void onBackendEvent();
    if (activeView === "settings") void loadImageUpdates();
  });
  EventsOn("app:error", (payload) => showErrorToast(payload));
  EventsOn("app:notice", (payload) => showInfoToast(payload));
  // Windows reopened CashPilot after a restart while this window was open.
  EventsOn("setup:resume", () => void applySetupResume().then(() => {
    if (dockerGuide) dockerGuide.showResume(setupResume);
    else render();
  }));
}

// A setup a restart interrupted, or a start with --resume-setup, lands on the
// Docker checklist, which reads every step again.
async function applySetupResume() {
  const resume = await SetupResume().catch(() => null);
  if (!resume?.active) return;
  setupResume = resume;
  if (state?.config.firstRunComplete) activeView = "runtime";
  else onboardingStep = "runtime";
}

// Publish every explicit host check to the shared runtime, even when it fails.
// A reading that a newer one replaced while it ran is not returned: its checklist
// rows would contradict the status on screen (a late "ready" after an earnings
// refresh read "stopped"). The newer reading may have had no rows, so read again.
async function hostCheck(): Promise<HostCheckResult> {
  for (let attempt = 1; ; attempt++) {
    const request = ++runtimeCheckStarted;
    const publish = (result: HostCheckResult | null) => {
      if (!state || !completeRuntimeCheck(request)) return false;
      if (!state.config.firstRunComplete) {
        firstRunHost = result;
        if (result) firstRunHasChecklist = !!result.facts?.supported && !!result.steps?.length;
      }
      if (result && setupFinished(result)) setupResume = null;
      setRuntimeReading(result?.runtime ?? unknownRuntime, result ? result.runtimeNotice : unknownRuntimeNotice);
      return true;
    };
    let result: HostCheckResult;
    try {
      result = await HostCheck();
    } catch (error) {
      publish(null);
      throw error;
    }
    if (publish(result) || !state) return result;
    if (attempt === 3) throw new Error(t("main.hostCheck.changed"));
  }
}

// What the checklist needs from Go. Skip differs per screen.
function guideDeps(skip: DockerGuideDeps["skip"]): DockerGuideDeps {
  return {
    hostCheck,
    currentRuntime: () => state?.runtime ?? unknownRuntime,
    startDocker: StartDockerDesktop,
    dockerEngineReady: DockerDesktopEngineReady,
    openWindowsUpdate: OpenWindowsUpdate,
    restartToFirmware: RestartToFirmware,
    runSetupStep: RunSetupStep,
    restartNow: RestartNow,
    signOutNow: SignOutNow,
    cancelSetup: async () => {
      await CancelSetup();
      setupResume = null;
    },
    openUrl: (url) => {
      if (url) BrowserOpenURL(url);
    },
    failed: (error) => showErrorToast({scope: "runtime", error: String(error)}),
    skip,
  };
}

// App-state reads check Docker too. Apply the result here, so a caller resuming
// after the await cannot replace a runtime reading published in the meantime.
async function loadAppState(scope: "all" | "runtime" = "all") {
  const request = ++runtimeCheckStarted;
  let next: AppState;
  try {
    next = await GetAppState();
  } catch (error) {
    if (completeRuntimeCheck(request)) setRuntimeReading(unknownRuntime, unknownRuntimeNotice);
    if (scope === "all") throw error;
    return;
  }

  const previous = state;
  if (completeRuntimeCheck(request)) {
    state = scope === "runtime" && previous
      ? { ...previous, runtime: next.runtime, notifications: next.notifications }
      : next;
  } else if (scope === "all" && previous) {
    const notice = previous.notifications?.find((item) => item.runtime) ?? null;
    state = { ...next, ...withRuntimeReading(next, previous.runtime, notice) };
  } else {
    return;
  }
  // The caller draws the first screen and the transition out of onboarding.
  if (previous && (previous.config.firstRunComplete || !state.config.firstRunComplete)) updateRuntimeSurfaces(state);
}

async function onBackendEvent() {
  if (eventRefreshInFlight) return;
  if (!state || !state.config.firstRunComplete) return;
  eventRefreshInFlight = true;
  try {
    await loadAppState();
  } catch {
    // transient background refresh failure — ignore; next event/read recovers
  } finally {
    eventRefreshInFlight = false;
  }
}

function render() {
  if (!state) return;
  // Whatever was on screen is replaced: a checklist there stops drawing.
  dockerGuide?.dispose();
  dockerGuide = null;
  wizardPreflightDraws.clear();
  // A seed phrase on screen goes with the view it was shown in.
  if (activeView !== "dashboard") hideRevealedSeed();
  if (activeView !== "wizard") dropNewWalletPhrase();
  if (activeView !== "wizard" || wizardStep !== 3) closeWizardDeployDrafts();
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
    void renderFleet();
    return;
  }
  if (activeView === "activity") {
    void renderActivity();
    return;
  }
  if (activeView === "import") {
    renderImport(state);
    return;
  }
  if (activeView === "runtime") {
    renderRuntimeView(state);
    return;
  }
  if (activeView === "security") {
    renderSecurityView(state);
    return;
  }
  renderDashboard(state);
}

function renderOnboarding(current: AppState) {
  if (!onboardingResumeChecked) {
    onboardingResumeChecked = true;
    if (current.config.mode === "lead" || current.config.mode === "follower") {
      firstRunPath = current.config.mode;
      onboardingStep = "runtime";
    }
  }
  if (onboardingStep === "welcome") {
    renderWelcome();
    return;
  }
  if (onboardingStep === "mode") {
    renderModeStep();
    return;
  }
  if (onboardingStep === "join") {
    renderJoinStep();
    return;
  }
  const runtime = current.runtime;
  // Until the first reading of this PC lands, the card says it is checking: the
  // generic message would tell a PC with virtualization off to open Docker.
  const checking = !runtime.available && !firstRunHostTried;
  if (checking) void firstRunHostCheck();
  const checklist = firstRunHasChecklist;
  // Onboarding proceeds when Docker answers, or when the native runtime can run at
  // least one catalog app here. Native-only is never called ready: the card says
  // most apps need Docker and points at its guide (render/onboarding.ts).
  const card = runtimeCard(runtime, {checking});
  root.innerHTML = gatePage(gateBar(), `
          ${renderGateSteps(firstRunSteps(firstRunPath), "runtime")}
          <h1 class="gate-title">${ht("main.onboarding.title")}</h1>
          <p class="gate-lede">${ht("main.onboarding.lede")}</p>
          <div id="runtime-card-slot">${card.html}</div>
          <div class="gate-actions">
            <button class="btn btn-primary btn-lg" id="continue-btn" ${card.canContinue ? "" : "disabled"}>${ht("main.onboarding.openDashboard")}</button>
            ${checklist ? "" : checkAgainButton("refresh-runtime", firstRunChecking || checking)}
          </div>
          <div id="docker-guide"></div>
          <div id="install-guides" class="guide-grid"></div>
  `, {wide: true});
  wireChrome();
  document.querySelector("#continue-btn")?.addEventListener("click", finishOnboarding);
  document.querySelector("#refresh-runtime")?.addEventListener("click", async () => {
    if (firstRunChecking) return;
    // HostCheck reads this PC afresh, so a fix the user just made shows at once.
    // The button is busy and disabled until it answers.
    firstRunChecking = true;
    render();
    try {
      await hostCheck();
    } catch (error) {
      showErrorToast({scope: "runtime", error: String(error)});
    } finally {
      firstRunChecking = false;
      render();
    }
  });
  const holder = document.querySelector<HTMLDivElement>("#docker-guide");
  if (checklist && holder) {
    dockerGuide = new DockerGuide(holder, guideDeps(finishOnboarding), {resume: setupResume, result: firstRunHost, checkFailed: !firstRunHost});
  }
  renderGuides(guidesBesideChecklist(current.guides || [], checklist));
}

async function finishOnboarding() {
  await CompleteOnboarding();
  await loadAppState();
  render();
}

// First run's first reading of this PC. It lands after the first draw, so the
// runtime step draws again with the reason and the checklist it brings.
let firstRunHostPending = false;
async function firstRunHostCheck() {
  if (firstRunHostPending) return;
  firstRunHostPending = true;
  try {
    await hostCheck();
  } catch {
    // hostCheck has already published the unknown status.
  } finally {
    firstRunHostTried = true;
    firstRunHostPending = false;
    if (state && !state.config.firstRunComplete && onboardingStep === "runtime") render();
  }
}

// The runtime page: the runtime card, then on Windows the Docker checklist and
// elsewhere the install guides. The runtime status in the top bar opens it.
function renderRuntimeView(current: AppState) {
  const card = runtimeCard(current.runtime);
  root.innerHTML = withChrome(`
    <div class="app-layout">
      <div class="main-content">
        ${topbar("runtime", current)}
        <main class="page-content">
          ${pageHead(t("main.runtime.title"))}
          <div id="runtime-card-slot">${card.html}</div>
          <div id="docker-guide"><p class="muted compact-copy">${ht("main.runtime.checking")}</p></div>
          <div id="install-guides" class="guide-grid"></div>
        </main>
        ${pageFoot()}
      </div>
    </div>
  `);
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  const holder = document.querySelector<HTMLDivElement>("#docker-guide");
  const guides = current.guides || [];
  const runtimeViewCurrent = () => activeView === "runtime" && !!holder?.isConnected;
  // The checklist reads the PC when it opens; outside Windows the reading says so
  // and the install guides stay the whole page (render/runtimepage.ts).
  void loadRuntimePage(hostCheck, guides, {
    current: runtimeViewCurrent,
    showGuides: renderGuides,
    showHolder: (html) => {
      if (!holder) return;
      holder.innerHTML = html;
      holder.querySelector("[data-runtime-recheck]")?.addEventListener("click", () => render());
    },
    readResume: SetupResume,
    showChecklist: (result, resume) => {
      if (!holder || !state) return;
      dockerGuide = new DockerGuide(holder, guideDeps(() => showView("dashboard")), {resume, result});
    },
  });
}

// The Security page: the Secure VM, its fence proof and where each earner
// runs (render/securevm.ts). It reads the status every few seconds while open,
// because Install and Verify run for minutes and the guard changes it alone.
let secureVM: SecureVMReading | null = null;
let secureVMAskUndo = false;
let secureVMRemoval: SecureVMRemoveResult | null = null;
let secureVMTimer: number | undefined;

function renderSecurityView(current: AppState) {
  root.innerHTML = withChrome(`
    <div class="app-layout">
      <div class="main-content">
        ${topbar("security", current)}
        <main class="page-content">
          ${pageHead(t("main.security.title"))}
          <div id="securevm-slot">${renderSecureVM(secureVM, { askUndo: secureVMAskUndo, removal: secureVMRemoval })}</div>
        </main>
        ${pageFoot()}
      </div>
    </div>
  `);
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  wireSecureVM();
  void refreshSecureVM();
  if (secureVMTimer === undefined) {
    secureVMTimer = window.setInterval(() => {
      if (activeView !== "security" || !document.querySelector("#securevm-slot")) {
        window.clearInterval(secureVMTimer);
        secureVMTimer = undefined;
        return;
      }
      // A read can take longer than the tick; another one then would only
      // queue up behind it.
      if (secureVMPolling) return;
      secureVMPolling = true;
      void refreshSecureVM().finally(() => { secureVMPolling = false; });
    }, 3000);
  }
}

// secureVMSeq numbers each read, so a slow read that ends after a newer one
// cannot draw the older status over it.
let secureVMSeq = 0;
let secureVMPolling = false;

async function refreshSecureVM() {
  const seq = ++secureVMSeq;
  let status: SecureVMReading;
  try {
    status = await SecureVMStatus();
  } catch (error) {
    if (seq !== secureVMSeq) return;
    showErrorToast({scope: "securevm", error: String(error)});
    // The last reading is stale now: show it as unread until a read works.
    if (!secureVM) return;
    status = unreadSecureVM(secureVM);
  }
  if (seq !== secureVMSeq) return;
  secureVM = status;
  const slot = document.querySelector<HTMLDivElement>("#securevm-slot");
  if (!slot || activeView !== "security") return;
  const focus = secureVMFocusSelector(slot);
  slot.innerHTML = renderSecureVM(secureVM, { askUndo: secureVMAskUndo, removal: secureVMRemoval });
  wireSecureVM();
  if (focus) slot.querySelector<HTMLElement>(focus)?.focus();
}

// secureVMFocusSelector names the focused control by its data attributes, so
// the redraw every few seconds puts the keyboard focus back on the same one.
function secureVMFocusSelector(slot: HTMLElement): string {
  const el = document.activeElement;
  if (!(el instanceof HTMLElement) || !slot.contains(el)) return "";
  const parts = Array.from(el.attributes)
    .filter((a) => a.name.startsWith("data-securevm") || a.name === "data-slug")
    .map((a) => `[${a.name}="${CSS.escape(a.value)}"]`);
  return parts.length ? el.tagName.toLowerCase() + parts.join("") : "";
}

// runSecureVM starts an action and redraws at once, so the page shows it busy;
// the action's own result redraws again when it ends.
function runSecureVM(action: () => Promise<unknown>) {
  const done = action().catch((error) => showErrorToast({scope: "securevm", error: String(error)}));
  window.setTimeout(() => void refreshSecureVM(), 300);
  void done.then(() => refreshSecureVM());
}

function wireSecureVM() {
  const slot = document.querySelector<HTMLDivElement>("#securevm-slot");
  if (!slot) return;
  slot.querySelectorAll<HTMLButtonElement>("[data-securevm-action]").forEach((button) => {
    button.addEventListener("click", () => {
      switch (button.dataset.securevmAction) {
        case "install": secureVMRemoval = null; runSecureVM(SecureVMInstall); break;
        case "verify": runSecureVM(SecureVMVerify); break;
        case "start": runSecureVM(SecureVMStart); break;
        case "stop": runSecureVM(SecureVMStop); break;
        case "screen": runSecureVM(() => SecureVMOpenScreen("")); break;
        case "undo-ask": secureVMAskUndo = true; void refreshSecureVM(); break;
        case "undo-cancel": secureVMAskUndo = false; void refreshSecureVM(); break;
        case "undo":
          secureVMAskUndo = false;
          runSecureVM(async () => {
            secureVMRemoval = await SecureVMUndo().catch((error) => {
              showErrorToast({scope: "securevm", error: String(error)});
              return null;
            });
          });
          break;
      }
    });
  });
  slot.querySelectorAll<HTMLButtonElement>("[data-securevm-place]").forEach((button) => {
    button.addEventListener("click", () => {
      const where = button.dataset.securevmPlace === "vm" ? "vm" : "host";
      runSecureVM(() => SecureVMPlace(button.dataset.slug || "", where));
    });
  });
  slot.querySelectorAll<HTMLButtonElement>("[data-securevm-signin]").forEach((button) => {
    button.addEventListener("click", () => runSecureVM(() => SecureVMOpenScreen(button.dataset.securevmSignin || "")));
  });
}

// First run: the same bar as the app, one column, one step at a time.
function gateBar() {
  return `<header class="gate-bar"><span class="topbar-brand">${officialLogoMark()}<span>CashPilot</span></span></header>`;
}

// First run, step 2: Run everything here, or Join my CashPilot.
function renderModeStep() {
  root.innerHTML = firstRunScreen("mode", {bar: gateBar(), path: firstRunPath, platform: chromePlatform});
  wireChrome();
  document.querySelectorAll<HTMLButtonElement>("[data-mode-choice]").forEach((button) => {
    button.addEventListener("click", async () => {
      if (button.dataset.modeChoice === "follower") {
        firstRunPath = "follower";
        onboardingStep = "join";
        render();
        return;
      }
      try {
        await StartAsLead();
        await loadAppState();
      } catch (error) {
        showErrorToast({scope: "mode", error: String(error)});
        return;
      }
      firstRunPath = "lead";
      onboardingStep = "runtime";
      render();
    });
  });
}

// First run, the join step of Join my CashPilot.
function renderJoinStep() {
  root.innerHTML = firstRunScreen("join", {bar: gateBar(), path: firstRunPath, platform: chromePlatform});
  wireChrome();
  wireJoinForm(document, () => {
    firstRunPath = "";
    onboardingStep = "mode";
    render();
  }, async () => {
    await loadAppState();
    onboardingStep = "runtime";
    render();
  });
}

// The join form, on the first run and in Settings: one live heartbeat, then the
// outcome under the form. A failure keeps what the user pasted; a success clears
// the fields, which hold the key, and offers Continue.
function wireJoinForm(scope: ParentNode, onBack: () => void, onContinue: () => void | Promise<void>) {
  const form = scope.querySelector<HTMLFormElement>("#join-form");
  if (!form) return;
  scope.querySelector<HTMLButtonElement>("#join-back")?.addEventListener("click", onBack);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const connect = form.querySelector<HTMLButtonElement>("#join-connect")!;
    const out = form.querySelector<HTMLElement>("#join-result")!;
    const field = (name: string) => form.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[name=${name}]`)!;
    connect.disabled = true;
    connect.textContent = t("main.join.connecting");
    let result: JoinResult;
    try {
      result = await JoinCashPilot({block: field("block").value, url: field("url").value, key: field("key").value});
    } catch (error) {
      showErrorToast({scope: "mode", error: String(error)});
      connect.disabled = false;
      connect.textContent = t("main.join.connect");
      return;
    }
    out.innerHTML = renderJoinOutcome(result);
    if (!result.ok) {
      connect.disabled = false;
      connect.textContent = t("main.join.connect");
      return;
    }
    field("block").value = "";
    field("key").value = "";
    connect.closest(".gate-actions")?.remove();
    out.querySelector<HTMLButtonElement>("#join-continue")?.addEventListener("click", () => void onContinue());
  });
}

function renderWelcome() {
  root.innerHTML = firstRunScreen("welcome", {bar: gateBar(), path: firstRunPath, platform: chromePlatform});
  wireChrome();
  document.querySelector("#get-started")?.addEventListener("click", () => {
    onboardingStep = "mode";
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
      <button class="guide-link" data-url="${escapeHtml(guide.url)}">${ht("main.guides.open")}</button>
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
  const completedDriftDeploys = Object.entries(driftDeploys)
    .filter(([slug, result]) => result.kind === "ok" && !current.drift?.[slug]?.length)
    .map(([, result]) => result);
  const attention = withReminders(attentionItems({
    runtime: current.runtime,
    services,
    deployments,
    copies,
    earnings,
    outdated: current.outdatedServices,
    health: current.health,
    drift: current.drift,
    driftDeploys,
    producerStates: current.producerStates,
    isolation: current.isolation,
  }).concat(stallItems(current.stalls)), current.reminders);

  root.innerHTML = withChrome(`
    <div class="app-layout">
      <div class="main-content">
        ${topbar("dashboard", current)}
        <main class="page-content">
        ${pageHead(t("main.dashboard.title"), `
          <button class="btn btn-ghost btn-sm" id="refresh">${ht("main.dashboard.refresh")}</button>
        `)}
        ${current.pairing ? renderJoinedBanner(current.pairing) : ""}
        ${current.serverCommands ? renderServerCommands(current.serverCommands) : ""}
        <!-- The money first and largest; the container count sits at the far
             end, smaller, because it answers a different question. -->
        <section class="stats-grid money-strip" aria-label="${ht("main.dashboard.earningsLabel")}">
          <div class="stat-card stat-card-balance">
            <div class="stat-label">${ht("main.dashboard.balance")}</div>
            <div class="stat-value">${escapeHtml(totalText(summary, disp))}</div>
            <div class="stat-note">${escapeHtml(totalCaption(summary))}</div>
            ${allTimeNote(summary, disp) ? `<div class="stat-note">${escapeHtml(allTimeNote(summary, disp))}</div>` : ""}
          </div>
          <div class="stat-card">
            <div class="stat-label">${ht("main.dashboard.today")}</div>
            <div class="stat-value">${escapeHtml(formatBalance(summary?.today ?? 0, disp))}</div>
            <div class="stat-change">${escapeHtml(changeCaption(summary?.todayChange ?? 0, "day"))}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">${ht("main.dashboard.month")}</div>
            <div class="stat-value">${escapeHtml(formatBalance(summary?.month ?? 0, disp))}</div>
            <div class="stat-change">${escapeHtml(summary?.monthChange ? changeCaption(summary.monthChange, "month") : t("main.dashboard.soFarThisMonth"))}</div>
          </div>
          <div class="stat-card stat-card-ops">
            <div class="stat-label">${ht("main.dashboard.containersUp")}</div>
            <div class="stat-value">${upCount}${containers.length !== upCount ? ` <span class="stat-qual">${ht("main.dashboard.ofTotal", {total: containers.length})}</span>` : ""}</div>
          </div>
        </section>

        ${renderFenceStatusLine(current.isolation, current.runtime)}
        ${renderAttention(attention, upCount, containers.length, completedDriftDeploys)}
        ${renderStallServerNote(current.stallsServer)}

        ${renderReminders(current.reminders)}

        ${renderStateBackups(current.stateBackups)}

        <section class="earnings" aria-labelledby="earnings-title">
          <div class="block-head">
            <h2 class="block-title" id="earnings-title">${ht("main.dashboard.earnedPerDay")}</h2>
            <div class="tab-strip">
              ${[7, 30].map((days) => `<button class="tab-btn ${chartDays === days ? "active" : ""}" data-chart-days="${days}">${ht("main.dashboard.days", {days})}</button>`).join("")}
            </div>
          </div>
          ${renderEarningsBars(summary?.daily || [], disp, chartDays)}
        </section>

        <section class="usage" aria-labelledby="usage-title">
          <div class="block-head">
            <h2 class="block-title" id="usage-title">${ht("main.dashboard.resourceUse")}</h2>
            <span class="block-meta">${ht("main.dashboard.usageMeta", {days: chartDays})}</span>
          </div>
          <div id="usage-panel">${renderUsagePanel(usageView && usageView.days === chartDays ? usageView : null)}</div>
        </section>

        <section class="services-block" aria-labelledby="services-title">
          <div class="block-head">
            <h2 class="block-title" id="services-title">${ht("main.dashboard.services")}</h2>
            <span class="block-meta">${ht("main.dashboard.balancesIn", {currency: disp})}</span>
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
              runtime: current.runtime,
            })}
          </div>
        </section>

        ${points.length ? renderPointsSection(points) : ""}

        ${current.fleet ? renderFleetSection(current.fleet) : ""}

        <div id="service-output" class="output dashboard-output"></div>
        </main>
        ${pageFoot()}
      </div>
    </div>
  `);
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
  document.querySelector<HTMLInputElement>("[data-server-commands-switch]")?.addEventListener("change", (event) => {
    const on = (event.target as HTMLInputElement).checked;
    void SetServerCommands(on).then(refreshState, (error) => setOutput(t("main.serverCommands.switchFailed", {error: String(error)})));
  });
  wireServerCommandsFirewall(document, {
    allow: AllowServerCommandsThroughFirewall,
    refresh: refreshState,
    setOutput,
    notice: (message) => showInfoToast({ scope: "Server commands", message }),
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
  wireWalletDetails();
  document.querySelectorAll<HTMLButtonElement>("[data-reminder-action]").forEach((button) => {
    button.addEventListener("click", () => void runReminderAction(button.dataset));
  });
}

// Money waiting: Done, Snooze, Not for me and Unmute, then redraw from the backend.
async function runReminderAction(data: DOMStringMap) {
  try {
    await ReminderAction(data.slug || "", data.rule || "", data.reminderAction || "", data.occurrence || "", Number(data.days || "0"));
    await refreshState();
  } catch (error) {
    setOutput(String(error));
  }
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
      showErrorToast(t("main.backup.passphrasesDiffer"));
      repeat.value = "";
      return;
    }
    const value = secret.value;
    const again = repeat.value;
    secret.value = "";
    repeat.value = "";
    const button = form.querySelector<HTMLButtonElement>("button[type=submit]")!;
    button.disabled = true;
    button.textContent = t("main.backup.encrypting");
    try {
      const path = await ExportStateBackup(form.dataset.slug || "", recipient ? "" : value, recipient ? "" : again, recipient ? value : "");
      if (path) {
        showInfoToast(t("main.backup.saved", {path}));
        await refreshState();
        return;
      }
    } catch (error) {
      showErrorToast(String(error));
    }
    button.disabled = false;
    button.textContent = t("main.backup.encryptAndSave");
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
      <button class="footer-link" data-url="https://github.com/GeiserX/CashPilot-Desktop">${ht("main.foot.source")}</button>
      <button class="footer-link" data-url="https://github.com/GeiserX/CashPilot-Desktop/issues">${ht("main.foot.reportProblem")}</button>
      <button class="footer-link" data-url="https://github.com/sponsors/GeiserX">${ht("main.foot.sponsor")}</button>
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
  return `
    <header class="topbar">
      <div class="topbar-brand">
        ${officialLogoMark()}
        <span>CashPilot</span>
      </div>
      ${renderTopnav(active)}
      <div class="topbar-right">
        ${renderAddServiceButton(active)}
        <button class="topbar-runtime ${runtimeUsable(current.runtime) ? "" : "text-warn"} ${active === "runtime" ? "active" : ""}" data-view="runtime" title="${escapeHtml(current.runtime.message || "")}">${topbarRuntimeLabel(current.runtime)}</button>
        <span class="topbar-earnings" title="${ht("main.topbar.totalTitle")}">${escapeHtml(totalText(current.summary, disp))}</span>
        <select class="currency-select" id="currency-select" title="${ht("main.topbar.currencyTitle")}">
          ${(current.currencies || ["USD", "EUR"]).map((currency) => `<option value="${currency}" ${currency === current.config.displayCurrency ? "selected" : ""}>${currency}</option>`).join("")}
        </select>
        <details class="notification-menu">
          ${renderAlerts(current.notifications)}
        </details>
      </div>
    </header>
  `;
}

function runtimeUsable(runtime: RuntimeStatus) {
  return runtime.available || (runtime.nativeAvailable && runtimeState(runtime).kind !== "unknown");
}

function topbarRuntimeLabel(runtime: RuntimeStatus) {
  const label = runtime.available ? ht("main.topbar.runtimeReady")
    : runtimeState(runtime).kind === "unknown" ? ht("main.topbar.runtimeUnknown")
    : runtime.nativeAvailable ? ht("main.topbar.nativeMode") : ht("main.topbar.runtimeOffline");
  return `<span class="runtime-dot ${runtimeUsable(runtime) ? "ok" : "warn"}"></span>${label}`;
}

function renderAlerts(notifications: NotificationItem[] | null) {
  return `
    <summary aria-label="${ht("main.alerts.title")}">${ht("main.alerts.title")}${notifications?.length ? ` <span class="notify-badge">${notifications.length}</span>` : ""}</summary>
    <div class="notification-popover">
      <strong>${ht("main.alerts.title")}</strong>
      ${notifications?.length ? notifications.map((item) => `
        <div class="notification-item ${escapeHtml(item.level)}">
          <span>${escapeHtml(item.title)}</span>
          <small>${escapeHtml(item.message)}</small>
        </div>
      `).join("") : `<p class="muted">${ht("main.alerts.none")}</p>`}
    </div>
  `;
}

function updateRuntimeChrome(current: AppState) {
  const button = document.querySelector<HTMLButtonElement>(".topbar-runtime");
  if (button) {
    button.classList.toggle("text-warn", !runtimeUsable(current.runtime));
    button.title = current.runtime.message;
    button.innerHTML = topbarRuntimeLabel(current.runtime);
  }
  const alerts = document.querySelector(".notification-menu");
  if (alerts) alerts.innerHTML = renderAlerts(current.notifications);
}

function setRuntimeReading(runtime: RuntimeStatus, notice: NotificationItem | null) {
  if (!state) return;
  state = { ...state, ...withRuntimeReading(state, runtime, notice) };
  updateRuntimeSurfaces(state);
}

function updateRuntimeSurfaces(current: AppState) {
  const slot = document.querySelector("#runtime-card-slot");
  if (slot) {
    const card = runtimeCard(current.runtime);
    slot.innerHTML = card.html;
    const go = document.querySelector<HTMLButtonElement>("#continue-btn");
    if (go) go.disabled = !card.canContinue;
  }
  if (!current.config.firstRunComplete) return;
  if (activeView === "dashboard") {
    render();
    return;
  }
  updateRuntimeChrome(current);
  // Import's Deploy, Fleet's proxied copies and Activity's sampling line follow Docker.
  if (dockerKey(current.runtime) !== dockerDrawn) {
    if (activeView === "import") renderImportIfShown();
    else if (activeView === "fleet") redrawFleetProxies(current);
    else if (activeView === "activity" && activityShown) void renderActivity(activityShown);
  }
  if (activeView === "wizard") {
    for (const draw of wizardPreflightDraws.values()) draw();
    const summary = document.querySelector("#wizard-runtime-summary");
    if (summary) summary.innerHTML = wizardSummaryServices((current.services || []).filter((service) => wizardSelected.includes(service.slug)));
  }
  if (activeView === "settings") {
    const card = document.querySelector("#runtime-settings-slot");
    if (card) {
      card.innerHTML = renderRuntimeSettings(current.runtime);
      wireShellNav(card);
    }
    const provider = document.querySelector("[data-runtime-provider-state]");
    if (provider) provider.outerHTML = runtimeProviderNote(current.runtime);
    const fence = document.querySelector("[data-fence-status]");
    if (fence) fence.outerHTML = renderFenceStatusLine(current.isolation, current.runtime, "small");
  }
}

// A Windows host reading can finish after GetAppState returned. Update the open
// surfaces without replacing any in-progress form.
async function refreshRuntimeSnapshot() {
  // The checklist already starts its own host check.
  if (!state?.config.firstRunComplete || activeView === "runtime") return;
  await loadAppState("runtime");
}

function showView(view: View) {
  activeView = view;
  render();
  // These views draw Docker controls from the shared reading. Docker may have
  // started or stopped outside CashPilot since it was read, so read it again.
  if (view === "dashboard" || view === "import" || view === "fleet" || view === "activity") void refreshRuntimeSnapshot();
}

function wireShellNav(scope: ParentNode = document) {
  scope.querySelectorAll<HTMLButtonElement>("[data-view]").forEach((button) => {
    button.addEventListener("click", () => {
      const next = button.dataset.view;
      if (next === "dashboard" || next === "wizard" || next === "catalog" || next === "settings" || next === "fleet" || next === "activity" || next === "import" || next === "runtime" || next === "security") {
        ({ step: wizardStep, drafts: wizardRouteDrafts } = wizardAfterNav(next, wizardStep));
        wizardWhereDrafts = {};
        resetScrollAfterRender = true;
        showView(next);
      }
    });
  });
  wireOpenLinks(scope, BrowserOpenURL);
  wireCopyText(scope, (text) => navigator.clipboard.writeText(text), (error) => showErrorToast({scope: "clipboard", error: String(error)}));
  scope.querySelector<HTMLSelectElement>("#currency-select")?.addEventListener("change", async (event) => {
    const currency = (event.target as HTMLSelectElement).value;
    await SaveSettings({displayCurrency: currency});
    await loadAppState();
    render();
  });
}

function renderCatalog(current: AppState) {
  const services = (current.services || []).filter((service) => {
    const matchesCategory = catalogFilter === "all" || service.category === catalogFilter;
    const haystack = `${service.name} ${service.shortDescription} ${service.description}`.toLowerCase();
    return matchesCategory && haystack.includes(catalogSearch.toLowerCase());
  });
  const categories = catalogFilterTabs(current.services || []);
  // The open editor's typed values live only in the DOM. Search, the filter tabs and
  // any refresh redraw this page, so the live form is put back over the redrawn copy.
  const liveEditor = document.querySelector<HTMLFormElement>("[data-custom-editor]");
  root.innerHTML = withChrome(`
    <div class="app-layout">
      <div class="main-content">
        ${topbar("catalog", current)}
        <main class="page-content">
          ${pageHead(t("main.catalog.title"))}
          ${renderCustomServicesSection(customViews, current.deployments || [], customEditing, customLoadError)}
          <div class="filter-bar">
            <div class="filter-tabs">
              ${categories.map((category) => `<button class="filter-tab ${catalogFilter === category ? "active" : ""}" data-filter="${category}">${escapeHtml(category === "all" ? t("main.catalog.all") : kindLabel(category) || capitalize(category))}</button>`).join("")}
            </div>
            <input class="catalog-search" id="catalog-search" type="search" placeholder="${ht("main.catalog.search")}" aria-label="${ht("main.catalog.search")}" value="${escapeHtml(catalogSearch)}" />
          </div>
          <section class="catalog-block">
            <div class="table-scroll">
              ${renderCatalogTable(services, current.deployments || [], current.outdatedServices)}
            </div>
            <p class="referral-note">${ht("main.catalog.referralNote")}</p>
          </section>
        </main>
        ${pageFoot()}
      </div>
    </div>
  `);
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
  wireCatalogRows(document, (slug) => openWizard(slug));
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
    customLoadError = t("main.custom.loadFailed", {error: String(error)});
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
    await loadAppState();
    customEditing = null;
    render();
    const deployed = state?.deployments?.some((dep) => dep.slug === saved.slug);
    showInfoToast({scope: "custom service", message: `${t("main.custom.saved", {name: saved.name})}${saved.warnings?.length ? ` ${saved.warnings.join(" ")}` : ""}${deployed ? ` ${t("main.custom.redeployToApply")}` : ""}`});
  } catch (error) {
    if (output) output.textContent = String(error);
  }
}

async function deleteCustomService(slug: string) {
  const view = customViews?.find((item) => item.slug === slug);
  if (!view) return;
  const question = view.volume
    ? t("main.custom.deleteConfirmVolume", {name: view.name, volume: view.volume})
    : t("main.custom.deleteConfirm", {name: view.name});
  if (!confirm(question)) return;
  try {
    await DeleteCustomService(slug);
    customViews = await ListCustomServices();
    await loadAppState();
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

// Imported services that need Docker here while it cannot run them, each with the
// note that says why. A native app is never in it (localDockerGap).
function importWaitsForDocker(current: AppState): Record<string, string> {
  const waits: Record<string, string> = {};
  for (const slug of importResult?.saved || []) {
    const service = current.services?.find((item) => item.slug === slug);
    if (localDockerGap(service, current.runtime)) waits[slug] = dockerBlockedNote(current.runtime);
  }
  return waits;
}

function renderImport(current: AppState) {
  const keep = renderedImportPreview === importPreview ? readImportForm() : null;
  dockerDrawn = dockerKey(current.runtime);
  root.innerHTML = withChrome(`
    <div class="app-layout">
      <div class="main-content">
        ${topbar("settings", current)}
        <main class="page-content">
          ${pageHead(t("main.import.title"), `<button class="compact-btn" data-view="settings">${ht("main.import.backToSettings")}</button>`)}
          ${renderImportInput(importSource)}
          ${importPreview ? renderImportPreview(importPreview) : ""}
          ${importResult && importResult.saved.length ? renderImportSaved(importResult, importPreview, importDeployed, importLinks, importDeploys, importWaitsForDocker(current)) : ""}
        </main>
        ${pageFoot()}
      </div>
    </div>
  `);
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
  if (picked.length > IMPORT_MAX_FILES) throw new Error(t("main.import.tooManyFiles", {n: IMPORT_MAX_FILES}));
  if (picked.reduce((n, f) => n + f.size, 0) > IMPORT_MAX_BYTES) {
    throw new Error(t("main.import.tooLarge", {kib: IMPORT_MAX_BYTES / 1024}));
  }
  // Strict UTF-8: file.text() would turn bad bytes into U+FFFD, which the
  // backend's UTF-8 check could then not see.
  const decoder = new TextDecoder("utf-8", { fatal: true });
  for (const file of picked) {
    // Refuse before reading a file that alone is over the limit.
    if (file.size > IMPORT_MAX_BYTES) throw new Error(t("main.import.fileTooLarge", {name: file.name}));
    let content: string;
    try {
      content = decoder.decode(await file.arrayBuffer());
    } catch {
      throw new Error(t("main.import.notUtf8", {name: file.name}));
    }
    files.push({ name: file.name, content });
  }
  const pasted = document.querySelector<HTMLTextAreaElement>("#import-paste")?.value || "";
  if (pasted.trim()) files.push({ name: t("main.import.pastedText"), content: pasted });
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
    importDeploys = {};
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
    showErrorToast({ scope: "import", error: t("main.import.tickOne") });
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
    await loadAppState();
    importLinks = {};
    importDeploys = {};
    showInfoToast({ scope: "import", message: t("main.import.saved", {n: importResult.saved.length}) });
    renderImportIfShown();
  } catch (error) {
    showErrorToast({ scope: "import", error: String(error) });
  }
}

async function runImportDeploy(slug: string) {
  if (!slug || importDeploying.has(slug)) return;
  importDeploying.add(slug);
  const statuses = importDeploys;
  const setFeedback = (status: DeployFeedback) => {
    statuses[slug] = status;
    if (statuses === importDeploys) renderImportIfShown();
  };
  try {
    const name = importPreview?.rows.find((row) => row.slug === slug)?.name || slug;
    const service = state?.services?.find((item) => item.slug === slug);
    const fields = service ? serviceFormFields(service, state?.collectorFields) : [];
    const saved = await GetCredentials(slug);
    const report = await PreflightService(slug).catch(() => null);
    if (statuses !== importDeploys || activeView !== "import") return;
    const gap = state && localDockerGap(service, state.runtime);
    if (gap) {
      setFeedback({ kind: "failed", text: gap });
      return;
    }
    const outcome = await runCheckedDeploy(fields, saved.values, true, t("main.deploy.deploying", {name}), {
      confirmReplace: (message) => {
        return confirm(report?.summary ? `${message}\n\n${report.summary}` : message);
      },
      adminStep: () => DeployAdminStep(slug),
      askConsent: (step) => askDeployConsent(document.querySelector(`[data-import-deploy-panel="${CSS.escape(slug)}"]`), name, step),
      say: (kind, text) => setFeedback({ kind, text }),
      deploy: async (approval) => {
        if (approval) await DeployServiceWithAdminApproval(slug, {}, null, null, false, approval);
        else await DeployService(slug, {}, null, null);
        if (statuses === importDeploys) importDeployed = [...importDeployed, slug];
        return { kind: "ok", text: t("main.deploy.deployed") };
      },
    }, {
      savedSecrets: saved.savedSecrets,
      localReplacement: t("main.import.replaceLocal", {name}),
    });
    if (outcome.kind === "missing") setFeedback(outcome);
    else if (outcome.kind === "blocked") setFeedback({ kind: "failed", text: outcome.message });
    else if (outcome.kind === "cancelled") setFeedback({ kind: "cancelled", text: cancelledText() });
    if (outcome.kind !== "done" || outcome.answer.kind !== "ok" || statuses !== importDeploys) return;
    await loadAppState().catch(() => {});
    const creds = await GetCredentials(slug).catch(() => null);
    const link = deviceLinkText(state?.services?.find((service) => service.slug === slug), creds?.values || {}, state?.addedDevices?.[slug] || "", state?.retryingDevices?.[slug] || "");
    if (link && statuses === importDeploys) {
      importLinks = { ...importLinks, [slug]: link };
      renderImportIfShown();
    }
  } catch (error) {
    setFeedback({ kind: "failed", text: deployErrorText(error) });
  } finally {
    importDeploying.delete(slug);
  }
}

function drawnSettingFields() {
  return Array.from(document.querySelectorAll<HTMLInputElement>("[data-setting]")).map(settingField);
}

function restoreSettingDrafts(before: SettingField[]) {
  const keep = unsavedDrafts(before, drawnSettingFields());
  for (const input of document.querySelectorAll<HTMLInputElement>("[data-setting]")) {
    const value = keep.get(input.dataset.setting || "");
    if (value !== undefined) input.value = value;
  }
}

async function renderSettings(current: AppState) {
  const request = ++settingsRequest;
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
  // A restore that did not finish holds its service; the card lists it. An error
  // reading the list is shown, never read as "none".
  let unfinished: UnfinishedRestore[] = [];
  try {
    unfinished = (await UnfinishedRestores()) || [];
  } catch (error) {
    showErrorToast({scope: "restore", error: String(error)});
  }
  try {
    modeState = await GetModeState();
  } catch (error) {
    modeState = null;
    showErrorToast({scope: "mode", error: String(error)});
  }
  if (request !== settingsRequest || activeView !== "settings") return;
  await refreshRuntimeSnapshot();
  if (request !== settingsRequest || activeView !== "settings") return;
  current = state ?? current;
  const { runtime } = current;
  // A redraw on this page (a language change, the background-helper toggle)
  // keeps what the user typed and did not save yet.
  const drafts = drawnSettingFields();
  const imageDraft = imageUpdateDraft();
  // The consents are read from the live state, not from `current`: a Withdraw
  // during the awaits above already changed them, and drawing the older map would
  // bring the withdrawn row back.
  root.innerHTML = withChrome(`
    <div class="app-layout">
      <div class="main-content">
        ${topbar("settings", current)}
        <main class="page-content">
          ${pageHead(t("main.settings.title"))}
          ${languageState ? renderLanguageCard(languageState) : ""}
          <div id="mode-card-slot">${modeState ? renderModeCard(modeState, modeCardView, modeLeft) : ""}</div>
          <div id="runtime-settings-slot">${renderRuntimeSettings(runtime)}</div>
          <section class="card">
            <div class="card-header">
              <div>
                <span class="card-title">${ht("main.settings.env.title")}</span>
                <p class="muted compact-copy">${ht("main.settings.env.help")}</p>
              </div>
              <div class="save-row">${saveNoteSlot("save-settings-note")}<button class="primary compact-btn" id="save-settings">${ht("main.settings.env.save")}</button></div>
            </div>
            <div class="settings-list">
              ${settings.environment.map((item) => renderEnvSetting(item, item.key === "CASHPILOT_ISOLATION" ? renderFenceStatusLine(current.isolation, runtime, "small") : item.key === "CASHPILOT_RUNTIME_PROVIDER" ? runtimeProviderNote(runtime) : "")).join("")}
            </div>
          </section>

          <section class="card">
            <div class="card-header">
              <div>
                <span class="card-title">${ht("main.settings.collection.title")}</span>
                <p class="muted compact-copy">${ht("main.settings.collection.help")}</p>
              </div>
            </div>
            <div class="collector-grid">
              ${settings.collectors.map(renderCollectorSetting).join("")}
            </div>
          </section>

          <div id="image-updates-slot"></div>

          <section class="card">
            <div class="card-header">
              <div>
                <span class="card-title">${ht("main.import.title")}</span>
                <p class="muted compact-copy">${ht("main.settings.import.help")}</p>
              </div>
              <button class="compact-btn" data-view="import">${ht("main.settings.import.open")}</button>
            </div>
          </section>

          ${renderSessionConsentCard((state ?? current).config?.sessionConsent, current.services)}

          ${renderRestoreCard(unfinished)}

          ${renderBackgroundCard(background, backgroundError)}
        </main>
        ${pageFoot()}
      </div>
    </div>
  `);
  restoreSettingDrafts(drafts);
  void loadImageUpdates(imageDraft);
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  document.querySelector("#save-settings")?.addEventListener("click", () => void saveSettingsFromForm());
  document.querySelector<HTMLSelectElement>("#language-select")?.addEventListener("change", (event) => void chooseLanguage((event.target as HTMLSelectElement).value));
  wireSecretSettings(settings.environment);
  document.querySelector<HTMLInputElement>("#bg-helper-toggle")?.addEventListener("change", (event) => void toggleBackgroundHelper(event));
  wireRestore();
  wireModeCard();
  wireSessionConsent(document);
  document.querySelectorAll<HTMLButtonElement>("[data-service]").forEach((button) => {
    button.addEventListener("click", () => openWizard(button.dataset.service));
  });
}

// Settings > App image updates (render/imageupdates.ts). A reload draws the card
// again only when every choice the owner changed is saved; a redraw of the whole
// page carries the unsaved ones over (imageUpdateDraft).
const imageUpdateEdits = new ImageUpdateEdits();

function imageUpdateDraft(): Record<string, string> | null {
  if (imageUpdateEdits.canRedraw()) return null;
  const selects = document.querySelectorAll<HTMLSelectElement>("#image-updates-slot select");
  return draftOf([...selects].map((el): [string, string] => [el.id || `app:${el.dataset.imageUpdatesApp}`, el.value]));
}

async function loadImageUpdates(draft: Record<string, string> | null = null) {
  imageUpdateEdits.holdDraft(draft);
  const load = imageUpdateEdits.beginLoad();
  let html: string;
  let failed = false;
  try {
    html = renderImageUpdatesCard(await GetImageUpdates());
  } catch (error) {
    html = renderImageUpdatesCard(null, String(error));
    failed = true;
  }
  // A newer load started meanwhile: its answer is the one to draw.
  if (!imageUpdateEdits.isLatest(load)) return;
  const slot = document.querySelector<HTMLElement>("#image-updates-slot");
  if (!slot) return;
  // A card on screen stays when a change on it is not saved yet, and when a
  // reload failed. An error card gives way to the next answer.
  const shown = slot.childElementCount === 0 ? "empty" : slot.querySelector("[data-image-updates-error]") ? "error" : "card";
  if (!drawsOver(shown, failed, imageUpdateEdits.canRedraw())) return;
  slot.innerHTML = html;
  // An error card has no choices: the draft waits for the card.
  const kept = failed ? null : imageUpdateEdits.takeDraft();
  if (kept) {
    slot.querySelectorAll<HTMLSelectElement>("select").forEach((el) => {
      const key = el.id || `app:${el.dataset.imageUpdatesApp}`;
      if (key in kept) el.value = kept[key];
    });
  }
  if (slot.dataset.wired) return;
  slot.dataset.wired = "1";
  slot.addEventListener("change", () => imageUpdateEdits.change());
  slot.addEventListener("click", (event) => {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>("[data-image-updates-action]");
    if (!button) return;
    const action = button.dataset.imageUpdatesAction;
    if (action === "save") void saveImageUpdates();
    else if (action === "enable") void saveImageUpdates("auto");
    else if (action === "run") void runImageUpdatesNow();
  });
}

async function saveImageUpdates(mode?: string) {
  const slot = document.querySelector<HTMLElement>("#image-updates-slot");
  if (!slot) return;
  const apps: Record<string, string> = {};
  slot.querySelectorAll<HTMLSelectElement>("[data-image-updates-app]").forEach((el) => {
    apps[el.dataset.imageUpdatesApp || ""] = el.value;
  });
  const modeSelect = slot.querySelector<HTMLSelectElement>("#image-updates-mode");
  // Enable saves Update automatically: the select shows it too, so a page
  // redraw before the answer carries that choice, not the old one.
  if (mode && modeSelect) modeSelect.value = mode;
  const chosen = mode || modeSelect?.value || "off";
  const hour = Number(slot.querySelector<HTMLSelectElement>("#image-updates-hour")?.value ?? 4);
  const sent = imageUpdateEdits.sent();
  try {
    await SaveImageUpdates(chosen, hour, apps);
    imageUpdateEdits.saved(sent);
    showInfoToast({ scope: "image updates", message: t("imageUpdates.saved") });
  } catch (error) {
    showErrorToast({ scope: "image updates", error: String(error) });
  }
  await loadImageUpdates();
}

async function runImageUpdatesNow() {
  try {
    await RunImageUpdates();
    showInfoToast({ scope: "image updates", message: t("imageUpdates.started") });
  } catch (error) {
    showErrorToast({ scope: "image updates", error: String(error) });
  }
  await loadImageUpdates();
}

// Settings > How this computer runs: each switch shows its confirm screen first.
function redrawModeCard() {
  const slot = document.querySelector<HTMLElement>("#mode-card-slot");
  if (!slot || !modeState) return;
  slot.innerHTML = renderModeCard(modeState, modeCardView, modeLeft);
  wireModeCard();
}

function wireModeCard() {
  const slot = document.querySelector<HTMLElement>("#mode-card-slot");
  if (!slot) return;
  const backToSummary = () => {
    modeCardView = "summary";
    redrawModeCard();
  };
  slot.querySelectorAll<HTMLButtonElement>("[data-mode-action]").forEach((button) => {
    button.addEventListener("click", async () => {
      const action = button.dataset.modeAction as ModeCardView | "leave" | "firewall-cleanup";
      if (action !== "leave" && action !== "firewall-cleanup") {
        if (action === "summary") modeLeft = null;
        modeCardView = action;
        redrawModeCard();
        return;
      }
      button.disabled = true;
      try {
        if (action === "firewall-cleanup") {
          await RetryServerCommandsFirewallRemoval();
          if (modeLeft) modeLeft = {...modeLeft, firewallError: ""};
        } else {
          modeLeft = await SwitchToLead();
          modeCardView = "left";
        }
        modeState = await GetModeState();
        await loadAppState();
      } catch (error) {
        showErrorToast({scope: "mode", error: String(error)});
        button.disabled = false;
        return;
      }
      // Redraw the whole page, not only the card: the Environment Variables form
      // below still holds the server address and hub setting from before the leave.
      render();
    });
  });
  if (modeCardView === "join") {
    wireJoinForm(slot, backToSummary, async () => {
      modeCardView = "summary";
      await loadAppState();
      render();
    });
  }
}

// Settings > Restore from a backup: choose a file, read its preview, confirm, restore.
// The Go side holds every rule (restore_binding.go); a refused restore makes nothing.
function wireRestore() {
  document.querySelector<HTMLButtonElement>("#restore-choose")?.addEventListener("click", async (event) => {
    const button = event.currentTarget as HTMLButtonElement;
    const out = document.querySelector<HTMLElement>("#restore-preview");
    if (!out) return;
    button.disabled = true;
    try {
      const preview = await ChooseRestoreFile();
      out.innerHTML = preview ? renderRestorePreview(preview) : "";
      if (preview) wireRestoreForm(out.querySelector<HTMLFormElement>(".restore-form"), preview.needsName || !preview.known);
    } catch (error) {
      out.innerHTML = "";
      showErrorToast({scope: "restore", error: String(error)});
    } finally {
      button.disabled = false;
    }
  });
  document.querySelectorAll<HTMLButtonElement>("[data-restore-discard]").forEach((button) => {
    button.addEventListener("click", async () => {
      const slug = button.dataset.restoreDiscard || "";
      if (!confirm(t("main.restore.discardConfirm", {slug}))) return;
      button.disabled = true;
      try {
        await DiscardPartialRestore(slug);
        showInfoToast({scope: "restore", message: t("main.restore.discarded", {slug})});
      } catch (error) {
        showErrorToast({scope: "restore", error: String(error)});
      }
      render();
    });
  });
}

function wireRestoreForm(form: HTMLFormElement | null, needsName: boolean) {
  if (!form) return;
  const passphrase = form.querySelector<HTMLInputElement>("input[name=passphrase]")!;
  const typed = form.querySelector<HTMLInputElement>("input[name=confirm_name]");
  (typed || passphrase).focus();
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const service = form.dataset.service || "";
    if (needsName && !typed?.value.trim()) {
      showErrorToast(t("main.restore.typeName", {service}));
      return;
    }
    // Desktop has one user: this confirm takes the place of the web's password step.
    if (!confirm(t("main.restore.confirm", {service}))) return;
    const secret = passphrase.value;
    passphrase.value = "";
    const button = form.querySelector<HTMLButtonElement>("button[type=submit]")!;
    button.disabled = true;
    button.textContent = t("main.restore.writing");
    try {
      const result = await RestoreStateBackup(form.dataset.token || "", secret, typed?.value || "");
      showInfoToast({scope: "restore", message: restoreResultText(service, result)});
      await refreshState();
    } catch (error) {
      // The file was used: a retry chooses it again and sees a fresh preview.
      showErrorToast({scope: "restore", error: String(error)});
    }
    render();
  });
}

// The Secret rows of Settings (render/settings.ts) carry no stored value in the
// page. Show reveals what the user types, Remove marks the saved value for
// deletion on Save, and Copy reads a read-only secret from memory.
function wireSecretSettings(environment: SettingsState["environment"]) {
  const stored = new Map(environment.filter((item) => item.secret).map((item) => [item.key, item.value]));
  document.querySelectorAll<HTMLButtonElement>("[data-secret-toggle]").forEach((button) => {
    const input = button.parentElement?.querySelector<HTMLInputElement>("input");
    button.addEventListener("click", () => {
      if (!input) return;
      const show = input.type === "password";
      input.type = show ? "text" : "password";
      button.textContent = show ? t("main.secret.hide") : t("main.secret.show");
      button.setAttribute("aria-pressed", String(show));
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-secret-clear]").forEach((button) => {
    const input = button.parentElement?.querySelector<HTMLInputElement>("input[data-secret]");
    if (!input) return;
    const savedPlaceholder = input.placeholder;
    button.addEventListener("click", () => {
      const clear = input.dataset.clear !== "true";
      if (clear) {
        input.value = "";
        input.dataset.clear = "true";
        input.placeholder = t("main.secret.removedOnSave");
      } else {
        delete input.dataset.clear;
        input.placeholder = savedPlaceholder;
      }
      button.textContent = clear ? t("main.secret.keep") : t("main.secret.remove");
      button.setAttribute("aria-pressed", String(clear));
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-secret-copy]").forEach((button) => {
    button.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(stored.get(button.dataset.secretCopy || "") || "");
        flashCopied(button);
      } catch (error) {
        showErrorToast({scope: "clipboard", error: String(error)});
      }
    });
  });
}

function renderCollectorSetting(item: SettingsState["collectors"][number]) {
  return `
    <button class="collector-row" data-service="${escapeHtml(item.slug)}">
      <span>${escapeHtml(item.name)}</span>
      <small>${escapeHtml(item.collector || t("main.collector.manual"))}</small>
      <strong class="${item.configured ? "configured" : ""}">${item.configured ? ht("main.collector.configured") : ht("main.collector.notConfigured")}</strong>
    </button>
  `;
}

// Save Variables: a note next to the button says Saving…, then Saved or Not saved
// with the error (render/savenote.ts).
async function saveSettingsFromForm() {
  const fields = Array.from(document.querySelectorAll<HTMLInputElement>("[data-setting]")).map(settingField);
  const values = settingsSaveValues(fields);
  await runSave({
    save: async () => {
      await SaveSettings(values);
    },
    refresh: async () => {
      await loadAppState();
      if (activeView === "settings" && state) await renderSettings(state);
      else render();
    },
    show: (html) => {
      const slot = document.querySelector("#save-settings-note");
      if (slot) slot.innerHTML = html;
    },
    busy: (on) => {
      const button = document.querySelector<HTMLButtonElement>("#save-settings");
      if (button) button.disabled = on;
    },
    refreshFailed: (error) => showErrorToast({scope: "settings", error: String(error)}),
  });
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
    title = t("main.background.unavailable.title");
    detail = backgroundError || t("main.background.unavailable.detail");
  } else if (on && running) {
    title = t("main.background.on.title");
    detail = t("main.background.on.detail");
  } else if (on) {
    title = t("main.background.starting.title");
    detail = t("main.background.starting.detail");
  } else {
    title = t("main.background.off.title");
    detail = t("main.background.off.detail");
  }
  const statusClass = unavailable ? "is-unavailable" : on && running ? "is-on" : "";
  return `
    <section class="card">
      <div class="card-header">
        <div>
          <span class="card-title">${ht("main.background.title")}</span>
          <p class="muted compact-copy">${ht("main.background.help")}</p>
        </div>
      </div>
      <div class="bg-toggle-row">
        <label class="switch" title="${ht("main.background.toggle")}">
          <input type="checkbox" id="bg-helper-toggle" role="switch" ${on ? "checked" : ""} ${unavailable ? "disabled" : ""} aria-label="${ht("main.background.toggle")}" />
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
      showInfoToast({scope: "background", message: t("main.background.turnedOn")});
    } else {
      await RemoveBackgroundHelper();
      showInfoToast({scope: "background", message: t("main.background.turnedOff")});
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

async function renderActivity(view?: ActivityView) {
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
  if (!state || page !== activityPage || activeView !== "activity" || request !== activityRequest) return;
  activityShown = data;
  dockerDrawn = dockerKey(state.runtime);
  root.innerHTML = withChrome(`
    <div class="app-layout">
      <div class="main-content">
        ${topbar("activity", state)}
        <main class="page-content">
          ${pageHead(t("main.activity.title"))}
          ${renderActivityPage(data, activityOpen, state.runtime)}
        </main>
        ${pageFoot()}
      </div>
    </div>
  `);
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  // The actions answer with the 24-hour view; render the window the owner chose.
  const run = async (action: () => Promise<ActivityView>) => {
    try {
      await action();
      await renderActivity();
    } catch (error) {
      showErrorToast({scope: "activity", error: String(error)});
      void renderActivity();
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
    void renderActivity({...data, enabled: false});
    void run(() => SetActivityEnabled(on)).finally(() => {
      activityDeleting--;
      activityOpen = {};
    });
  });
  document.querySelector<HTMLSelectElement>("#activity-days")?.addEventListener("change", (event) => {
    activityDays = Number((event.target as HTMLSelectElement).value) || 1;
    activityRequest++;
    activityOpen = {};
    void renderActivity();
  });
  document.querySelector("#activity-delete")?.addEventListener("click", () => {
    if (!window.confirm(t("main.activity.deleteConfirm"))) return;
    activityRequest++;
    activityOpen = {};
    // As on switch-off: no row opens until the delete is done, and the open
    // rows' addresses leave the screen now.
    activityDeleting++;
    void renderActivity(data);
    void run(() => DeleteActivityData()).finally(() => {
      activityDeleting--;
      activityOpen = {};
    });
  });
  document.querySelector("#activity-networks")?.addEventListener("click", () => {
    showInfoToast({scope: "activity", message: t("main.activity.downloadingNetworks")});
    void run(() => RefreshActivityNetworks());
  });
  document.querySelectorAll<HTMLButtonElement>("[data-activity-expand]").forEach((button) => {
    button.addEventListener("click", async () => {
      const name = button.dataset.activityExpand || "";
      if (activityDeleting > 0) return;
      if (Object.hasOwn(activityOpen, name)) {
        delete activityOpen[name];
        void renderActivity(data);
        return;
      }
      const open = activityOpen;
      open[name] = null;
      void renderActivity(data);
      let detail: ActivityDetail;
      try {
        detail = await GetActivityDetail(name, activityDays);
      } catch (error) {
        if (open !== activityOpen || open[name] !== null) return;
        delete open[name];
        showErrorToast({scope: "activity", error: String(error)});
        if (activeView === "activity" && activityShown) void renderActivity(activityShown);
        return;
      }
      // The window changed, the data was deleted or the row was closed meanwhile:
      // this answer belongs to a page that is gone.
      if (open !== activityOpen || open[name] !== null) return;
      open[name] = detail;
      // Redraw the newest view, not the one this click came from.
      if (activeView === "activity" && activityShown) void renderActivity(activityShown);
    });
  });
}

// Counts Fleet redraws, so one that finishes after a newer one never writes the page.
let fleetRequest = 0;

async function renderFleet() {
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
  if (!state || !fleetRedrawIsCurrent(request, fleetRequest, activeView)) return;
  proxiesState = proxies;
  dockerDrawn = dockerKey(state.runtime);
  // The open proxy editor's typed values live only in the DOM; see keepsLiveProxyEditor.
  const liveEditor = document.querySelector<HTMLFormElement>("[data-proxy-editor]");
  root.innerHTML = withChrome(`
    <div class="app-layout">
      <div class="main-content">
        ${topbar("fleet", state)}
        <main class="page-content">
          ${pageHead(t("main.fleet.title"))}
          <section class="stats-grid">
            ${metricCard(t("main.fleet.workers"), `${fleet.workers}`, t("main.fleet.workersCaption"))}
            ${metricCard(t("main.fleet.mobiles"), `${fleet.mobiles}`, t("main.fleet.mobilesCaption"))}
            ${metricCard(t("main.fleet.online"), `${fleet.online}`, t("main.fleet.onlineCaption"))}
            ${metricCard(t("main.fleet.services"), `${fleet.services}`, t("main.fleet.servicesCaption"))}
          </section>

          <section class="card">
            <div class="card-header">
              <span class="card-title">${ht("main.fleet.add.title")}</span>
            </div>
            ${renderFleetConnect(fleet, renderFleetSnippets(fleet))}
            <div class="fleet-form">
              <input id="fleet-name" placeholder="${ht("main.fleet.add.name")}" />
              <select id="fleet-kind">
                <option value="worker">${ht("main.fleet.add.worker")}</option>
                <option value="mobile">${ht("main.fleet.add.mobile")}</option>
              </select>
              <input id="fleet-endpoint" placeholder="${ht("main.fleet.add.endpoint")}" />
              <input id="fleet-services" placeholder="${ht("main.fleet.add.services")}" />
              <button class="primary compact-btn" id="add-fleet-device">${ht("main.fleet.add.register")}</button>
            </div>
          </section>

          <section class="card">
            <div class="card-header">
              <span class="card-title">${ht("main.fleet.devices")}</span>
            </div>
            <div class="fleet-list">
              ${fleet.devices.map((device) => renderFleetDevice(device) + (device.kind === "worker" ? workerCopiesSection(device.id, fleet.workerCommands?.[String(device.id)]) : "")).join("")}
            </div>
          </section>

          ${proxiesState ? renderProxiesSection(proxiesState, proxyEditing, proxyDeployStatus, state.runtime) : ""}
        </main>
        ${pageFoot()}
      </div>
    </div>
  `);
  wireChrome();
  wireShellNav();
  maybeResetScroll();
  document.querySelector("#add-fleet-device")?.addEventListener("click", () => void addFleetDevice());
  document.querySelectorAll<HTMLButtonElement>("[data-remove-device]").forEach((button) => {
    button.addEventListener("click", () => void removeFleetDevice(Number(button.dataset.removeDevice || 0)));
  });
  document.querySelectorAll<HTMLButtonElement>("[data-worker-copy]").forEach((button) => {
    button.addEventListener("click", () => void commandWorkerCopy(button, fleet));
  });
  wireProxies();
  const freshEditor = document.querySelector<HTMLFormElement>("[data-proxy-editor]");
  if (liveEditor && freshEditor && keepsLiveProxyEditor(liveEditor.dataset.proxyEditor, freshEditor.dataset.proxyEditor)) {
    freshEditor.replaceWith(liveEditor);
  }
  // The page shows the snippets with the key masked; the real ones stay in memory.
  const snippets: Record<FleetSnippet, string> = {worker: fleet.workerSnippet, mobile: fleet.mobileSnippet};
  document.querySelectorAll<HTMLButtonElement>("[data-fleet-copy]").forEach((button) => {
    button.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(snippets[button.dataset.fleetCopy as FleetSnippet] || "");
        flashCopied(button);
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
          <p class="muted compact-copy">${escapeHtml(device.endpoint || t("main.fleet.device.noEndpoint"))} · ${escapeHtml(device.os || t("main.fleet.device.unknownOs"))} ${escapeHtml(device.arch || "")} · ${escapeHtml(device.lastSeen || t("main.fleet.device.neverSeen"))}</p>
        </div>
        <div class="device-actions">
          <span class="badge">${escapeHtml(deviceKindLabel(device.kind))}</span>
          ${device.id > 0 ? `<button class="danger compact-btn" data-remove-device="${device.id}">${ht("main.fleet.device.remove")}</button>` : ""}
        </div>
      </div>
      <div class="badge-row">${(device.services || []).map((service) => `<span class="badge success">${escapeHtml(service)}</span>`).join("") || `<span class="badge">${ht("main.fleet.device.noServices")}</span>`}</div>
    </article>
  `;
}

// A device's kind on its badge. A kind this file does not know is shown as sent.
function deviceKindLabel(kind: string) {
  switch (kind) {
    case "worker":
      return t("main.fleet.device.kindWorker");
    case "mobile":
      return t("main.fleet.device.kindMobile");
    default:
      return kind;
  }
}

// A Docker change on Fleet touches only the Proxies section. Redraw that section
// alone, so the Add Worker form, the Run a copy choices and an open proxy editor
// keep what the user typed.
function redrawFleetProxies(current: AppState) {
  dockerDrawn = dockerKey(current.runtime);
  const panel = document.querySelector<HTMLElement>(".proxies-panel");
  if (!panel || !proxiesState) return;
  const liveEditor = panel.querySelector<HTMLFormElement>("[data-proxy-editor]");
  const chosen = [...panel.querySelectorAll<HTMLSelectElement>("[data-proxy-run] select")].map((select) => select.value);
  const holder = document.createElement("div");
  holder.innerHTML = renderProxiesSection(proxiesState, proxyEditing, proxyDeployStatus, current.runtime);
  const fresh = holder.querySelector<HTMLElement>(".proxies-panel");
  if (!fresh) return;
  fresh.querySelectorAll<HTMLSelectElement>("[data-proxy-run] select").forEach((select, index) => {
    const value = chosen[index];
    if (value !== undefined && [...select.options].some((option) => option.value === value && !option.disabled)) select.value = value;
  });
  panel.replaceWith(fresh);
  wireShellNav(fresh);
  wireProxies();
  const freshEditor = fresh.querySelector<HTMLFormElement>("[data-proxy-editor]");
  if (liveEditor && freshEditor && keepsLiveProxyEditor(liveEditor.dataset.proxyEditor, freshEditor.dataset.proxyEditor)) {
    freshEditor.replaceWith(liveEditor);
  }
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
    showInfoToast({scope: "proxy test", message: result.country
      ? t("main.proxy.testResultCountry", {ip: result.exitIp ?? "", country: result.country, ms: result.ms ?? ""})
      : t("main.proxy.testResult", {ip: result.exitIp ?? "", ms: result.ms ?? ""})});
  });
  on("[data-proxy-delete]", async (b) => {
    if (!confirm(t("main.proxy.deleteConfirm"))) return;
    await DeleteProxy(Number(b.dataset.proxyDelete));
  });
  on("[data-proxy-reset]", async (b) => {
    const started = await ResetProxyUsage(Number(b.dataset.proxyReset));
    showInfoToast({scope: "proxy", message: started.length ? t("main.proxy.periodStartedWith", {started: started.join(", ")}) : t("main.proxy.periodStarted")});
  });
  on("[data-proxy-copy-stop]", (b) => StopProxiedCopy(b.dataset.slug || "", Number(b.dataset.proxyCopyStop)));
  on("[data-proxy-copy-start]", (b) => StartProxiedCopy(b.dataset.slug || "", Number(b.dataset.proxyCopyStart)));
  on("[data-proxy-copy-remove]", async (b) => {
    if (!confirm(t("main.proxy.removeCopyConfirm"))) return;
    await RemoveProxiedCopy(b.dataset.slug || "", Number(b.dataset.proxyCopyRemove));
  });
  on("[data-proxy-run-deploy]", async () => {
    if (proxyDeployStatus?.kind === "busy") return;
    const slug = valueOf("[data-proxy-run-service]");
    const id = Number(valueOf("[data-proxy-run-proxy]"));
    const say = (kind: DeployStatus["kind"], text: string) => {
      proxyDeployStatus = { kind, text };
      const slot = document.querySelector<HTMLElement>("[data-proxy-deploy-status]");
      if (slot) slot.innerHTML = deployNotice(kind, text);
    };
    if (!slug || !id) {
      say("cancelled", t("main.proxy.chooseFirst"));
      return;
    }
    const service = proxiesState?.candidates.find((item) => item.slug === slug)?.name || slug;
    const proxy = proxyById(id)?.name || t("main.proxy.selected");
    await runDeployWithStatus(t("main.proxy.deploying", {service, proxy}), say, async () => {
      const result = await DeployProxiedCopy(slug, id);
      return {
        kind: "ok",
        text: `${result.exitIp ? t("main.proxy.running", {service, proxy, ip: result.exitIp}) : t("main.proxy.runningNoIp", {service, proxy})}${result.warning ? ` ${result.warning}` : ""}`,
      };
    });
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

// One command to one copy on a worker (render/workercopies.ts). Only Remove asks first.
async function commandWorkerCopy(button: HTMLButtonElement, fleet: FleetState) {
  const workerId = Number(button.dataset.workerId || 0);
  const slug = button.dataset.slug || "";
  const command = button.dataset.workerCopy || "";
  const instance = Number(button.dataset.instance || 1);
  const worker = fleet.devices.find((device) => device.id === workerId);
  const question = copyCommandQuestion(command, slug, instance, worker?.name || t("main.worker.this"), fleet.workerCommands?.[String(workerId)]?.copies || []);
  if (question && !confirm(question)) return;
  button.disabled = true;
  try {
    showInfoToast({scope: "worker", message: await CommandWorkerCopy(workerId, slug, command, instance)});
    render();
  } catch (error) {
    showErrorToast({scope: "worker", error: String(error)});
    button.disabled = false;
  }
}

async function removeFleetDevice(id: number) {
  if (!confirm(t("main.fleet.removeConfirm"))) return;
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

// The change against the day or the month before, as a caption under the amount.
function changeCaption(pct: number, period: "day" | "month") {
  if (!pct || !Number.isFinite(pct)) return period === "day" ? t("main.change.noneDay") : t("main.change.noneMonth");
  const args = {arrow: pct > 0 ? "▲" : "▼", pct: Math.abs(pct).toFixed(1)};
  return period === "day" ? t("main.change.day", args) : t("main.change.month", args);
}

function renderPointsSection(points: PointsBalance[]) {
  return `
    <section class="card points-panel">
      <div class="card-header">
        <div>
          <span class="card-title">${ht("main.points.title")}</span>
          <p class="muted compact-copy">${ht("main.points.help")}</p>
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

function openWizard(input?: string | Event) {
  closeWizardDeployDrafts();
  const slug = typeof input === "string" ? input : "";
  activeView = "wizard";
  wizardStep = slug ? 3 : 1;
  wizardSelected = slug ? [slug] : [];
  wizardRouteDrafts = {};
  wizardWhereDrafts = {};
  const service = state?.services.find((svc) => svc.slug === slug);
  wizardCategories = service ? [service.category] : [];
  render();
}

function renderSetupWizard(current: AppState) {
  // Reuse only the edits from cards that stay open during this redraw.
  const secretDrafts = new Map([...root.querySelectorAll<HTMLInputElement>('[data-wizard-env][type="password"]')]
    .filter((input) => input.value)
    .map((input) => [wizardSecretInputKey(input), input.value]));
  const services = current.services || [];
  const categories = wizardCategoryChoices(services);
  if (wizardCategories.length === 0 && categories.length > 0) {
    wizardCategories = [categories[0]];
  }
  const filtered = services.filter((svc) => wizardCategories.includes(svc.category));
  const selectedServices = services.filter((svc) => wizardSelected.includes(svc.slug));
  root.innerHTML = withChrome(`
    <div class="app-layout">
      <div class="main-content">
        ${topbar("wizard", current)}
      <main class="page-content setup-content">
        ${pageHead(t("main.wizard.title"), `<button class="btn btn-ghost btn-sm" id="close-wizard">${ht("main.wizard.backToDashboard")}</button>`)}
        <p class="page-lede">${ht("main.wizard.lede")}</p>
        ${renderWizardProgress()}
        <section class="panel wizard-panel active">
          ${wizardStep === 1 ? renderWizardCategories(categories) : ""}
          ${wizardStep === 2 ? renderWizardServices(filtered) : ""}
          ${wizardStep === 3 ? renderWizardSetup(selectedServices) : ""}
          ${wizardStep === 4 ? renderWizardSummary(selectedServices) : ""}
        </section>
        <div class="wizard-footer">
          <button class="secondary" id="wizard-prev" ${wizardStep === 1 ? "disabled" : ""}>${ht("main.wizard.back")}</button>
          <button class="primary" id="wizard-next">${wizardStep === 4 ? ht("main.wizard.goToDashboard") : wizardStep === 3 ? ht("main.wizard.toSummary") : ht("main.wizard.next")}</button>
        </div>
      </main>
        ${pageFoot()}
      </div>
    </div>
  `);
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
  document.querySelectorAll<HTMLSelectElement>("[data-wizard-where]").forEach((select) => {
    select.addEventListener("change", () => {
      select.setCustomValidity("");
      wizardWhereDrafts[select.dataset.wizardWhere || ""] = whereChoice(select.value);
    });
  });
  document.querySelectorAll<HTMLButtonElement>("[data-wizard-action]").forEach((button) => {
    button.addEventListener("click", () => {
      const slug = button.dataset.slug || "";
      const action = button.dataset.wizardAction || "";
      if (action === "deploy") {
        const where = button.closest("[data-form-slug]")?.querySelector<HTMLSelectElement>("[data-wizard-where]");
        const workerId = whereChoice(where?.value);
        const refusal = missingWizardWorker(workerId);
        where?.setCustomValidity(refusal);
        if (refusal) where?.reportValidity();
      }
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
  wireSessionConsent(document);
  wireWalletChoices();
  for (const input of root.querySelectorAll<HTMLInputElement>('[data-wizard-env][type="password"]')) {
    input.value = secretDrafts.get(wizardSecretInputKey(input)) || "";
  }
  fillNewWalletPanel();
  selectedServices.forEach((service) => {
    if (service.collector?.accrual === "pool") {
      const card = root.querySelector(`[data-form-slug="${CSS.escape(service.slug)}"]`);
      const facts = card?.querySelector<HTMLElement>("[data-payout-facts]");
      const output = card?.querySelector<HTMLElement>("[data-output-slug]");
      card?.querySelectorAll("[data-wizard-env]").forEach((input) => {
        input.addEventListener("input", () => {
          if (facts) facts.innerHTML = renderPayoutFacts(service);
          if (output) output.textContent = "";
        });
      });
    }
    void hydrateWizardForm(service);
  });
  selectedServices.forEach((service) => void hydrateWizardPreflight(service));
  selectedServices.forEach((service) => void hydratePayouts(service));
}

// The desktop-app risk disclosure (render/session.ts). The tickbox gate itself is in
// render/session_wire.ts. Accept and Withdraw record the choice in the config, then
// redraw only that app's slot and the Settings card, never the whole page, which
// would drop credentials typed in the other cards but not saved yet.
function wireSessionConsent(scope: ParentNode) {
  wireSessionConsentGate(scope, {
    accept: (slug, textVersion) => queueSessionConsent(() => acceptSessionRisk(slug, textVersion)),
    revoke: (slug) => queueSessionConsent(() => revokeSessionRisk(slug)),
  });
}

// Accept and Withdraw run one after the other (render/session_wire.ts, oneAtATime).
const queueSessionConsent = oneAtATime();

async function acceptSessionRisk(slug: string, textVersion: string) {
  try {
    applySessionConsents(await AcceptSessionRisk(slug, textVersion), slug);
  } catch (error) {
    showErrorToast({scope: "consent", error: String(error)});
  }
}

async function revokeSessionRisk(slug: string) {
  const name = state?.services.find((svc) => svc.slug === slug)?.name || slug;
  if (!confirm(t("main.consent.withdrawConfirm", {name}))) return;
  try {
    applySessionConsents(await RevokeSessionRisk(slug), slug);
  } catch (error) {
    showErrorToast({scope: "consent", error: String(error)});
  }
}

function applySessionConsents(consents: Record<string, SessionConsent> | null, slug: string) {
  if (state) state = {...state, config: {...state.config, sessionConsent: consents}};
  const service = state?.services.find((svc) => svc.slug === slug);
  const slot = document.querySelector<HTMLElement>(`[data-session-slot="${CSS.escape(slug)}"]`);
  if (slot && service) {
    slot.innerHTML = renderSessionSlot(service, consents?.[slug]);
    wireSessionConsent(slot);
    slot.querySelectorAll<HTMLButtonElement>("[data-url]").forEach((button) => {
      button.addEventListener("click", () => {
        if (button.dataset.url) BrowserOpenURL(button.dataset.url);
      });
    });
  }
  const card = document.querySelector<HTMLElement>("[data-session-consent-card]");
  if (card) {
    card.insertAdjacentHTML("afterend", renderSessionConsentCard(consents, state?.services));
    const fresh = card.nextElementSibling;
    card.remove();
    if (fresh) wireSessionConsent(fresh);
  }
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
    showErrorToast({scope: "cash-out", error: t("main.payout.enterAmount")});
    return;
  }
  try {
    const payout = await RecordPayout(service.slug, amount, field("currency"), field("method"), field("date"));
    if (payout.fxRateUsd === null || payout.fxRateUsd === undefined) {
      showInfoToast({scope: "cash-out", message: t("main.payout.noRate")});
    }
    await afterPayoutChange(service);
  } catch (error) {
    showErrorToast({scope: "cash-out", error: String(error)});
  }
}

async function removePayout(service: Service, id: number) {
  if (!confirm(t("main.payout.removeConfirm"))) return;
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
  const labels = [ht("main.wizard.step.categories"), ht("main.wizard.step.services"), ht("main.wizard.step.setup"), ht("main.wizard.step.summary")];
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
  return `
    <h2>${ht("main.wizard.categories.title")}</h2>
    <p class="muted">${ht("main.wizard.categories.lede")}</p>
    <div class="wizard-category-grid">
      ${categories.map((category) => `
        <button class="wizard-category ${wizardCategories.includes(category) ? "selected" : ""}" data-category="${category}">
          <strong>${escapeHtml(wizardCategoryName(category))}</strong>
          <span>${escapeHtml(wizardCategoryCopy(category))}</span>
        </button>
      `).join("")}
    </div>
  `;
}

// A category card's title. The wizard has always written DePIN as "Depin"; the
// catalog's kind names (kindLabel) give every other one the same word.
function wizardCategoryName(category: string) {
  return category === "depin" ? capitalize(category) : kindLabel(category) || capitalize(category);
}

function renderWizardServices(services: Service[]) {
  return `
    <h2>${ht("main.wizard.services.title")}</h2>
    <p class="muted">${ht("main.wizard.services.lede")}</p>
    <div class="wizard-service-grid">
      ${services.map((service) => `
        <button class="wizard-service ${wizardSelected.includes(service.slug) ? "selected" : ""}" data-wizard-service="${escapeHtml(service.slug)}">
          <div class="service-icon">${escapeHtml(service.name[0] || "?")}</div>
          <div>
            <strong>${escapeHtml(service.name)}</strong>
            <span>${escapeHtml(service.shortDescription || service.category)}</span>
            <small>${service.manualOnly ? ht("main.wizard.services.manual") : ht("main.wizard.services.docker")}${service.requirements?.residentialIp ? ` / ${ht("main.wizard.services.residentialIp")}` : ""}</small>
          </div>
        </button>
      `).join("") || `<p class="muted">${ht("main.wizard.services.none")}</p>`}
    </div>
  `;
}

function renderWizardSetup(services: Service[]) {
  if (services.length === 0) {
    return `<p class="muted">${ht("main.wizard.setup.none")}</p>`;
  }
  return `
    <h2>${ht("main.wizard.setup.title")}</h2>
    <p class="muted">${ht("main.wizard.setup.lede")}</p>
    ${composeSelectionExport(services)}
    <div class="wizard-setup-list">
      ${services.map((service) => renderWizardServiceSetup(service, state?.collectorFields, state?.hostname, state?.summary?.breakdown, deploymentOf(service.slug)?.customImage, state?.scaling?.[service.slug], deploymentOf(service.slug)?.instances, state?.route, wizardRouteDrafts[service.slug], state?.config?.sessionConsent, Boolean(deploymentOf(service.slug)), state?.workerTargets, wizardWhereDrafts[service.slug], wizardDeployFeedback[service.slug])).join("")}
    </div>
  `;
}

function renderWizardSummary(services: Service[]) {
  return `
    <div class="wizard-summary">
      <h2>${ht("main.wizard.summary.title")}</h2>
      <p class="muted">${ht("main.wizard.summary.lede")}</p>
      <div class="earnings-breakdown" id="wizard-runtime-summary">${wizardSummaryServices(services)}</div>
    </div>
  `;
}

function wizardSummaryServices(services: Service[]) {
  return services.map((service) => {
    const gap = state && wizardWhereDrafts[service.slug] == null && localDockerGap(service, state.runtime);
    const status = service.manualOnly ? ht("main.wizard.summary.tracking") : gap ? ht("main.wizard.summary.checkDocker") : ht("main.wizard.summary.ready");
    return `<div class="earning-chip"><span>${escapeHtml(service.name)}</span><strong>${status}</strong><small>${escapeHtml(service.category)}</small></div>`;
  }).join("");
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

// The Where box's answer: a worker id, or null for this computer.
function readWizardWhere(slug: string): number | null {
  const select = document.querySelector<HTMLSelectElement>(`[data-form-slug="${slug}"] [data-wizard-where]`);
  return select ? whereChoice(select.value) : null;
}

function missingWizardWorker(workerId: number | null): string {
  if (workerId === null || state?.workerTargets?.some((worker) => worker.id === workerId)) return "";
  return t("main.wizard.workerUnavailable");
}

function closeWizard() {
  dropNewWalletPhrase();
  wizardStep = 1;
  wizardRouteDrafts = {};
  wizardWhereDrafts = {};
  showView("dashboard");
}

function wizardSecretInputKey(input: HTMLInputElement) {
  return JSON.stringify([input.closest<HTMLElement>("[data-form-slug]")?.dataset.formSlug, input.dataset.wizardEnv]);
}

function closeWizardDeployDrafts() {
  for (const input of root.querySelectorAll<HTMLInputElement>('[data-wizard-env][type="password"]')) input.value = "";
  for (const settings of wizardDeploying.values()) {
    settings.open = false;
    clearRequestSecrets(settings.values, settings.secretKeys, settings.wallet);
  }
  for (const feedback of Object.values(wizardDeployFeedback)) delete feedback.retrySecret;
}

function wizardCredentialInputs(slug: string) {
  return document.querySelectorAll<HTMLInputElement>(`[data-form-slug="${CSS.escape(slug)}"] [data-wizard-env]`);
}

function markWizardSecretsSaved(slug: string, keys: string[]) {
  for (const input of wizardCredentialInputs(slug)) {
    if (input.type === "password" && keys.includes(input.dataset.wizardEnv || "")) {
      input.dataset.savedSecret = "true";
      input.placeholder = t("main.wizard.secretSaved");
    }
  }
}

async function hydrateWizardForm(service: Service) {
  const inputs = wizardCredentialInputs(service.slug);
  try {
    const { values, savedSecrets } = await GetCredentials(service.slug);
    for (const input of inputs) {
      if (!input.isConnected) continue;
      const key = input.dataset.wizardEnv || "";
      if (input.type === "password") {
        if (savedSecrets[key]) {
          input.dataset.savedSecret = "true";
          input.placeholder = t("main.wizard.secretSaved");
        }
      } else if (values[key]) {
        input.value = values[key];
      }
    }
  } catch (error) {
    showErrorToast({scope: "credentials", error: String(error)});
  }
}

// The pre-deploy check, filled in beside the Deploy button once the backend has
// answered. It runs after the card is on screen because it talks to the container
// runtime for the engine architecture. The shared Docker status drives the local
// Deploy button and setup message while those findings load.
async function hydrateWizardPreflight(service: Service) {
  const slot = document.querySelector<HTMLDivElement>(`[data-preflight-slug="${service.slug}"]`);
  if (!slot) return;
  const select = document.querySelector<HTMLSelectElement>(`[data-wizard-where="${service.slug}"]`);
  const deploy = document.querySelector<HTMLButtonElement>(`[data-wizard-action="deploy"][data-slug="${service.slug}"]`);
  let report: PreflightReport | null = null;
  const draw = () => {
    if (!slot.isConnected || !state) return;
    const worker = whereChoice(select?.value);
    const gap = localDockerGap(service, state.runtime);
    if (deploy) deploy.disabled = service.manualOnly || (worker === null && !!gap);
    slot.innerHTML = renderPreflight(report, worker, gap);
    slot.querySelector('[data-view="runtime"]')?.addEventListener("click", () => {
      ({ step: wizardStep, drafts: wizardRouteDrafts } = wizardAfterNav("runtime", wizardStep));
      wizardWhereDrafts = {};
      resetScrollAfterRender = true;
      showView("runtime");
    });
  };
  wizardPreflightDraws.set(service.slug, draw);
  select?.addEventListener("change", draw);
  draw();
  report = await PreflightService(service.slug).catch(() => null);
  draw();
}

function readWizardForm(slug: string) {
  const values: Record<string, string> = {};
  const savedSecrets: Record<string, boolean> = {};
  const secretKeys: string[] = [];
  for (const input of wizardCredentialInputs(slug)) {
    const key = input.dataset.wizardEnv || "";
    values[key] = input.value;
    if (input.type === "password") {
      if (input.dataset.savedSecret) savedSecrets[key] = true;
      secretKeys.push(key);
      input.value = "";
    }
  }
  const typedSecrets = secretKeys.filter((key) => values[key].trim());
  return { values, savedSecrets, secretKeys, typedSecrets };
}

function clearRequestSecrets(values: Record<string, string>, keys: string[], wallet?: WalletChoice) {
  for (const key of keys) delete values[key];
  if (wallet) wallet.phrase = "";
}

// Wails serializes the arguments during the call. Drop the secret fields
// before awaiting the reply or refreshing the app.
function sendCredentialRequest<T>(
  values: Record<string, string>, keys: string[], send: (request: Record<string, string>) => Promise<T>,
): Promise<T> {
  const request = { ...values };
  try {
    return send(request);
  } finally {
    clearRequestSecrets(request, keys);
  }
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
  const workerId = action === "deploy" ? readWizardWhere(slug) : null;
  const invalid = workerId === null ? firstInvalidWizardInput(slug) : null;
  if (invalid && action === "save") {
    invalid.reportValidity();
    if (output) output.textContent = t("main.wizard.badShape", {field: invalid.dataset.wizardEnv ?? ""});
    return;
  }
  if (action === "deploy") {
    invalid?.reportValidity();
    if (wizardDeploying.has(slug)) return;
    try {
      await wizardDeploy(slug, workerId, invalid ? invalid.dataset.wizardEnv || t("main.wizard.thisField") : undefined);
    } finally {
      const settings = wizardDeploying.get(slug);
      if (settings) clearRequestSecrets(settings.values, settings.secretKeys, settings.wallet);
      wizardDeploying.delete(slug);
    }
    return;
  }
  if (action !== "save" && action !== "collect") return;
  const { values, secretKeys, typedSecrets } = readWizardForm(slug);
  const pool = action === "collect" ? state?.services?.find((service) => service.slug === slug && service.collector?.accrual === "pool") : undefined;
  const card = pool ? output?.closest(".wizard-setup-card") : null;
  const facts = card?.querySelector<HTMLElement>("[data-payout-facts]");
  const controls = card?.querySelectorAll<HTMLInputElement | HTMLButtonElement>('[data-wizard-env], [data-wizard-action="collect"]');
  try {
    controls?.forEach((control) => { control.disabled = true; });
    if (pool && facts) facts.innerHTML = renderPayoutFacts(pool);
    const saved = sendCredentialRequest(values, secretKeys, (request) => SaveCredentials(slug, request));
    clearRequestSecrets(values, secretKeys);
    await saved;
    markWizardSecretsSaved(slug, typedSecrets);
    const feedback = wizardDeployFeedback[slug];
    if (feedback?.retrySecret) {
      delete feedback.retrySecret;
      setWizardDeployFeedback(slug, feedback);
    }
    if (action === "save") {
      if (output) output.textContent = t("main.wizard.credentialsSaved");
      return;
    }
    const record = await CollectService(slug);
    if (pool && facts) {
      const balance = record.error ? null : { amount: record.balance, currency: record.currency };
      facts.innerHTML = renderPayoutFacts(pool, balance);
    }
    if (output) {
      const success = facts ? t("main.wizard.poolUpdated") : t("main.service.collected", {amount: formatBalance(record.balance, record.currency)});
      output.textContent = record.error || success;
    }
    await loadAppState();
  } catch (error) {
    if (output) output.textContent = String(error);
  } finally {
    controls?.forEach((control) => { control.disabled = false; });
    clearRequestSecrets(values, secretKeys);
  }
}

function setWizardDeployFeedback(slug: string, status: DeployFeedback) {
  wizardDeployFeedback[slug] = status;
  const panel = document.querySelector<HTMLDivElement>(`[data-deploy-panel="${CSS.escape(slug)}"]`);
  if (!panel) return;
  panel.innerHTML = renderDeployFeedback(status);
  panel.scrollIntoView({ block: "nearest" });
}

function sayDeploy(slug: string, kind: DeployStatus["kind"], text: string) {
  setWizardDeployFeedback(slug, { kind, text });
}

// A redraw or navigation can remove the screen while it waits for an answer.
function askDeployConsent(panel: HTMLElement | null, name: string, step: AdminStep): Promise<boolean> {
  if (!panel?.isConnected) return Promise.resolve(false);
  return new Promise((resolve) => {
    const finish = (approved: boolean) => {
      observer.disconnect();
      resolve(approved);
    };
    const observer = new MutationObserver(() => {
      if (!panel.isConnected) finish(false);
    });
    observer.observe(root, { childList: true, subtree: true });
    panel.innerHTML = adminConsentScreen(name, step);
    panel.scrollIntoView({ block: "nearest" });
    panel.querySelectorAll<HTMLButtonElement>("[data-deploy-consent]").forEach((button) => {
      button.addEventListener("click", () => finish(panel.isConnected && button.dataset.deployConsent === "go"), { once: true });
    });
    panel.querySelector<HTMLButtonElement>('[data-deploy-consent="go"]')?.focus();
  });
}

interface WizardDeploySettings {
  open: boolean;
  values: Record<string, string>;
  proxyId: number | null;
  image: string | null;
  instances: number | null;
  route: RouteState | null | undefined;
  secretKeys: string[];
  typedSecrets: string[];
  wallet?: WalletChoice;
}

// A deploy from the card: every field marked * is filled, and when Windows will
// show its admin prompt, CashPilot's own screen comes first. Then the deploy, whose
// answer replaces "Deploying..." next to Deploy however it ends.
async function wizardDeploy(slug: string, workerId: number | null, invalidField?: string) {
  const service = state?.services?.find((item) => item.slug === slug);
  const name = service?.name || slug;
  const fields = service && !service.custom ? serviceFormFields(service, state?.collectorFields) : [];
  const panel = document.querySelector<HTMLDivElement>(`[data-deploy-panel="${slug}"]`);
  const { values, secretKeys, typedSecrets, savedSecrets } = readWizardForm(slug);
  const settings: WizardDeploySettings = {
    open: true, values, secretKeys, typedSecrets,
    proxyId: readWizardRoute(slug),
    image: readWizardImage(slug),
    instances: readWizardInstances(slug),
    route: state?.route,
    wallet: workerId === null && seedFileVar(service) ? readWalletChoice(slug) : undefined,
  };
  wizardDeploying.set(slug, settings);
  const restoreSecretEdits = () => {
    if (!settings.open) return false;
    let restored = false;
    for (const input of wizardCredentialInputs(slug)) {
      const key = input.dataset.wizardEnv || "";
      if (input.type === "password" && typedSecrets.includes(key) && values[key] && !input.value) {
        input.value = values[key];
        restored = true;
      }
    }
    clearRequestSecrets(values, secretKeys);
    return restored;
  };
  const say = (kind: DeployStatus["kind"], text: string) => {
    if (kind === "partial") {
      markWizardSecretsSaved(slug, typedSecrets);
      clearRequestSecrets(values, secretKeys);
    }
    const retrySecret = (kind === "failed" || kind === "cancelled") && restoreSecretEdits();
    setWizardDeployFeedback(slug, { kind, text, retrySecret, worker: workerId !== null });
  };
  const { proxyId, route } = settings;
  const replacesLocal = proxyId === null
    ? state?.deployments?.some((deployment) => deployment.slug === slug)
    : route?.recorded?.[slug] === proxyId;
  const outcome = await runCheckedDeploy(fields, values, proxyId === null, t("main.deploy.deploying", {name}), {
    confirmReplace: (message) => confirm(message),
    adminStep: () => DeployAdminStep(slug),
    askConsent: (step) => askDeployConsent(panel, name, step),
    say,
    deploy: async (approval, worker) => {
      if (!settings.open) return { kind: "cancelled", text: t("main.deploy.setupClosed") };
      const answer = await deployFromCard(slug, settings, worker, approval);
      if (answer.kind === "ok") clearRequestSecrets(values, secretKeys);
      return answer;
    },
  }, {
    workerId,
    workers: state?.workerTargets,
    workerBusyText: (worker) => t("main.deploy.deployingToWorker", {name, worker: worker.name}),
    invalidField,
    savedSecrets,
    localReplacement: replacesLocal
      ? proxyId === null ? t("main.deploy.redeployHere", {name}) : t("main.deploy.redeployProxy", {name})
      : undefined,
  }).catch((error) => {
    say("failed", deployErrorText(error));
    return null;
  });
  const form = document.querySelector<HTMLElement>(`[data-form-slug="${slug}"]`);
  form?.querySelectorAll<HTMLInputElement>("[data-wizard-env]").forEach((input) => input.removeAttribute("aria-invalid"));
  if (!outcome || outcome.kind === "done") return;
  if (outcome.kind === "missing") {
    const keys = new Set(outcome.missing.map((field) => field.key));
    form?.querySelectorAll<HTMLInputElement>("[data-wizard-env]").forEach((input) => {
      if (keys.has(input.dataset.wizardEnv || "")) input.setAttribute("aria-invalid", "true");
    });
    setWizardDeployFeedback(slug, { kind: "missing", missing: outcome.missing, retrySecret: restoreSecretEdits() });
    form?.querySelector<HTMLInputElement>('[aria-invalid="true"]')?.focus();
    return;
  }
  if (outcome.kind === "cancelled") say("cancelled", cancelledText());
  else say("failed", outcome.message);
}

// The deploy itself, once the card's checks passed: to a worker, with a wallet
// seed (Sia's hostd), or on this computer, directly or through a proxy. Every path
// answers with what the note next to Deploy says.
async function deployFromCard(
  slug: string, settings: WizardDeploySettings, worker: WorkerTarget | undefined, approval: AdminStep | null,
): Promise<DeployAnswer> {
  const { values, proxyId, image, instances, route, secretKeys, typedSecrets, wallet } = settings;
  if (worker) {
    const outcome = await runWorkerDeploy(slug, worker, proxyId, instances, {
      confirm: (message) => settings.open && confirm(message),
      deploy: (yes) => sendCredentialRequest(values, secretKeys, (request) =>
        DeployToWorker(slug, { workerId: worker.id, values: request, image: image || "", ...yes }),
      ),
    });
    if (outcome.kind === "deployed") {
      if (!outcome.result.credentialWarning) markWizardSecretsSaved(slug, typedSecrets);
      return { kind: "ok", text: workerDeployText(outcome.result) };
    }
    return { kind: "cancelled", text: outcome.kind === "refused" ? outcome.message : t("main.deploy.nothingDeployed") };
  }
  if (wallet) return runWalletDeploy(slug, values, image, instances, wallet, secretKeys, typedSecrets);
  const outcome = await runRouteDeploy(slug, proxyId, image, instances, route, {
    confirm: (message) => confirm(message),
    routed: (proxyId) => sendCredentialRequest(values, secretKeys, (request) =>
      DeployServiceRouted(slug, request, proxyId),
    ),
    direct: (leaveRoute) => sendCredentialRequest(values, secretKeys, (request) => {
      if (approval) return DeployServiceWithAdminApproval(slug, request, image, instances, leaveRoute, approval);
      return leaveRoute ? DeployServiceLeavingRoute(slug, request, image, instances) : DeployService(slug, request, image, instances);
    }),
  });
  if (outcome.kind === "refused" || outcome.kind === "cancelled") {
    return { kind: "cancelled", text: outcome.kind === "refused" ? outcome.message : t("main.deploy.nothingDeployed") };
  }
  markWizardSecretsSaved(slug, typedSecrets);
  const followUp = {
    failure: t("main.deploy.refreshFailed"),
    run: async () => {
      await loadAppState();
      return deviceLinkText(state?.services?.find((service) => service.slug === slug), values, state?.addedDevices?.[slug] || "", state?.retryingDevices?.[slug] || "");
    },
  };
  if (outcome.kind === "routed") {
    return { kind: "ok", text: routedDeployText(outcome.result, ""), followUp };
  }
  const deployment = outcome.deployment;
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
  return { kind: "ok", text: t("main.deploy.deployed"), followUp };
}

// A wallet seed kept in a file (Sia's hostd; render/wallet.ts). The phrase is in this
// file's memory only while a panel shows it: newWallet from the deploy that made a
// wallet until its three words are checked back or the panel is closed, revealed for
// up to a minute after a reveal. Never in storage, a toast, the console or an error.
let newWallet: (PhrasePanelView & { words: string[] }) | null = null;
// token marks the reveal request a busy view waits on: an answer that arrives
// after a Cancel, another reveal or leaving the dashboard finds it gone and drops
// the phrase instead of drawing it.
let revealed: { slug: string; view: RevealView; timer: number | null; token?: object } | null = null;
const REVEAL_MS = 60_000;

function dropNewWalletPhrase() {
  if (!newWallet) return;
  newWallet.words.fill("");
  newWallet = null;
}

function hideRevealedSeed() {
  if (!revealed) return;
  if (revealed.timer !== null) window.clearTimeout(revealed.timer);
  if (revealed.view.kind === "shown") revealed.view.words.fill("");
  revealed = null;
}

// The deploy card's wallet choice: the phrase box shows only for "Use a wallet I
// already have".
function wireWalletChoices() {
  document.querySelectorAll<HTMLFieldSetElement>("[data-wallet-choice]").forEach((box) => {
    const sync = () => {
      const mode = box.querySelector<HTMLInputElement>("[data-wallet-mode]:checked")?.value;
      const phraseBox = box.querySelector<HTMLElement>("[data-wallet-phrase-box]");
      if (phraseBox) phraseBox.hidden = mode !== "import";
    };
    box.querySelectorAll<HTMLInputElement>("[data-wallet-mode]").forEach((radio) => radio.addEventListener("change", sync));
    sync();
  });
}

// The choice in slug's card, with the typed phrase read once and cleared from the box.
function readWalletChoice(slug: string): WalletChoice {
  const box = document.querySelector<HTMLElement>(`[data-wallet-choice="${slug}"]`);
  const mode = (box?.querySelector<HTMLInputElement>("[data-wallet-mode]:checked")?.value || "keep") as WalletChoice["mode"];
  const input = box?.querySelector<HTMLInputElement>("[data-wallet-phrase]");
  const phrase = mode === "import" ? input?.value || "" : "";
  if (input) input.value = "";
  return { mode, phrase };
}

async function runWalletDeploy(
  slug: string, values: Record<string, string>, image: string | null, instances: number | null, choice: WalletChoice,
  secretKeys: string[], typedSecrets: string[],
): Promise<DeployAnswer> {
  const refusal = walletChoiceRefusal(choice);
  if (refusal) return refusal;
  if (choice.mode === "generate") sayDeploy(slug, "busy", t("main.wallet.creating"));
  const result = await sendCredentialRequest(values, secretKeys, (request) => {
    const requestChoice = { ...choice };
    choice.phrase = "";
    try {
      return DeployServiceWallet(slug, request, image, instances, requestChoice);
    } finally {
      requestChoice.phrase = "";
    }
  });
  markWizardSecretsSaved(slug, typedSecrets);
  const wallet = result.wallet;
  dropNewWalletPhrase();
  if (wallet?.phrase) {
    newWallet = { slug, words: phraseWords(wallet.phrase), address: wallet.address || "", step: "show", positions: [], error: "" };
    wallet.phrase = "";
  }
  fillNewWalletPanel();
  return {
    ...walletDeployAnswer(wallet, newWallet !== null),
    followUp: {
      failure: t("main.deploy.refreshFailed"),
      run: async () => { await loadAppState(); },
    },
  };
}

// Draws the one-time panel in its card, and wires it. Called after every wizard draw.
function fillNewWalletPanel() {
  document.querySelectorAll<HTMLElement>("[data-wallet-panel]").forEach((slot) => {
    slot.innerHTML = newWallet && newWallet.slug === slot.dataset.walletPanel ? renderPhrasePanel(newWallet) : "";
  });
  if (!newWallet) return;
  const panel = document.querySelector<HTMLElement>(`[data-wallet-panel="${newWallet.slug}"]`);
  if (!panel) return;
  panel.querySelector("[data-wallet-written]")?.addEventListener("click", () => {
    if (!newWallet) return;
    newWallet.step = "check";
    newWallet.positions = pickPositions(newWallet.words.length, 3, Math.random);
    newWallet.error = "";
    fillNewWalletPanel();
  });
  panel.querySelector("[data-wallet-back]")?.addEventListener("click", () => {
    if (!newWallet) return;
    newWallet.step = "show";
    newWallet.error = "";
    fillNewWalletPanel();
  });
  panel.querySelector("[data-wallet-dismiss]")?.addEventListener("click", () => {
    if (!confirm(t("main.wallet.dismissConfirm"))) return;
    dropNewWalletPhrase();
    fillNewWalletPanel();
  });
  panel.querySelector("[data-wallet-finish]")?.addEventListener("click", () => {
    if (!newWallet) return;
    const inputs = [...panel.querySelectorAll<HTMLInputElement>("[data-wallet-word]")];
    const typed = newWallet.positions.map((position) => inputs.find((input) => input.dataset.walletWord === String(position))?.value || "");
    inputs.forEach((input) => { input.value = ""; });
    if (!wordsMatch(newWallet.words, newWallet.positions, typed)) {
      newWallet.error = t("main.wallet.mismatch");
      fillNewWalletPanel();
      return;
    }
    const { slug, address } = newWallet;
    dropNewWalletPhrase();
    panel.innerHTML = renderPhrasePanel({ slug, words: [], address, step: "done", positions: [], error: "" });
    wireWalletCopy(panel);
  });
  wireWalletCopy(panel);
}

function wireWalletCopy(scope: ParentNode) {
  scope.querySelectorAll<HTMLButtonElement>("[data-wallet-copy]").forEach((button) => {
    button.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(button.dataset.walletCopy || "");
        flashCopied(button);
      } catch (error) {
        showErrorToast({scope: "clipboard", error: String(error)});
      }
    });
  });
}

// The dashboard's wallet line: the address's Copy button and the reveal. Called after
// every dashboard draw, so a redraw keeps a reveal that is still within its minute.
function wireWalletDetails() {
  wireWalletCopy(document);
  document.querySelectorAll<HTMLElement>("[data-wallet-reveal-slot]").forEach((slot) => drawReveal(slot));
  if (revealed && !document.querySelector(`[data-wallet-reveal-slot="${revealed.slug}"]`)) hideRevealedSeed();
}

function drawReveal(slot: HTMLElement) {
  const slug = slot.dataset.walletRevealSlot || "";
  const name = state?.services?.find((service) => service.slug === slug)?.name || slug;
  const view: RevealView = revealed && revealed.slug === slug ? revealed.view : { kind: "idle" };
  slot.innerHTML = renderRevealPanel(slug, name, view);
  slot.querySelector("[data-wallet-reveal-start]")?.addEventListener("click", () => {
    hideRevealedSeed();
    revealed = { slug, view: { kind: "ask" }, timer: null };
    drawReveal(slot);
    slot.querySelector<HTMLInputElement>("[data-wallet-reveal-name]")?.focus();
  });
  slot.querySelector("[data-wallet-reveal-hide]")?.addEventListener("click", () => {
    hideRevealedSeed();
    drawReveal(slot);
  });
  const show = async () => {
    // One request at a time, and none for an empty box: each try counts against
    // the three the backend allows in ten minutes.
    if (revealed?.slug === slug && revealed.view.kind === "ask" && revealed.view.busy) return;
    const input = slot.querySelector<HTMLInputElement>("[data-wallet-reveal-name]");
    const typed = input?.value || "";
    if (!typed.trim()) {
      input?.focus();
      return;
    }
    const token = {};
    revealed = { slug, view: { kind: "ask", busy: true }, timer: null, token };
    drawReveal(slot);
    const stale = () => revealed?.token !== token || activeView !== "dashboard";
    try {
      const phrase = await RevealWalletSeed(slug, typed);
      if (stale()) {
        phraseWords(phrase).fill("");
        return;
      }
      hideRevealedSeed();
      const timer = window.setTimeout(() => {
        hideRevealedSeed();
        const current = document.querySelector<HTMLElement>(`[data-wallet-reveal-slot="${slug}"]`);
        if (current) drawReveal(current);
      }, REVEAL_MS);
      revealed = { slug, view: { kind: "shown", words: phraseWords(phrase) }, timer };
    } catch (error) {
      if (stale()) return;
      revealed = { slug, view: { kind: "ask", error: String(error) }, timer: null };
    }
    const current = document.querySelector<HTMLElement>(`[data-wallet-reveal-slot="${slug}"]`);
    if (current) drawReveal(current);
  };
  slot.querySelector("[data-wallet-reveal-show]")?.addEventListener("click", () => void show());
  slot.querySelector<HTMLInputElement>("[data-wallet-reveal-name]")?.addEventListener("keydown", (event) => {
    // A held Enter on the button that opened this box repeats into it once it has
    // focus: only a fresh press, outside an IME composition, asks.
    if (event.key !== "Enter" || event.repeat || event.isComposing) return;
    event.preventDefault();
    void show();
  });
}

// Save a compose file for one service. The backend generates it, asks where to put
// it, and answers with the path — or with "" when the save dialog was dismissed,
// which is not an error and must not read like one.
async function exportCompose(slugs: string[], arch: string, outputSlug: string) {
  const output = document.querySelector<HTMLPreElement>(`[data-output-slug="${outputSlug}"]`);
  try {
    const path = await ExportCompose(slugs, arch);
    if (output) output.textContent = path ? t("main.compose.saved", {path}) : "";
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
    return confirm(t("main.service.removeConfirm", {name: serviceName})) ? {deleteData: false, allowCritical: false} : null;
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
      setOutput(record.error ? record.error : t("main.service.collected", {amount: formatBalance(record.balance, record.currency)}));
      return;
    }
    if (action === "logs") {
      setLogs(await GetLogs(slug, 200));
      return;
    }
    if (action === "stop") {
      await StopService(slug);
      setOutput(t("main.service.stopped", {slug}));
    }
    if (action === "start") {
      await StartService(slug);
      setOutput(t("main.service.started", {slug}));
    }
    if (action === "remove" && removal) {
      await RemoveService(slug, removal.deleteData, removal.allowCritical);
      // Refresh first, then write the message. The generic "complete" line below used
      // to land on top of this one, so the user never got to read which of the two
      // removals actually happened - the one fact they need after an irreversible act.
      await refreshState();
      setOutput(removal.deleteData
        ? t("main.service.removedWithData", {slug})
        : t("main.service.removedKeptData", {slug}));
      return;
    }
    await refreshState();
    setOutput(t("main.service.actionComplete", {slug, action}));
  } catch (error) {
    setOutput(String(error));
  }
}

// The two buttons on a "Needs fixing" row. Redeploy sends no credentials, no image and
// no copy count, so the deploy keeps the saved credentials, the image in use and the
// number of copies, and rebuilds the rest from the current catalog.
async function runDriftAction(slug: string, action: string) {
  const name = state?.services.find((svc) => svc.slug === slug)?.name || slug;
  if (driftDeploys[slug]?.kind === "busy") return;
  if (action === "redeploy") {
    const say = (kind: DeployStatus["kind"], text: string) => {
      driftDeploys[slug] = { kind, text };
      const panel = document.querySelector<HTMLElement>(`[data-drift-deploy-status="${slug}"]`);
      if (panel) panel.innerHTML = deployNotice(kind, text);
    };
    if (!confirm(t("main.drift.redeployConfirm", {name}))) {
      say("cancelled", t("main.deploy.nothingDeployed"));
      return;
    }
    const answer = await runDeployWithStatus(t("main.drift.redeploying", {name}), say, async () => {
      await DeployService(slug, {}, null, null);
      return { kind: "ok", text: t("main.drift.redeployed", {name}) };
    });
    if (answer.kind === "ok") await refreshState().catch(() => undefined);
    return;
  }
  if (action === "ignore") {
    try {
      await IgnoreDrift(slug);
      delete driftDeploys[slug];
      await refreshState();
      setOutput(t("main.drift.ignored", {name}));
    } catch (error) {
      setOutput(String(error));
    }
  }
}

// "Turn on isolation": the fence goes up, then each running earner is redeployed onto
// the fenced network with the credentials, image and copies it already has.
async function runEnableIsolation() {
  const names = state?.isolation?.outside || [];
  if (!confirm(t("main.isolation.confirm", {n: names.length}))) return;
  try {
    setOutput(t("main.isolation.turningOn"));
    const result = await EnableIsolation();
    await refreshState();
    const failed = Object.entries(result.failed || {});
    const moved = result.moved || [];
    // The status after the moves, not the switch: the last check can find the fence
    // down, or Settings turned off meanwhile.
    const after = result.status;
    // The fence being up does not mean every service is behind it: say the fence is
    // applied, and name any service that did not move as still able to reach the LAN.
    const fence = {mode: after?.mode || t("main.isolation.unknown"), state: after?.state || t("main.isolation.unknown"), error: after?.error ?? ""};
    const head = after && after.mode === "on" && after.state === "applied"
      ? t("main.isolation.up")
      : after?.error ? t("main.isolation.notInPlaceError", fence) : t("main.isolation.notInPlace", fence);
    const movedList = moved.join(", ") || t("main.isolation.none");
    setOutput(failed.length
      ? t("main.isolation.movedSome", {head, moved: movedList, failed: failed.map(([slug, why]) => `${slug} (${why})`).join("; ")})
      : t("main.isolation.moved", {head, moved: movedList}));
  } catch (error) {
    setOutput(String(error));
  }
}

// Logs, Stop and Start on copy n (2 and up) of a service. Copy 1 is the service and
// goes through runServiceAction.
async function runCopyAction(slug: string, n: number, action: string) {
  try {
    if (action === "logs") {
      setLogs(await GetCopyLogs(slug, n, 200));
      return;
    }
    if (action === "stop") await StopServiceCopy(slug, n);
    if (action === "start") await StartServiceCopy(slug, n);
    await refreshState();
    setOutput(action === "stop" ? t("main.copy.stopped", {slug, n}) : t("main.copy.started", {slug, n}));
  } catch (error) {
    setOutput(String(error));
  }
}

async function refreshState() {
  if (!state) return;
  const deployments = await RefreshDeployments().catch(() => state?.deployments || []);
  await loadAppState();
  state = {...state, deployments};
  render();
}

function setOutput(value: string) {
  const out = document.querySelector<HTMLElement>("#service-output");
  if (!out) return;
  out.classList.remove("output-logs", "log-wrap");
  out.textContent = value;
}

const logWrapKey = "cashpilot-log-wrap";
// Read once from storage, then kept here: a webview that refuses to read or
// write storage still shows logs and still honours the toggle.
let logWrap: boolean | null = null;

function logWrapOn(): boolean {
  if (logWrap === null) {
    try {
      logWrap = localStorage.getItem(logWrapKey) === "1";
    } catch {
      logWrap = false;
    }
  }
  return logWrap;
}

// A log goes into the same box as action messages, one entry per line.
function setLogs(value: string) {
  const out = document.querySelector<HTMLElement>("#service-output");
  if (!out) return;
  const wrap = logWrapOn();
  out.innerHTML = renderLogLines(value || t("logs.empty"), new Date(), wrap);
  out.classList.add("output-logs");
  out.classList.toggle("log-wrap", wrap);
  out.querySelector<HTMLButtonElement>("[data-log-wrap]")?.addEventListener("click", (event) => {
    const on = !out.classList.contains("log-wrap");
    logWrap = on;
    try {
      localStorage.setItem(logWrapKey, on ? "1" : "0");
    } catch {
      // kept in memory
    }
    out.classList.toggle("log-wrap", on);
    (event.currentTarget as HTMLButtonElement).setAttribute("aria-pressed", String(on));
    out.scrollTop = out.scrollHeight;
  });
  out.scrollTop = out.scrollHeight;
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
    stack.style.cssText = "position:fixed;bottom:16px;inset-inline-end:16px;z-index:9999;display:grid;gap:8px;width:320px;max-width:calc(100vw - 32px);";
    document.body.appendChild(stack);
  }
  const toast = document.createElement("div");
  toast.className = className;
  toast.style.cssText = `border-inline-start-color:var(${borderVar});background:var(--bg-secondary);box-shadow:0 18px 60px rgba(0,0,0,0.45);`;
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
  showToast("notification-item error", "--error", scope ? t("main.toast.scopedError", {scope: scopeLabel(scope)}) : t("main.toast.error"), message);
}

// showInfoToast surfaces a backend app:notice — an informational advisory (e.g. a
// "restart required" heads-up) styled distinctly from an error so a successful action
// is not reported as a failure.
function showInfoToast(payload: {scope?: string; message?: string} | string) {
  const scope = typeof payload === "object" && payload ? payload.scope ?? "" : "";
  const message = typeof payload === "object" && payload ? payload.message ?? "" : String(payload);
  showToast("notification-item", "--warning", scope ? t("main.toast.scopedNotice", {scope: scopeLabel(scope)}) : t("main.toast.notice"), message);
}

// A toast's scope as its title says it. The scopes are this file's own and the
// backend's (emitError, emitNotice); one this list does not know is shown as sent.
function scopeLabel(scope: string) {
  switch (scope) {
    case "activity":
      return t("main.scope.activity");
    case "background":
      return t("main.scope.background");
    case "cash-out":
      return t("main.scope.cashOut");
    case "clipboard":
      return t("main.scope.clipboard");
    case "consent":
      return t("main.scope.consent");
    case "credentials":
      return t("main.scope.credentials");
    case "custom service":
      return t("main.scope.customService");
    case "fleet":
      return t("main.scope.fleet");
    case "import":
      return t("main.scope.import");
    case "language":
      return t("main.scope.language");
    case "mode":
      return t("main.scope.mode");
    case "proxies":
      return t("main.scope.proxies");
    case "proxy":
      return t("main.scope.proxy");
    case "proxy test":
      return t("main.scope.proxyTest");
    case "securevm":
      return t("main.scope.securevm");
    case "restore":
      return t("main.scope.restore");
    case "runtime":
      return t("main.scope.runtime");
    case "Server commands":
      return t("main.scope.serverCommands");
    case "settings":
      return t("main.scope.settings");
    case "worker":
      return t("main.scope.worker");
    default:
      return scope;
  }
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
