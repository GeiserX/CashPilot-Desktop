// One service's card in the wizard's "Configure and deploy" step, extracted from
// main.ts so it can be TESTED.
//
// render/fields.ts already decides WHICH credentials a service needs. This file is
// the other half: turning that list into the actual inputs, and that half carries
// its own failure modes, none of which a test on the field list can see.
//
//   - a dropped input means a credential the user can never enter, and the deploy
//     or the collect then fails on a value nobody was asked for;
//   - `type="text"` on a secret writes the password on screen in plain sight;
//   - a missing data-wizard-env attribute makes the input invisible to
//     readWizardForm(), so the value is typed, looks saved, and is silently discarded;
//   - an unexpanded "{hostname}" default ships the literal token to the provider as
//     the device name.
//
// Pure: takes the service plus the pieces of app state the card reads (the
// backend's collector-field map, this machine's hostname and the earnings
// breakdown the facts strip shows a balance from) and returns HTML.

import type { CollectorField, RouteState, ScalingLimit, Service, ServiceEarning, SessionConsent, WorkerTarget } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { fieldMark, serviceFormFields } from "./fields.js";
import { composeExportControl, credentialHint, referralNote, serviceFacts, signupButton } from "./details.js";
import { ratingBlock } from "./rating.js";
import { imageChoiceSection } from "./image.js";
import { instancesSection } from "./instances.js";
import { routeChoiceSection } from "./route.js";
import { whereChoiceSection } from "./where.js";
import { renderDeployPanel, type DeployFeedback } from "./deploy_consent.js";
import { renderCustomServiceCard } from "./custom.js";
import { needsSessionConsent, renderSessionSlot } from "./session.js";
import { walletChoiceSection } from "./wallet.js";
import { minerFee } from "./preflight.js";

/**
 * Render the setup card for one service.
 *
 * `hostname` is this machine's name, substituted into a `{hostname}` catalog default
 * so the field shows the value the deploy path will actually send. It falls back to
 * "desktop", which is what the backend substitutes when it does not know either.
 *
 * `customImage` is the image this service's deployment records instead of the
 * catalog's, which the Advanced section's Image box starts with.
 *
 * `scaling` is how many copies this machine may run (the Instances box; absent, no
 * box) and `instances` how many the deployment runs now.
 *
 * `route` is the Route box's state: the proxies the service can run through from its
 * first start (absent: no box). `routeDraft` is the owner's pick in that box from
 * before this redraw (undefined: none, the recorded route shows).
 *
 * `sessionConsent` is the desktop-app risk disclosures the user accepted, per slug
 * (render/session.ts). A manual-only desktop app shows the disclosure in place of
 * its install tip and setup guide until the user accepts the current text.
 *
 * `deployed` says whether this machine runs the service now. A service that keeps
 * its wallet seed in a file (render/wallet.ts) then defaults to keeping its wallet.
 *
 * `workerTargets` are the Docker workers a lead can deploy to (the Where box;
 * absent or empty, no box) and `whereDraft` the owner's pick from before a redraw.
 */
