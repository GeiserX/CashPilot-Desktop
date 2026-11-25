declare module "../wailsjs/go/main/App" {
  export function GetAppState(): Promise<AppState>;
  export function GetSettingsState(): Promise<SettingsState>;
  export function SaveSettings(values: Record<string, string>): Promise<SettingsState>;
  export function GetFleetState(): Promise<FleetState>;
  export function AddFleetDevice(values: Record<string, string>): Promise<FleetState>;
  export function RemoveFleetDevice(id: number): Promise<FleetState>;
  // A lead's commands to the CashPilot Docker workers that report to it (lead_workers.go).
  export function DeployToWorker(slug: string, req: WorkerDeployRequest): Promise<WorkerDeployResult>;
  export function CommandWorkerCopy(workerId: number, slug: string, command: string, instance: number): Promise<string>;
  export function CompleteOnboarding(): Promise<void>;
  export function InstallBackgroundHelper(): Promise<void>;
  export function RemoveBackgroundHelper(): Promise<void>;
  export function BackgroundHelperStatus(): Promise<BackgroundStatus>;
  export function CheckRuntime(): Promise<RuntimeStatus>;
  // Reads this PC afresh (Windows only; elsewhere facts.supported is false).
  export function HostCheck(): Promise<HostCheckResult>;
  // Checks the local Docker Desktop pipes without reading the full checklist.
  export function DockerDesktopEngineReady(): Promise<boolean>;
  // The Docker checklist's actions (Windows only; elsewhere they fail).
  export function StartDockerDesktop(): Promise<void>;
  export function OpenWindowsUpdate(): Promise<void>;
  // Only from the confirm screen: restarts the PC now, into its BIOS.
  export function RestartToFirmware(step: string): Promise<FirmwareRestart>;
  export function SetupResume(): Promise<SetupResumeView>;
  export function CancelSetup(): Promise<void>;
  // Only from a change's confirm screen: makes the change (docker_setup.go).
  export function RunSetupStep(op: string, webDownload: boolean): Promise<SetupStepResult>;
  // Only from the restart or sign-out screen, while a setup step waits for it.
  export function RestartNow(): Promise<void>;
  export function SignOutNow(): Promise<void>;
  export function GetRuntimeGuides(): Promise<InstallGuide[]>;
  export function SaveCredentials(slug: string, values: Record<string, string>): Promise<void>;
  export function GetCredentials(slug: string): Promise<CredentialView>;
  export function PreflightService(slug: string): Promise<PreflightReport>;
  // image: null keeps the image the deployment already uses, "" goes back to the
  // catalog's, anything else runs that image instead. instances: null keeps the number
  // of copies the deployment already runs (1 for a new one).
  export function DeployService(slug: string, values: Record<string, string>, image: string | null, instances: number | null): Promise<Deployment>;
  // A service routed through a proxy from its first start, and the owner's confirmed
  // switch of a routed service back to this machine's own IP.
  export function DeployServiceRouted(slug: string, values: Record<string, string>, proxyId: number): Promise<ProxiedResult>;
  export function DeployServiceLeavingRoute(slug: string, values: Record<string, string>, image: string | null, instances: number | null): Promise<Deployment>;
  // Whether a deploy of slug makes Windows show its admin prompt, and what for. Reads only.
  export function DeployAdminStep(slug: string): Promise<AdminStep>;
  export function DeployServiceWithAdminApproval(slug: string, values: Record<string, string>, image: string | null, instances: number | null, leaveRoute: boolean, approval: AdminStep): Promise<Deployment>;
  // DeployService for a service that keeps its wallet seed in a file (Sia's hostd),
  // with the owner's wallet choice (wallet_app.go).
  export function DeployServiceWallet(slug: string, values: Record<string, string>, image: string | null, instances: number | null, choice: WalletChoice): Promise<WalletDeployResult>;
  // Reads the wallet seed phrase back from the node's data volume. Go checks the
  // typed name and limits the tries; every reveal goes into the service's history.
  export function RevealWalletSeed(slug: string, typedName: string): Promise<string>;
  export function StartService(slug: string): Promise<void>;
  export function StopService(slug: string): Promise<void>;
  export function RestartService(slug: string): Promise<void>;
  export function RemoveService(slug: string, deleteData: boolean, allowCritical: boolean): Promise<void>;
  export function PlanServiceRemoval(slug: string): Promise<RemovalPlan>;
  export function GetLogs(slug: string, lines: number): Promise<string>;
  // Copy n (2 and up) of a service that runs more than once; copy 1 is the service.
  export function StopServiceCopy(slug: string, n: number): Promise<void>;
  export function StartServiceCopy(slug: string, n: number): Promise<void>;
  export function GetCopyLogs(slug: string, n: number, lines: number): Promise<string>;
  export function RefreshDeployments(): Promise<Deployment[]>;
  export function CollectService(slug: string): Promise<EarningsRecord>;
  export function ListServices(): Promise<Service[]>;
  export function GetService(slug: string): Promise<Service>;
  export function ManagedRuntimePlan(): Promise<ManagedRuntimePlan>;
  export function GetEarningsSummary(): Promise<EarningsSummary>;
  // Backups of irreplaceable state. ExportStateBackup takes either a passphrase (typed
  // twice) or an X25519 public key, and returns the path saved, or "" when dismissed.
  export function ConfirmStateBackup(slug: string): Promise<void>;
  export function UndoStateBackup(slug: string): Promise<void>;
  export function ExportStateBackup(slug: string, passphrase: string, passphraseRepeat: string, recipientPublicKey: string): Promise<string>;
  // Restore from a backup (restore_binding.go). ChooseRestoreFile opens the file
  // dialog and returns null when it was dismissed. RestoreStateBackup takes the
  // preview's token, the passphrase and, for a file this app did not make, the
  // service's name typed.
  export function ChooseRestoreFile(): Promise<RestorePreview | null>;
  export function RestoreStateBackup(token: string, passphrase: string, typedName: string): Promise<RestoreResult>;
  export function UnfinishedRestores(): Promise<UnfinishedRestore[] | null>;
  export function DiscardPartialRestore(slug: string): Promise<void>;
  // container "" is every container (the machine total).
  export function GetUsage(days: number, container: string): Promise<UsageView>;
  // Activity: what each earner talks to (internal/activity). Off by default.
  export function GetActivity(days: number): Promise<ActivityView>;
  export function GetActivityDetail(container: string, days: number): Promise<ActivityDetail>;
  export function SetActivityEnabled(on: boolean): Promise<ActivityView>;
  export function DeleteActivityData(): Promise<ActivityView>;
  export function RefreshActivityNetworks(): Promise<ActivityView>;
  // Mutes the "needs fixing" warning of slug until the catalog changes a field the
  // check judges.
  export function IgnoreDrift(slug: string): Promise<void>;
  // The desktop-app risk disclosure (session_consent_binding.go): accept records the
  // text version and the date for one manual-only app, revoke deletes it. Both return
  // every consent now recorded.
  export function AcceptSessionRisk(slug: string, textVersion: string): Promise<Record<string, SessionConsent>>;
  export function RevokeSessionRisk(slug: string): Promise<Record<string, SessionConsent>>;
  // The payout ledger: cash-outs the user records by hand. currency "" is the
  // service's balance unit; date "" is today, otherwise YYYY-MM-DD.
  export function ListPayouts(slug: string): Promise<Payout[]>;
  export function RecordPayout(slug: string, amount: number, currency: string, method: string, date: string): Promise<Payout>;
  export function RemovePayout(id: number): Promise<void>;
  // Services the user adds from an image reference. slug "" creates one. A secret's
  // value never comes back: a blank secret on save keeps the stored one.
  export function ListCustomServices(): Promise<CustomServiceView[]>;
  export function SaveCustomService(slug: string, definition: CustomDefinition): Promise<CustomServiceView>;
  export function DeleteCustomService(slug: string): Promise<void>;
  // Proxies and the copies that run through them (docs/proxies.md). id 0 creates a
  // proxy. A secret never comes back; a blank one on save keeps the stored value and
  // clear_<field> = "true" drops it.
  export function GetProxiesState(): Promise<ProxiesState>;
  export function SaveProxy(id: number, values: Record<string, string>): Promise<ProxyView>;
  export function DeleteProxy(id: number): Promise<void>;
  export function TestProxy(id: number): Promise<ProxyTestResult>;
  export function DeployProxiedCopy(slug: string, id: number): Promise<ProxiedResult>;
  export function StopProxiedCopy(slug: string, id: number): Promise<void>;
  export function StartProxiedCopy(slug: string, id: number): Promise<void>;
  export function RemoveProxiedCopy(slug: string, id: number): Promise<void>;
  export function ResetProxyUsage(id: number): Promise<string[]>;
  // Turns LAN isolation on for an upgraded install: applies the fence, then
  // redeploys each running earner onto the fenced network.
  export function EnableIsolation(): Promise<IsolationResult>;
  // Bring logins over from money4band, CashFactory or InternetIncome. Preview is the
  // dry run (a secret is only ever "set"); Apply parses the same files again and
  // saves the ticked, ready services, never over a value already set here.
  export function PreviewImport(req: ImportRequest): Promise<ImportPreview>;
  export function ApplyImport(req: ImportApplyRequest): Promise<ImportResult>;
  // Money waiting: the reminders before a provider writes off your earnings.
  // action is done, snooze (days 1 or 7), mute or unmute; rule "*" is every
  // reminder of the service; occurrence "" is the current one.
  export function GetReminders(): Promise<RemindersView>;
  export function ReminderAction(slug: string, rule: string, action: string, occurrence: string, days: number): Promise<RemindersView>;
}

