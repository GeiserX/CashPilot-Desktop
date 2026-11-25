#!/usr/bin/env node
// Browser-free checks on the Money waiting panel: the reminders before a provider
// writes off your earnings (AppState.reminders, internal/obligations).
//
// Each row must say which provider, what to do, by when and what is lost if not,
// with Done where the web app has it. A failed check is never an all-clear. While
// joined to a CashPilot server the panel says the server keeps the reminders and
// shows none. A reminder due within 7 days also reaches Needs attention.
//
//   node scripts/reminders_render_check.mjs      # against ./.harness-build

import { renderReminders, reminderAttention, withReminders, reminderWhen } from "../.harness-build/render/reminders.js";
import { renderAttention } from "../.harness-build/render/attention.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const grass = {
  slug: "grass", name: "Grass", rule: "claim-stage2-usdc", kind: "rule", action: "claim",
  title: "Claim your Grass Stage 2 USDC", loss: "Grass keeps every Stage 2 allocation not claimed by 22 Jan 2027.",
  occurrence: "2027-01-22", stage: "d60", due: "2027-01-22", daysLeft: 52, censored: false,
  how: "Sign in on app.grass.io and open the Allocation tab.", url: "https://app.grass.io/dashboard",
  source: "https://x.com/grassfdn/status/1", checked: "2026-11-24", canDone: true, canSnooze: true,
};
const honeygain = {
  slug: "honeygain", name: "Honeygain", rule: "keep-active-6-months", kind: "rule", action: "keep-active",
  title: "Keep a Honeygain device sharing, or make a payout", loss: "Honeygain treats the unpaid balance as expired.",
  occurrence: "2026-05-01", stage: "overdue", due: "2026-10-28", daysLeft: -3, censored: true,
  how: "", url: "https://dashboard.honeygain.com", source: "", checked: "", canDone: true, canSnooze: false,
};
const cash = {
  slug: "repocket", name: "Repocket", rule: "cash-out", kind: "cash-out", action: "withdraw",
  title: "Cash out your Repocket balance", loss: "Money left at a provider can be lost.",
  occurrence: "2026-11-20", stage: "ready", due: "", daysLeft: null, censored: false,
  how: "", url: "https://repocket.com/dashboard/wallet", source: "", checked: "", canDone: true, canSnooze: true,
  balance: 12.5, currency: "USD", balanceUsd: 12.5,
};

// A due reminder: who, what, by when, what is lost, and Done.
let html = renderReminders({ joined: false, serverUrl: "", unknown: false, items: [grass], muted: [] });
check("the panel has its title and count", html.includes("Money waiting") && html.includes('<span class="count">1</span>'), html);
check("the row names the provider", html.includes('<span class="attn-what">Grass</span>'), html);
check("the row says what to do", html.includes("Claim your Grass Stage 2 USDC"), html);
check("the row says by when", html.includes("by 22 Jan 2027, 52 days left"), html);
check("the row says what is lost", html.includes("Grass keeps every Stage 2 allocation"), html);
check("the row has Done for its occurrence",
  html.includes('data-reminder-action="done" data-slug="grass" data-rule="claim-stage2-usdc" data-occurrence="2027-01-22">Done</button>'), html);
check("the row opens the provider's page", html.includes('data-url="https://app.grass.io/dashboard">Open Grass</button>'), html);
check("the row offers snooze and Not for me",
  html.includes('data-days="1">Snooze a day') && html.includes('data-days="7">Snooze a week') && html.includes(">Not for me<"), html);
check("an upcoming reminder is not drawn as urgent", html.includes("attn-row attn-ask"), html);

// Overdue, censored, no snooze.
html = renderReminders({ joined: false, serverUrl: "", unknown: false, items: [honeygain], muted: [] });
check("an overdue row says so", html.includes(">Overdue<") && html.includes("attn-fail") && html.includes("may already have written this off"), html);
check("a censored due date says or earlier", html.includes("by 28 Oct 2026 or earlier, 3 days ago"), html);
check("an overdue row offers no snooze", !html.includes("Snooze"), html);

// Cash-out: the balance, and Requested in place of Done.
html = renderReminders({ joined: false, serverUrl: "", unknown: false, items: [cash], muted: [] });
check("a cash-out row shows the balance", html.includes("Cash out your Repocket balance: 12.50 USD"), html);
check("a cash-out row says Requested", html.includes(">Requested</button>"), html);
check("a cash-out row has no due date", !html.includes("by "), html);

// Catalog text is escaped.
html = renderReminders({ joined: false, serverUrl: "", unknown: false, items: [{ ...grass, title: "<img src=x onerror=alert(1)>" }], muted: [] });
check("catalog text is escaped", !html.includes("<img") && html.includes("&lt;img"), html);

// Nothing to do: hidden. A failed check: never an all-clear.
check("nothing to do hides the panel", renderReminders({ joined: false, serverUrl: "", unknown: false, items: [], muted: [] }) === "");
check("no state hides the panel", renderReminders(null) === "");
html = renderReminders({ joined: false, serverUrl: "", unknown: true, items: [], muted: [] });
check("a failed check says so", html.includes("Could not check"), html);

// Muted reminders are listed with Unmute.
html = renderReminders({ joined: false, serverUrl: "", unknown: false, items: [], muted: [{ slug: "grass", rule: "*", name: "Grass", title: "Every reminder" }] });
check("muted reminders show with Unmute", html.includes("Not for me: Grass, Every reminder") && html.includes('data-reminder-action="unmute" data-slug="grass" data-rule="*"'), html);

// Joined: one line, no reminders.
html = renderReminders({ joined: true, serverUrl: "https://hub.example", unknown: false, items: [grass], muted: [] });
check("joined says the server keeps the reminders", html.includes("your CashPilot server keeps the payout reminders"), html);
check("joined shows no reminder", !html.includes("Claim your Grass") && !html.includes("data-reminder-action"), html);
check("joined adds nothing to Needs attention", reminderAttention({ joined: true, serverUrl: "", unknown: false, items: [honeygain], muted: [] }).length === 0);

// Needs attention: one row while something is overdue or due within 7 days.
check("an upcoming reminder stays out of Needs attention", reminderAttention({ joined: false, serverUrl: "", unknown: false, items: [grass], muted: [] }).length === 0);
const soon = { ...grass, daysLeft: 5, stage: "d7" };
let rows = reminderAttention({ joined: false, serverUrl: "", unknown: false, items: [soon], muted: [] });
check("a reminder due within 7 days reaches Needs attention", rows.length === 1 && rows[0].level === "warn" && rows[0].tag === "Deadline", JSON.stringify(rows));
const block = renderAttention(rows, 3, 3);
check("Needs attention then does not say nothing needs you", !block.includes("Nothing needs you") && block.includes("See Money waiting below"), block);
rows = withReminders([{ level: "warn", tag: "Outdated", what: "Bitping", why: "", actions: "" }], { joined: false, serverUrl: "", unknown: false, items: [honeygain, soon], muted: [] });
check("an overdue reminder sorts first as a failure", rows[0].level === "fail" && rows[0].tag === "Overdue" && rows[0].what === "2 reminders", JSON.stringify(rows));

check("due today reads today", reminderWhen({ ...grass, daysLeft: 0 }) === "by 22 Jan 2027, today");
check("one day reads singular", reminderWhen({ ...grass, daysLeft: 1 }) === "by 22 Jan 2027, 1 day left");

if (failures) {
  console.error(`\n${failures} of ${checks} reminder checks failed`);
  process.exit(1);
}
console.log(`reminders: ${checks} checks passed`);
