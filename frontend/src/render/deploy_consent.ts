// What happens between a click on Deploy and the deploy itself, on a service's
// setup card, extracted so it can be TESTED (scripts/deploy_consent_check.mjs).
//
// Rules this file holds:
//   - A field the card marks with * (one the deploy needs) that is empty stops
//     the deploy before any call: nothing is saved, no admin prompt, no
//     container. The card names every empty field. The backend checks the same
//     (ValidateDeploy). A login only the earnings collector reads is marked
//     "(for earnings)" and never stops a deploy.
//   - When the deploy would make Windows show its admin prompt (a native
//     program's firewall rule, internal/runtime/firewall_consent.go), CashPilot
//     first shows its own screen: what changes, what Windows will ask, what
//     follows, and how to undo it, with Continue and Cancel. Only Continue
//     approves the prompt; the backend refuses to raise it otherwise.
//   - Every outcome, a refusal or an error included, is written next to the
//     Deploy button, never only below the card. Once the deploy call ends, the
//     note there is its answer, never "Deploying..." any more.
//   - Continue passes the approved firewall change only to the deploy that asked.
//
// Pure: takes the model, returns HTML or an outcome. main.ts passes the Wails
// bindings and the DOM.

import type { AdminStep, WorkerTarget } from "../wails";
import { deployNeeds, type ServiceField } from "./fields.js";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** The program Windows names in its permission prompt. */
export const POWERSHELL_PATH = "%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\powershell.exe";

/**
 * The fields the deploy needs that values leaves empty, in the card's order. A
 * field with a default is never missing: the deploy uses the default. A login
 * only the earnings collector reads is never missing either: the deploy does not
 * read it.
 */
export function missingRequiredFields(
  fields: ServiceField[], values: Record<string, string>, savedSecrets?: Record<string, boolean>,
): ServiceField[] {
  return fields.filter((field) => deployNeeds(field) && !(values[field.key] || "").trim() &&
    !(field.secret && savedSecrets?.[field.key]));
}

/** The note next to Deploy when fields marked * are empty. */
export function missingFieldsNotice(missing: ServiceField[], retrySecret = false): string {
  return `
    <div class="deploy-notice warn" data-deploy-notice="missing" role="alert">
      <p><strong>Fill in every field marked * first.</strong> Nothing was deployed.</p>
      <ul>${missing.map((field) => `<li data-missing-field="${escapeHtml(field.key)}">${escapeHtml(field.label)}</li>`).join("")}</ul>
      ${retrySecret ? `<p>${RETRY_SECRET_TEXT}</p>` : ""}
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
  const rule = step.ruleName;
  const oldRule = step.removeRuleName;
  const program = step.program || serviceName;
  const ruleChange = step.replaceRule
    ? `CashPilot removes the existing outbound rule "${rule}" and recreates it under the same name.`
    : `Windows adds one outbound firewall rule, "${rule}".`;
  return {
    title: rule && oldRule
      ? `Change the Windows Firewall rules for ${serviceName}?`
      : `${oldRule ? "Remove" : step.replaceRule ? "Replace" : "Add"} a Windows Firewall rule for ${serviceName}?`,
    changes: [
      oldRule ? `Before ${serviceName} starts, CashPilot removes the old outbound rule "${oldRule}". The new deployment no longer needs it.` : "",
      rule ? `Before it starts, ${ruleChange} It blocks only ${serviceName}'s program (${program}) from reaching ${rangesText(step.networks || [])}.` : "",
    ].filter(Boolean).join(" "),
    prompt:
      `Windows then ${rule && oldRule ? "shows a permission prompt for each rule" : "shows a permission prompt"}. It names Windows PowerShell (${POWERSHELL_PATH}), because CashPilot changes the firewall through it. ` +
      "If your account is not an administrator, an administrator types their password there. " +
      `Choose Yes to go ahead. If you choose No, ${oldRule || step.replaceRule ? "CashPilot stops the deploy" : "CashPilot adds no rule"} and ${serviceName} does not run. ` +
      `If ${serviceName} was running, the deploy stopped it before the prompt, and it stays stopped until you deploy again and choose Yes.`,
    after: rule
      ? `No restart of Windows. The new rule stays while ${serviceName} is deployed, so later starts need no prompt while the rule is in place.`
      : "No restart of Windows.",
    undo: rule
      ? `Removing ${serviceName} in CashPilot deletes the rule, with one more permission prompt. You can also delete it yourself in Windows Defender Firewall with Advanced Security, under Outbound Rules, "${rule}".`
      : "Deploying a version that needs the old rule will ask to add it again.",
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

/** What a deploy that ran says next to Deploy once it ended. */
export interface DeployAnswer {
  kind: "ok" | "cancelled" | "failed" | "partial";
  text: string;
  followUp?: { failure: string; run(): Promise<string | void> };
}

/** The note next to Deploy while it runs, or once it answered. */
export function deployNotice(kind: "busy" | DeployAnswer["kind"], text: string, worker = false): string {
  const unfinished = kind === "failed" || kind === "partial";
  const tone = kind === "failed" ? "error" : kind === "cancelled" || kind === "partial" ? "warn" : kind === "ok" ? "ok" : "";
  const role = unfinished ? "alert" : "status";
  const lead = unfinished && !worker ? "<strong>The deploy did not finish.</strong> " : "";
  return `<div class="deploy-notice ${tone}" data-deploy-notice="${kind}" role="${role}"><p>${lead}${escapeHtml(text)}</p></div>`;
}

/** What Cancel on the admin-step screen leaves behind. */
export const CANCELLED_TEXT = "Nothing was deployed. You chose Cancel, so Windows was not asked for anything.";