export interface ProxyCapView {
  enabled: boolean;
  capBytes: number;
  thresholdPct: number;
  // 0 when the cap is reset by hand.
  resetDay: number;
  usedBytes: number;
  periodStart: string;
  cappedAt: string;
  capReason: string;
}

export interface ProxyCopyView {
  slug: string;
  name: string;
  status: string;
  tunnelStatus: string;
  exitIp: string;
  exitCountry: string;
  stoppedByCap: boolean;
}

export interface ProxyView {
  id: number;
  name: string;
  kind: string;
  host: string;
  port: number;
  exitNode: string;
  loginServer: string;
  rotating: boolean;
  // Which secrets are stored; their values never reach the frontend.
  secrets: Record<string, boolean>;
  cap: ProxyCapView;
  lastExitIp: string;
  lastCountry: string;
  lastCheckedAt: string;
  lastTest: { ok: boolean; at: string; error: string } | null;
  copies: ProxyCopyView[];
}

export interface ProxyCandidate {
  slug: string;
  name: string;
  // Why it cannot run behind a proxy, "" when it can.
  refusal: string;
}

export interface ProxiesState {
  proxies: ProxyView[];
  candidates: ProxyCandidate[];
  tunnelImage: string;
  tunnelImageError: string;
}

export interface ProxiedResult {
  exitIp: string;
  country: string;
  warning: string;
}

export interface ProxyTestResult {
  exitIp: string;
  country: string;
  ms: number;
}

