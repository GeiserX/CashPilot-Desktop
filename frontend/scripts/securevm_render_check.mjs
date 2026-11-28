#!/usr/bin/env node
// Browser-free checks on the Secure VM section of the Security page.
//
// The rules these hold:
//   1. Only "connected" with the fence "held" reads as green. Any other state
//      is shown, with the guard's reason.
//   2. An unknown reading is "unknown", never "no"; an unknown drop count is an
//      em dash, never 0.
//   3. A locked earner cannot be put in the VM, and the page says why.
//   4. Undo shows what it removed, what it kept on purpose and what is left;
//      "nothing is left" appears only when the check found nothing.
//
//   node scripts/securevm_render_check.mjs      # against ./.harness-build

import { renderSecureVM, renderSecureVMRemoval, secureVMGreen } from "../.harness-build/render/securevm.js";
import { t } from "../.harness-build/render/i18n.js";

let failures = 0;
let checks = 0;
function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const now = Date.parse("2026-11-27T12:00:00Z");
const base = (over = {}) => ({
  platform: "macos", available: true, unavailable: "", installed: true, updateNeeded: false, unfinished: false,
  network: "connected", fence: "held", reason: "", changes: null,
  provedAt: "2026-11-27T11:00:00Z", proving: false, blocked: 12, blockedSince: "2026-11-27T11:30:00Z",
  busy: "", step: "", fraction: -1, lastError: "", startError: "", guestLimit: false,
  freeDisk: 200 * 1024 ** 3, lowDisk: false,
  cost: { memoryGb: 6, cores: 4, diskGb: 80, downloadGb: 19.8 },
  proof: null, apps: [], ...over,
});
const app = (over = {}) => ({
  slug: "grass", name: "Grass", placement: "", canVm: true, locked: "",
  installed: "unknown", running: "unknown", signedIn: "unknown", detail: "", note: "", layers: null, ...over,
});

// 1. Green and not green
check("RULE 1: connected + held is green", secureVMGreen(base()));
for (const [network, fence] of [["disconnected", "cut"], ["connected", "reprove-needed"], ["off", "held"], ["disconnected", "unproven"]]) {
  check(`RULE 1: ${network} + ${fence} is not green`, !secureVMGreen(base({ network, fence })));
}
const green = renderSecureVM(base(), { now });
check("RULE 1: the green state names itself", green.includes('data-securevm-state="connected/held"') && green.includes(t("securevm.network.connected")) && green.includes("Online"), green);
check("RULE 1: the green state has no warning tone", !/class="text-warn" data-securevm-state/.test(green));
const cut = renderSecureVM(base({ network: "disconnected", fence: "cut", reason: "the fence changed, so the Secure VM is cut off the network: rules" }), { now });
check("RULE 1: a cut fence warns", /class="text-warn" data-securevm-state="disconnected\/cut"/.test(cut), cut);
check("RULE 1: a cut fence shows the guard's reason", cut.includes("cut off the network: rules"));
const silent = renderSecureVM(base({ fence: "unknown", reason: "The guard has not checked the fence for 5m0s." }), { now });
check("RULE 1: a silent guard is not green", !secureVMGreen(base({ fence: "unknown" })) && /class="text-warn" data-securevm-state="connected\/unknown"/.test(silent), silent);
check("RULE 1: a silent guard says so", silent.includes(t("securevm.fence.unknown")) && silent.includes("has not checked the fence"), silent);