// app_credentials.go adds this code to the message that Wails rejects with.
const DEPLOY_CREDENTIALS_SAVED = "[deploy_credentials_saved] ";

function deployErrorAnswer(error: unknown): DeployAnswer {
  const text = (error instanceof Error ? error.message : String(error ?? "")).trim();
  if (text.startsWith(DEPLOY_CREDENTIALS_SAVED)) {
    return { kind: "partial", text: text.slice(DEPLOY_CREDENTIALS_SAVED.length) };
  }
  return { kind: "failed", text: text || "CashPilot got no reason back. Check the service before trying Deploy again." };
}

/** The text of a rejected deploy: the backend's own reason, never an empty line. */
export function deployErrorText(error: unknown): string {
  return deployErrorAnswer(error).text;
}

/** What a checked deploy did. */
export type CheckedDeployOutcome =
  | { kind: "missing"; missing: ServiceField[] }
  | { kind: "blocked"; message: string }
  | { kind: "cancelled" }
  | { kind: "done"; answer: DeployAnswer };

/** The calls a checked deploy makes; main.ts passes the Wails bindings and the DOM. */
export interface CheckedDeployCalls {
  /**
   * DeployAdminStep. A failure to answer stops here with its reason: a deploy
   * that went on would only meet the backend's refusal, with no way forward.
   */
  adminStep(): Promise<AdminStep | null>;
  /** Shows adminConsentScreen and resolves true on Continue, false on Cancel. */
  askConsent(step: AdminStep): Promise<boolean>;
  /** Writes the note next to Deploy. */
  say(kind: "busy" | DeployAnswer["kind"], text: string): void;
  /** The deploy itself, with every question it still asks; it says what happened. */
  deploy(approval: AdminStep | null, worker?: WorkerTarget): Promise<DeployAnswer>;
  confirmReplace?(message: string): boolean | Promise<boolean>;
}

interface CheckedDeployTarget {
  workerId?: number | null;
  workers?: WorkerTarget[] | null;
  localReplacement?: string;
  invalidField?: string;
  savedSecrets?: Record<string, boolean>;
  workerBusyText?(worker: WorkerTarget): string;
}

export type DeployStatus = { kind: "busy" | DeployAnswer["kind"]; text: string; worker?: boolean };

export type DeployFeedback = (DeployStatus | { kind: "missing"; missing: ServiceField[] }) & { retrySecret?: boolean };

const RETRY_SECRET_TEXT = "Your secret changes were not saved. CashPilot will use them if you retry in this card.";

export function renderDeployFeedback(status?: DeployFeedback): string {
  if (!status) return "";
  if (status.kind === "missing") return missingFieldsNotice(status.missing, status.retrySecret);
  const text = status.retrySecret ? `${status.text} ${RETRY_SECRET_TEXT}` : status.text;
  return deployNotice(status.kind, text, status.worker);
}

export function renderDeployPanel(slug: string, status?: DeployFeedback): string {
  return `<div class="deploy-panel" data-deploy-panel="${escapeHtml(slug)}" aria-live="polite">${renderDeployFeedback(status)}</div>`;
}

export async function runDeployWithStatus(
  busyText: string,
  say: CheckedDeployCalls["say"],
  deploy: () => Promise<DeployAnswer>,
): Promise<DeployAnswer> {
  say("busy", busyText);
  let answer: DeployAnswer;
  try {
    answer = await deploy();
  } catch (error) {
    answer = deployErrorAnswer(error);
  }
  say(answer.kind, answer.text);
  if (answer.kind === "ok" && answer.followUp) {
    let detail: string | void;
    try {
      detail = await answer.followUp.run();
    } catch (error) {
      detail = `${answer.followUp.failure} ${deployErrorText(error)}`;
    }
    if (detail) {
      answer = { kind: "ok", text: `${answer.text}\n\n${detail}` };
      say(answer.kind, answer.text);
    }
  }
  return answer;
}

/** Resolve the target before checking fields or asking about a replacement. */
export async function runCheckedDeploy(
  fields: ServiceField[],
  values: Record<string, string>,
  direct: boolean,
  busyText: string,
  calls: CheckedDeployCalls,
  target: CheckedDeployTarget = {},
): Promise<CheckedDeployOutcome> {
  const worker = target.workers?.find((candidate) => candidate.id === target.workerId);
  if (target.workerId != null && !worker) {
    return { kind: "blocked", message: "The selected worker is no longer available. Choose a target and try again." };
  }
  // DeployToWorker validates the merged typed, saved and default settings in Go.
  if (!worker) {
    if (target.invalidField) {
      const answer = { kind: "failed" as const, text: `${target.invalidField} does not have the shape this provider expects.` };
      calls.say(answer.kind, answer.text);
      return { kind: "done", answer };
    }
    const missing = missingRequiredFields(fields, values, target.savedSecrets);
    if (missing.length) return { kind: "missing", missing };
    if (target.localReplacement && !(await calls.confirmReplace?.(target.localReplacement))) {
      const answer = { kind: "cancelled" as const, text: "Nothing was deployed." };
      calls.say(answer.kind, answer.text);
      return { kind: "done", answer };
    }
  }
  let approval: AdminStep | null = null;
  if (direct && !worker) {
    let step: AdminStep | null;
    try {
      step = await calls.adminStep();
    } catch (error) {
      return { kind: "blocked", message: deployErrorText(error) };
    }
    if (step?.needed) {
      if (!(await calls.askConsent(step))) return { kind: "cancelled" };
      approval = step;
    }
  }
  const answer = await runDeployWithStatus(
    (worker && target.workerBusyText?.(worker)) || busyText,
    calls.say,
    () => calls.deploy(approval, worker),
  );
  return { kind: "done", answer };
}