// FenceStatus mirrors runtime.FenceStatus: whether container earners are fenced off
// the LAN, this computer, a tailnet and cloud metadata.
export interface FenceStatus {
  // off, on, legacy or unsupported (Podman); "" while Docker cannot be reached.
  mode: string;
  // What Settings ask for: auto, on or off.
  setting: string;
  // applied, reduced (masquerade only), failed, unsupported or n/a.
  state: string;
  backend: string;
  error: string;
  // Running earners that can reach the LAN; a redeploy moves them.
  outside: string[] | null;
  // Running earners behind the fence: on the fenced network and no other. Set only
  // while the mode is on.
  inside: string[] | null;
  // Stopped earners outside the fence, left alone until their next deploy.
  outsideStopped: string[] | null;
  // Fence-gated earners that are down and that CashPilot starts itself once the
  // fence is confirmed. One the user stopped is not listed.
  waiting: string[] | null;
}

export interface IsolationResult {
  status: FenceStatus;
  moved: string[] | null;
  failed: Record<string, string> | null;
}

export interface ImportFile {
  name: string;
  content: string;
}

export interface ImportRequest {
  source: string;
  files: ImportFile[];
}

export interface ImportApplyRequest extends ImportRequest {
  slugs: string[];
  extra: Record<string, Record<string, string>>;
}

export type ImportFieldState = "imported" | "missing" | "retype" | "placeholder" | "invalid";
export type ImportRowState = "ready" | "missing_fields" | "invalid" | "unusable" | "not_deployable";

export interface ImportField {
  key: string;
  label: string;
  secret: boolean;
  required: boolean;
  state: ImportFieldState;
  pattern: string;
  // Never set for a secret field.
  value?: string;
}

export interface ImportRow {
  slug: string;
  name: string;
  app: string;
  selected: boolean;
  offInSource: boolean;
  state: ImportRowState;
  reason: string;
  fields: ImportField[];
  existing: { configured: boolean; deployed: boolean };
}

export interface ImportPreview {
  source: string;
  sourceName: string;
  rows: ImportRow[];
  notImported: { key: string; reason: string }[];
  notices: string[];
}

export interface ImportResult {
  saved: string[];
  kept: Record<string, string[]>;
}

export interface CustomEnv {
  key: string;
  value: string;
  secret: boolean;
}

export interface CustomDefinition {
  name: string;
  image: string;
  env: CustomEnv[];
  command: string;
  dataPath: string;
  memLimit: string;
}

export interface CustomEnvView extends CustomEnv {
  // Whether a value is stored; a secret's value is always "" here.
  isSet: boolean;
}

export interface CustomServiceView {
  slug: string;
  name: string;
  image: string;
  env: CustomEnvView[];
  command: string;
  dataPath: string;
  memLimit: string;
  // The named volume the data path is kept in, "" with no data path.
  volume: string;
  warnings: string[];
}

export interface AppState {
  config: AppConfig;
  runtime: RuntimeStatus;
  services: Service[];
  deployments: Deployment[] | null;
  // Slugs of deployed services whose running image no longer matches the catalog
  // (provider changed/re-pinned it): deployed but likely earning nothing.
  outdatedServices: string[] | null;
  health: Record<string, HealthScore> | null;
  // The separate "is it actually earning?" verdict per deployed slug. `health`
  // above is the container's reputation, which scores a service that has earned
  // nothing for a month at full marks. A slug missing from this map has no
  // verdict -- which is not the same claim as a verdict of "fine".
  producerStates: Record<string, ProducerReport> | null;
  earnings: EarningsRecord[] | null;
  guides: InstallGuide[];
  notifications: NotificationItem[];
  currencies: string[];
  summary: EarningsSummary;
  serviceDetails: Record<string, string> | null;
  // The paired server's ACCOUNT-LEVEL figures for the platforms this machine
  // runs. null means show the local numbers alone: not paired, or paired but the
  // server has not reported yet -- and it is what a machine returns to after
  // unlinking, because its own rows were copied upstream, never moved.
  fleet: FleetView | null;
  // The link to the CashPilot server while this computer is joined; null when it
  // runs everything here.
  pairing?: PairingStatus | null;
  // This machine's hostname, so a {hostname}-defaulted deploy field renders the real
  // value the deploy path will substitute rather than the literal "{hostname}".
  hostname: string;
  // Per slug, the credential inputs the earnings collector needs that the container's
  // docker.env does not already ask for. The backend owns this list (internal/collectors)
  // so a web-catalog rename of a service's env keys cannot leave the wizard with no
  // field for the credentials its collector reads.
  collectorFields: Record<string, CollectorField[]> | null;
  // Per slug, how many copies this machine may run and why not more (the deploy
  // form's Instances box). A slug missing here gets no box.
  scaling: Record<string, ScalingLimit> | null;
  // Per slug, copies 2..N of a service that runs more than once. Copy 1 is its
  // entry in deployments.
  copies: Record<string, ServiceCopy[]> | null;
  // Per slug, the device id the last deploy added to the user's account itself.
  addedDevices: Record<string, string> | null;
  // Per slug, the device id the provider did not know yet at deploy time; the app
  // keeps trying to add it to the account for a day.
  retryingDevices: Record<string, string> | null;
  // Deployed services whose catalog marks state irreplaceable, and whether the
  // user has a backup of it (the amber card under Needs attention).
  stateBackups: StateBackupRow[] | null;
  // Per slug, how its running container differs from what a redeploy would build
  // from the current catalog, in plain words. Missing: matches, ignored, or the
  // warnings are off.
  drift: Record<string, string[]> | null;
  // The LAN fence's status; null before the first check.
  isolation: FenceStatus | null;
  route: RouteState | null;
  // The Money waiting panel; null from a backend that predates it.
  reminders?: RemindersView | null;
  // Running copies that carry almost no traffic while the other copies of their
  // service here are busy (stall_alerts.go).
  stalls: StallAlert[] | null;
  // The CashPilot server that raises the stall alerts of this computer's own
  // copies while joined to it; "" while this computer raises them.
  stallsServer?: string;
  // The Docker workers the deploy form's Where box offers; null when this computer
  // sends workers no commands (joined, or not accepting workers).
  workerTargets?: WorkerTarget[] | null;
}

