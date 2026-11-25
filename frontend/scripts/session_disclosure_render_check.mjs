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
//   4. THE GATE FAILS CLOSED. Every manual-only app is asked unless its catalog list
//      is non-empty and names only phones or hardware. A browser extension, an empty
//      list and a missing list are asked, because the lists are not reliable. A
//      container service and a custom service are not asked.
//   5. THE ISOLATION CARD lists every consent with its date and version, marks one to
//      an older text, keeps a withdraw button for a service the catalog dropped, and
//      says so when there is none.
//   6. CATALOG TEXT IS ESCAPED: a name and a slug land in an innerHTML sink.
//   7. THE TEXT CANNOT CHANGE WITHOUT A NEW VERSION. The visible text of each variant
//      is hashed and compared with the hash recorded for its version below.
//   8. THE SECURE VM VARIANT HAS ITS OWN VERSION. A consent given before the Secure VM
//      existed does not cover its extra risk; a consent to it covers the plain text.
//   9. THE WIRING GATES THE ACCEPT (src/render/session_wire.ts), run against the real
//      markup in a small element tree: the button waits for the tick, a click with no
//      tick does nothing, and accept sends the version of the variant shown.
//
//   node scripts/session_disclosure_render_check.mjs      # against ./.harness-build

import { createHash } from "node:crypto";
import {
  SESSION_DISCLOSURE_VERSION,
  consentDay,
  consentIsCurrent,
  needsSessionConsent,
  renderSessionConsentCard,
  renderSessionDisclosure,
  renderSessionSlot,
  sessionDisclosureVersion,
} from "../.harness-build/render/session.js";
import { wireSessionConsent } from "../.harness-build/render/session_wire.js";

// The SHA-256 of the visible disclosure text, per version (RULE 7). Add a line when
// you bump SESSION_DISCLOSURE_VERSION; never change the hash of a version that
// shipped, because users accepted exactly that text.
const TEXT_SHA256 = {
  "2026-11-24": "f4661263a583656da0b79b472478769db19bf187bff8534108dc35da60bf8c12",
  "2026-11-24+vm": "8fdeabedbab2a4f63b0efe51de97d5c8282fddccca9c96e509fa0ad94a064e86",
};

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
check("RULE 4: an Android-only app is not", !needsSessionConsent(service({ platforms: ["android"] })));
check("RULE 4: a hardware device the catalog marks as hardware is not", !needsSessionConsent(service({ platforms: ["hardware"] })));
check("RULE 4: the platform names are read in any case", !needsSessionConsent(service({ platforms: ["Android", "iOS"] })));
// The real catalog: Teneo and UpRock ship a Windows app but list only these.
check("RULE 4: a browser extension is asked (Teneo's list)", needsSessionConsent(service({ slug: "teneo", platforms: ["browser-extension"] })));
check("RULE 4: a browser extension with a phone app is asked (UpRock's list)", needsSessionConsent(service({ slug: "uprock", platforms: ["browser-extension", "android"] })));
check("RULE 4: a phone app with any other platform is asked", needsSessionConsent(service({ platforms: ["android", "windows"] })));
check("RULE 4: an empty platform list is asked", needsSessionConsent(service({ platforms: [] })));
check("RULE 4: a missing platform list is asked", needsSessionConsent(service({ platforms: null })) && needsSessionConsent(service({ platforms: undefined })));
check("RULE 4: an unknown platform is asked", needsSessionConsent(service({ platforms: ["toaster"] })));
check("RULE 4: a container service is not", !needsSessionConsent(service({ manualOnly: false })));
check("RULE 4: a container service with no platforms is not", !needsSessionConsent(service({ manualOnly: false, platforms: [] })));
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