// 2. Unknowns
const noCount = renderSecureVM(base({ blocked: null }), { now });
check("RULE 2: an unknown drop count is an em dash", noCount.includes("Blocked packets: —") && !/blocked 0 packets/.test(noCount), noCount);
check("RULE 2: a real zero is shown as zero", renderSecureVM(base({ blocked: 0 }), { now }).includes("blocked 0 packets"));
check("one dropped packet reads as one packet", renderSecureVM(base({ blocked: 1 }), { now }).includes("blocked 1 packet since"));
const hostile = renderSecureVM(base({ lastError: '<img src=x onerror=alert(1)>' }), { now });
check("an error from the guest is escaped", hostile.includes("&lt;img") && !hostile.includes("<img"), hostile);
const unknownApp = renderSecureVM(base({ apps: [app({ placement: "vm", installed: "yes", running: "unknown", signedIn: "unknown", layers: ["secure-vm", "vm-fence"] })] }), { now });
check("RULE 2: an unknown sign-in reads unknown, not no", unknownApp.includes("Signed in: unknown") && !unknownApp.includes("Signed in: no"), unknownApp);

// 3. Locks
const locked = renderSecureVM(base({ apps: [app({ slug: "honeygain", name: "Honeygain", canVm: false, locked: "Its terms forbid or warn against VMs, so it runs only on this computer.", placement: "host" })] }), { now });
check("RULE 3: a locked earner's VM choice is disabled", /data-securevm-place="vm" data-slug="honeygain" disabled/.test(locked), locked);
check("RULE 3: a locked earner says why", locked.includes("data-securevm-locked") && locked.includes("Its terms forbid"));
check("RULE 3: a locked earner can still be on this computer", /data-securevm-place="host" data-slug="honeygain"(?! disabled)/.test(locked));
const absentApps = renderSecureVM(base({ installed: false, network: "absent", fence: "unproven", apps: [app()] }), { now });
check("RULE 3: without a VM the VM choice is disabled", /data-securevm-place="vm" data-slug="grass" disabled title="Install the Secure VM first."/.test(absentApps), absentApps);

// Earner in the VM
const inVM = renderSecureVM(base({ apps: [app({ placement: "vm", installed: "yes", running: "yes", signedIn: "no", layers: ["secure-vm", "vm-fence"], detail: "It is not signed in. Press Sign in to open it in the Secure VM." })] }), { now });
check("a signed-out earner offers Sign in", inVM.includes('data-securevm-signin="grass"'), inVM);
check("an earner in the VM lists its layers", inVM.includes("Protection: Secure VM, VM network fence"));
check("an earner in the VM shows its placement", inVM.includes('data-placement="vm"') && /data-securevm-place="vm"[^>]*aria-pressed="true"/.test(inVM));
const signedIn = renderSecureVM(base({ apps: [app({ placement: "vm", installed: "yes", running: "yes", signedIn: "yes", layers: ["secure-vm", "vm-fence"] })] }), { now });
check("a signed-in earner offers no Sign in", !signedIn.includes("data-securevm-signin"));
const onHost = renderSecureVM(base({ apps: [app({ placement: "host" })] }), { now });
check("an earner on this computer shows no VM status", !onHost.includes("Signed in:") && onHost.includes("Protection: none beyond this computer"));

// Install, unavailable, errors, busy
const absent = renderSecureVM(base({ installed: false, network: "absent", fence: "unproven" }), { now });
check("no VM: Install is offered", absent.includes('data-securevm-action="install"') && !absent.includes('data-securevm-action="verify"'), absent);
check("no VM: the cost is shown before Install", absent.includes("6 GB of memory") && absent.includes("4 processor cores") && absent.includes("80 GB") && absent.includes("20 GB download"), absent);
check("no VM: the licence line shows before Install", absent.includes("data-securevm-licence") && absent.includes("personal, non-commercial use"), absent);
const unavailable = renderSecureVM(base({ available: false, installed: false, unavailable: "The Secure VM needs macOS 26 or newer." }), { now });
check("unavailable: the reason is shown and nothing is offered", unavailable.includes("macOS 26 or newer") && !unavailable.includes("data-securevm-action"), unavailable);
const limit = renderSecureVM(base({ network: "off", fence: "held", guestLimit: true, startError: "macOS lets a Mac run at most 2 macOS virtual machines at once. Stop one in the app that runs it, then press Start." }), { now });
check("the two-guest limit is said plainly", limit.includes("data-securevm-start-error") && limit.includes("at most 2 macOS virtual machines"), limit);
check("a stopped VM offers Start, not Stop", limit.includes('data-securevm-action="start"') && !limit.includes('data-securevm-action="stop"'));
const low = renderSecureVM(base({ lowDisk: true, freeDisk: 6 * 1024 ** 3 }), { now });
check("low disk is said with the free space", low.includes("data-securevm-low-disk") && low.includes("6 GB free"), low);
const busy = renderSecureVM(base({ installed: false, network: "absent", busy: "install", step: "Downloading macOS for the VM", fraction: 0.42 }), { now });
check("busy: the step and its percentage show", busy.includes("Downloading macOS for the VM 42%"), busy);
check("busy: Install is disabled while it runs", /data-securevm-action="install" disabled/.test(busy));
const failed = renderSecureVM(base({ lastError: "the administrator prompt was cancelled, so nothing changed on this Mac" }), { now });
check("a failed action says why", failed.includes("data-securevm-last-error") && failed.includes("prompt was cancelled"));