// WorkerTarget mirrors the Go WorkerTarget (lead_workers.go).
export interface WorkerTarget {
  id: number;
  name: string;
  online: boolean;
  // Why this worker cannot take commands now; "" when it can.
  reason: string;
  services: string[] | null;
}

// WorkerDeployRequest mirrors the Go WorkerDeployRequest (lead_workers.go).
export interface WorkerDeployRequest {
  workerId: number;
  values: Record<string, string>;
  image: string;
  confirmed: boolean;
  allowSharedIdentity: boolean;
  replaceExisting: boolean;
}

// WorkerDeployResult mirrors the Go WorkerDeployResult. With ask set nothing was
// deployed: the owner answers message, and a yes sends the deploy again.
export interface CredentialView {
  values: Record<string, string>;
  savedSecrets: Record<string, boolean>;
}

export interface WorkerDeployResult {
  credentialWarning?: string;
  ask: string;
  message: string;
  worker: string;
  containerId: string;
  notes: string[] | null;
}

// WorkerCopy mirrors store.FleetContainer: one copy a worker's heartbeat listed.
export interface WorkerCopy {
  slug: string;
  status: string;
  instance: number;
  managed: boolean;
}

// WorkerCommandsView mirrors the Go WorkerCommandsView.
export interface WorkerCommandsView {
  reason: string;
  copies: WorkerCopy[] | null;
}

// RemindersView mirrors the Go RemindersView (reminders_app.go).
export interface RemindersView {
  // Joined to a CashPilot server, which owns the reminders: items is empty.
  joined: boolean;
  serverUrl: string;
  // The check failed. Never shown as an all-clear.
  unknown: boolean;
  items: ReminderItem[] | null;
  muted: ReminderMuted[] | null;
}

// ReminderItem mirrors internal/obligations.Item.
export interface ReminderItem {
  slug: string;
  name: string;
  rule: string;
  // "rule" for a catalog rule, "cash-out" for the built-in nudge.
  kind: string;
  // claim, withdraw or keep-active.
  action: string;
  title: string;
  loss: string;
  occurrence: string;
  // d<N>, overdue, recheck, ready or still-ready.
  stage: string;
  // YYYY-MM-DD, "" for a cash-out item.
  due: string;
  daysLeft?: number | null;
  censored: boolean;
  how: string;
  url: string;
  source: string;
  checked: string;
  canDone: boolean;
  canSnooze: boolean;
  balance?: number | null;
  currency?: string;
  balanceUsd?: number | null;
}

export interface ReminderMuted {
  slug: string;
  rule: string;
  name: string;
  title: string;
}

// StallAlert mirrors the Go StallAlert: one copy that runs but gets almost no work.
// since is the start of its first low hour (Unix seconds); message is the whole
// alert in plain words.
export interface StallAlert {
  slug: string;
  service: string;
  container: string;
  // 0 for a proxied copy, which has proxyId instead.
  copy: number;
  proxyId?: number;
  since: number;
  hours: number;
  message: string;
}

// ScalingLimit mirrors services.ScalingLimit. reason is "" when max is above 1.
export interface RouteProxy {
  id: number;
  name: string;
  exitIp: string;
  country: string;
}

export interface RouteState {
  proxies: RouteProxy[] | null;
  refusals: Record<string, string> | null;
  recorded: Record<string, number> | null;
  running: Record<string, number> | null;
  proxiedOnly: boolean;
}

export interface ScalingLimit {
  max: number;
  reason: string;
}

// ServiceCopy mirrors runtime.ContainerInfo for one extra copy.
export interface ServiceCopy {
  slug: string;
  containerId: string;
  name: string;
  image: string;
  status: string;
  cpuPercent: number;
  memoryMb: number;
  instance: number;
}

// FleetView mirrors the Go FleetView: what the paired CashPilot server reports
// about the platforms this machine runs.
//
// Every money field is `number | null` because null means UNKNOWN, not zero. A
// platform with no reading has never been collected for -- usually a missing
// collector or credentials nobody entered -- and rendering it as 0.00 reports a
// loss that did not happen.
export interface FleetView {
  serverUrl: string;
  // RFC3339. Shown rather than implying the figure is live: the heartbeat is on
  // a timer, so the number may be an hour old.
  reportedAt: string;
  windowDays: number;
  currency: string;
  platforms: FleetViewPlatform[] | null;
  // null when NOTHING is known. The server sums only what it has readings for.
  totalUsd: number | null;
  // Platforms this machine runs that the server has no figure for at all. These
  // are the reason a total is lower than the user expects, so they are shown.
  withoutReadings: string[] | null;
}

export interface FleetViewPlatform {
  slug: string;
  usd: number | null;
  // More than one worker on the fleet runs this platform, which is exactly when
  // "this machine earned it" stops being true.
  shared: boolean;
}

// MystNode mirrors the Go mystNode struct: one Mysterium node's per-node
// earnings, flattened from the MystNodes cloud API. The backend marshals an
// array of these to JSON and stashes it in serviceDetails under the "mysterium"
// slug, alongside the flat total-earnings balance.
export interface MystNode {
  identity: string;
  name: string;
  localIp: string;
  country: string;
  version: string;
  online: boolean;
  earnings30dMyst: number;
  lifetimeMyst: number;
  lifetimeSettledMyst: number;
  lifetimeUnsettledMyst: number;
}

