// The risk disclosure for a desktop app the user installs in their own session.
//
// A manual-only provider has no image, so CashPilot cannot fence it: the user
// installs the provider's closed-source app next to their files, their home network
// and the logins CashPilot keeps. Before this, the wizard showed the install tip with
// no warning. Now the card shows this disclosure first, and the install steps only
// after the user ticks the box and accepts it for that one app.
//
// The consent is stored per app with the text version and the date
// (session_consent_binding.go). CHANGE THE TEXT, CHANGE THE VERSION: a consent at an
// older version no longer counts, so everyone who accepted is asked again.
//
// Pure, like the rest of src/render: state in, HTML out.

import type { SessionConsent, Service } from "../wails";
import { escapeHtml } from "./format.js";
import { ht, t } from "./i18n.js";

/**
 * The version of the text below. Bump it with every change to the text.
 * scripts/session_disclosure_render_check.mjs holds a hash of the text per version
 * and fails when the text changes and this version does not.
 */
export const SESSION_DISCLOSURE_VERSION = "2026-11-24";

/**
 * The version stored with a consent to the text shown. The Secure VM variant adds a
 * risk, so it has its own version: a consent given before the Secure VM existed does
 * not cover it, and the card asks again once the VM is set up.
 */
export function sessionDisclosureVersion(secureVM = false): string {
  return secureVM ? `${SESSION_DISCLOSURE_VERSION}+vm` : SESSION_DISCLOSURE_VERSION;
}

// The platforms whose app never runs on this computer. The gate fails closed: a
// manual-only app is asked unless its catalog list is non-empty and holds only these.
// The lists are not reliable. Teneo and UpRock list a browser extension and ship a
// Windows app, and a missing list says nothing about where the app runs.
const OFF_COMPUTER_PLATFORMS = ["android", "ios", "hardware"];

/** Whether the service's card must ask for consent before its install steps. */
export function needsSessionConsent(service: Service): boolean {
  if (!service.manualOnly || service.custom || service.collector?.accrual === "pool") return false;
  const platforms = service.platforms || [];
  return platforms.length === 0 || !platforms.every((p) => OFF_COMPUTER_PLATFORMS.includes(String(p).trim().toLowerCase()));
}

/**
 * Whether a stored consent covers the text the app shows now. A consent to the
 * Secure VM variant also covers the plain text, which is the same text less one risk.
 */
export function consentIsCurrent(consent: SessionConsent | null | undefined, secureVM = false): boolean {
  if (!consent) return false;
  return consent.textVersion === sessionDisclosureVersion(true) || (!secureVM && consent.textVersion === sessionDisclosureVersion(false));
}

/** The day a consent was given, as YYYY-MM-DD (UTC), or "an unknown date". */
export function consentDay(consent: SessionConsent): string {
  const at = Date.parse(consent.acceptedAt);
  return Number.isNaN(at) ? t("session.unknownDate") : new Date(at).toISOString().slice(0, 10);
}

/**
 * The disclosure with its tickbox and accept button.
 *
 * `secureVM` is true only when a Secure VM exists on this computer. The Hyper-V
 * paragraph applies only then: setting the VM up gave this Windows account full
 * control of Hyper-V, so an app in this session can change the VM's fence. There is
 * no Secure VM yet, so every caller passes false today. The accept button carries
 * the version of the variant shown (sessionDisclosureVersion), and that is the
 * version the consent stores.
 */
export function renderSessionDisclosure(service: Service, secureVM = false): string {
  const name = service.name;
  const slug = escapeHtml(service.slug);
  return `
    <div class="session-risk" data-session-disclosure="${slug}">
      <h4>${ht("session.disclosure.title", { name })}</h4>
      <p>${ht("session.disclosure.intro")}</p>
      <ul>
        <li>${ht("session.disclosure.risk.network")}</li>
        <li>${ht("session.disclosure.risk.vpn")}</li>
        <li>${ht("session.disclosure.risk.files")}</li>
        <li>${ht("session.disclosure.risk.logins")}</li>
        <li>${ht("session.disclosure.risk.software")}</li>
      </ul>
      ${secureVM ? `<p data-session-vm-line>${ht("session.disclosure.secureVM")}</p>` : ""}
      <p>${ht("session.disclosure.antivirus")}</p>
      <p>${ht("session.disclosure.traffic")}</p>
      <div class="session-accept">
        <label class="session-tick"><input type="checkbox" data-session-tick="${slug}" /> ${ht("session.disclosure.understand", { name })}</label>
        <button class="primary compact-btn" data-session-accept="${slug}" data-session-version="${escapeHtml(sessionDisclosureVersion(secureVM))}" disabled>${ht("session.disclosure.accept")}</button>
      </div>
    </div>
  `;
}

/**
 * What the card shows in place of the old install tip, for a service that needs
 * consent: the disclosure until the user accepts the current text, then the install
 * steps, the setup guide and a way to withdraw.
 */
export function renderSessionSlot(service: Service, consent: SessionConsent | null | undefined, secureVM = false): string {
  const name = service.name;
  const slug = escapeHtml(service.slug);
  if (!consentIsCurrent(consent, secureVM)) {
    const stale = consent
      ? `<p class="tip" data-session-stale>${ht("session.slot.stale", { day: consentDay(consent), name })}</p>`
      : "";
    return `${stale}${renderSessionDisclosure(service, secureVM)}`;
  }
  return `
    <p class="tip" data-session-install="${slug}">${ht("session.slot.install")}</p>
    <div class="session-accepted">
      <button class="secondary compact-btn" data-url="https://geiserx.github.io/CashPilot/guides/${slug}/">${ht("session.slot.guide")}</button>
      <span class="muted">${ht("session.slot.accepted", { name, day: consentDay(consent!) })}</span>
      <button class="danger compact-btn" data-session-revoke="${slug}">${ht("session.withdraw")}</button>
    </div>
  `;
}

/**
 * The Isolation card in Settings: every app the user agreed to run in their own
 * session, when, and whether the text changed since, each with a Withdraw button.
 * A consent for a service the catalog no longer lists shows its slug, so it can
 * still be withdrawn.
 */
export function renderSessionConsentCard(
  consents: Record<string, SessionConsent> | null | undefined,
  services: Service[] | null | undefined,
  secureVM = false,
): string {
  const entries = Object.entries(consents || {}).sort(([a], [b]) => a.localeCompare(b));
  const nameOf = (slug: string) => (services || []).find((svc) => svc.slug === slug)?.name || slug;
  const rows = entries.map(([slug, consent]) => `
    <div class="row session-consent-row" data-session-consent="${escapeHtml(slug)}">
      <div>
        <strong>${escapeHtml(nameOf(slug))}</strong>
        <p class="muted compact-copy">${ht("session.card.acceptedOn", { day: consentDay(consent), version: consent.textVersion })}${consentIsCurrent(consent, secureVM) ? "" : ` ${ht("session.card.textChanged")}`}</p>
      </div>
      <button class="danger compact-btn" data-session-revoke="${escapeHtml(slug)}">${ht("session.withdraw")}</button>
    </div>
  `).join("");
  return `
    <section class="card" data-session-consent-card>
      <div class="card-header">
        <div>
          <span class="card-title">${ht("session.card.title")}</span>
          <p class="muted compact-copy">${ht("session.card.help")}</p>
        </div>
      </div>
      ${rows || `<p class="muted" data-session-consent-empty>${ht("session.card.empty")}</p>`}
    </section>
  `;
}
