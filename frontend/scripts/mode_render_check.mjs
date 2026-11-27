#!/usr/bin/env node
// Browser-free checks on "How this computer runs" (src/render/mode.ts).
//
//   1. THE FIRST-RUN STEPS ARE DATA. Run everything here is Welcome, How it runs,
//      Runtime; Join my CashPilot adds Join before Runtime. A later step is one
//      more entry, and the bar counts it.
//   2. THE CHOICE shows both cards, says who should pick which, and selects none.
//   3. THE JOIN FORM takes the pasted block or the two fields, the key field is a
//      password field, and the form never carries a value.
//   4. EACH JOIN FAILURE shows its plain message, says nothing was saved, and is
//      escaped. A success names the server and this computer, says it now appears
//      on the Fleet page, and lists the hub devices only when the hub was on.
//   5. THE DASHBOARD BANNER says the earnings are on the CashPilot server, and
//      shows the last heartbeat and the last error.
//   6. THE SETTINGS CARD shows the mode and a switch, and each switch has a
//      confirm screen that says what happens in that direction.
//
//   node scripts/mode_render_check.mjs      # against ./.harness-build

import {
  firstRunSteps,
  pairingLine,
  renderGateSteps,
  renderJoinForm,
  renderJoinOutcome,
  renderJoinedBanner,
  renderModeCard,
  renderModeChoice,
} from "../.harness-build/render/mode.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const text = (html) => html.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
const now = Date.parse("2026-11-25T12:00:00Z");

// 1. Steps.
const ids = (path) => firstRunSteps(path).map((s) => s.id).join(",");
check("lead path steps", ids("lead") === "welcome,mode,runtime", ids("lead"));
check("no choice yet: same as lead", ids("") === "welcome,mode,runtime", ids(""));
check("follower path adds Join before Runtime", ids("follower") === "welcome,mode,join,runtime", ids("follower"));
{
  const bar = renderGateSteps(firstRunSteps("follower"), "join");
  check("bar counts the steps", bar.includes('aria-label="Step 3 of 4"'), bar);
  check("steps before the active one are done", (bar.match(/gate-step completed/g) || []).length === 2, bar);
  check("one active step", (bar.match(/gate-step active/g) || []).length === 1 && bar.includes('class="gate-step active" data-step="join"'), bar);
  const later = [...firstRunSteps("lead"), { id: "desktop-apps", label: "Desktop apps" }];
  const withVm = renderGateSteps(later, "desktop-apps");
  check("a later step needs no template change", withVm.includes('aria-label="Step 4 of 4"') && withVm.includes("Desktop apps"), withVm);
}

// 2. The choice.
{
  const html = renderModeChoice();
  check("two cards", (html.match(/data-mode-choice=/g) || []).length === 2, html);
  check("lead card", html.includes('data-mode-choice="lead"') && text(html).includes("Run everything here"));
  check("follower card", html.includes('data-mode-choice="follower"') && text(html).includes("Join my CashPilot"));
  check("lead says no server needed", text(html).includes("No CashPilot server needed"));
  check("follower says the server shows the earnings", text(html).includes("The server shows the earnings"));
  check("each card says who should pick it", (html.match(/Pick this if/g) || []).length === 2);
  check("nothing pre-selected", !/selected|aria-pressed="true"|checked/.test(html));
  check("says it can switch later", text(html).includes("switch later in Settings"));
}

// 3. The form.
{
  const html = renderJoinForm();
  check("paste box", /<textarea name="block"/.test(html));
  check("Add a box instructions", text(html).includes("Fleet") && text(html).includes("Add a box") && text(html).includes("Reveal API Key"));
  check("typed address field", /<input name="url" type="text"/.test(html));
  check("key is a password field", /<input name="key" type="password"/.test(html));
  check("form carries no value", !/\svalue=/.test(html), html);
  check("outcome slot", html.includes('id="join-result"'));
  check("connect and back", html.includes('id="join-connect"') && html.includes('id="join-back"'));
}