/**
 * One service's earning verdict, from internal/health. `state` is "failing",
 * "idle" or "not-checked"; there is deliberately no "earning" state, because
 * nothing Desktop can see from a container proves that money moved. `reasons` is
 * what the user is shown, and it is filled in for "not-checked" too -- "why don't
 * you know?" is the first question a bare "not checked" provokes.
 */
export interface ProducerReport {
  slug: string;
  state: string;
  reasons: string[] | null;
}

export interface HealthScore {
  score: number;
  uptimePercent: number;
  samples: number;
  restarts: number;
  crashes: number;
  stops: number;
}

export interface EarningsSummary {
  displayCurrency: string;
  total: number;
  /**
   * False when `total` is 0 only because nothing could be priced. A total of 0
   * is a real measurement for a user who has earned nothing and a fabrication
   * for a user whose display currency has no rate -- branch on this, never on
   * the number. See render/total.ts.
   */
  totalKnown: boolean;
  today: number;
  month: number;
  todayChange: number;
  monthChange: number;
  breakdown: ServiceEarning[];
  points: PointsBalance[];
  daily: DailyPoint[];
  ratesStale: boolean;
  ratesUpdated: string;
  /**
   * Recorded cash-outs in the display currency, and the balance total plus them.
   * null when unknown (ledger unreadable, no rate, or total unknown), never 0.
   * See allTimeNote in render/total.ts.
   */
  paidOut: number | null;
  earnedAllTime: number | null;
  unpricedPayouts: number;
}

/** A cash-out from the payout ledger. fxRateUsd null: no rate for that day. */
export interface Payout {
  id: number;
  platform: string;
  amount: number;
  currency: string;
  fxRateUsd: number | null;
  method: string;
  paidOn: string;
  createdAt: string;
}

export interface ServiceEarning {
  platform: string;
  name: string;
  balance: number;
  currency: string;
  balanceDisplay: number;
  convertible: boolean;
  error: string;
  cashout: CashoutProgress;
}

export interface CashoutProgress {
  minAmount: number;
  currency: string;
  percent: number;
  eligible: boolean;
  comparable: boolean;
  method: string;
  dashboardUrl: string;
  notes: string;
}

export interface PointsBalance {
  platform: string;
  name: string;
  balance: number;
  currency: string;
}

export interface DailyPoint {
  day: string;
  amount: number;
}

// One container in the usage graphs' picker: one with a reading in the window.
// noCounters: its traffic is unknown (host network or a native program).
export interface UsageContainer {
  name: string;
  slug: string;
  service: string;
  noCounters: boolean;
}

// The usage graphs (internal/usage.View). hours is the start of every point,
// Unix seconds, oldest first; every series has one value per point, and null is
// "no reading", drawn as a gap, never as 0. A *Partial flag marks a point whose
// total leaves out a container it could not read: a minimum.
export interface UsageView {
  days: number;
  step: number;
  hours: number[];
  containers: UsageContainer[] | null;
  container: string;
  cpuPercent: (number | null)[];
  memoryMb: (number | null)[];
  rxRate: (number | null)[];
  txRate: (number | null)[];
  rxBytes: (number | null)[];
  txBytes: (number | null)[];
  trafficPartial: boolean[];
  cpuPartial: boolean[];
  memoryPartial: boolean[];
}

export interface AppConfig {
  firstRunComplete: boolean;
  displayCurrency: string;
  runtimeProvider: string;
  autoUpdate: boolean;
  hostnamePrefix: string;
  collectIntervalMinutes: number;
  timezone: string;
  fleetApiKey: string;
  fleetBindAddress: string;
  fleetPort: number;
  // Per service slug, the desktop-app risk disclosure the user accepted. Absent: none.
  sessionConsent?: Record<string, SessionConsent> | null;
  // "lead" (Run everything here), "follower" (Join my CashPilot), or absent before
  // the first run chose.
  mode?: string;
}

// The link to the CashPilot server, as the last heartbeat left it (Go PairingStatus).
export interface PairingStatus {
  serverUrl: string;
  // RFC3339 of the last accepted heartbeat; "" never.
  lastHeartbeat: string;
  lastAttempt: string;
  // Why the last try failed, in plain words; "" when it worked.
  lastError: string;
  errorCode: string;
}

// What the Settings card "How this computer runs" shows (Go ModeState).
export interface ModeState {
  // "lead", "follower", or "" before the first run chose.
  mode: string;
  name: string;
  pairing?: PairingStatus | null;
  hubOn: boolean;
  hubDevices: string[] | null;
}

// The answer to one join (Go JoinResult). A failed connection has ok false.
export interface JoinResult {
  ok: boolean;
  code: string;
  message: string;
  serverUrl: string;
  serverHost: string;
  name: string;
  workerId: number;
  hubWasOn: boolean;
  hubDevices: string[] | null;
}

// What to do on the server after leaving it (Go LeaveResult).
export interface LeaveResult {
  serverUrl: string;
  name: string;
}

export interface SessionConsent {
  textVersion: string;
  // RFC3339, UTC.
  acceptedAt: string;
}

export interface NotificationItem {
  runtime?: boolean;
  level: string;
  title: string;
  message: string;
}

export interface EnvSetting {
  key: string;
  label: string;
  value: string;
  source: string;
  secret: boolean;
  readOnly: boolean;
  help: string;
}

export interface CollectorSetting {
  slug: string;
  name: string;
  configured: boolean;
  collector: string;
}

export interface SettingsState {
  environment: EnvSetting[];
  collectors: CollectorSetting[];
  config: AppConfig;
}

