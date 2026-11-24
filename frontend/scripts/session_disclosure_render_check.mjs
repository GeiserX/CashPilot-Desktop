#!/usr/bin/env node
// Browser-free checks on the desktop-app risk disclosure (src/render/session.ts).
//
// A manual-only provider's app runs in the user's own session, next to their files,
// their home network and the logins CashPilot keeps. These checks hold that:
//
//   1. THE TEXT APPEARS, with every risk it lists, for the app it names.
//   2. THE TICKBOX GATES THE INSTALL STEPS. No consent, or a consent to an older text,
//      shows the disclosure and no install tip and no setup guide. A consent to the
//      current text shows the install tip, the guide and a way to withdraw.
//   3. THE HYPER-V LINE APPEARS ONLY WHEN A SECURE VM EXISTS.
//   4. ONLY A DESKTOP APP IS ASKED. A phone app, a browser extension, a hardware
//      hotspot, a container service and a custom service are not.
//   5. THE ISOLATION CARD lists every consent with its date and version, marks one to
//      an older text, keeps a withdraw button for a service the catalog dropped, and
//      says so when there is none.
//   6. CATALOG TEXT IS ESCAPED: a name and a slug land in an innerHTML sink.
//
//   node scripts/session_disclosure_render_check.mjs      # against ./.harness-build

import {
  SESSION_DISCLOSURE_VERSION,
  consentDay,
  consentIsCurrent,
  needsSessionConsent,
  renderSessionConsentCard,
  renderSessionDisclosure,
  renderSessionSlot,
} from "../.harness-build/render/session.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const service = (over = {}) => ({ name: "Wipter", slug: "wipter", manualOnly: true, platforms: ["windows", "macos", "linux", "android"], ...over });
const wipter = service();
const current = { textVersion: SESSION_DISCLOSURE_VERSION, acceptedAt: "2026-11-24T18:30:00Z" };
const older = { textVersion: "2020-01-01", acceptedAt: "2026-01-02T09:00:00Z" };
const tickOf = (html) => html.match(/<input\b[^>]*data-session-tick="([^"]*)"[^>]*>/);
const acceptOf = (html) => html.match(/<button\b[^>]*data-session-accept="([^"]*)"[^>]*>/);

// ---------------------------------------------------------------------------
// RULE 1: the text appears, for this app, with every risk.
// ---------------------------------------------------------------------------
const text = renderSessionDisclosure(wipter);
check("CONTROL: the disclosure renders", text.includes('data-session-disclosure="wipter"'), text);
check("RULE 1: the question names the app", text.includes("Run Wipter in your own session on this computer?"), text);
for (const [risk, phrase] of [
  ["the home network", "reach every device on your home network"],
  ["a tailnet, VPN or work network", "reach your Tailscale, VPN or work network"],
  ["files, browsers and saved passwords", "read your files, your browsers and the passwords"],
  ["CashPilot's own logins", "read the logins that CashPilot keeps"],
  ["admin installs", "install other software, because most of these apps run as administrator"],
  ["the antivirus request", "Do not turn off antivirus scanning"],
  ["the ISP and abuse risk", "other people's traffic leaves through your internet address"],
]) {
  check(`RULE 1: the text names ${risk}`, text.includes(phrase), text);
}
check("RULE 1: the risks are a list, one item each", (text.match(/<li>/g) || []).length === 5, text);

// ---------------------------------------------------------------------------
// RULE 2: the tickbox gates the install steps.
// ---------------------------------------------------------------------------
const tick = tickOf(text);
check("RULE 2: there is a tickbox for this app", tick?.[1] === "wipter", text);
check("RULE 2: it is a checkbox, never pre-ticked", /type="checkbox"/.test(tick?.[0] || "") && !/\bchecked\b/.test(tick?.[0] || ""), tick?.[0]);
check("RULE 2: the tickbox says what is accepted, for this app", text.includes("I understand these risks for Wipter."), text);
const accept = acceptOf(text);
check("RULE 2: the accept button is for this app", accept?.[1] === "wipter", text);
check("RULE 2: the accept button starts disabled", /\bdisabled\b/.test(accept?.[0] || ""), accept?.[0]);

const none = renderSessionSlot(wipter, undefined);
check("RULE 2: with no consent the slot shows the disclosure", none.includes('data-session-disclosure="wipter"'), none);
check("RULE 2: with no consent there is no install tip", !none.includes("Install this provider's native app"), none);
check("RULE 2: with no consent there is no setup guide", !none.includes("/guides/"), none);
check("RULE 2: with no consent there is nothing to withdraw", !none.includes("data-session-revoke"), none);

const given = renderSessionSlot(wipter, current);
check("RULE 2: with consent the install tip shows", given.includes('data-session-install="wipter"') && given.includes("Install this provider's native app"), given);
check("RULE 2: with consent the setup guide shows", given.includes('data-url="https://geiserx.github.io/CashPilot/guides/wipter/"'), given);
check("RULE 2: with consent the disclosure is gone", !given.includes("data-session-disclosure"), given);
check("RULE 2: with consent it says when it was given", given.includes("on 2026-11-24"), given);
check("RULE 2: with consent there is a way to withdraw it", given.includes('data-session-revoke="wipter"'), given);

const stale = renderSessionSlot(wipter, older);
check("RULE 2: a consent to an older text is not current", !consentIsCurrent(older) && consentIsCurrent(current));
check("RULE 2: a consent to an older text shows the disclosure again", stale.includes('data-session-disclosure="wipter"'), stale);
check("RULE 2: and hides the install tip", !stale.includes("Install this provider's native app"), stale);
check("RULE 2: and says the text changed since that date", stale.includes("data-session-stale") && stale.includes("2026-01-02"), stale);
check("RULE 2: a consent with a missing version is not current", !consentIsCurrent({ acceptedAt: current.acceptedAt }));
check("RULE 2: no consent at all is not current", !consentIsCurrent(null) && !consentIsCurrent(undefined));

