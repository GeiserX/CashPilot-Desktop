declare module "../wailsjs/go/main/App" {
  export function GetAppState(): Promise<AppState>;
  export function CompleteOnboarding(): Promise<void>;
  export function CheckRuntime(): Promise<RuntimeStatus>;
  export function GetRuntimeGuides(): Promise<InstallGuide[]>;
  export function SaveCredentials(slug: string, values: Record<string, string>): Promise<void>;
  export function GetCredentials(slug: string): Promise<Record<string, string>>;
  export function DeployService(slug: string, values: Record<string, string>): Promise<Deployment>;
  export function StopService(slug: string): Promise<void>;
  export function RestartService(slug: string): Promise<void>;
  export function RemoveService(slug: string): Promise<void>;
  export function GetLogs(slug: string, lines: number): Promise<string>;
  export function RefreshDeployments(): Promise<Deployment[]>;
  export function CollectService(slug: string): Promise<EarningsRecord>;
  export function ManagedRuntimePlan(): Promise<ManagedRuntimePlan>;
}

export interface AppState {
  config: AppConfig;
  runtime: RuntimeStatus;
  services: Service[];
  deployments: Deployment[] | null;
  earnings: EarningsRecord[] | null;
  guides: InstallGuide[];
}

export interface AppConfig {
  firstRunComplete: boolean;
  displayCurrency: string;
  runtimeProvider: string;
  autoUpdate: boolean;
}

export interface RuntimeStatus {
  available: boolean;
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
  requirements: Record<string, unknown>;
  payment: { methods: string[]; minimumPayout: string; currency: string; frequency: string };
  earnings: { monthlyLow: number; monthlyHigh: number; currency: string; per: string; notes: string };
  cashout: { dashboardUrl: string; minAmount: number; currency: string; notes: string };
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