// ---------------------------------------------------------------------------
// RULE 7: the text cannot change without a new version.
// ---------------------------------------------------------------------------
// Only the words count, so a change of markup or class does not ask everyone again.
const visibleText = (html) => html.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim();
const sha256 = (text) => createHash("sha256").update(text).digest("hex");
for (const secureVM of [false, true]) {
  const version = sessionDisclosureVersion(secureVM);
  const hash = sha256(visibleText(renderSessionDisclosure(wipter, secureVM)));
  check(
    `RULE 7: the ${secureVM ? "Secure VM" : "plain"} text matches the hash recorded for version ${version}`,
    TEXT_SHA256[version] === hash,
    TEXT_SHA256[version]
      ? `The disclosure text changed, but SESSION_DISCLOSURE_VERSION did not. Bump it in src/render/session.ts, then add "${version.replace(SESSION_DISCLOSURE_VERSION, "<new version>")}": "${hash}" to TEXT_SHA256 here.`
      : `No hash is recorded for version ${version}. Add "${version}": "${hash}" to TEXT_SHA256 here.`,
  );
}
check("CONTROL: the hash sees a one-word change", sha256(visibleText(renderSessionDisclosure(wipter).replace("hostile", "unfriendly"))) !== sha256(visibleText(renderSessionDisclosure(wipter))));
check("CONTROL: the hash ignores markup", sha256(visibleText(renderSessionDisclosure(wipter).replace('class="session-risk"', 'class="other"'))) === sha256(visibleText(renderSessionDisclosure(wipter))));
const recorded = Object.values(TEXT_SHA256);
check("RULE 7: no two versions record the same text", new Set(recorded).size === recorded.length, JSON.stringify(TEXT_SHA256));

// ---------------------------------------------------------------------------
// RULE 8: the Secure VM variant has its own version.
// ---------------------------------------------------------------------------
const vmVersion = sessionDisclosureVersion(true);
const atVM = { textVersion: vmVersion, acceptedAt: current.acceptedAt };
check("RULE 8: the plain version is the text version", sessionDisclosureVersion(false) === SESSION_DISCLOSURE_VERSION && sessionDisclosureVersion() === SESSION_DISCLOSURE_VERSION);
check("RULE 8: the Secure VM version differs from the plain one", vmVersion !== SESSION_DISCLOSURE_VERSION && vmVersion.startsWith(SESSION_DISCLOSURE_VERSION), vmVersion);
check("RULE 8: a plain consent does not cover the Secure VM text", !consentIsCurrent(current, true));
check("RULE 8: so the slot asks again once a Secure VM exists", renderSessionSlot(wipter, current, true).includes('data-session-disclosure="wipter"') && !renderSessionSlot(wipter, current, true).includes("Install this provider's native app"));
check("RULE 8: a Secure VM consent covers the Secure VM text", consentIsCurrent(atVM, true) && !renderSessionSlot(wipter, atVM, true).includes("data-session-disclosure"));
check("RULE 8: a Secure VM consent covers the plain text too", consentIsCurrent(atVM, false) && consentIsCurrent(atVM));
check("RULE 8: a Secure VM consent to an older text does not count", !consentIsCurrent({ textVersion: "2020-01-01+vm", acceptedAt: current.acceptedAt }, true));
check("RULE 8: the Isolation card marks a plain consent once a Secure VM exists", renderSessionConsentCard({ wipter: current }, catalog, true).includes("text changed"));
check("RULE 8: and not before", !renderSessionConsentCard({ wipter: current }, catalog).includes("text changed"));
check("RULE 8: the plain accept button carries the plain version", acceptOf(text)?.[0].includes(`data-session-version="${SESSION_DISCLOSURE_VERSION}"`), acceptOf(text)?.[0]);
check("RULE 8: the Secure VM accept button carries its version", acceptOf(withVM)?.[0].includes(`data-session-version="${vmVersion}"`), acceptOf(withVM)?.[0]);