// ---------------------------------------------------------------------------
// RULE 3: the Hyper-V line only with a Secure VM.
// ---------------------------------------------------------------------------
check("RULE 3: without a Secure VM there is no Hyper-V line", !text.includes("data-session-vm-line") && !text.includes("Hyper-V"), text);
const withVM = renderSessionDisclosure(wipter, true);
check("RULE 3: with a Secure VM the Hyper-V line appears", withVM.includes("data-session-vm-line") && withVM.includes("full control of Hyper-V"), withVM);
check("RULE 3: the slot passes the Secure VM on", renderSessionSlot(wipter, undefined, true).includes("data-session-vm-line"));
check("RULE 3: and the slot leaves it out by default", !renderSessionSlot(wipter, undefined).includes("data-session-vm-line"));

// ---------------------------------------------------------------------------
// RULE 4: only a desktop app is asked.
// ---------------------------------------------------------------------------
check("RULE 4: a manual-only desktop app is asked", needsSessionConsent(wipter));
check("RULE 4: a Windows-only app is asked", needsSessionConsent(service({ platforms: ["windows"] })));
check("RULE 4: a Linux-only app is asked", needsSessionConsent(service({ platforms: ["linux"] })));
check("RULE 4: a phone-only app is not", !needsSessionConsent(service({ platforms: ["ios", "android"] })));
check("RULE 4: a browser extension is not", !needsSessionConsent(service({ platforms: ["browser-extension"] })));
check("RULE 4: a hardware hotspot (no platforms) is not", !needsSessionConsent(service({ platforms: [] })));
check("RULE 4: a missing platform list is not", !needsSessionConsent(service({ platforms: null })));
check("RULE 4: a container service is not", !needsSessionConsent(service({ manualOnly: false })));
check("RULE 4: a custom service is not", !needsSessionConsent(service({ custom: true })));

// ---------------------------------------------------------------------------
// RULE 5: the Isolation card.
// ---------------------------------------------------------------------------
const catalog = [wipter, service({ name: "PassiveApp", slug: "passiveapp", platforms: ["windows"] })];
const empty = renderSessionConsentCard(undefined, catalog);
check("CONTROL: the card renders", empty.includes("data-session-consent-card") && empty.includes(">Isolation<"), empty);
check("RULE 5: with no consent the card says so", empty.includes("data-session-consent-empty"), empty);
check("RULE 5: and offers nothing to withdraw", !empty.includes("data-session-revoke"), empty);
check("RULE 5: an empty map reads the same as none", renderSessionConsentCard({}, catalog).includes("data-session-consent-empty"));

const card = renderSessionConsentCard({ wipter: current, passiveapp: older, gone: current }, catalog);
const rowOf = (slug) => (card.split(`data-session-consent="${slug}"`)[1] || "").split("data-session-consent=")[0];
check("RULE 5: every consent gets a row", ["wipter", "passiveapp", "gone"].every((slug) => card.includes(`data-session-consent="${slug}"`)), card);
check("RULE 5: a row names the app by its catalog name", rowOf("wipter").includes("<strong>Wipter</strong>"), rowOf("wipter"));
check("RULE 5: a row shows the date and the version", rowOf("wipter").includes("2026-11-24") && rowOf("wipter").includes(`text version ${SESSION_DISCLOSURE_VERSION}`), rowOf("wipter"));
check("RULE 5: a current consent is not marked as changed", !rowOf("wipter").includes("text changed"), rowOf("wipter"));
check("RULE 5: a consent to an older text is marked", rowOf("passiveapp").includes("text changed"), rowOf("passiveapp"));
check("RULE 5: every row can be withdrawn", ["wipter", "passiveapp", "gone"].every((slug) => card.includes(`data-session-revoke="${slug}"`)), card);
check("RULE 5: a service the catalog dropped shows its slug", rowOf("gone").includes("<strong>gone</strong>"), rowOf("gone"));
check("RULE 5: no empty-state line next to rows", !card.includes("data-session-consent-empty"), card);
check("RULE 5: an unreadable date says so, never Invalid Date", consentDay({ textVersion: "x", acceptedAt: "nope" }) === "an unknown date");

// ---------------------------------------------------------------------------
// RULE 6: catalog text is escaped.
// ---------------------------------------------------------------------------
const hostile = service({ name: "<img src=x onerror=alert(1)>", slug: 'a" onfocus="alert(1)' });
for (const [what, html] of [
  ["disclosure", renderSessionDisclosure(hostile)],
  ["consented slot", renderSessionSlot(hostile, current)],
  ["stale slot", renderSessionSlot(hostile, older)],
  ["card", renderSessionConsentCard({ [hostile.slug]: { textVersion: "<b>v</b>", acceptedAt: current.acceptedAt } }, [hostile])],
]) {
  check(`RULE 6: a hostile name in the ${what} is escaped`, !html.includes("<img"), html);
  check(`RULE 6: a hostile slug in the ${what} cannot leave its attribute`, !html.includes('onfocus="alert'), html);
}
check("RULE 6: a hostile version is escaped", !renderSessionConsentCard({ x: { textVersion: "<b>v</b>", acceptedAt: "" } }, []).includes("<b>"));

if (failures > 0) {
  console.error(`\n${failures} of ${checks} session disclosure render checks FAILED`);
  process.exit(1);
}
console.log(`session disclosure render checks: ${checks} passed`);