// BackgroundStatus mirrors the Go bgservice.Status returned by
// BackgroundHelperStatus: whether the OS login agent is registered
// (installed) and whether the service manager reports the helper alive
// (running). label is the agent's service identifier.
export interface BackgroundStatus {
  installed: boolean;
  running: boolean;
  label: string;
}

export interface FleetDevice {
  id: number;
  name: string;
  kind: string;
  endpoint: string;
  os: string;
  arch: string;
  status: string;
  services: string[];
  lastSeen: string;
  createdAt: string;
}

// FleetReach mirrors main.FleetReach (fleet_reach.go): whether another machine can
// report to this Desktop's fleet API now, and why not.
export interface FleetReach {
  open: boolean;
  // Accept workers (hub mode) is off, so heartbeats are refused.
  hubOff: boolean;
  // The API listens on a loopback address, which only this PC reaches.
  thisPcOnly: boolean;
  // The API did not start (its port was taken, for instance).
  notListening: boolean;
  // The address the API listens on now, "" when it does not.
  listen: string;
  // A saved bind address or port waits for a restart.
  restartPending: boolean;
}

export interface FleetState {
  workers: number;
  mobiles: number;
  online: number;
  services: number;
  devices: FleetDevice[];
  uiUrl: string;
  localApiUrl: string;
  reach: FleetReach;
  apiKey: string;
  apiListening: boolean;
  workerSnippet: string;
  mobileSnippet: string;
  // Per worker id, its copies and why it takes no commands now.
  workerCommands?: Record<string, WorkerCommandsView> | null;
}

export interface RuntimeStatus {
  available: boolean;
  // The native runtime is registered AND can run at least one catalog app here.
  nativeAvailable: boolean;
  // The catalog apps the native runtime can run on this machine.
  nativeServices: string[] | null;
  setupTask?: string;
  kind: string;
  // Why Docker is or is not ready: running, not-installed, not-running, unreachable,
  // windows-containers, access-denied, wsl-missing, wsl-outdated,
  // virtualization-off or first-start-pending. message says it in plain words.
  reason: string;
  message: string;
  version: string;
  context: string;
  tools: Record<string, string> | null;
}

// "yes", "no" or "unknown": a probe that failed or timed out leaves "unknown".
export type Tri = "yes" | "no" | "unknown";

// One reading of this Windows PC (internal/winvm). Nothing here is localized text.
export interface HostFacts {
  os: string;
  supported: boolean;
  build: number;
  ubr: number;
  editionId: string;
  arch: string;
  hypervisorPresent: Tri;
  virtualizationFirmware: Tri;
  slat: Tri;
  wslFeature: Tri;
  vmPlatformFeature: Tri;
  wslInstalled: Tri;
  wslVersion: string;
  dockerDesktopInstalled: Tri;
  dockerDesktopRunning: Tri;
  dockerDesktopStarted: Tri;
  dockerService: string;
  userSid: string;
  dockerUsersGroup: Tri;
  dockerUsersListed: Tri;
  dockerUsersToken: Tri;
  restartPending: Tri;
  restartReasons: string[] | null;
  problems: Record<string, string> | null;
  virtualization: Tri;
  hyperVEdition: Tri;
  wsl: "ready" | "missing" | "outdated" | "unknown";
  secureVmHost: { eligible: Tri; reason: string };
  // Server service start type: auto, manual, disabled, absent or unknown.
  lanmanServer: string;
  otherHypervisors: Tri;
  otherHypervisorNames: string[] | null;
  manufacturer: string;
  model: string;
  // LastBootUpTime, RFC 3339 UTC; "" unknown.
  lastBoot: string;
  dockerBackend: "wsl" | "hyperv" | "unknown";
  isVm: Tri;
}

// One row of the Docker checklist (internal/winvm/dockerguide.go).
export type GuideStepState = "done" | "needs-you" | "blocked" | "unknown" | "note";
export interface GuideStepAction {
  // "" none, url, windows-update, start-docker, show-docker, firmware,
  // elevated (an admin change, after its confirm screen) or run (a change made
  // as the user, after its confirm screen). url is also the download page of
  // the Docker install.
  kind: string;
  label: string;
  url?: string;
  op?: string;
  manual?: string;
}
export interface GuideStep {
  id: string;
  title: string;
  state: GuideStepState;
  text: string;
  blockedBy?: string;
  action: GuideStepAction;
  help?: string;
  helpUrl?: string;
}

export interface HostCheckResult {
  facts: HostFacts;
  runtime: RuntimeStatus;
  runtimeNotice: NotificationItem | null;
  // The Docker checklist; null outside Windows.
  steps: GuideStep[] | null;
  // This reading ended the guided setup: this PC's own Docker Desktop runs
  // Linux containers (host_check.go).
  setupFinished: boolean;
}

export interface FirmwareRestart {
  started: boolean;
  reopens: boolean;
}

// Whether the app opens on the checklist after a restart (internal/setupresume).
export interface SetupResumeView {
  active: boolean;
  flow: string;
  step: string;
  restarted: "yes" | "no" | "unknown" | "";
  exeMoved: boolean;
  attempts: number;
  fromRunOnce: boolean;
  // What the step waits for: a restart, or a sign-out and back in.
  finish?: "restart" | "signout" | "";
  // The sign-out a step waited for happened: RunOnce opened the app, or this
  // session's token already carries docker-users.
  signedIn?: boolean;
}

// What a deploy's Windows admin prompt is for (internal/runtime/firewall_consent.go).
export interface AdminStep {
  needed: boolean;
  // "firewall-rule" when needed.
  kind?: string;
  ruleName?: string;
  replaceRule?: boolean;
  program?: string;
  networks?: string[];
  removeRuleName?: string;
}