const upd = renderSecureVM(base({ updateNeeded: true }), { now });
check("an update of the app offers to update the VM", upd.includes("data-securevm-update") && upd.includes('data-securevm-action="install"'), upd);
const unf = renderSecureVM(base({ unfinished: true }), { now });
check("an unfinished setup offers to finish it", unf.includes("data-securevm-unfinished") && unf.includes('data-securevm-action="install"'), unf);
check("a finished setup offers no finish", !green.includes("data-securevm-unfinished"));
check("no update needed offers no update", !green.includes("data-securevm-update") && !green.includes('data-securevm-action="install"'));

// Proof
const proof = { at: "2026-11-27T11:00:00Z", green: false, why: "a host address answered", steps: [
  { id: "red-both-layers-open", kind: "red", outcome: "went-red", detail: "" },
  { id: "green-production", kind: "green", outcome: "leak", detail: "" },
] };
const red = renderSecureVM(base({ network: "disconnected", fence: "cut", proof }), { now });
check("a red proof says the VM stays cut off", red.includes('data-securevm-proof="red"') && red.includes("stays cut off"), red);
check("a leak is shown as LEAKED", /data-proof-outcome="leak"[\s\S]*LEAKED/.test(red));
check("proof steps have plain names", red.includes("A hole opened on purpose") && red.includes("The whole fence"), red);
check("a red step that went red reads as expected", /data-proof-outcome="went-red"[\s\S]*reached, as it should/.test(red));

// 4. Undo
const ask = renderSecureVM(base(), { now, askUndo: true });
check("Undo asks first and says what goes", ask.includes("data-securevm-undo-ask") && ask.includes("administrator password") && ask.includes('data-securevm-action="undo"'), ask);
const clean = renderSecureVMRemoval({ removed: ["the VM CashPilot-VM"], kept: ["/Library/Preferences/x.plist: it was there before"], left: [] });
check("RULE 4: kept items are shown", clean.includes("data-securevm-kept") && clean.includes("it was there before"), clean);
check("RULE 4: nothing left says so", clean.includes("data-securevm-clean"));
const dirty = renderSecureVMRemoval({ removed: [], kept: null, left: ["pf rules in com.apple/cashpilot-vm"] });
check("RULE 4: a leftover is listed and nothing-left is not claimed", dirty.includes("data-securevm-left") && dirty.includes("pf rules") && !dirty.includes("data-securevm-clean"), dirty);

// Escaping
const evil = renderSecureVM(base({ reason: "<img src=x>", network: "disconnected", fence: "cut" }), { now });
check("the reason is escaped", !evil.includes("<img") && evil.includes("&lt;img"));

if (failures) {
  console.error(`\n${failures} of ${checks} Secure VM render checks failed`);
  process.exit(1);
}
console.log(`ok   ${checks} Secure VM render checks`);