// ---------------------------------------------------------------------------
// RULE 9: the wiring gates the accept, on the real markup.
// ---------------------------------------------------------------------------
// A small element tree built from the rendered HTML: enough of the DOM for
// session_wire.ts (attribute selectors, closest, dataset, listeners).
const VOID = new Set(["input", "br", "img", "hr", "meta", "link"]);
function parse(html) {
  const root = element("root", {});
  let at = root;
  for (const [, close, tag, rest] of html.matchAll(/<(\/?)([a-zA-Z][\w-]*)([^>]*)>/g)) {
    if (close) {
      at = at.parent || root;
      continue;
    }
    const attrs = {};
    for (const [, name, value] of rest.matchAll(/([\w-]+)(?:="([^"]*)")?/g)) attrs[name] = value ?? "";
    const child = element(tag.toLowerCase(), attrs);
    child.parent = at;
    at.children.push(child);
    if (!VOID.has(child.tag)) at = child;
  }
  return root;
}
function element(tag, attrs) {
  const dataset = {};
  for (const [name, value] of Object.entries(attrs)) {
    if (name.startsWith("data-")) dataset[name.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = value;
  }
  const el = {
    tag, attrs, dataset, children: [], parent: null, listeners: {},
    checked: "checked" in attrs, disabled: "disabled" in attrs,
    matches(selector) {
      const m = selector.match(/^\[([\w-]+)(?:="([^"]*)")?\]$/);
      if (!m) throw new Error(`the test DOM does not support the selector ${selector}`);
      return m[1] in attrs && (m[2] === undefined || attrs[m[1]] === m[2]);
    },
    querySelectorAll(selector) {
      const out = [];
      const walk = (node) => node.children.forEach((c) => { if (c.matches(selector)) out.push(c); walk(c); });
      walk(el);
      return out;
    },
    querySelector(selector) { return el.querySelectorAll(selector)[0] || null; },
    closest(selector) {
      for (let node = el; node && node.tag !== "root"; node = node.parent) if (node.matches(selector)) return node;
      return null;
    },
    addEventListener(type, fn) { (el.listeners[type] ||= []).push(fn); },
    // Unlike a browser, this fires on a disabled button too: that is the case the
    // second check in the click handler is for.
    fire(type) { (el.listeners[type] || []).forEach((fn) => fn()); },
  };
  return el;
}
function wired(html) {
  const calls = { accept: [], revoke: [] };
  const dom = parse(html);
  wireSessionConsent(dom, { accept: (slug, version) => calls.accept.push([slug, version]), revoke: (slug) => calls.revoke.push(slug) });
  return { dom, calls };
}

{
  const { dom, calls } = wired(renderSessionSlot(wipter, undefined));
  const tickEl = dom.querySelector('[data-session-tick="wipter"]');
  const acceptEl = dom.querySelector('[data-session-accept="wipter"]');
  check("CONTROL: the test DOM finds the tickbox and the accept button", !!tickEl && !!acceptEl);
  check("RULE 9: the accept button starts disabled", acceptEl?.disabled === true);
  tickEl.checked = true;
  tickEl.fire("change");
  check("RULE 9: the tick enables the accept button", acceptEl.disabled === false);
  tickEl.checked = false;
  tickEl.fire("change");
  check("RULE 9: unticking disables it again", acceptEl.disabled === true);
  acceptEl.fire("click");
  check("RULE 9: a click with no tick records nothing", calls.accept.length === 0, JSON.stringify(calls));
  tickEl.checked = true;
  tickEl.fire("change");
  acceptEl.fire("click");
  check("RULE 9: a click after the tick records the consent for this app at the plain version", JSON.stringify(calls.accept) === JSON.stringify([["wipter", SESSION_DISCLOSURE_VERSION]]), JSON.stringify(calls));
  check("RULE 9: the disclosure offers nothing to withdraw", calls.revoke.length === 0);
}
{
  const { dom, calls } = wired(renderSessionSlot(wipter, current, true));
  const tickEl = dom.querySelector('[data-session-tick="wipter"]');
  tickEl.checked = true;
  tickEl.fire("change");
  dom.querySelector('[data-session-accept="wipter"]').fire("click");
  check("RULE 9: with a Secure VM the accept records the Secure VM version", JSON.stringify(calls.accept) === JSON.stringify([["wipter", vmVersion]]), JSON.stringify(calls));
}
{
  // Two cards on one page: the wizard lists every selected service.
  const { dom, calls } = wired(renderSessionSlot(wipter, undefined) + renderSessionSlot(service({ name: "PassiveApp", slug: "passiveapp" }), undefined));
  const wipterTick = dom.querySelector('[data-session-tick="wipter"]');
  wipterTick.checked = true;
  wipterTick.fire("change");
  const otherAccept = dom.querySelector('[data-session-accept="passiveapp"]');
  check("RULE 9: ticking one app does not enable another app's button", otherAccept.disabled === true && dom.querySelector('[data-session-accept="wipter"]').disabled === false);
  otherAccept.fire("click");
  check("RULE 9: one app's tick does not accept another app", calls.accept.length === 0, JSON.stringify(calls));
}
{
  const { dom, calls } = wired(renderSessionSlot(wipter, current));
  dom.querySelector('[data-session-revoke="wipter"]').fire("click");
  check("RULE 9: Withdraw on the card withdraws that app", JSON.stringify(calls.revoke) === JSON.stringify(["wipter"]), JSON.stringify(calls));
  const settings = wired(renderSessionConsentCard({ wipter: current, gone: older }, catalog));
  settings.dom.querySelector('[data-session-revoke="gone"]').fire("click");
  check("RULE 9: Withdraw in Settings withdraws that app, one the catalog dropped included", JSON.stringify(settings.calls.revoke) === JSON.stringify(["gone"]), JSON.stringify(settings.calls));
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} session disclosure render checks FAILED`);
  process.exit(1);
}
console.log(`session disclosure render checks: ${checks} passed`);
