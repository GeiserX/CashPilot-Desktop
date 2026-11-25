// What happens between a click on Deploy and the deploy itself, on a service's
// setup card, extracted so it can be TESTED (scripts/deploy_consent_check.mjs).
//
// Rules this file holds:
//   - A field the card marks with * that is empty stops the deploy before any
//     call: nothing is saved, no admin prompt, no container. The card names
//     every empty field. The backend checks the same (deploy_fields.go).
//   - When the deploy would make Windows show its admin prompt (a native
//     program's firewall rule, internal/runtime/firewall_consent.go), CashPilot
//     first shows its own screen: what changes, what Windows will ask, what
//     follows, and how to undo it, with Continue and Cancel. Only Continue
//     approves the prompt; the backend refuses to raise it otherwise.
//   - Every outcome, a refusal or an error included, is written next to the
//     Deploy button, never only below the card.
//
// Pure: takes the model, returns HTML or an outcome. main.ts passes the Wails
// bindings and the DOM.

import type { AdminStep } from "../wails";
import type { ServiceField } from "./fields.js";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** The program Windows names in its permission prompt. */
export const POWERSHELL_PATH = "%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\powershell.exe";

/**
 * The fields the card marks required that values leaves empty, in the card's
 * order. A field with a default is never missing: the deploy uses the default.
 */
export function missingRequiredFields(fields: ServiceField[], values: Record<string, string>): ServiceField[] {
  return fields.filter((field) => field.required && !field.default && !(values[field.key] || "").trim());
}

/** The note next to Deploy when fields marked * are empty. */
export function missingFieldsNotice(missing: ServiceField[]): string {
  return `
    <div class="deploy-notice warn" data-deploy-notice="missing" role="alert">
      <p><strong>Fill in every field marked * first.</strong> Nothing was deployed, and Windows was not asked for anything.</p>
      <ul>${missing.map((field) => `<li data-missing-field="${escapeHtml(field.key)}">${escapeHtml(field.label)}</li>`).join("")}</ul>
    </div>`;
}

/** Plain words for the ranges a firewall rule blocks. */
const RANGE_WORDS: Record<string, string> = {
  "100.64.0.0/10": "shared carrier addresses, which Tailscale also uses",
  "169.254.0.0/16": "link-local addresses, where cloud metadata services answer",
};

function rangesText(networks: string[]): string {
  return networks.map((net) => (RANGE_WORDS[net] ? `${net} (${RANGE_WORDS[net]})` : net)).join(" and ");
}

/** What the admin-step screen says, line by line. */
export interface AdminConsentText {
  title: string;
  changes: string;
  prompt: string;
  after: string;
  undo: string;
  go: string;
}

export function adminConsentText(serviceName: string, step: AdminStep): AdminConsentText {
  const rule = step.ruleName || "CashPilot: block";
  const program = step.program || serviceName;
  return {
    title: `Add a Windows Firewall rule for ${serviceName}?`,
    changes:
      `${serviceName} carries other people's traffic. Before it starts, Windows adds one outbound firewall rule, "${rule}". ` +
      `It blocks only ${serviceName}'s program (${program}) from reaching ${rangesText(step.networks || [])}. ` +
      "Nothing else on this PC changes.",
    prompt:
      `Windows then shows a permission prompt. It names Windows PowerShell (${POWERSHELL_PATH}), because CashPilot adds the rule through it. ` +
      "If your account is not an administrator, an administrator types their password there. " +
      `Choose Yes to go ahead. If you choose No, ${serviceName} does not run and nothing changes.`,
    after: `No restart. The prompt comes once: the rule stays while ${serviceName} is deployed, so later starts need no prompt.`,
    undo:
      `Removing ${serviceName} in CashPilot deletes the rule, with one more permission prompt. ` +
      `You can also delete it yourself in Windows Defender Firewall with Advanced Security, under Outbound Rules, "${rule}".`,
    go: "Continue",
  };
}

/** CashPilot's own screen before the Windows admin prompt. */
export function adminConsentScreen(serviceName: string, step: AdminStep): string {
  const c = adminConsentText(serviceName, step);
  return `
    <div class="deploy-notice docker-guide-confirm" data-deploy-notice="consent" role="alertdialog" aria-labelledby="deploy-consent-title">
      <h2 id="deploy-consent-title">${escapeHtml(c.title)}</h2>
      <ul class="mode-effects">
        <li data-consent="changes">${escapeHtml(c.changes)}</li>
        <li data-consent="prompt">${escapeHtml(c.prompt)}</li>
        <li data-consent="after">${escapeHtml(c.after)}</li>
        <li data-consent="undo">${escapeHtml(c.undo)}</li>
      </ul>
      <div class="gate-actions">
        <button class="btn btn-primary" data-deploy-consent="go">${escapeHtml(c.go)}</button>
        <button class="btn btn-ghost" data-deploy-consent="cancel">Cancel</button>
      </div>
    </div>`;
}

/** The note next to Deploy while it runs, or once it answered. */
export function deployNotice(kind: "busy" | "ok" | "cancelled" | "failed", text: string): string {
  const tone = kind === "failed" ? "error" : kind === "cancelled" ? "warn" : kind === "ok" ? "ok" : "";
  const role = kind === "failed" ? "alert" : "status";
  const lead = kind === "failed" ? "<strong>The deploy did not finish.</strong> " : "";
  return `<div class="deploy-notice ${tone}" data-deploy-notice="${kind}" role="${role}"><p>${lead}${escapeHtml(text)}</p></div>`;
}

/** What Cancel on the admin-step screen leaves behind. */
export const CANCELLED_TEXT = "Nothing was deployed. You chose Cancel, so Windows was not asked for anything.";

/** The text of a rejected deploy: the backend's own reason, never an empty line. */
export function deployErrorText(error: unknown): string {
  const text = (error instanceof Error ? error.message : String(error ?? "")).trim();
  return text || "CashPilot got no reason back. Nothing on this card changed; try Deploy again.";
}

/** What a checked deploy did. */
export type CheckedDeployOutcome<T> =
  | { kind: "missing"; missing: ServiceField[] }
  | { kind: "cancelled" }
  | { kind: "done"; result: T };

/** The calls a checked deploy makes; main.ts passes the Wails bindings and the DOM. */
export interface CheckedDeployCalls<T> {
  /** DeployAdminStep. A failure to answer is treated as "no screen": the backend still refuses an unapproved prompt. */
  adminStep(): Promise<AdminStep | null>;
  /** Shows adminConsentScreen and resolves true on Continue, false on Cancel. */
  askConsent(step: AdminStep): Promise<boolean>;
  /** ApproveDeployAdminStep. */
  approve(): Promise<void>;
  deploy(): Promise<T>;
}

/**
 * Deploy from the setup card: the field check, then the admin-step screen when
 * Windows will ask, then the deploy. direct is false for a deploy through a
 * proxy, which runs in a container and needs no admin prompt. An error from the
 * deploy reaches the caller, which writes it next to Deploy.
 */
export async function runCheckedDeploy<T>(
  fields: ServiceField[],
  values: Record<string, string>,
  direct: boolean,
  calls: CheckedDeployCalls<T>,
): Promise<CheckedDeployOutcome<T>> {
  const missing = missingRequiredFields(fields, values);
  if (missing.length) return { kind: "missing", missing };
  if (direct) {
    const step = await calls.adminStep().catch(() => null);
    if (step?.needed) {
      if (!(await calls.askConsent(step))) return { kind: "cancelled" };
      await calls.approve();
    }
  }
  return { kind: "done", result: await calls.deploy() };
}
