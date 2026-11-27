// What a service card says about a provider, beyond its name.
//
// The catalog carries four things Desktop was throwing away, and each of them is a
// question the user asks while they are deciding whether to run something:
//
//   collector.credential_hint  WHERE do I find this token? Every provider hides it
//                              somewhere different, and the sentence that says where
//                              was written for exactly this moment.
//   cashout.min_amount         HOW MUCH before I can get paid, and in WHAT unit.
//   payment.methods/frequency  HOW does the money reach me, and how often.
//   disclosure.*               WHAT is this actually selling, and what does it do to
//                              my connection and my account.
//
// Two rules run through the rendering.
//
// An unknown value renders as an em dash, never as 0 and never as a blank. "No
// minimum documented" and "minimum is zero" are different facts, and a 0 in that
// slot tells the user they can cash out today.
//
// A minimum is only ever compared against a balance in the SAME unit. Storj's
// collector reports USD while the catalog declares the minimum in STORJ, so
// subtracting one from the other produced "0.50 to go" out of a dollar figure and a
// token figure — see app/payouts.py (min_payout_in) in the web CashPilot, which this
// is the port of. With no rate to hand, the honest answer is no countdown at all.
//
// Pure: takes catalog data, returns HTML strings, touches no global.

import type { Payout, Service } from "../wails";
import { coinAmount, escapeHtml, miningBalancePrecision } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { stripHtml } from "./fields.js";
import { ht, t } from "./i18n.js";

/** What an undocumented value looks like. Never 0, never an empty cell. */
export const UNKNOWN = "—";

export type Fact = { label: string; value: string };

/** A balance as the earnings table records it: an amount and the unit it is in. */
export type Balance = { amount: number; currency: string };

/**
 * The sentence that says where this provider's credentials live.
 *
 * The catalog writes it with links in it, for a web page that inserts it as markup.
 * Desktop is an app with bindings behind its webview, so the markup is stripped and
 * the text is escaped: the address stays readable, and a catalog entry can never
 * inject an element into the app. Empty when nobody has written one — a card with no
 * hint is better than a card with an empty grey box.
 */
export function credentialHint(service: Service): string {
  const hint = stripHtml(service.collector?.credentialHint || "").trim();
  if (!hint) return "";
  return `<p class="tip credential-hint">${escapeHtml(hint)}</p>`;
}

/**
 * The minimum cashout expressed in `balanceCurrency`, or null when the two cannot
 * honestly be compared.
 *
 * Null covers three different unknowns, all of which mean "do not count down": no
 * documented minimum, no unit on either side, or two different units with no rate
 * here to bridge them. A guess would send someone to a withdrawal page that refuses
 * them.
 */
export function minimumIn(service: Service, balanceCurrency: string | null | undefined): number | null {
  const minimum = service.cashout?.minAmount;
  if (typeof minimum !== "number" || !Number.isFinite(minimum) || minimum <= 0) return null;
  const declared = (service.cashout?.currency || "").trim().toUpperCase();
  const target = (balanceCurrency || "").trim().toUpperCase();
  // Most entries omit the unit because the provider pays in the unit its collector
  // reports; taking the minimum at face value is right whenever the two agree, and
  // there is nothing better available when one is unstated.
  if (!declared || !target || declared === target) return minimum;
  return null;
}

/** The unit a minimum is counted in: the cashout's own, or the payment currency. */
function minimumUnit(service: Service): string {
  return (service.cashout?.currency || service.payment?.currency || "").trim().toUpperCase();
}

/** Trim a money figure: 20 stays 20, 4.5 stays 4.5, 0.10 becomes 0.1. */
function trimNumber(value: number, currency: string): string {
  const decimals = miningBalancePrecision(currency) ?? 4;
  return value.toFixed(decimals).replace(/(\.\d*?[1-9])0+$|\.0+$/, "$1");
}

/**
 * What it takes to get paid: the minimum with its unit, how it is paid, how often.
 *
 * With a balance in hand the card also says how much is left to go — but only when
 * the balance and the minimum are in the same unit.
 */
