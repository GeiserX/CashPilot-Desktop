declare module "../wailsjs/go/main/App" {
  export function GetAppState(): Promise<AppState>;
  export function GetSettingsState(): Promise<SettingsState>;
  export function SaveSettings(values: Record<string, string>): Promise<SettingsState>;
  export function GetFleetState(): Promise<FleetState>;
  export function AddFleetDevice(values: Record<string, string>): Promise<FleetState>;
  export function RemoveFleetDevice(id: number): Promise<FleetState>;
  export function CompleteOnboarding(): Promise<void>;
  export function InstallBackgroundHelper(): Promise<void>;
  export function RemoveBackgroundHelper(): Promise<void>;
  export function BackgroundHelperStatus(): Promise<BackgroundStatus>;
  export function CheckRuntime(): Promise<RuntimeStatus>;
  export function GetRuntimeGuides(): Promise<InstallGuide[]>;
  export function SaveCredentials(slug: string, values: Record<string, string>): Promise<void>;
  export function GetCredentials(slug: string): Promise<Record<string, string>>;
  export function DeployService(slug: string, values: Record<string, string>): Promise<Deployment>;
  export function StartService(slug: string): Promise<void>;
  export function StopService(slug: string): Promise<void>;
  export function RestartService(slug: string): Promise<void>;
  export function RemoveService(slug: string): Promise<void>;
  export function GetLogs(slug: string, lines: number): Promise<string>;
  export function RefreshDeployments(): Promise<Deployment[]>;
  export function CollectService(slug: string): Promise<EarningsRecord>;
  export function ListServices(): Promise<Service[]>;
  export function GetService(slug: string): Promise<Service>;
  export function ManagedRuntimePlan(): Promise<ManagedRuntimePlan>;
  export function GetEarningsSummary(): Promise<EarningsSummary>;
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
  earnings: EarningsRecord[] | null;
  guides: InstallGuide[];
  notifications: NotificationItem[];
  currencies: string[];
  summary: EarningsSummary;
  serviceDetails: Record<string, string> | null;
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
  today: number;
  month: number;
  todayChange: number;
  monthChange: number;
  breakdown: ServiceEarning[];
  points: PointsBalance[];
  daily: DailyPoint[];
  ratesStale: boolean;
  ratesUpdated: string;
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
}

export interface NotificationItem {
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

export interface FleetState {
  workers: number;
  mobiles: number;
  online: number;
  services: number;
  devices: FleetDevice[];
  uiUrl: string;
  localApiUrl: string;
  apiKey: string;
  apiListening: boolean;
  workerSnippet: string;
  mobileSnippet: string;
}

export interface RuntimeStatus {
  available: boolean;
  nativeAvailable: boolean;
  kind: string;
  message: string;
  version: string;
  context: string;
  tools: Record<string, string> | null;
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
  name: string;
  slug: string;
  category: string;
  status: string;
  website: string;
  description: string;
  shortDescription: string;
  referral: { signupUrl: string };
  docker: DockerConfig;
  requirements: {
    residentialIp: boolean;
    vpsIp: boolean;
    devicesPerAccount: number;
    devicesPerIp: number;
    minBandwidth: string;
    gpu: boolean;
    minStorage: string;
    note: string;
  };
  payment: { methods: string[]; minimumPayout: string; currency: string; frequency: string };
  earnings: { monthlyLow: number; monthlyHigh: number; currency: string; per: string; notes: string };
  cashout: { method: string; dashboardUrl: string; minAmount: number; currency: string; notes: string };
  platforms: string[];
  collector: { type: string; notes: string };
  manualOnly: boolean;
}

export interface DockerConfig {
  image: string;
  env: EnvVar[];
  ports: string[] | null;
  volumes: string[] | null;
  command: string;
  networkMode: string;
  capAdd: string[] | null;
  privileged: boolean;
  setup: string;
  notes: string;
}

export interface EnvVar {
  key: string;
  label: string;
  required: boolean;
  secret: boolean;
  description: string;
  default: string;
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
}

export interface EarningsRecord {
  platform: string;
  balance: number;
  currency: string;
  error?: string;
  createdAt: string;
}

export interface ManagedRuntimePlan {
  summary: string;
  phases: string[];
  risks: string[];
  providers: string[];
}
