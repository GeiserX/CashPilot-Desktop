#!/usr/bin/env node
// Browser-free checks on the dashboard card a joined computer shows about its
// server's commands (src/render/server_commands.ts).
//
//   1. THE TITLE names the server: "<server> can start and stop services on this
//      computer", and the card says what it can and cannot do.
//   2. THE SWITCH is checked when on, unchecked when off, and off says the server
//      can no longer change anything.
//   3. REACH: while listening it names the address the server uses; while not, it
//      says why, escaped.
//   4. THE LAST COMMAND shows what, which service, when and how it ended; a refusal
//      shows its reason; none yet says so.
//   5. WINDOWS: the firewall button shows only where a rule is needed, the switch is
//      on and no rule exists, and the text says what it opens before the button.
//
//   node scripts/server_commands_check.mjs      # against ./.harness-build

import { lastCommandLine, renderServerCommands } from "../.harness-build/render/server_commands.js";

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

const base = {
  server: "cashpilot.example:8080",
  on: true,
  listening: true,
  url: "http://192.168.1.30:8086",
  problem: "",
  firewallNeeded: false,
  firewallRule: false,
  opens: "incoming TCP port 8086 to CashPilot only, on networks Windows calls private, from 10.0.0.0/8",
  last: null,
};

// 1. Title and scope.
{
  const html = renderServerCommands(base, now);
  check("title names the server", text(html).includes("cashpilot.example:8080 can start and stop services on this computer"), text(html));
  check("says it cannot delete irreplaceable data", text(html).includes("cannot delete data that cannot be recovered"));
  check("says which images", text(html).includes("images CashPilot pins here"));
}

// 2. Switch.
{
  const on = renderServerCommands(base, now);
  const off = renderServerCommands({ ...base, on: false, listening: false }, now);
  check("switch checked when on", /data-server-commands-switch[^>]*checked/.test(on));
  check("switch unchecked when off", !/data-server-commands-switch[^>]*checked/.test(off), off);
  check("off says it cannot change anything", text(off).includes("cannot change it"), text(off));
}

// 3. Reach.
{
  const on = renderServerCommands(base, now);
  check("names the address", text(on).includes("http://192.168.1.30:8086"), text(on));
  const down = renderServerCommands({ ...base, listening: false, url: "", problem: "<b>port busy</b>" }, now);
  check("problem escaped", down.includes("&lt;b&gt;port busy&lt;/b&gt;") && !down.includes("<b>port busy"), down);
}

// 4. Last command.
{
  check("none yet", text(lastCommandLine(null, now)) === "No command received yet.");
  const ok = text(lastCommandLine({ at: "2026-11-25T11:55:00Z", action: "deploy", slug: "honeygain", ok: true, detail: "deployed" }, now));
  check("ok line", ok.includes("Deploy honeygain") && ok.includes("5 minutes ago") && ok.includes("done"), ok);
  const no = lastCommandLine({ at: "2026-11-25T11:59:50Z", action: "deploy", slug: "x", ok: false, detail: "Missing <required> fields" }, now);
  check("refusal shows reason, escaped", text(no).includes("refused: Missing &lt;required&gt; fields") && no.includes("warn"), no);
  check("card carries the last command", text(renderServerCommands({ ...base, last: { at: "2026-11-25T11:00:00Z", action: "stop", slug: "earnapp", ok: true, detail: "stopped" } }, now)).includes("Stop earnapp"));
}

// 5. Windows Firewall.
{
  check("no button where no rule is needed", !renderServerCommands(base, now).includes("data-server-commands-firewall"));
  const need = renderServerCommands({ ...base, firewallNeeded: true }, now);
  check("button where a rule is needed", need.includes("data-server-commands-firewall"));
  const t = text(need);
  check("says what it opens before the button", t.indexOf("incoming TCP port 8086") >= 0 && t.indexOf("incoming TCP port 8086") < t.indexOf("Allow through Windows Firewall"), t);
  check("says Windows asks for an administrator", t.includes("asks for an administrator"));
  const made = renderServerCommands({ ...base, firewallNeeded: true, firewallRule: true }, now);
  check("no button once the rule exists", !made.includes("data-server-commands-firewall") && text(made).includes("removes this rule"), text(made));
  check("no button while off", !renderServerCommands({ ...base, firewallNeeded: true, on: false }, now).includes("data-server-commands-firewall"));
}

if (failures) {
  console.error(`\n${failures} of ${checks} checks failed`);
  process.exit(1);
}
console.log(`server_commands_check: ${checks} checks passed`);