// How a change of the checklist ended (docker_setup.go).
export interface SetupStepResult {
  step: string;
  op: string;
  state: "done" | "restart" | "signout" | "failed" | "timeout" | "cancelled" | "policy" | "fallback" | "note";
  text: string;
  code?: string;
  // "web-download": a WSL change may work through a direct download.
  retry?: string;
  url?: string;
  reopens: boolean;
}

export interface InstallGuide {
  id: string;
  name: string;
  description: string;
  platforms: string[];
  url: string;
  commands: string[] | null;
  notes: string[] | null;
}

export interface Service {
  native?: { env?: EnvVar[] };
  name: string;
  slug: string;
  category: string;
  status: string;
  website: string;
  // The provider's support page, offered when it has disabled the account. "" when none.
  supportUrl: string;
  description: string;
  shortDescription: string;
  // program is three-valued: true/false are verified answers, null means nobody has
  // checked whether the provider runs a referral programme.
  referral: {
    signupUrl: string;
    code: string;
    program: boolean | null;
    bonus: { referrer: string; referee: string };
    // false: the provider has no account to sign up for, so the link is labelled
    // Visit. null or absent: an account is needed.
    account?: boolean | null;
  };
  docker: DockerConfig;
  requirements: {
    residentialIp: boolean;
    vpsIp: boolean;
    devicesPerAccount: number;
    // null means nobody has documented a per-IP limit, which is not the same as 0
    // ("the provider imposes none"). Rendering null as 0 would tell the user a second
    // instance is allowed when nobody has checked.
    devicesPerIp: number | null;
    containerProhibited: boolean;
    minBandwidth: string;
    gpu: boolean;
    minStorage: string;
    note: string;
    noteColumn: string;
  };
  payment: {
    methods: string[];
    cryptoToken: string;
    minimumPayout: string;
    currency: string;
    frequency: string;
  };
  earnings: { monthlyLow: number; monthlyHigh: number; currency: string; per: string; notes: string };
  cashout: { method: string; dashboardUrl: string; minAmount: number; currency: string; notes: string };
  platforms: string[];
  collector: { type: string; notes: string; credentialHint: string; perNodeEarnings: boolean; accrual?: string };
  payout: {
    model: string;
    chain: string;
    addressEnv: string;
    addressSource: string;
    addressRead?: { command: string[]; pattern: string };
    notes: string;
  };
  disclosure: {
    sells: string;
    thirdPartyTraffic: string;
    dataCollected: string;
    ispRisk: string;
    accountRules: string;
  };
  // Set for providers that pay a device only once its id is added to the account.
  deviceLink: { idEnv: string; url: string; steps: string } | null;
  resurrectionChecked: string;
  // The provider's grade, computed in Go from services/_ratings.yml. Null for a
  // service the user added, which nobody has reviewed.
  ratingResult?: RatingResult | null;
  manualOnly: boolean;
  // A service the user added from an image reference, not a reviewed catalog entry.
  custom?: boolean;
}

export interface RatingItem {
  text: string;
  criterion: string;
  kind: string;
  source: string;
  checkedOn: string;
}

// internal/catalog/rating.go RatingResult. rated false means "Not rated yet": no
// grade, and nothing else below is meaningful.
export interface RatingResult {
  rated: boolean;
  grade: string;
  bandGrade: string;
  score: number;
  max: number;
  percent: number;
  caps: { key: string; maxGrade: string; reason: string }[] | null;
  unknownCount: number;
  thinEvidence: boolean;
  reviewedOn: string;
  verdicts: { key: string; title: string; verdict: string }[] | null;
  notes: Record<string, string> | null;
  cons: RatingItem[] | null;
  pros: RatingItem[] | null;
  topCons: RatingItem[] | null;
  summary: string;
}

export interface DockerConfig {
  image: string;
  imageByArch: Record<string, string> | null;
  platforms: string[] | null;
  env: EnvVar[];
  ports: string[] | null;
  volumes: string[] | null;
  criticalVolumes: { target: string; holds: string }[] | null;
  command: string;
  networkMode: string;
  capAdd: string[] | null;
  devices: string[] | null;
  healthSignals: { pattern: string; means: string; state: string }[] | null;
  advertisedAddressEnv: string;
  privileged: boolean;
  stopTimeout: number;
  setup: string;
  notes: string;
}

// CollectorField is one credential input the earnings collector needs that the
// container's docker.env does not already ask for. Mirrors collectors.Field.
export interface CollectorField {
  key: string;
  label: string;
  description: string;
  secret?: boolean;
  required?: boolean;
  /** A regular expression the whole value must match (catalog docker.env pattern). */
  pattern?: string;
}

export interface EnvVar {
  key: string;
  label: string;
  required: boolean;
  secret: boolean;
  description: string;
  default: string;
  pattern?: string;
  /** "start:end" with 1 <= start <= end <= 65535, checked by the Go side. */
  portRange?: boolean;
  /**
   * Set when the value is a wallet seed phrase that lives in a file in the service's
   * own volume (catalog.SeedFile): never a form field, never stored, never sent.
   */
  seedFile?: SeedFile;
}

export interface SeedFile {
  path: string;
  key: string;
  words: number;
  generate: string[];
  phrasePattern: string;
  addressPattern: string;
}

// The deploy form's wallet choice (wallet_app.go WalletChoice).
export interface WalletChoice {
  mode: "generate" | "import" | "keep";
  phrase: string;
}

// What the deploy did with the wallet. phrase is set only for a wallet made on this
// deploy: shown once, checked back, then dropped.
export interface WalletOutcome {
  created: boolean;
  migrated: boolean;
  address: string;
  phrase?: string;
}