export function payoutFacts(service: Service, balance?: Balance | null): Fact[] {
  const minimum = service.cashout?.minAmount;
  const unit = minimumUnit(service);
  const pool = service.collector?.accrual === "pool";
  const documented = typeof minimum === "number" && Number.isFinite(minimum) && minimum > 0;
  const minimumText = documented ? `${trimNumber(minimum, unit)}${unit ? ` ${unit}` : ""}` : UNKNOWN;
  const poolTerms = pool ? service.payment?.minimumPayout?.trim() : "";
  const facts: Fact[] = [
    {
      label: pool ? t("details.payoutTerms") : t("details.minimum"),
      value: poolTerms || minimumText,
    },
    { label: t("details.paidBy"), value: (service.payment?.methods || []).join(", ") || UNKNOWN },
    { label: t("details.paidOut"), value: (service.payment?.frequency || "").trim() || UNKNOWN },
  ];

  const poolNotes = pool ? service.cashout?.notes?.trim() : "";
  if (poolNotes) facts.push({ label: t("details.payoutNotes"), value: poolNotes });

  if (balance && Number.isFinite(balance.amount)) {
    if (pool) {
      const unit = balance.currency.trim().toUpperCase();
      const amount = miningBalancePrecision(unit) === undefined ? coinAmount(balance.amount) : trimNumber(balance.amount, unit);
      facts.push({ label: t("details.unpaidPool"), value: `${amount} ${unit}`.trim() });
    }
    const threshold = minimumIn(service, balance.currency);
    if (threshold !== null) {
      const remaining = threshold - balance.amount;
      const reached = pool ? t("details.poolReached") : t("details.reached");
      facts.push({
        label: pool ? t("details.stillToPoolMinimum") : t("details.stillToGo"),
        value: remaining <= 0 ? reached : `${trimNumber(remaining, balance.currency)} ${(balance.currency || "").trim().toUpperCase()}`.trim(),
      });
    }
  }
  return facts;
}

/** What the provider does with the machine, in the catalog's own words. */
export function disclosureFacts(service: Service): Fact[] {
  const disclosure = service.disclosure || ({} as Service["disclosure"]);
  return [
    { label: t("details.sells"), value: text(disclosure.sells) },
    { label: t("details.thirdPartyTraffic"), value: text(disclosure.thirdPartyTraffic) },
    { label: t("details.dataCollected"), value: text(disclosure.dataCollected) },
    { label: t("details.ispRisk"), value: text(disclosure.ispRisk) },
    { label: t("details.accountRules"), value: text(disclosure.accountRules) },
  ];
}

function text(value: string | undefined | null): string {
  return stripHtml(String(value || "")).trim() || UNKNOWN;
}

/**
 * The signup button.
 *
 * The URL is the catalog's signup_url, used character for character: it carries the
 * referral code, and a code that gets trimmed, canonicalised or replaced by the bare
 * website still opens the provider's page, still lets the user sign up, and quietly
 * earns nothing forever. The provider's own site is the fallback only when the
 * catalog has no signup link at all.
 *
 * A provider with no account to sign up for (referral.account: false, such as a
 * relay that needs only a wallet) gets "Visit site" instead: "Create account" sent
 * people looking for an account form that does not exist. The web app does the same.
 */
export function signupButton(service: Service, label = t("details.createAccount")): string {
  const url = (service.referral?.signupUrl || "").trim() || (service.website || "").trim();
  if (!url) return "";
  const text = hasAccount(service) ? label : t("details.visitSite");
  return `<button class="primary" data-url="${escapeHtml(url)}">${escapeHtml(text)}</button>`;
}

/** False only when the catalog says the provider has no account to sign up for. */
export function hasAccount(service: Pick<Service, "referral">): boolean {
  return service.referral?.account !== false;
}

/**
 * The line under a signup button that says it is a referral link.
 *
 * Next to the button, before the click, smaller than the text around it: the reader
 * should not have to find a README section to learn the maintainer may earn from it.
 * Only where the catalog has a referral code, since a bare signup URL is not a
 * referral link. The web app renders the same note (app.js referralNote).
 *
 * Some signups never read the code from the link (Dawn's extension asks for it):
 * referral.code_entry is then the code to type, shown first with a Copy button, so
 * a user who signs up from here still credits the referral.
 */
