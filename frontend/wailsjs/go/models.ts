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
	
	    static createFrom(source: any = {}) {
	        return new AppConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.firstRunComplete = source["firstRunComplete"];
	        this.displayCurrency = source["displayCurrency"];
	        this.runtimeProvider = source["runtimeProvider"];
	        this.autoUpdate = source["autoUpdate"];
	    }
	}

}

export namespace main {
	
	export class AppState {
	    config: config.AppConfig;
	    runtime: runtime.Status;
	    services: catalog.Service[];
	    deployments: store.Deployment[];
	    earnings: store.EarningsRecord[];
	    history: store.EarningsRecord[];
	    guides: runtime.InstallGuide[];
	
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
	        this.history = this.convertValues(source["history"], store.EarningsRecord);
	        this.guides = this.convertValues(source["guides"], runtime.InstallGuide);
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

}

