export namespace activity {
	
	export class IPGeoStatus {
	    present: boolean;
	    ageSeconds?: number;
	    rows?: number;
	
	    static createFrom(source: any = {}) {
	        return new IPGeoStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.present = source["present"];
	        this.ageSeconds = source["ageSeconds"];
	        this.rows = source["rows"];
	    }
	}

}

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
	
	export class AddressRead {
	    command: string[];
	    pattern: string;
	
	    static createFrom(source: any = {}) {
	        return new AddressRead(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.command = source["command"];
	        this.pattern = source["pattern"];
	    }
	}
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
	export class NodeRead {
	    command: string[];
	    node: string;
	    balance: string;
	    lifetime: string;
	    settledNet?: string;
	    settledFees?: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeRead(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.command = source["command"];
	        this.node = source["node"];
	        this.balance = source["balance"];
	        this.lifetime = source["lifetime"];
	        this.settledNet = source["settledNet"];
	        this.settledFees = source["settledFees"];
	    }
	}
	export class CollectorMetadata {
	    type: string;
	    notes: string;
	    credentialHint: string;
	    perNodeEarnings: boolean;
	    quietDays: number;
	    nodeRead?: NodeRead;
	
	    static createFrom(source: any = {}) {
	        return new CollectorMetadata(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.notes = source["notes"];
	        this.credentialHint = source["credentialHint"];
	        this.perNodeEarnings = source["perNodeEarnings"];
	        this.quietDays = source["quietDays"];
	        this.nodeRead = this.convertValues(source["nodeRead"], NodeRead);
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
	export class CriticalVolume {
	    target: string;
	    holds: string;
	
	    static createFrom(source: any = {}) {
	        return new CriticalVolume(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target = source["target"];
	        this.holds = source["holds"];
	    }
	}
	export class DeviceLink {
	    idEnv: string;
	    url: string;
	    steps: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceLink(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.idEnv = source["idEnv"];
	        this.url = source["url"];
	        this.steps = source["steps"];
	    }
	}
	export class Disclosure {
	    sells: string;
	    thirdPartyTraffic: string;
	    dataCollected: string;
	    ispRisk: string;
	    accountRules: string;
	
	    static createFrom(source: any = {}) {
	        return new Disclosure(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sells = source["sells"];
	        this.thirdPartyTraffic = source["thirdPartyTraffic"];
	        this.dataCollected = source["dataCollected"];
	        this.ispRisk = source["ispRisk"];
	        this.accountRules = source["accountRules"];
	    }
	}
	export class ResourceLimits {
	    memLimit: string;
	    memReservation: string;
	    oomScoreAdj?: number;
	    cpuShares: number;
	
	    static createFrom(source: any = {}) {
	        return new ResourceLimits(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.memLimit = source["memLimit"];
	        this.memReservation = source["memReservation"];
	        this.oomScoreAdj = source["oomScoreAdj"];
	        this.cpuShares = source["cpuShares"];
	    }
	}
	export class HealthSignal {
	    pattern: string;
	    means: string;
	    state: string;
	    unless?: string;
	    windowHours?: number;
	    minMatches?: number;
	
	    static createFrom(source: any = {}) {
	        return new HealthSignal(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pattern = source["pattern"];
	        this.means = source["means"];
	        this.state = source["state"];
	        this.unless = source["unless"];
	        this.windowHours = source["windowHours"];
	        this.minMatches = source["minMatches"];
	    }
	}
	export class NodeFirewall {
	    flag: string;
	    chain: string;
	    blackhole: string;
	
	    static createFrom(source: any = {}) {
	        return new NodeFirewall(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.flag = source["flag"];
	        this.chain = source["chain"];
	        this.blackhole = source["blackhole"];
	    }
	}
	export class EnvVar {
	    key: string;
	    label: string;
	    required: boolean;
	    secret: boolean;
	    description: string;
	    default: string;
	    pattern?: string;
	    portRange?: boolean;
	
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
	        this.pattern = source["pattern"];
	        this.portRange = source["portRange"];
	    }
	}
	export class DockerConfig {
	    image: string;
	    tag: string;
	    imageByArch: Record<string, string>;
	    platforms: string[];
	    env: EnvVar[];
	    ports: string[];
	    volumes: string[];
	    criticalVolumes: CriticalVolume[];
	    command: string;
	    entrypoint: string[];
	    networkMode: string;
	    nodeFirewall: NodeFirewall;
	    capAdd: string[];
	    devices: string[];
	    healthSignals: HealthSignal[];
	    advertisedAddressEnv: string;
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
	        this.tag = source["tag"];
	        this.imageByArch = source["imageByArch"];
	        this.platforms = source["platforms"];
	        this.env = this.convertValues(source["env"], EnvVar);
	        this.ports = source["ports"];
	        this.volumes = source["volumes"];
	        this.criticalVolumes = this.convertValues(source["criticalVolumes"], CriticalVolume);
	        this.command = source["command"];
	        this.entrypoint = source["entrypoint"];
	        this.networkMode = source["networkMode"];
	        this.nodeFirewall = this.convertValues(source["nodeFirewall"], NodeFirewall);
	        this.capAdd = source["capAdd"];
	        this.devices = source["devices"];
	        this.healthSignals = this.convertValues(source["healthSignals"], HealthSignal);
	        this.advertisedAddressEnv = source["advertisedAddressEnv"];
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
	    args?: string;
	    firewallBlock?: string[];
	
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
	        this.args = source["args"];
	        this.firewallBlock = source["firewallBlock"];
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
	    cryptoToken: string;
	    minimumPayout: string;
	    currency: string;
	    frequency: string;
	
	    static createFrom(source: any = {}) {
	        return new Payment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.methods = source["methods"];
	        this.cryptoToken = source["cryptoToken"];
	        this.minimumPayout = source["minimumPayout"];
	        this.currency = source["currency"];
	        this.frequency = source["frequency"];
	    }
	}
	export class Payout {
	    model: string;
	    chain: string;
	    addressEnv: string;
	    addressSource: string;
	    addressRead?: AddressRead;
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new Payout(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.chain = source["chain"];
	        this.addressEnv = source["addressEnv"];
	        this.addressSource = source["addressSource"];
	        this.addressRead = this.convertValues(source["addressRead"], AddressRead);
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
	export class ProxyProhibited {
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxyProhibited(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	    }
	}
	export class RatingCap {
	    key: string;
	    maxGrade: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new RatingCap(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.maxGrade = source["maxGrade"];
	        this.reason = source["reason"];
	    }
	}
	export class RatingItem {
	    text: string;
	    criterion: string;
	    kind: string;
	    source: string;
	    checkedOn: string;
	
	    static createFrom(source: any = {}) {
	        return new RatingItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.text = source["text"];
	        this.criterion = source["criterion"];
	        this.kind = source["kind"];
	        this.source = source["source"];
	        this.checkedOn = source["checkedOn"];
	    }
	}
	export class RatingVerdict {
	    key: string;
	    title: string;
	    verdict: string;
	
	    static createFrom(source: any = {}) {
	        return new RatingVerdict(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.title = source["title"];
	        this.verdict = source["verdict"];
	    }
	}
	export class RatingResult {
	    rated: boolean;
	    grade: string;
	    bandGrade: string;
	    score: number;
	    max: number;
	    percent: number;
	    caps: RatingCap[];
	    unknownCount: number;
	    thinEvidence: boolean;
	    reviewedOn: string;
	    verdicts: RatingVerdict[];
	    notes: Record<string, string>;
	    cons: RatingItem[];
	    pros: RatingItem[];
	    topCons: RatingItem[];
	    summary: string;
	
	    static createFrom(source: any = {}) {
	        return new RatingResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rated = source["rated"];
	        this.grade = source["grade"];
	        this.bandGrade = source["bandGrade"];
	        this.score = source["score"];
	        this.max = source["max"];
	        this.percent = source["percent"];
	        this.caps = this.convertValues(source["caps"], RatingCap);
	        this.unknownCount = source["unknownCount"];
	        this.thinEvidence = source["thinEvidence"];
	        this.reviewedOn = source["reviewedOn"];
	        this.verdicts = this.convertValues(source["verdicts"], RatingVerdict);
	        this.notes = source["notes"];
	        this.cons = this.convertValues(source["cons"], RatingItem);
	        this.pros = this.convertValues(source["pros"], RatingItem);
	        this.topCons = this.convertValues(source["topCons"], RatingItem);
	        this.summary = source["summary"];
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
	
	export class ReferralBonus {
	    referrer: string;
	    referee: string;
	
	    static createFrom(source: any = {}) {
	        return new ReferralBonus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.referrer = source["referrer"];
	        this.referee = source["referee"];
	    }
	}
	export class Referral {
	    signupUrl: string;
	    code: string;
	    program?: boolean;
	    bonus: ReferralBonus;
	
	    static createFrom(source: any = {}) {
	        return new Referral(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.signupUrl = source["signupUrl"];
	        this.code = source["code"];
	        this.program = source["program"];
	        this.bonus = this.convertValues(source["bonus"], ReferralBonus);
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
	
	export class Requirements {
	    residentialIp: boolean;
	    vpsIp: boolean;
	    devicesPerAccount: number;
	    devicesPerIp?: number;
	    minBandwidth: string;
	    gpu: boolean;
	    minStorage: string;
	    containerProhibited: boolean;
	    proxyProhibited?: ProxyProhibited;
	    note: string;
	    noteColumn: string;
	
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
	        this.containerProhibited = source["containerProhibited"];
	        this.proxyProhibited = this.convertValues(source["proxyProhibited"], ProxyProhibited);
	        this.note = source["note"];
	        this.noteColumn = source["noteColumn"];
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
	
	export class Service {
	    name: string;
	    slug: string;
	    category: string;
	    status: string;
	    priority: number;
	    website: string;
	    supportUrl: string;
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
	    payout: Payout;
	    disclosure: Disclosure;
	    deviceLink?: DeviceLink;
	    resurrectionChecked: string;
	    ratingResult?: RatingResult;
	    sourcePath: string;
	    manualOnly: boolean;
	    custom: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Service(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.slug = source["slug"];
	        this.category = source["category"];
	        this.status = source["status"];
	        this.priority = source["priority"];
	        this.website = source["website"];
	        this.supportUrl = source["supportUrl"];
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
	        this.payout = this.convertValues(source["payout"], Payout);
	        this.disclosure = this.convertValues(source["disclosure"], Disclosure);
	        this.deviceLink = this.convertValues(source["deviceLink"], DeviceLink);
	        this.resurrectionChecked = source["resurrectionChecked"];
	        this.ratingResult = this.convertValues(source["ratingResult"], RatingResult);
	        this.sourcePath = source["sourcePath"];
	        this.manualOnly = source["manualOnly"];
	        this.custom = source["custom"];
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

export namespace collectors {
	
	export class Field {
	    key: string;
	    label: string;
	    description: string;
	    secret?: boolean;
	    required?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Field(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.description = source["description"];
	        this.secret = source["secret"];
	        this.required = source["required"];
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
	    fleetServerEnabled: boolean;
	    upstreamUrl?: string;
	    upstreamIntervalMinutes?: number;
	    upstreamHistoryPushedTo?: string;
	    driftWarningsOff?: boolean;
	    driftIgnored?: Record<string, string>;
	    isolation?: string;
	    activityEnabled?: boolean;
	
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
	        this.fleetServerEnabled = source["fleetServerEnabled"];
	        this.upstreamUrl = source["upstreamUrl"];
	        this.upstreamIntervalMinutes = source["upstreamIntervalMinutes"];
	        this.upstreamHistoryPushedTo = source["upstreamHistoryPushedTo"];
	        this.driftWarningsOff = source["driftWarningsOff"];
	        this.driftIgnored = source["driftIgnored"];
	        this.isolation = source["isolation"];
	        this.activityEnabled = source["activityEnabled"];
	    }
	}

}

export namespace health {
	
	export class Report {
	    slug: string;
	    state: string;
	    reasons: string[];
	
	    static createFrom(source: any = {}) {
	        return new Report(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.state = source["state"];
	        this.reasons = source["reasons"];
	    }
	}

}

export namespace importers {
	
	export class Existing {
	    configured: boolean;
	    deployed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Existing(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.configured = source["configured"];
	        this.deployed = source["deployed"];
	    }
	}
	export class Field {
	    key: string;
	    label: string;
	    secret: boolean;
	    required: boolean;
	    state: string;
	    pattern: string;
	    value?: string;
	
	    static createFrom(source: any = {}) {
	        return new Field(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.secret = source["secret"];
	        this.required = source["required"];
	        this.state = source["state"];
	        this.pattern = source["pattern"];
	        this.value = source["value"];
	    }
	}
	export class File {
	    name: string;
	    content: string;
	
	    static createFrom(source: any = {}) {
	        return new File(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.content = source["content"];
	    }
	}
	export class Skipped {
	    key: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new Skipped(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.reason = source["reason"];
	    }
	}
	export class Row {
	    slug: string;
	    name: string;
	    app: string;
	    selected: boolean;
	    offInSource: boolean;
	    state: string;
	    reason: string;
	    fields: Field[];
	    existing: Existing;
	
	    static createFrom(source: any = {}) {
	        return new Row(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.name = source["name"];
	        this.app = source["app"];
	        this.selected = source["selected"];
	        this.offInSource = source["offInSource"];
	        this.state = source["state"];
	        this.reason = source["reason"];
	        this.fields = this.convertValues(source["fields"], Field);
	        this.existing = this.convertValues(source["existing"], Existing);
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
	export class Preview {
	    source: string;
	    sourceName: string;
	    rows: Row[];
	    notImported: Skipped[];
	    notices: string[];
	
	    static createFrom(source: any = {}) {
	        return new Preview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.sourceName = source["sourceName"];
	        this.rows = this.convertValues(source["rows"], Row);
	        this.notImported = this.convertValues(source["notImported"], Skipped);
	        this.notices = source["notices"];
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

export namespace main {
	
	export class ActivityTop {
	    value: string;
	    sightings: number;
	
	    static createFrom(source: any = {}) {
	        return new ActivityTop(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.value = source["value"];
	        this.sightings = source["sightings"];
	    }
	}
	export class ActivityDetail {
	    container: string;
	    ports: ActivityTop[];
	    asn: ActivityTop[];
	    cc: ActivityTop[];
	    classes: ActivityTop[];
	    raw: store.ActivityRawRow[];
	    rawHours: number;
	
	    static createFrom(source: any = {}) {
	        return new ActivityDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.container = source["container"];
	        this.ports = this.convertValues(source["ports"], ActivityTop);
	        this.asn = this.convertValues(source["asn"], ActivityTop);
	        this.cc = this.convertValues(source["cc"], ActivityTop);
	        this.classes = this.convertValues(source["classes"], ActivityTop);
	        this.raw = this.convertValues(source["raw"], store.ActivityRawRow);
	        this.rawHours = source["rawHours"];
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
	export class ActivityRow {
	    container: string;
	    status: string;
	    reason: string;
	    members: string[];
	    networkMode: string;
	    listenPorts: number[];
	    lastWindow: number;
	    estMax?: number;
	    remotesTotal?: number;
	    truncated: boolean;
	    opensLast?: number;
	    attemptFailsLast?: number;
	    series: store.ActivityPoint[];
	    topPorts: ActivityTop[];
	    topAsn: ActivityTop[];
	    topCc: ActivityTop[];
	    classes: Record<string, number>;
	    dirs: Record<string, number>;
	    alerts: number;
	
	    static createFrom(source: any = {}) {
	        return new ActivityRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.container = source["container"];
	        this.status = source["status"];
	        this.reason = source["reason"];
	        this.members = source["members"];
	        this.networkMode = source["networkMode"];
	        this.listenPorts = source["listenPorts"];
	        this.lastWindow = source["lastWindow"];
	        this.estMax = source["estMax"];
	        this.remotesTotal = source["remotesTotal"];
	        this.truncated = source["truncated"];
	        this.opensLast = source["opensLast"];
	        this.attemptFailsLast = source["attemptFailsLast"];
	        this.series = this.convertValues(source["series"], store.ActivityPoint);
	        this.topPorts = this.convertValues(source["topPorts"], ActivityTop);
	        this.topAsn = this.convertValues(source["topAsn"], ActivityTop);
	        this.topCc = this.convertValues(source["topCc"], ActivityTop);
	        this.classes = source["classes"];
	        this.dirs = source["dirs"];
	        this.alerts = source["alerts"];
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
	
	export class activityRound {
	    at: number;
	    workerStatus: string;
	    leftoverProbes: string[];
	
	    static createFrom(source: any = {}) {
	        return new activityRound(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at = source["at"];
	        this.workerStatus = source["workerStatus"];
	        this.leftoverProbes = source["leftoverProbes"];
	    }
	}
	export class ActivityView {
	    enabled: boolean;
	    days: number;
	    rawHours: number;
	    lastWindow: number;
	    round: activityRound;
	    networks: activity.IPGeoStatus;
	    networksEnabled: boolean;
	    containers: ActivityRow[];
	    alerts: store.ActivityAlert[];
	
	    static createFrom(source: any = {}) {
	        return new ActivityView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.days = source["days"];
	        this.rawHours = source["rawHours"];
	        this.lastWindow = source["lastWindow"];
	        this.round = this.convertValues(source["round"], activityRound);
	        this.networks = this.convertValues(source["networks"], activity.IPGeoStatus);
	        this.networksEnabled = source["networksEnabled"];
	        this.containers = this.convertValues(source["containers"], ActivityRow);
	        this.alerts = this.convertValues(source["alerts"], store.ActivityAlert);
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
	export class FleetViewPlatform {
	    slug: string;
	    usd?: number;
	    shared: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FleetViewPlatform(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.usd = source["usd"];
	        this.shared = source["shared"];
	    }
	}
	export class FleetView {
	    serverUrl: string;
	    reportedAt: string;
	    windowDays: number;
	    currency: string;
	    platforms: FleetViewPlatform[];
	    totalUsd?: number;
	    withoutReadings: string[];
	
	    static createFrom(source: any = {}) {
	        return new FleetView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.serverUrl = source["serverUrl"];
	        this.reportedAt = source["reportedAt"];
	        this.windowDays = source["windowDays"];
	        this.currency = source["currency"];
	        this.platforms = this.convertValues(source["platforms"], FleetViewPlatform);
	        this.totalUsd = source["totalUsd"];
	        this.withoutReadings = source["withoutReadings"];
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
	    paidOut?: number;
	    earnedAllTime?: number;
	    unpricedPayouts: number;
	
	    static createFrom(source: any = {}) {
	        return new EarningsSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.displayCurrency = source["displayCurrency"];
	        this.total = source["total"];
	        this.totalKnown = source["totalKnown"];
	        this.today = source["today"];
	        this.month = source["month"];
	        this.todayChange = source["todayChange"];
	        this.monthChange = source["monthChange"];
	        this.breakdown = this.convertValues(source["breakdown"], ServiceEarning);
	        this.points = this.convertValues(source["points"], PointsBalance);
	        this.daily = this.convertValues(source["daily"], DailyPoint);
	        this.ratesStale = source["ratesStale"];
	        this.ratesUpdated = source["ratesUpdated"];
	        this.paidOut = source["paidOut"];
	        this.earnedAllTime = source["earnedAllTime"];
	        this.unpricedPayouts = source["unpricedPayouts"];
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
	    outdatedServices: string[];
	    earnings: store.EarningsRecord[];
	    guides: runtime.InstallGuide[];
	    notifications: Notification[];
	    currencies: string[];
	    summary: EarningsSummary;
	    health: Record<string, store.HealthScore>;
	    producerStates: Record<string, health.Report>;
	    serviceDetails: Record<string, string>;
	    fleet?: FleetView;
	    hostname: string;
	    collectorFields: Record<string, Array<collectors.Field>>;
	    scaling: Record<string, services.ScalingLimit>;
	    copies: Record<string, Array<runtime.ContainerInfo>>;
	    addedDevices: Record<string, string>;
	    stateBackups: statebackup.Row[];
	    drift: Record<string, Array<string>>;
	    isolation?: runtime.FenceStatus;
	
	    static createFrom(source: any = {}) {
	        return new AppState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.config = this.convertValues(source["config"], config.AppConfig);
	        this.runtime = this.convertValues(source["runtime"], runtime.Status);
	        this.services = this.convertValues(source["services"], catalog.Service);
	        this.deployments = this.convertValues(source["deployments"], store.Deployment);
	        this.outdatedServices = source["outdatedServices"];
	        this.earnings = this.convertValues(source["earnings"], store.EarningsRecord);
	        this.guides = this.convertValues(source["guides"], runtime.InstallGuide);
	        this.notifications = this.convertValues(source["notifications"], Notification);
	        this.currencies = source["currencies"];
	        this.summary = this.convertValues(source["summary"], EarningsSummary);
	        this.health = this.convertValues(source["health"], store.HealthScore, true);
	        this.producerStates = this.convertValues(source["producerStates"], health.Report, true);
	        this.serviceDetails = source["serviceDetails"];
	        this.fleet = this.convertValues(source["fleet"], FleetView);
	        this.hostname = source["hostname"];
	        this.collectorFields = this.convertValues(source["collectorFields"], Array<collectors.Field>, true);
	        this.scaling = this.convertValues(source["scaling"], services.ScalingLimit, true);
	        this.copies = this.convertValues(source["copies"], Array<runtime.ContainerInfo>, true);
	        this.addedDevices = source["addedDevices"];
	        this.stateBackups = this.convertValues(source["stateBackups"], statebackup.Row);
	        this.drift = source["drift"];
	        this.isolation = this.convertValues(source["isolation"], runtime.FenceStatus);
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
	
	
	export class ImportApplyRequest {
	    source: string;
	    files: importers.File[];
	    slugs: string[];
	    extra: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new ImportApplyRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.files = this.convertValues(source["files"], importers.File);
	        this.slugs = source["slugs"];
	        this.extra = source["extra"];
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
	export class ImportRequest {
	    source: string;
	    files: importers.File[];
	
	    static createFrom(source: any = {}) {
	        return new ImportRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.files = this.convertValues(source["files"], importers.File);
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
	export class ImportResult {
	    saved: string[];
	    kept: Record<string, Array<string>>;
	
	    static createFrom(source: any = {}) {
	        return new ImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.saved = source["saved"];
	        this.kept = source["kept"];
	    }
	}
	export class IsolationResult {
	    status: runtime.FenceStatus;
	    moved: string[];
	    failed: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new IsolationResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = this.convertValues(source["status"], runtime.FenceStatus);
	        this.moved = source["moved"];
	        this.failed = source["failed"];
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
	
	
	export class ProxyCandidate {
	    slug: string;
	    name: string;
	    refusal: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxyCandidate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.name = source["name"];
	        this.refusal = source["refusal"];
	    }
	}
	export class ProxiesState {
	    proxies: proxies.View[];
	    candidates: ProxyCandidate[];
	    tunnelImage: string;
	    tunnelImageError: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxiesState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.proxies = this.convertValues(source["proxies"], proxies.View);
	        this.candidates = this.convertValues(source["candidates"], ProxyCandidate);
	        this.tunnelImage = source["tunnelImage"];
	        this.tunnelImageError = source["tunnelImageError"];
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

export namespace preflight {
	
	export class Finding {
	    verdict: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new Finding(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.verdict = source["verdict"];
	        this.message = source["message"];
	    }
	}
	export class Report {
	    slug: string;
	    name: string;
	    verdict: string;
	    summary: string;
	    findings: Finding[];
	    notChecked: string[];
	    machineArch: string;
	    blocking: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Report(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.name = source["name"];
	        this.verdict = source["verdict"];
	        this.summary = source["summary"];
	        this.findings = this.convertValues(source["findings"], Finding);
	        this.notChecked = source["notChecked"];
	        this.machineArch = source["machineArch"];
	        this.blocking = source["blocking"];
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

export namespace proxies {
	
	export class CapView {
	    enabled: boolean;
	    capBytes: number;
	    thresholdPct: number;
	    resetDay: number;
	    usedBytes: number;
	    periodStart: string;
	    cappedAt: string;
	    capReason: string;
	
	    static createFrom(source: any = {}) {
	        return new CapView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.capBytes = source["capBytes"];
	        this.thresholdPct = source["thresholdPct"];
	        this.resetDay = source["resetDay"];
	        this.usedBytes = source["usedBytes"];
	        this.periodStart = source["periodStart"];
	        this.cappedAt = source["cappedAt"];
	        this.capReason = source["capReason"];
	    }
	}
	export class CopyView {
	    slug: string;
	    name: string;
	    status: string;
	    tunnelStatus: string;
	    exitIp: string;
	    exitCountry: string;
	    stoppedByCap: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CopyView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.name = source["name"];
	        this.status = source["status"];
	        this.tunnelStatus = source["tunnelStatus"];
	        this.exitIp = source["exitIp"];
	        this.exitCountry = source["exitCountry"];
	        this.stoppedByCap = source["stoppedByCap"];
	    }
	}
	export class TestResult {
	    ok: boolean;
	    at: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new TestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.at = source["at"];
	        this.error = source["error"];
	    }
	}
	export class View {
	    id: number;
	    name: string;
	    kind: string;
	    host: string;
	    port: number;
	    exitNode: string;
	    loginServer: string;
	    rotating: boolean;
	    secrets: Record<string, boolean>;
	    cap: CapView;
	    lastExitIp: string;
	    lastCountry: string;
	    lastCheckedAt: string;
	    lastTest?: TestResult;
	    copies: CopyView[];
	
	    static createFrom(source: any = {}) {
	        return new View(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.host = source["host"];
	        this.port = source["port"];
	        this.exitNode = source["exitNode"];
	        this.loginServer = source["loginServer"];
	        this.rotating = source["rotating"];
	        this.secrets = source["secrets"];
	        this.cap = this.convertValues(source["cap"], CapView);
	        this.lastExitIp = source["lastExitIp"];
	        this.lastCountry = source["lastCountry"];
	        this.lastCheckedAt = source["lastCheckedAt"];
	        this.lastTest = this.convertValues(source["lastTest"], TestResult);
	        this.copies = this.convertValues(source["copies"], CopyView);
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
	
	export class ContainerInfo {
	    slug: string;
	    containerId: string;
	    name: string;
	    image: string;
	    status: string;
	    cpuPercent: number;
	    memoryMb: number;
	    instance: number;
	
	    static createFrom(source: any = {}) {
	        return new ContainerInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.containerId = source["containerId"];
	        this.name = source["name"];
	        this.image = source["image"];
	        this.status = source["status"];
	        this.cpuPercent = source["cpuPercent"];
	        this.memoryMb = source["memoryMb"];
	        this.instance = source["instance"];
	    }
	}
	export class DataMount {
	    target: string;
	    source: string;
	    volume: boolean;
	    critical: boolean;
	    holds: string;
	
	    static createFrom(source: any = {}) {
	        return new DataMount(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target = source["target"];
	        this.source = source["source"];
	        this.volume = source["volume"];
	        this.critical = source["critical"];
	        this.holds = source["holds"];
	    }
	}
	export class FenceStatus {
	    mode: string;
	    setting: string;
	    state: string;
	    backend: string;
	    error: string;
	    outside: string[];
	    outsideStopped: string[];
	
	    static createFrom(source: any = {}) {
	        return new FenceStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.setting = source["setting"];
	        this.state = source["state"];
	        this.backend = source["backend"];
	        this.error = source["error"];
	        this.outside = source["outside"];
	        this.outsideStopped = source["outsideStopped"];
	    }
	}
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
	export class RemovalPlan {
	    slug: string;
	    name: string;
	    volumes: DataMount[];
	    binds: DataMount[];
	    catalogKnown: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RemovalPlan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.name = source["name"];
	        this.volumes = this.convertValues(source["volumes"], DataMount);
	        this.binds = this.convertValues(source["binds"], DataMount);
	        this.catalogKnown = source["catalogKnown"];
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

export namespace services {
	
	export class CustomEnv {
	    key: string;
	    value: string;
	    secret: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CustomEnv(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.value = source["value"];
	        this.secret = source["secret"];
	    }
	}
	export class CustomDefinition {
	    name: string;
	    image: string;
	    env: CustomEnv[];
	    command: string;
	    dataPath: string;
	    memLimit: string;
	
	    static createFrom(source: any = {}) {
	        return new CustomDefinition(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.image = source["image"];
	        this.env = this.convertValues(source["env"], CustomEnv);
	        this.command = source["command"];
	        this.dataPath = source["dataPath"];
	        this.memLimit = source["memLimit"];
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
	
	export class CustomEnvView {
	    key: string;
	    value: string;
	    secret: boolean;
	    isSet: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CustomEnvView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.value = source["value"];
	        this.secret = source["secret"];
	        this.isSet = source["isSet"];
	    }
	}
	export class CustomServiceView {
	    slug: string;
	    name: string;
	    image: string;
	    env: CustomEnvView[];
	    command: string;
	    dataPath: string;
	    memLimit: string;
	    volume: string;
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new CustomServiceView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.name = source["name"];
	        this.image = source["image"];
	        this.env = this.convertValues(source["env"], CustomEnvView);
	        this.command = source["command"];
	        this.dataPath = source["dataPath"];
	        this.memLimit = source["memLimit"];
	        this.volume = source["volume"];
	        this.warnings = source["warnings"];
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
	export class ProxiedResult {
	    exitIp: string;
	    country: string;
	    warning: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxiedResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.exitIp = source["exitIp"];
	        this.country = source["country"];
	        this.warning = source["warning"];
	    }
	}
	export class ProxyTestResult {
	    exitIp: string;
	    country: string;
	    ms: number;
	
	    static createFrom(source: any = {}) {
	        return new ProxyTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.exitIp = source["exitIp"];
	        this.country = source["country"];
	        this.ms = source["ms"];
	    }
	}
	export class ScalingLimit {
	    max: number;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new ScalingLimit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.max = source["max"];
	        this.reason = source["reason"];
	    }
	}

}

export namespace statebackup {
	
	export class Volume {
	    target: string;
	    holds: string;
	
	    static createFrom(source: any = {}) {
	        return new Volume(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target = source["target"];
	        this.holds = source["holds"];
	    }
	}
	export class Row {
	    slug: string;
	    name: string;
	    volumes: Volume[];
	    state: string;
	    confirmedAt: string;
	    method: string;
	    tracksChanges: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Row(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slug = source["slug"];
	        this.name = source["name"];
	        this.volumes = this.convertValues(source["volumes"], Volume);
	        this.state = source["state"];
	        this.confirmedAt = source["confirmedAt"];
	        this.method = source["method"];
	        this.tracksChanges = source["tracksChanges"];
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

export namespace store {
	
	export class ActivityAlert {
	    kind: string;
	    subject: string;
	    message: string;
	    createdAt: number;
	
	    static createFrom(source: any = {}) {
	        return new ActivityAlert(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.subject = source["subject"];
	        this.message = source["message"];
	        this.createdAt = source["createdAt"];
	    }
	}
	export class ActivityPoint {
	    t: number;
	    opens?: number;
	    attemptFails?: number;
	    estMax?: number;
	
	    static createFrom(source: any = {}) {
	        return new ActivityPoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.t = source["t"];
	        this.opens = source["opens"];
	        this.attemptFails = source["attemptFails"];
	        this.estMax = source["estMax"];
	    }
	}
	export class ActivityRawRow {
	    windowEnd: number;
	    dir: string;
	    proto: string;
	    ip: string;
	    port: number;
	    class: string;
	    asn: string;
	    cc: string;
	    seen: number;
	
	    static createFrom(source: any = {}) {
	        return new ActivityRawRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.windowEnd = source["windowEnd"];
	        this.dir = source["dir"];
	        this.proto = source["proto"];
	        this.ip = source["ip"];
	        this.port = source["port"];
	        this.class = source["class"];
	        this.asn = source["asn"];
	        this.cc = source["cc"];
	        this.seen = source["seen"];
	    }
	}
	export class Deployment {
	    slug: string;
	    containerId: string;
	    name: string;
	    image: string;
	    status: string;
	    runtime: string;
	    cpuPercent: number;
	    memoryMb: number;
	    customImage: string;
	    instances: number;
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
	        this.customImage = source["customImage"];
	        this.instances = source["instances"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class EarningsRecord {
	    platform: string;
	    balance: number;
	    currency: string;
	    error?: string;
	    errorKind?: string;
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
	        this.errorKind = source["errorKind"];
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
	export class Payout {
	    id: number;
	    platform: string;
	    amount: number;
	    currency: string;
	    fxRateUsd?: number;
	    method: string;
	    paidOn: string;
	    createdAt: string;
	
	    static createFrom(source: any = {}) {
	        return new Payout(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.platform = source["platform"];
	        this.amount = source["amount"];
	        this.currency = source["currency"];
	        this.fxRateUsd = source["fxRateUsd"];
	        this.method = source["method"];
	        this.paidOn = source["paidOn"];
	        this.createdAt = source["createdAt"];
	    }
	}

}

export namespace usage {
	
	export class Container {
	    name: string;
	    slug: string;
	    service: string;
	    noCounters: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Container(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.slug = source["slug"];
	        this.service = source["service"];
	        this.noCounters = source["noCounters"];
	    }
	}
	export class View {
	    days: number;
	    step: number;
	    hours: number[];
	    containers: Container[];
	    container: string;
	    cpuPercent: number[];
	    memoryMb: number[];
	    rxRate: number[];
	    txRate: number[];
	    rxBytes: number[];
	    txBytes: number[];
	    trafficPartial: boolean[];
	    cpuPartial: boolean[];
	    memoryPartial: boolean[];
	
	    static createFrom(source: any = {}) {
	        return new View(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.days = source["days"];
	        this.step = source["step"];
	        this.hours = source["hours"];
	        this.containers = this.convertValues(source["containers"], Container);
	        this.container = source["container"];
	        this.cpuPercent = source["cpuPercent"];
	        this.memoryMb = source["memoryMb"];
	        this.rxRate = source["rxRate"];
	        this.txRate = source["txRate"];
	        this.rxBytes = source["rxBytes"];
	        this.txBytes = source["txBytes"];
	        this.trafficPartial = source["trafficPartial"];
	        this.cpuPartial = source["cpuPartial"];
	        this.memoryPartial = source["memoryPartial"];
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