export function referralNote(service: Service): string {
  if (!service.referral?.code || !(service.referral.signupUrl || "").trim()) return "";
  // The new user's bonus first, when the catalog records one: it is the reason to use the link.
  const referee = (service.referral.bonus?.referee || "").trim();
  const bonus = referee ? `${ht("details.bonus", { bonus: referee })} ` : "";
  const entry = (service.referral.codeEntry || "").trim();
  const typed = entry
    ? `<p class="referral-code">${ht("details.typeCode", { name: service.name, code: entry })} ${codeEntryCopy(service, ht("details.copy"))}</p>`
    : "";
  return `${typed}<p class="referral-note">${bonus}${ht("details.referralNote")}</p>`;
}

/**
 * The Copy button for a code typed by hand (referral.codeEntry), or "" when the
 * link carries the code. label is already HTML. The accessible name says what it
 * copies. The catalog row uses it beside Visit and Sign up too (render/catalog.ts).
 */
export function codeEntryCopy(service: Pick<Service, "name" | "referral">, label: string): string {
  const entry = (service.referral?.codeEntry || "").trim();
  if (!entry || !service.referral?.code || !(service.referral.signupUrl || "").trim()) return "";
  const name = escapeHtml(t("details.copyCodeLabel", { name: service.name, code: entry }));
  return `<button type="button" class="secondary compact-btn" data-copy-text="${escapeHtml(entry)}" aria-label="${name}" title="${name}">${label}</button>`;
}

/**
 * The step after a deploy for a provider that pays a device only once its id is on
 * the user's account (EarnApp, ProxyRack): an unlinked node runs and earns for
 * nobody. Plain text for the deploy output, "" when there is nothing to link, and a
 * short "added" note when the deploy added the device itself (addedId). The web app
 * shows the same step (app.js _showDeviceLink).
 */
export function deviceLinkText(service: Service | undefined, values: Record<string, string>, addedId = "", retryingId = ""): string {
  const link = service?.deviceLink;
  const id = (values[link?.idEnv || ""] || "").trim();
  if (!link || !id) return "";
  // The deploy added it through the provider's API (ProxyRack with its API key).
  if (addedId === id) return [t("details.addedToAccount"), id].join("\n");
  const url = (link.url || "").replace(/\$\{(\w+)\}/g, (_, key: string) => encodeURIComponent((values[key] || "").trim()));
  // The provider did not know the new device yet; the app keeps trying to add it.
  const retrying = retryingId === id
    ? t("details.retryingLink")
    : "";
  return [t("details.linkDevice"), retrying || link.steps.trim(), id, url].filter(Boolean).join("\n");
}

/** One block of label/value rows. */
function factList(title: string, facts: Fact[]): string {
  return `
    <strong class="detail-title">${escapeHtml(title)}</strong>
    <dl class="detail-facts">
      ${facts
        .map((fact) => `<div class="detail-fact"><dt>${escapeHtml(fact.label)}</dt><dd>${escapeHtml(fact.value)}</dd></div>`)
        .join("")}
    </dl>
  `;
}

export function renderPayoutFacts(service: Service, balance?: Balance | null): string {
  return factList(t("details.gettingPaid"), payoutFacts(service, balance));
}

/** Payout and disclosure together, as they appear under a service's setup card. */
export function serviceFacts(service: Service, balance?: Balance | null): string {
  return `
    <div class="service-details">
      <div class="detail-block" data-payout-facts>${renderPayoutFacts(service, balance)}</div>
      <div class="detail-block">${factList(t("details.withYourMachine"), disclosureFacts(service))}</div>
      <div class="detail-block payout-ledger" data-payouts-slug="${escapeHtml(service.slug)}">${payoutLedger(service, undefined)}</div>
    </div>
  `;
}

/**
 * The cash-outs recorded for a service, and the form to record one.
 *
 * The balance total drops on every cash-out, so without these the dashboard says
 * what is waiting and never what a service made. Desktop does not detect payouts,
 * so every entry is one the user typed: a cash-out made before CashPilot was
 * installed is exactly the one to enter. Port of GeiserX/CashPilot#487.
 *
 * `payouts` undefined is "still loading"; null is "could not be read", which is
 * said out loud, because an empty list would invite the user to record again a
 * cash-out that is already there. The form comes with the list, loaded or not
 * (never in the first render, so the setup card's only inputs stay its own).
 */
