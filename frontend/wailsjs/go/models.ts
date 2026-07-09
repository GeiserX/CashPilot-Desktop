export namespace bgservice {
	
	export class Status {
	    installed: boolean;
	    running: boolean;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.running = source["running"];
	        this.label = source["label"];
	    }
	}

}

export namespace catalog {
	
	export class Cashout {
	    method: string;
	    dashboardUrl: string;
	    minAmount: number;
	    currency: string;
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new Cashout(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.method = source["method"];
	        this.dashboardUrl = source["dashboardUrl"];
	        this.minAmount = source["minAmount"];
	        this.currency = source["currency"];
	        this.notes = source["notes"];
	    }
	}
	export class CollectorMetadata {
	    type: string;
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new CollectorMetadata(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.notes = source["notes"];
	    }
	}
	export class ResourceLimits {
	    memLimit: string;
	    memReservation: string;
	    oomScoreAdj?: number;
	
	    static createFrom(source: any = {}) {
	        return new ResourceLimits(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.memLimit = source["memLimit"];
	        this.memReservation = source["memReservation"];
	        this.oomScoreAdj = source["oomScoreAdj"];
	    }
	}
	export class EnvVar {
	    key: string;
	    label: string;
	    required: boolean;
	    secret: boolean;
	    description: string;
	    default: string;
	
	    static createFrom(source: any = {}) {
	        return new EnvVar(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.required = source["required"];
	        this.secret = source["secret"];
	        this.description = source["description"];
	        this.default = source["default"];
	    }
	}
	export class DockerConfig {
	    image: string;
	    platforms: string[];
	    env: EnvVar[];
	    ports: string[];
	    volumes: string[];
	    command: string;
	    networkMode: string;
	    capAdd: string[];
	    privileged: boolean;
	    stopTimeout: number;
	    resources: ResourceLimits;
	    setup: string;
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new DockerConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.image = source["image"];
	        this.platforms = source["platforms"];
	        this.env = this.convertValues(source["env"], EnvVar);
	        this.ports = source["ports"];
	        this.volumes = source["volumes"];
	        this.command = source["command"];
	        this.networkMode = source["networkMode"];
	        this.capAdd = source["capAdd"];
	        this.privileged = source["privileged"];
	        this.stopTimeout = source["stopTimeout"];
	        this.resources = this.convertValues(source["resources"], ResourceLimits);
	        this.setup = source["setup"];
	        this.notes = source["notes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class EarningsEstimate {
	    monthlyLow: number;
	    monthlyHigh: number;
	    currency: string;
	    per: string;
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new EarningsEstimate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.monthlyLow = source["monthlyLow"];
	        this.monthlyHigh = source["monthlyHigh"];
	        this.currency = source["currency"];
	        this.per = source["per"];
	        this.notes = source["notes"];
	    }
	}
	
	export class NativeBinary {
	    os: string;
	    arch: string;
	    url: string;
	    sha256: string;
	    archive: string;
	    bin: string;
	
	    static createFrom(source: any = {}) {
	        return new NativeBinary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.os = source["os"];
	        this.arch = source["arch"];
	        this.url = source["url"];
	        this.sha256 = source["sha256"];
	        this.archive = source["archive"];
	        this.bin = source["bin"];
	    }
	}
	export class NativeConfig {
	    binaries: NativeBinary[];
	    command: string;
	    env: EnvVar[];
	
