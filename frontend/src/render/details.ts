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

import type { Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { stripHtml } from "./fields.js";

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
function trimNumber(value: number): string {
  return String(Number(value.toFixed(4)));
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
  const documented = typeof minimum === "number" && Number.isFinite(minimum) && minimum > 0;
  const facts: Fact[] = [
    {
      label: "Minimum to cash out",
      value: documented ? `${trimNumber(minimum)}${unit ? ` ${unit}` : ""}` : UNKNOWN,
    },
    { label: "Paid by", value: (service.payment?.methods || []).join(", ") || UNKNOWN },
    { label: "Paid out", value: (service.payment?.frequency || "").trim() || UNKNOWN },
  ];

  if (balance && Number.isFinite(balance.amount)) {
    const threshold = minimumIn(service, balance.currency);
    if (threshold !== null) {
      const remaining = threshold - balance.amount;
      facts.push({
        label: "Still to go",
        value:
          remaining <= 0
            ? "Nothing — this can be cashed out now"
            : `${trimNumber(remaining)} ${(balance.currency || "").trim().toUpperCase()}`.trim(),
      });
    }
  }
  return facts;
}

/** What the provider does with the machine, in the catalog's own words. */
export function disclosureFacts(service: Service): Fact[] {
  const disclosure = service.disclosure || ({} as Service["disclosure"]);
  return [
    { label: "What it sells", value: text(disclosure.sells) },
    { label: "Other people's traffic", value: text(disclosure.thirdPartyTraffic) },
    { label: "What it collects", value: text(disclosure.dataCollected) },
    { label: "Risk to your connection", value: text(disclosure.ispRisk) },
    { label: "Account rules", value: text(disclosure.accountRules) },
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
 */
export function signupButton(service: Service, label = "Create account"): string {
  const url = (service.referral?.signupUrl || "").trim() || (service.website || "").trim();
  if (!url) return "";
  return `<button class="primary" data-url="${escapeHtml(url)}">${escapeHtml(label)}</button>`;
}

/** One block of label/value rows. */
function factList(title: string, facts: Fact[]): string {
  return `
    <div class="detail-block">
      <strong class="detail-title">${escapeHtml(title)}</strong>
      <dl class="detail-facts">
        ${facts
          .map((fact) => `<div class="detail-fact"><dt>${escapeHtml(fact.label)}</dt><dd>${escapeHtml(fact.value)}</dd></div>`)
          .join("")}
      </dl>
    </div>
  `;
}

/** Payout and disclosure together, as they appear under a service's setup card. */
export function serviceFacts(service: Service, balance?: Balance | null): string {
  return `
    <div class="service-details">
      ${factList("Getting paid", payoutFacts(service, balance))}
      ${factList("What it does with your machine", disclosureFacts(service))}
    </div>
  `;
}

/**
 * The compose export control: pick the machine the file is for, then save it.
 *
 * The architecture choice is here because the file is often written FOR another box
 * — a Raspberry Pi, a NAS, a server — and two catalog images publish ARM builds
 * Docker cannot pick from the manifest. "This machine" leaves the choice to whatever
 * runs the file, which is right for everything else.
 */
export function composeExportControl(service: Service): string {
  if (service.manualOnly) return "";
  const slug = escapeHtml(service.slug);
  return `
    <div class="compose-export">
      <label class="compose-arch">
        <span>Compose file for</span>
        <select data-compose-arch="${slug}">
          <option value="">This machine</option>
          <option value="amd64">x86-64 (amd64)</option>
          <option value="arm64">64-bit ARM (Raspberry Pi 4/5, Apple silicon)</option>
          <option value="arm">32-bit ARM (Raspberry Pi 2/3)</option>
        </select>
      </label>
      <button class="secondary" data-compose-export="${slug}">Export compose file</button>
      <small class="muted">Runs this service without CashPilot. Your credentials stay out of the file.</small>
    </div>
  `;
}