export function payoutLedger(service: Service, payouts: Payout[] | null | undefined): string {
  const slug = escapeHtml(service.slug);
  let list: string;
  if (payouts === undefined) {
    return `<strong class="detail-title">${ht("details.cashedOut")}</strong><p class="muted">${ht("details.loadingPayouts")}</p>`;
  }
  if (payouts === null) {
    list = `<p class="tip">${ht("details.payoutsFailed")}</p>`;
  } else if (payouts.length === 0) {
    list = `<p class="muted">${ht("details.noneRecorded")}</p>`;
  } else {
    list = `<dl class="detail-facts">${payouts
      .map((payout) => {
        const what = `${trimNumber(payout.amount, payout.currency || "")} ${(payout.currency || "").toUpperCase()}${payout.method ? ` · ${payout.method}` : ""}`;
        const unpriced = payout.fxRateUsd === null || payout.fxRateUsd === undefined;
        return `<div class="detail-fact"><dt>${escapeHtml(payout.paidOn)}</dt><dd>${escapeHtml(unpriced ? t("details.unpriced", { what }) : what)} <button class="secondary" data-remove-payout="${escapeHtml(String(payout.id))}" data-slug="${slug}">${ht("details.remove")}</button></dd></div>`;
      })
      .join("")}</dl>`;
  }
  const unit = minimumUnit(service);
  return `
    <strong class="detail-title">${ht("details.cashedOut")}</strong>
    ${list}
    <details class="payout-record">
      <summary>${ht("details.recordPayout")}</summary>
      <div class="credential-grid">
        <label><span>${ht("details.amount")}</span><input data-payout-field="amount" type="number" step="any" min="0" placeholder="${ht("details.example", { example: "37.54" })}" /></label>
        <label><span>${ht("details.currency")}</span><input data-payout-field="currency" type="text" maxlength="12" placeholder="${escapeHtml(unit || t("details.sameAsBalance"))}" /></label>
        <label><span>${ht("details.date")}</span><input data-payout-field="date" type="date" /></label>
        <label><span>${ht("details.method")}</span><input data-payout-field="method" type="text" maxlength="40" placeholder="${ht("details.example", { example: "PayPal" })}" /></label>
      </div>
      <div class="actions left"><button class="primary" data-record-payout="${slug}">${ht("details.savePayout")}</button></div>
      <small class="muted">${ht("details.payoutHelp")}</small>
    </details>
  `;
}

/**
 * The same export for everything on the page at once: one compose file holding
 * every service being set up, which is how a whole box gets stood up in one go.
 * Nothing selected means nothing to export, so the control stays away.
 */
export function composeSelectionExport(services: Service[]): string {
  const exportable = services.filter((service) => !service.manualOnly);
  if (exportable.length === 0) return "";
  return `
    <div class="compose-export">
      <label class="compose-arch">
        <span>${ht("details.composeFor")}</span>
        <select data-compose-arch="selection">
          <option value="">${ht("details.arch.thisMachine")}</option>
          <option value="amd64">${ht("details.arch.amd64")}</option>
          <option value="arm64">${ht("details.arch.arm64")}</option>
          <option value="arm">${ht("details.arch.arm")}</option>
        </select>
      </label>
      <button class="secondary" data-compose-export-selection="${exportable.map((service) => escapeHtml(service.slug)).join(" ")}">
        ${ht("details.exportAll", { n: exportable.length })}
      </button>
      <pre class="output compose-output" data-output-slug="compose-selection"></pre>
    </div>
  `;
}

/**
 * The compose export control: pick the machine the file is for, then save it.
 *
 * The architecture choice is here because the file is often written FOR another box
 * — a Raspberry Pi, a NAS, a server — and one catalog image (Traffmonetizer)
 * publishes ARM builds Docker cannot pick from the manifest. "This machine" is the
 * architecture this copy of CashPilot runs on, which the backend fills in.
 */
export function composeExportControl(service: Service): string {
  if (service.manualOnly) return "";
  const slug = escapeHtml(service.slug);
  return `
    <div class="compose-export">
      <label class="compose-arch">
        <span>${ht("details.composeFor")}</span>
        <select data-compose-arch="${slug}">
          <option value="">${ht("details.arch.thisMachine")}</option>
          <option value="amd64">${ht("details.arch.amd64")}</option>
          <option value="arm64">${ht("details.arch.arm64")}</option>
          <option value="arm">${ht("details.arch.arm")}</option>
        </select>
      </label>
      <button class="secondary" data-compose-export="${slug}">${ht("details.exportCompose")}</button>
      <small class="muted">${ht("details.composeHelp")}</small>
    </div>
  `;
}