// 4. Outcomes.
const failure = (code, message) => ({ ok: false, code, message, serverUrl: "http://192.168.1.10:8080", serverHost: "192.168.1.10:8080", name: "my-pc", workerId: 0, hubWasOn: false, hubDevices: [] });
for (const [code, message] of [
  ["unreachable", "Cannot reach 192.168.1.10:8080. Check that CashPilot is running there, and that this computer is on the same network or Tailscale."],
  ["dns", "Cannot find nas.lan:8080. Check the address for a typo."],
  ["not-cashpilot", "That address answers, but it is not a CashPilot dashboard."],
  ["key-refused", "Your CashPilot refused the key. Copy it again from Fleet, Add a box, Reveal API Key."],
  ["too-old", "Your CashPilot is too old to accept this computer. Update it, then connect again."],
  ["address", "Use https, or a LAN or Tailscale address. Plain http to a public address would send the key in clear text."],
]) {
  const html = renderJoinOutcome(failure(code, message));
  check(`${code}: message shown`, text(html).includes(message), html);
  check(`${code}: says nothing was saved`, text(html).includes("Nothing was saved"));
  check(`${code}: announced`, html.includes('role="alert"') && html.includes(`data-code="${code}"`));
  check(`${code}: no continue`, !html.includes("join-continue"));
}
{
  const html = renderJoinOutcome(failure("input", `<img src=x onerror=alert(1)>`));
  check("failure message escaped", !html.includes("<img") && html.includes("&lt;img"), html);
}
{
  const ok = { ok: true, code: "", message: "", serverUrl: "http://192.168.1.10:8080", serverHost: "192.168.1.10:8080", name: "my-pc", workerId: 4, hubWasOn: false, hubDevices: [] };
  const html = renderJoinOutcome(ok);
  check("success names the server", text(html).includes("Connected to 192.168.1.10:8080"));
  check("success says Fleet page", text(html).includes("This computer now appears in your CashPilot Fleet page as my-pc"));
  check("success says collection stops", text(html).includes("stops logging in to your provider accounts"));
  check("success says history copied once", text(html).includes("copied to your CashPilot once"));
  check("success offers continue", html.includes('id="join-continue"'));
  check("switch off: says the server cannot deploy here", text(html).includes("cannot deploy or remove services here") && !text(html).includes("can also deploy"), text(html));
  const on = renderJoinOutcome({ ...ok, serverCommandsOn: true });
  check("switch on: says the server can deploy here", text(on).includes("can also deploy, stop and remove services here") && text(on).includes("turn off the switch on the dashboard"), text(on));
  check("the app still adds and removes apps", text(on).includes("add and remove earning apps here"));
  check("no hub line when the hub was off", !text(html).includes("workers and phones"));
  const hub = renderJoinOutcome({ ...ok, hubWasOn: true, hubDevices: ["nas-worker", "<b>phone</b>"] });
  check("hub line when the hub was on", text(hub).includes("no longer accepts workers and phones"));
  check("hub devices listed and escaped", hub.includes("nas-worker") && hub.includes("&lt;b&gt;phone") && !hub.includes("<b>phone"), hub);
  const named = renderJoinOutcome({ ...ok, name: "<script>x</script>" });
  check("computer name escaped", !named.includes("<script>"), named);
}

// 5. Banner.
{
  const good = { serverUrl: "http://192.168.1.10:8080", lastHeartbeat: "2026-11-25T11:58:00Z", lastAttempt: "2026-11-25T11:58:00Z", lastError: "", errorCode: "" };
  const html = renderJoinedBanner(good, now);
  check("banner says where the earnings are", text(html).includes("Your earnings are shown on your CashPilot at http://192.168.1.10:8080"), text(html));
  check("banner says this computer does not log in", text(html).includes("It logs in to your provider accounts"));
  check("banner shows the last heartbeat", text(html).includes("Last heartbeat 2 minutes ago"), text(html));
  check("banner opens the server", html.includes('data-url="http://192.168.1.10:8080"'));
  const bad = renderJoinedBanner({ ...good, lastError: "Cannot reach 192.168.1.10:8080.", errorCode: "unreachable" }, now);
  check("banner shows the last error", text(bad).includes("Last try failed: Cannot reach 192.168.1.10:8080."), text(bad));
  const never = pairingLine({ ...good, lastHeartbeat: "" }, now);
  check("no heartbeat yet is said", text(never).includes("No heartbeat has reached it yet"), never);
  const refused = pairingLine({ ...good, lastError: "Your CashPilot refused this computer's key.", errorCode: "unauthorized" }, now);
  check("a refusal after heartbeats keeps the last heartbeat", text(refused).includes("Last heartbeat 2 minutes ago") && text(refused).includes("Last try failed"), text(refused));
  const restarted = pairingLine({ ...good, lastHeartbeat: "", lastError: "Your CashPilot refused this computer's key.", errorCode: "unauthorized" }, now);
  check("a failing link with no heartbeat since start never says none ever arrived", text(restarted).includes("since CashPilot started") && !text(restarted).includes("yet"), text(restarted));
  const xss = renderJoinedBanner({ ...good, serverUrl: `http://x"><script>`, lastError: "<i>e</i>" }, now);
  check("banner escapes", !xss.includes("<script>") && !xss.includes("<i>e</i>"), xss);
}