	    static createFrom(source: any = {}) {
	        return new NativeConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.binaries = this.convertValues(source["binaries"], NativeBinary);
	        this.command = source["command"];
	        this.env = this.convertValues(source["env"], EnvVar);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Payment {
	    methods: string[];
	    minimumPayout: string;
	    currency: string;
	    frequency: string;
	
	    static createFrom(source: any = {}) {
	        return new Payment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.methods = source["methods"];
	        this.minimumPayout = source["minimumPayout"];
	        this.currency = source["currency"];
	        this.frequency = source["frequency"];
	    }
	}
	export class Referral {
	    signupUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new Referral(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.signupUrl = source["signupUrl"];
	    }
	}
	export class Requirements {
	    residentialIp: boolean;
	    vpsIp: boolean;
	    devicesPerAccount: number;
	    devicesPerIp: number;
	    minBandwidth: string;
	    gpu: boolean;
	    minStorage: string;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new Requirements(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.residentialIp = source["residentialIp"];
	        this.vpsIp = source["vpsIp"];
	        this.devicesPerAccount = source["devicesPerAccount"];
	        this.devicesPerIp = source["devicesPerIp"];
	        this.minBandwidth = source["minBandwidth"];
	        this.gpu = source["gpu"];
	        this.minStorage = source["minStorage"];
	        this.note = source["note"];
	    }
	}
	
	export class Service {
	    name: string;
	    slug: string;
	    category: string;
	    status: string;
	    website: string;
	    description: string;
	    shortDescription: string;
	    referral: Referral;
	    docker: DockerConfig;
	    native: NativeConfig;
	    requirements: Requirements;
	    payment: Payment;
	    earnings: EarningsEstimate;
	    cashout: Cashout;
	    platforms: string[];
	    collector: CollectorMetadata;
	    sourcePath: string;
	    manualOnly: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Service(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.slug = source["slug"];
	        this.category = source["category"];
	        this.status = source["status"];
	        this.website = source["website"];
	        this.description = source["description"];
	        this.shortDescription = source["shortDescription"];
	        this.referral = this.convertValues(source["referral"], Referral);
	        this.docker = this.convertValues(source["docker"], DockerConfig);
	        this.native = this.convertValues(source["native"], NativeConfig);
	        this.requirements = this.convertValues(source["requirements"], Requirements);
	        this.payment = this.convertValues(source["payment"], Payment);
	        this.earnings = this.convertValues(source["earnings"], EarningsEstimate);
	        this.cashout = this.convertValues(source["cashout"], Cashout);
	        this.platforms = source["platforms"];
	        this.collector = this.convertValues(source["collector"], CollectorMetadata);
	        this.sourcePath = source["sourcePath"];
	        this.manualOnly = source["manualOnly"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace config {
	
	export class AppConfig {
	    firstRunComplete: boolean;
	    displayCurrency: string;
	    runtimeProvider: string;
	    autoUpdate: boolean;
	    hostnamePrefix: string;
	    collectIntervalMinutes: number;
	    retentionDays: number;
	    timezone: string;
	    fleetApiKey?: string;
	    fleetBindAddress: string;
	    fleetPort: number;
	    metricsEnabled: boolean;
	    workerUrlPolicy: string;
	    workerAllowedHosts: string[];
	    workerAllowMetadata: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AppConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.firstRunComplete = source["firstRunComplete"];
	        this.displayCurrency = source["displayCurrency"];
	        this.runtimeProvider = source["runtimeProvider"];
	        this.autoUpdate = source["autoUpdate"];
	        this.hostnamePrefix = source["hostnamePrefix"];
	        this.collectIntervalMinutes = source["collectIntervalMinutes"];
	        this.retentionDays = source["retentionDays"];
	        this.timezone = source["timezone"];
	        this.fleetApiKey = source["fleetApiKey"];
	        this.fleetBindAddress = source["fleetBindAddress"];
	        this.fleetPort = source["fleetPort"];
	        this.metricsEnabled = source["metricsEnabled"];
	        this.workerUrlPolicy = source["workerUrlPolicy"];
	        this.workerAllowedHosts = source["workerAllowedHosts"];
	        this.workerAllowMetadata = source["workerAllowMetadata"];
	    }
	}

}

export namespace main {
	
	export class DailyPoint {
	    day: string;
	    amount: number;
	
	    static createFrom(source: any = {}) {
	        return new DailyPoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.day = source["day"];
	        this.amount = source["amount"];
	    }
	}
	export class PointsBalance {
	    platform: string;
	    name: string;
	    balance: number;
	    currency: string;
	
	    static createFrom(source: any = {}) {
	        return new PointsBalance(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.platform = source["platform"];
	        this.name = source["name"];
	        this.balance = source["balance"];
	        this.currency = source["currency"];
	    }
	}
	export class CashoutProgress {
	    minAmount: number;
	    currency: string;
	    percent: number;
	    eligible: boolean;
	    comparable: boolean;
	    method: string;
	    dashboardUrl: string;
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new CashoutProgress(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.minAmount = source["minAmount"];
	        this.currency = source["currency"];
	        this.percent = source["percent"];
	        this.eligible = source["eligible"];
	        this.comparable = source["comparable"];
	        this.method = source["method"];
	        this.dashboardUrl = source["dashboardUrl"];
	        this.notes = source["notes"];
	    }
	}
	export class ServiceEarning {
	    platform: string;
	    name: string;
	    balance: number;
	    currency: string;
	    balanceDisplay: number;
	    convertible: boolean;
	    error: string;
	    cashout: CashoutProgress;
	
	    static createFrom(source: any = {}) {
	        return new ServiceEarning(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.platform = source["platform"];
	        this.name = source["name"];
	        this.balance = source["balance"];
	        this.currency = source["currency"];
	        this.balanceDisplay = source["balanceDisplay"];
	        this.convertible = source["convertible"];
	        this.error = source["error"];
	        this.cashout = this.convertValues(source["cashout"], CashoutProgress);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class EarningsSummary {
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
	
	    static createFrom(source: any = {}) {
	        return new EarningsSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.displayCurrency = source["displayCurrency"];
	        this.total = source["total"];
	        this.today = source["today"];
	        this.month = source["month"];
	        this.todayChange = source["todayChange"];
	        this.monthChange = source["monthChange"];
	        this.breakdown = this.convertValues(source["breakdown"], ServiceEarning);
	        this.points = this.convertValues(source["points"], PointsBalance);
	        this.daily = this.convertValues(source["daily"], DailyPoint);
	        this.ratesStale = source["ratesStale"];
	        this.ratesUpdated = source["ratesUpdated"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Notification {
	    level: string;
	    title: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new Notification(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.level = source["level"];
	        this.title = source["title"];
	        this.message = source["message"];
	    }
	}
	export class AppState {
	    config: config.AppConfig;
	    runtime: runtime.Status;
	    services: catalog.Service[];
	    deployments: store.Deployment[];
	    earnings: store.EarningsRecord[];
	    guides: runtime.InstallGuide[];
	    notifications: Notification[];
	    currencies: string[];
	    summary: EarningsSummary;
	    health: Record<string, store.HealthScore>;
	    serviceDetails: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new AppState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.config = this.convertValues(source["config"], config.AppConfig);
	        this.runtime = this.convertValues(source["runtime"], runtime.Status);
	        this.services = this.convertValues(source["services"], catalog.Service);
	        this.deployments = this.convertValues(source["deployments"], store.Deployment);
	        this.earnings = this.convertValues(source["earnings"], store.EarningsRecord);
	        this.guides = this.convertValues(source["guides"], runtime.InstallGuide);
	        this.notifications = this.convertValues(source["notifications"], Notification);
	        this.currencies = source["currencies"];
	        this.summary = this.convertValues(source["summary"], EarningsSummary);
	        this.health = this.convertValues(source["health"], store.HealthScore, true);
	        this.serviceDetails = source["serviceDetails"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class CollectorSetting {
	    slug: string;
	    name: string;
	    configured: boolean;
	    collector: string;
	
	    static createFrom(source: any = {}) {
	        return new CollectorSetting(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.name = source["name"];
	        this.configured = source["configured"];
	        this.collector = source["collector"];
	    }
	}
	
	
	export class EnvSetting {
	    key: string;
	    label: string;
	    value: string;
	    source: string;
	    secret: boolean;
	    readOnly: boolean;
	    help: string;
	
	    static createFrom(source: any = {}) {
	        return new EnvSetting(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.value = source["value"];
	        this.source = source["source"];
	        this.secret = source["secret"];
	        this.readOnly = source["readOnly"];
	        this.help = source["help"];
	    }
	}
	export class FleetState {
	    workers: number;
	    mobiles: number;
	    online: number;
	    services: number;
	    devices: store.FleetDevice[];
	    uiUrl: string;
	    localApiUrl: string;
	    apiKey: string;
	    apiListening: boolean;
	    workerSnippet: string;
	    mobileSnippet: string;
	
	    static createFrom(source: any = {}) {
	        return new FleetState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workers = source["workers"];
	        this.mobiles = source["mobiles"];
	        this.online = source["online"];
	        this.services = source["services"];
	        this.devices = this.convertValues(source["devices"], store.FleetDevice);
	        this.uiUrl = source["uiUrl"];
	        this.localApiUrl = source["localApiUrl"];
	        this.apiKey = source["apiKey"];
	        this.apiListening = source["apiListening"];
	        this.workerSnippet = source["workerSnippet"];
	        this.mobileSnippet = source["mobileSnippet"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	export class SettingsState {
	    environment: EnvSetting[];
	    collectors: CollectorSetting[];
	    config: config.AppConfig;
	
	    static createFrom(source: any = {}) {
	        return new SettingsState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.environment = this.convertValues(source["environment"], EnvSetting);
	        this.collectors = this.convertValues(source["collectors"], CollectorSetting);
	        this.config = this.convertValues(source["config"], config.AppConfig);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace runtime {
	
	export class InstallGuide {
	    id: string;
	    name: string;
	    description: string;
	    platforms: string[];
	    url: string;
	    commands: string[];
	    notes: string[];
	
	    static createFrom(source: any = {}) {
	        return new InstallGuide(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.platforms = source["platforms"];
	        this.url = source["url"];
	        this.commands = source["commands"];
	        this.notes = source["notes"];
	    }
	}
	export class ManagedRuntimePlan {
	    summary: string;
	    phases: string[];
	    risks: string[];
	    providers: string[];
	
	    static createFrom(source: any = {}) {
	        return new ManagedRuntimePlan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.summary = source["summary"];
	        this.phases = source["phases"];
	        this.risks = source["risks"];
	        this.providers = source["providers"];
	    }
	}
	export class Status {
	    available: boolean;
	    nativeAvailable: boolean;
	    kind: string;
	    message: string;
	    version: string;
	    context: string;
	    tools: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.nativeAvailable = source["nativeAvailable"];
	        this.kind = source["kind"];
	        this.message = source["message"];
	        this.version = source["version"];
	        this.context = source["context"];
	        this.tools = source["tools"];
	    }
	}

}

export namespace store {
	
	export class Deployment {
	    slug: string;
	    containerId: string;
	    name: string;
	    image: string;
	    status: string;
	    runtime: string;
	    cpuPercent: number;
	    memoryMb: number;
	    createdAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new Deployment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.containerId = source["containerId"];
	        this.name = source["name"];
	        this.image = source["image"];
	        this.status = source["status"];
	        this.runtime = source["runtime"];
	        this.cpuPercent = source["cpuPercent"];
	        this.memoryMb = source["memoryMb"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class EarningsRecord {
	    platform: string;
	    balance: number;
	    currency: string;
	    error?: string;
	    createdAt: string;
	
	    static createFrom(source: any = {}) {
	        return new EarningsRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.platform = source["platform"];
	        this.balance = source["balance"];
	        this.currency = source["currency"];
	        this.error = source["error"];
	        this.createdAt = source["createdAt"];
	    }
	}
	export class FleetDevice {
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
	
	    static createFrom(source: any = {}) {
	        return new FleetDevice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.endpoint = source["endpoint"];
	        this.os = source["os"];
	        this.arch = source["arch"];
	        this.status = source["status"];
	        this.services = source["services"];
	        this.lastSeen = source["lastSeen"];
	        this.createdAt = source["createdAt"];
	    }
	}
	export class HealthScore {
	    score: number;
	    uptimePercent: number;
	    samples: number;
	    restarts: number;
	    crashes: number;
	    stops: number;
	
	    static createFrom(source: any = {}) {
	        return new HealthScore(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.score = source["score"];
	        this.uptimePercent = source["uptimePercent"];
	        this.samples = source["samples"];
	        this.restarts = source["restarts"];
	        this.crashes = source["crashes"];
	        this.stops = source["stops"];
	    }
	}

}

