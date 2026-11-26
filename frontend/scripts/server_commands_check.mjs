#!/usr/bin/env node
// Browser-free checks on the dashboard card a joined computer shows about its
// server's commands (src/render/server_commands.ts).
//
//   1. THE TITLE names the server: "<server> can start and stop services on this
//      computer", and the card says what it can and cannot do.
//   2. THE SWITCH is checked when on, unchecked when off, and off says the server
//      can no longer change anything.
//   3. REACH: while listening it names the address the server uses; while not, it
//      says why, escaped. A server too old to send commands is named as such.
//   4. THE LAST COMMAND shows what, which service, when and how it ended; a refusal
//      shows its reason; none yet says so.
//   5. WINDOWS: the firewall button shows only where a rule is needed, the switch is
//      on and no rule exists, and the text says what it opens before the button.
//
//   node scripts/server_commands_check.mjs      # against ./.harness-build

import { fileURLToPath } from "node:url";
import { runInNewContext } from "node:vm";
import { build } from "vite";
import { lastCommandLine, renderServerCommands } from "../.harness-build/render/server_commands.js";

import { wireServerCommandsFirewall } from "../.harness-build/render/server_commands_wire.js";

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
  const old = renderServerCommands({ ...base, serverTooOld: true }, now);
  check(
    "an old server is told apart from a reachable one",
    text(old).includes("older than CashPilot 3.0.0") && text(old).includes("only sees what runs here") && !text(old).includes("It reaches this computer"),
    text(old),
  );
  const oldOff = renderServerCommands({ ...base, on: false, serverTooOld: true }, now);
  check("off still says off with an old server", text(oldOff).includes("Off.") && !text(oldOff).includes("older than"), text(oldOff));
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
  check("says it removes a Windows block rule, before the button", t.indexOf("removes that block too") >= 0 && t.indexOf("removes that block too") < t.indexOf("Allow through Windows Firewall"), t);
  check("says nothing listens until then", t.includes("does not listen until Windows Firewall lets it in"), t);
  const made = renderServerCommands({ ...base, firewallNeeded: true, firewallRule: true }, now);
  check("no button once the rule exists", !made.includes("data-server-commands-firewall") && text(made).includes("removes this rule"), text(made));
  check("no button while off", !renderServerCommands({ ...base, firewallNeeded: true, on: false }, now).includes("data-server-commands-firewall"));
}

// 6. A pending Allow can finish after either leave path. Its error must reach
// the app-wide notice even when the Dashboard output is no longer on the page.
for (const leavePath of ["Leave button", "clear address"]) {
  for (const pageAfterLeave of ["Dashboard", "Settings"]) {
    const button = new EventTarget();
    let activePage = "Dashboard";
    const output = [];
    const notices = [];
    const rule = "CashPilot: server commands (TCP 8086)";
    let declineRollback;
    const pending = new Promise((_, reject) => { declineRollback = reject; });
    wireServerCommandsFirewall({
      querySelector: (selector) => selector === "[data-server-commands-firewall]" ? button : null,
    }, {
      allow: () => pending,
      refresh: async () => {},
      setOutput: (message) => { if (activePage === "Dashboard") output.push(message); },
      notice: (message) => notices.push({ page: activePage, message }),
    });
    button.dispatchEvent(new Event("click"));
    activePage = pageAfterLeave;
    declineRollback(`the pairing changed and the firewall rule "${rule}" could not be removed`);
    await pending.catch(() => {});
    check(`late rollback after ${leavePath} shows the rule on ${pageAfterLeave}`,
      notices.length === 1 && notices[0].page === pageAfterLeave && notices[0].message.includes(rule),
      `notices: ${JSON.stringify(notices)}, Dashboard output: ${JSON.stringify(output)}`);
  }
}

// 7. Boot the real Dashboard so the check covers how it wires the notice.
{
  const root = fileURLToPath(new URL("../", import.meta.url));
  const { output } = await build({
    root,
    logLevel: "silent",
    build: {
      write: false,
      minify: false,
      rolldownOptions: {
        input: fileURLToPath(new URL("../src/main.ts", import.meta.url)),
        output: { format: "iife" },
      },
    },
  });
  const app = { innerHTML: "", querySelectorAll: () => [] };
  const pageOutput = { textContent: "" };
  const button = new EventTarget();
  const toasts = [];
  let toastStack;
  const document = {
    querySelector(selector) {
      if (selector === "#app") return app;
      if (selector === "#toast-stack") return toastStack;
      if (selector === "#service-output" && app.innerHTML.includes('id="service-output"')) return pageOutput;
      if (selector === "[data-server-commands-firewall]" && app.innerHTML.includes("data-server-commands-firewall")) return button;
      return null;
    },
    querySelectorAll: () => [],
    createElement: () => ({ style: {}, appendChild: (toast) => toasts.push(toast.innerHTML) }),
    head: { appendChild: () => {} },
    body: { appendChild: (stack) => { toastStack = stack; } },
  };
  const error = "Windows could not remove the firewall rule";
  runInNewContext(output.find((file) => file.type === "chunk").code, {
    document,
    navigator: { userAgent: "Windows" },
    setTimeout: () => {},
    window: {
      runtime: { EventsOnMultiple: () => {} },
      go: { main: { App: {
        GetAppState: async () => ({
          config: { firstRunComplete: true },
          runtime: { available: true },
          serverCommands: { ...base, firewallNeeded: true },
        }),
        SetupResume: async () => null,
        AllowServerCommandsThroughFirewall: async () => { throw error; },
      } } },
    },
  });
  await new Promise(setImmediate);
  button.dispatchEvent(new Event("click"));
  await new Promise(setImmediate);
  check("the Dashboard shows a firewall error in the toast stack",
    toasts.length === 1 && text(toasts[0]).includes(error) && !pageOutput.textContent.includes(error),
    `toasts: ${JSON.stringify(toasts)}, Dashboard output: ${pageOutput.textContent}`);
}

if (failures) {
  console.error(`\n${failures} of ${checks} checks failed`);
  process.exit(1);
}
console.log(`server_commands_check: ${checks} checks passed`);