export function renderWizardServiceSetup(
  service: Service,
  collectorFields: Record<string, CollectorField[]> | null | undefined,
  hostname: string | null | undefined,
  breakdown?: ServiceEarning[] | null,
  customImage?: string | null,
  scaling?: ScalingLimit | null,
  instances?: number | null,
  route?: RouteState | null,
  routeDraft?: number | null,
  sessionConsent?: Record<string, SessionConsent> | null,
  deployed?: boolean,
  workerTargets?: WorkerTarget[] | null,
  whereDraft?: number | null,
  deployFeedback?: DeployFeedback,
): string {
  // A custom service runs its saved definition: no credentials, no Image box.
  if (service.custom) return renderCustomServiceCard(service, scaling, instances, deployFeedback);
  const dashboardUrl = service.cashout?.dashboardUrl || service.website;
  const fields = serviceFormFields(service, collectorFields);
  const earning = breakdown?.find((item) => item.platform === service.slug);
  const balance = earning && !earning.error ? {amount: earning.balance, currency: earning.currency} : null;
  const pool = service.collector?.accrual === "pool";
  const gated = needsSessionConsent(service);
  const consent = sessionConsent?.[service.slug];
  // A gated app's setup guide is its install steps, so it leaves the signup strip and
  // shows inside the session slot once the user accepts (render/session.ts).
  return `
    <article class="wizard-setup-card" data-form-slug="${escapeHtml(service.slug)}">
      <div class="split">
        <div>
          <h3>${escapeHtml(service.name)}</h3>
          <p class="muted">${escapeHtml(service.shortDescription || service.description)}</p>
        </div>
        <span class="pill">${service.manualOnly ? "manual" : "docker"}</span>
      </div>
      <div class="signup-strip">
        ${signupButton(service)}
        ${dashboardUrl ? `<button class="secondary" data-url="${escapeHtml(dashboardUrl)}">Provider dashboard</button>` : ""}
        ${gated ? "" : `<button class="secondary" data-url="https://geiserx.github.io/CashPilot/guides/${escapeHtml(service.slug)}/">${pool ? "Web setup guide" : "Setup guide"}</button>`}
      </div>
      ${referralNote(service)}
      ${ratingBlock(service)}
      ${gated
        ? `<div data-session-slot="${escapeHtml(service.slug)}">${renderSessionSlot(service, consent)}</div>`
        : pool ? `<p class="tip">Check pool balance saves the address and reads the unpaid balance your mining pool holds for this address.</p>`
        : service.manualOnly ? `<p class="tip">Install this provider's native app, then save collector credentials here so CashPilot can track earnings.</p>` : ""}
      ${credentialHint(service)}
      <div class="credential-grid">
        ${fields.map((item) => `
          <label>
            <span>${escapeHtml(item.label)}${fieldMark(item)}</span>
            <input data-wizard-env="${escapeHtml(item.key)}" type="${item.secret ? "password" : "text"}"${item.secret ? ' autocomplete="new-password"' : ""}${item.pattern ? ` pattern="${escapeHtml(item.pattern)}"` : ""} placeholder="${escapeHtml(item.description)}" value="${item.secret ? "" : escapeHtml((item.default || "").replaceAll("{hostname}", hostname || "desktop"))}" />
            ${item.secret ? '<small class="muted">Leave blank to keep a saved value. Enter a new value to replace it.</small>' : ""}
          </label>
        `).join("") || `<p class="muted">No credentials are required by the catalog for this service.</p>`}
      </div>
      ${walletChoiceSection(service, Boolean(deployed))}
      ${instancesSection(service, scaling, instances)}
      ${whereChoiceSection(service, workerTargets, whereDraft)}
      ${routeChoiceSection(service, route, routeDraft)}
      ${imageChoiceSection(service, customImage)}
      ${minerFee(service)}
      <div data-preflight-slug="${escapeHtml(service.slug)}"></div>
      <div class="actions left">
        ${pool ? "" : `<button class="secondary" data-wizard-action="save" data-slug="${escapeHtml(service.slug)}">Save Credentials</button>`}
        <button class="primary" data-wizard-action="deploy" data-slug="${escapeHtml(service.slug)}" ${service.manualOnly ? "disabled" : ""}>Deploy</button>
        <button class="secondary" data-wizard-action="collect" data-slug="${escapeHtml(service.slug)}">${pool ? "Check pool balance" : "Collect Earnings"}</button>
      </div>
      ${renderDeployPanel(service.slug, deployFeedback)}
      ${composeExportControl(service)}
      ${serviceFacts(service, balance)}
      <pre class="output wizard-output" data-output-slug="${escapeHtml(service.slug)}"></pre>
      <div data-wallet-panel="${escapeHtml(service.slug)}"></div>
    </article>
  `;
}
