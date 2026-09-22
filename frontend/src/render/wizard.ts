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
// Pure: takes the service plus the two pieces of app state the card reads (the
// backend's collector-field map and this machine's hostname) and returns HTML.

import type { CollectorField, Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { serviceFormFields } from "./fields.js";

/**
 * Render the setup card for one service.
 *
 * `hostname` is this machine's name, substituted into a `{hostname}` catalog default
 * so the field shows the value the deploy path will actually send. It falls back to
 * "desktop", which is what the backend substitutes when it does not know either.
 */
export function renderWizardServiceSetup(
  service: Service,
  collectorFields: Record<string, CollectorField[]> | null | undefined,
  hostname: string | null | undefined,
): string {
  const signupUrl = service.referral?.signupUrl || service.website;
  const dashboardUrl = service.cashout?.dashboardUrl || service.website;
  const fields = serviceFormFields(service, collectorFields);
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
        ${signupUrl ? `<button class="primary" data-url="${escapeHtml(signupUrl)}">Create account</button>` : ""}
        ${dashboardUrl ? `<button class="secondary" data-url="${escapeHtml(dashboardUrl)}">Provider dashboard</button>` : ""}
        <button class="secondary" data-url="https://geiserx.github.io/CashPilot/guides/${escapeHtml(service.slug)}/">Setup guide</button>
      </div>
      ${service.manualOnly ? `<p class="tip">Install this provider's native app, then save collector credentials here so CashPilot can track earnings.</p>` : ""}
      <div class="credential-grid">
        ${fields.map((item) => `
          <label>
            <span>${escapeHtml(item.label)}${item.required ? " *" : ""}</span>
            <input data-wizard-env="${item.key}" type="${item.secret ? "password" : "text"}" placeholder="${escapeHtml(item.description)}" value="${escapeHtml((item.default || "").replaceAll("{hostname}", hostname || "desktop"))}" />
          </label>
        `).join("") || `<p class="muted">No credentials are required by the catalog for this service.</p>`}
      </div>
      <div data-preflight-slug="${escapeHtml(service.slug)}"></div>
      <div class="actions left">
        <button class="secondary" data-wizard-action="save" data-slug="${escapeHtml(service.slug)}">Save Credentials</button>
        <button class="primary" data-wizard-action="deploy" data-slug="${escapeHtml(service.slug)}" ${service.manualOnly ? "disabled" : ""}>Deploy</button>
        <button class="secondary" data-wizard-action="collect" data-slug="${escapeHtml(service.slug)}">Collect Earnings</button>
      </div>
      <pre class="output wizard-output" data-output-slug="${escapeHtml(service.slug)}"></pre>
    </article>
  `;
}