// 6. Settings card.
{
  const lead = { mode: "lead", name: "my-pc", pairing: null, hubOn: false, hubDevices: [] };
  const summary = renderModeCard(lead, "summary", null, now);
  check("card title", text(summary).includes("How this computer runs"));
  check("lead summary names the mode", text(summary).includes("Run everything here."));
  check("lead summary offers the switch", summary.includes('data-mode-action="confirm-join"'));

  const confirmJoin = renderModeCard({ ...lead, hubOn: true, hubDevices: ["nas-worker"] }, "confirm-join", null, now);
  const cj = text(confirmJoin);
  check("join confirm: earners keep running", cj.includes("keep running"));
  check("join confirm: history copied once", cj.includes("copied to your CashPilot once"));
  check("join confirm: collection stops", cj.includes("stops collecting earnings"));
  check("join confirm: hub turns off and lists devices", cj.includes("stops accepting workers and phones") && cj.includes("nas-worker"));
  check("join confirm: continue goes to the form", confirmJoin.includes('data-mode-action="join"'));
  check("join confirm: cancel", confirmJoin.includes('data-mode-action="summary"'));
  check("join confirm: no hub line when off", !text(renderModeCard(lead, "confirm-join", null, now)).includes("workers and phones"));
  check("join view shows the form", renderModeCard(lead, "join", null, now).includes('id="join-form"'));

  const pairing = { serverUrl: "http://192.168.1.10:8080", lastHeartbeat: "2026-11-25T11:00:00Z", lastAttempt: "2026-11-25T11:59:00Z", lastError: "Your CashPilot refused the key.", errorCode: "key-refused" };
  const follower = { mode: "follower", name: "my-pc", pairing, hubOn: false, hubDevices: [] };
  const fs = renderModeCard(follower, "summary", null, now);
  check("follower summary names the server", text(fs).includes("Join my CashPilot.") && text(fs).includes("http://192.168.1.10:8080"));
  check("follower summary shows the pairing", text(fs).includes("Last heartbeat 1 hour ago") && text(fs).includes("Last try failed: Your CashPilot refused the key."), text(fs));
  check("follower summary offers the switch", fs.includes('data-mode-action="confirm-lead"'));
  check("follower summary: no hub line when off", !fs.includes("data-hub-still-on"));
  const upgraded = text(renderModeCard({ ...follower, hubOn: true, hubDevices: ["nas-worker", "<b>x</b>"] }, "summary", null, now));
  check("follower with the hub on: says it still accepts workers and names them", upgraded.includes("still accepts workers and phones") && upgraded.includes("nas-worker") && upgraded.includes("Accept workers (hub mode)"), upgraded);
  check("follower with the hub on: names are escaped", !renderModeCard({ ...follower, hubOn: true, hubDevices: ["<b>x</b>"] }, "summary", null, now).includes("<b>x</b>"));

  const cl = text(renderModeCard(follower, "confirm-lead", null, now));
  check("lead confirm: earners keep running", cl.includes("keep running"));
  check("lead confirm: keys forgotten", cl.includes("forgets its keys"));
  check("lead confirm: collection restarts", cl.includes("starts collecting earnings again"));
  check("lead confirm: remove on the Fleet page", cl.includes("Remove it on its Fleet page") && cl.includes("my-pc"));
  check("lead confirm: the server's commands end", cl.includes("can no longer start and stop services on this computer"), cl);
  check("lead confirm: switch button", renderModeCard(follower, "confirm-lead", null, now).includes('data-mode-action="leave"'));

  const left = text(renderModeCard(lead, "left", { serverUrl: "http://192.168.1.10:8080", name: "my-pc" }, now));
  check("after leaving: says what to remove where", left.includes("remove my-pc on the Fleet page") && left.includes("http://192.168.1.10:8080"), left);
  check("after leaving: no firewall line when the rule went", !left.includes("Windows Defender Firewall"), left);
  const cleanup = renderModeCard({ ...lead, firewallCleanup: "Rule is still in Windows Firewall <x>" }, "summary", null, now);
  check("Lead after restart offers firewall cleanup", cleanup.includes('data-mode-action="firewall-cleanup"') && cleanup.includes("&lt;x&gt;"), cleanup);
  const stuck = renderModeCard(lead, "left", { serverUrl: "http://192.168.1.10:8080", name: "my-pc", firewallError: "the firewall rule <x> was not removed" }, now);
  check("after leaving: a rule that stayed is named, escaped", text(stuck).includes("You can delete it in Windows Defender Firewall") && stuck.includes("&lt;x&gt;"), stuck);
}

console.log(`${checks - failures}/${checks} mode render checks passed`);
if (failures) process.exit(1);
