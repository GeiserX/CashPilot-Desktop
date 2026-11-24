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

/** The version of the text below. Bump it with every change to the text. */
export const SESSION_DISCLOSURE_VERSION = "2026-11-24";

// The platforms whose app runs in the user's desktop session. A phone app, a browser
// extension or a hardware hotspot is not installed on this computer, and this text
// does not describe it.
const DESKTOP_PLATFORMS = ["windows", "macos", "linux"];

/** Whether the service's card must ask for consent before its install steps. */
export function needsSessionConsent(service: Service): boolean {
  return !!service.manualOnly && !service.custom && (service.platforms || []).some((p) => DESKTOP_PLATFORMS.includes(p));
}

/** Whether a stored consent covers the text the app shows now. */
export function consentIsCurrent(consent: SessionConsent | null | undefined): boolean {
  return !!consent && consent.textVersion === SESSION_DISCLOSURE_VERSION;
}

/** The day a consent was given, as YYYY-MM-DD (UTC), or "an unknown date". */
export function consentDay(consent: SessionConsent): string {
  const at = Date.parse(consent.acceptedAt);
  return Number.isNaN(at) ? "an unknown date" : new Date(at).toISOString().slice(0, 10);
}

/**
 * The disclosure with its tickbox and accept button.
 *
 * `secureVM` is true only when a Secure VM exists on this computer. The Hyper-V
 * paragraph applies only then: setting the VM up gave this Windows account full
 * control of Hyper-V, so an app in this session can change the VM's fence. There is
 * no Secure VM yet, so every caller passes false today.
 */
export function renderSessionDisclosure(service: Service, secureVM = false): string {
  const name = escapeHtml(service.name);
  const slug = escapeHtml(service.slug);
  return `
    <div class="session-risk" data-session-disclosure="${slug}">
      <h4>Run ${name} in your own session on this computer?</h4>
      <p>Earning apps sell your internet connection to other people. Their code is closed, and most of them ask for admin rights. They also update themselves. In your own session, nothing stands between these apps and your computer. If an app is hostile, or a bad update reaches it, the app can:</p>
      <ul>
        <li>reach every device on your home network, such as your router's settings page, a NAS, cameras, printers and other computers.</li>
        <li>reach your Tailscale, VPN or work network.</li>
        <li>read your files, your browsers and the passwords that your browsers save.</li>
        <li>read the logins that CashPilot keeps for your earning accounts, and connect to CashPilot itself.</li>
        <li>install other software, because most of these apps run as administrator.</li>
      </ul>
      ${secureVM ? `<p data-session-vm-line>The app can also change the protection of your Secure VM. When you set up the Secure VM, your Windows account got full control of Hyper-V. Apps in this session run as you. CashPilot detects the change and disconnects the VM within one minute.</p>` : ""}
      <p>Do not turn off antivirus scanning for the folder of an app. Some apps ask you to do this.</p>
      <p>Wherever an earning app runs, other people's traffic leaves through your internet address. Your internet provider may forbid this. If a buyer uses your connection for abuse, the abuse is traced to your address.</p>
      <div class="session-accept">
        <label class="session-tick"><input type="checkbox" data-session-tick="${slug}" /> I understand these risks for ${name}.</label>
        <button class="primary compact-btn" data-session-accept="${slug}" disabled>Run in my session</button>
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
  const name = escapeHtml(service.name);
  const slug = escapeHtml(service.slug);
  if (!consentIsCurrent(consent)) {
    const stale = consent
      ? `<p class="tip" data-session-stale>You accepted an older version of this text on ${escapeHtml(consentDay(consent))}. The text changed, so read it again before you install ${name}.</p>`
      : "";
    return `${stale}${renderSessionDisclosure(service, secureVM)}`;
  }
  return `
    <p class="tip" data-session-install="${slug}">Install this provider's native app, then save collector credentials here so CashPilot can track earnings.</p>
    <div class="session-accepted">
      <button class="secondary compact-btn" data-url="https://geiserx.github.io/CashPilot/guides/${slug}/">Setup guide</button>
      <span class="muted">You accepted the risks of running ${name} in your own session on ${escapeHtml(consentDay(consent!))}.</span>
      <button class="danger compact-btn" data-session-revoke="${slug}">Withdraw</button>
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
): string {
  const entries = Object.entries(consents || {}).sort(([a], [b]) => a.localeCompare(b));
  const nameOf = (slug: string) => (services || []).find((svc) => svc.slug === slug)?.name || slug;
  const rows = entries.map(([slug, consent]) => `
    <div class="row session-consent-row" data-session-consent="${escapeHtml(slug)}">
      <div>
        <strong>${escapeHtml(nameOf(slug))}</strong>
        <p class="muted compact-copy">Accepted on ${escapeHtml(consentDay(consent))}, text version ${escapeHtml(consent.textVersion)}.${consentIsCurrent(consent) ? "" : " The text changed since then, so the setup card asks again."}</p>
      </div>
      <button class="danger compact-btn" data-session-revoke="${escapeHtml(slug)}">Withdraw</button>
    </div>
  `).join("");
  return `
    <section class="card" data-session-consent-card>
      <div class="card-header">
        <div>
          <span class="card-title">Isolation</span>
          <p class="muted compact-copy">Desktop apps that you chose to run in your own session. CashPilot does not fence these apps. If you withdraw your consent, the setup card hides the install steps and asks again. Withdrawing does not uninstall the app.</p>
        </div>
      </div>
      ${rows || `<p class="muted" data-session-consent-empty>You did not accept the risks for any desktop app.</p>`}
    </section>
  `;
}