export interface WalletDeployResult {
  deployment: Deployment;
  wallet: WalletOutcome | null;
}

// PreflightFinding is one thing that could stop a service earning on this machine,
// in the words the user reads. Mirrors preflight.Finding.
export interface PreflightFinding {
  verdict: string;
  message: string;
}

// PreflightReport is what CashPilot knows before a deploy runs. blocking is false
// unless the service cannot run here at all (a Docker app with no Docker engine);
// otherwise the report informs the decision, it never takes it. notChecked names what
// nobody looked at, so a clean report is not mistaken for a guarantee. Mirrors
// preflight.Report.
export interface PreflightReport {
  slug: string;
  name: string;
  verdict: string;
  summary: string;
  findings: PreflightFinding[] | null;
  notChecked: string[] | null;
  machineArch: string;
  blocking: boolean;
}

export interface Deployment {
  slug: string;
  containerId: string;
  name: string;
  image: string;
  status: string;
  runtime: string;
  cpuPercent: number;
  memoryMb: number;
  // The image the user chose instead of the catalog's; "" when it follows the catalog.
  customImage: string;
  // How many copies of the service this machine runs; this entry is the first.
  instances: number;
  // The public address of the wallet a seed-file service keeps (Sia), "" when none.
  walletAddress?: string;
}

export interface EarningsRecord {
  platform: string;
  balance: number;
  currency: string;
  error?: string;
  // "unread": the login worked but no node could be read, so the credentials are fine.
  errorKind?: string;
  createdAt: string;
}

export interface ManagedRuntimePlan {
  summary: string;
  phases: string[];
  risks: string[];
  providers: string[];
}

// DataMount is one place a deployed service keeps data on this machine.
export interface DataMount {
  target: string;
  source: string;
  // Named Docker volumes are the only kind removal can delete; a bind mount is a
  // folder the user owns and is always left alone.
  volume: boolean;
  critical: boolean;
  holds: string;
}

// StateBackupRow is one deployed service with irreplaceable state. state is "none",
// "confirmed", "changed" (the volume was recreated or moved since), "recheck"
// (confirmed before the app could tell) or "expired" (over six months old).
export interface StateBackupRow {
  slug: string;
  name: string;
  volumes: { target: string; holds: string }[] | null;
  state: "none" | "confirmed" | "changed" | "recheck" | "expired";
  confirmedAt: string;
  method: string;
  tracksChanges: boolean;
}

// RestorePreview is what restoring the chosen backup file would do on this machine.
// ok is false when this machine refuses it now, and why says why. known is true when
// this app exported this exact file; needsName says whether the restore needs the
// name typed.
export interface RestorePreview {
  token: string;
  file: string;
  slug: string;
  service: string;
  bytes: number;
  sha256: string;
  known: boolean;
  // The machine the file's header names. Not proof of anything.
  madeOn: string;
  container: string;
  creates: { target: string; volume: string }[] | null;
  ok: boolean;
  why: string;
  // Fleet machines that report this service now. Any of them may run the identity
  // in the file, known or not.
  runsOn: string[] | null;
  // The restore needs the service name typed: the file is not known, or a fleet
  // machine runs the service now.
  needsName: boolean;
}

// RestoreResult is what a restore made. started is false while a fence-gated node
// waits for the LAN fence.
export interface RestoreResult {
  containerId: string;
  name: string;
  image: string;
  targets: string[] | null;
  files: number;
  started: boolean;
  waiting?: string;
  left?: string[] | null;
}

// UnfinishedRestore is a restore that is running or did not finish: Deploy and
// Start refuse its service until the partial restore is deleted.
export interface UnfinishedRestore {
  slug: string;
  service: string;
  volumes: string[] | null;
  startedAt: string;
}

// RemovalPlan is what removing a service would delete, read off the live container so
// the question the user answers names the real data.
export interface RemovalPlan {
  slug: string;
  name: string;
  volumes: DataMount[] | null;
  binds: DataMount[] | null;
  catalogKnown: boolean;
}

export interface ActivityTop {
  value: string;
  sightings: number;
}

export interface ActivityPoint {
  t: number;
  opens: number | null;
  attemptFails: number | null;
  estMax: number | null;
}

export interface ActivityRow {
  container: string;
  status: string;
  reason: string;
  members: string[];
  networkMode: string;
  listenPorts: number[];
  lastWindow: number;
  estMax: number | null;
  remotesTotal: number | null;
  truncated: boolean;
  opensLast: number | null;
  attemptFailsLast: number | null;
  series: ActivityPoint[];
  topPorts: ActivityTop[];
  topAsn: ActivityTop[];
  topCc: ActivityTop[];
  classes: Record<string, number>;
  dirs: Record<string, number>;
  alerts: number;
}

export interface ActivityAlert {
  kind: string;
  subject: string;
  message: string;
  createdAt: number;
}

export interface ActivityView {
  enabled: boolean;
  days: number;
  rawHours: number;
  lastWindow: number;
  round: { at: number; workerStatus: string; leftoverProbes: string[] };
  networks: { present: boolean; ageSeconds: number | null; rows: number | null };
  networksEnabled: boolean;
  containers: ActivityRow[];
  alerts: ActivityAlert[];
}

export interface ActivityRawRow {
  windowEnd: number;
  dir: string;
  proto: string;
  ip: string;
  port: number;
  class: string;
  asn: string;
  cc: string;
  seen: number;
}

export interface ActivityDetail {
  container: string;
  ports: ActivityTop[];
  asn: ActivityTop[];
  cc: ActivityTop[];
  classes: ActivityTop[];
  raw: ActivityRawRow[];
  rawHours: number;
}
