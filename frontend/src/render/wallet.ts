// A wallet seed kept in a file in the node's own data volume (Sia's hostd), and
// nowhere in CashPilot (internal/runtime/seedfile.go).
//
// The seed phrase IS the wallet: whoever reads it can spend the node's funds. So the
// deploy form never asks for it as a field. It offers a choice instead: make a new
// wallet, paste one the owner has, or keep the node's. A new wallet's phrase comes
// back once; this panel shows it, asks for three of its words back, and then holds
// nothing. The dashboard can read the phrase back from the node (a reveal), and
// shows it only for as long as main.ts keeps it.
//
// Pure: takes plain values, returns HTML or plain data. main.ts holds the phrase in
// one module variable while a panel needs it, and wipes it after.

import type { Deployment, EnvVar, Service, WalletChoice, WalletOutcome } from "../wails";
import type { DeployAnswer } from "./deploy_consent.js";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";

/** The env entry of service whose value lives in a seed file, if any. */
export function seedFileVar(service: Service | undefined | null): EnvVar | undefined {
  return (service?.docker?.env || []).find((item) => item.seedFile);
}

/** The words of a phrase as hostd reads it: lowercase, any spacing. */
export function phraseWords(phrase: string | null | undefined): string[] {
  return String(phrase || "").toLowerCase().split(/\s+/).filter(Boolean);
}

/**
 * Why a wallet deploy stops before any call, or null when it may go. "Use a
 * wallet I already have" with no phrase typed deploys nothing.
 */
export function walletChoiceRefusal(choice: WalletChoice): DeployAnswer | null {
  if (choice.mode === "import" && phraseWords(choice.phrase).length === 0) {
    return { kind: "cancelled", text: t("wallet.typePhrase") };
  }
  return null;
}

/**
 * What a wallet deploy says next to Deploy once it ended. shownOnce is true when
 * its new wallet's phrase is on screen now, for the one time it ever is.
 */
export function walletDeployAnswer(wallet: WalletOutcome | null | undefined, shownOnce: boolean): DeployAnswer {
  if (shownOnce) return { kind: "ok", text: t("wallet.deployed.new") };
  if (wallet?.migrated) return { kind: "ok", text: t("wallet.deployed.migrated") };
  if (wallet?.created) return { kind: "ok", text: t("wallet.deployed.typed") };
  if (wallet) return { kind: "ok", text: t("wallet.deployed.kept") };
  return { kind: "ok", text: t("wallet.deployed.plain") };
}

/**
 * The wallet choice in a seed-file service's deploy card. A node not deployed yet
 * defaults to a new wallet; a deployed one defaults to keeping its wallet, which a
 * redeploy must never replace by surprise. "" for any other service.
 */
export function walletChoiceSection(service: Service, deployed: boolean): string {
  if (!seedFileVar(service)) return "";
  const slug = escapeHtml(service.slug);
  const option = (value: string, label: string, hint: string, checked: boolean) => `
    <label class="wallet-option">
      <input type="radio" name="wallet-mode-${slug}" data-wallet-mode value="${value}"${checked ? " checked" : ""} />
      <span><strong>${label}</strong><small class="muted">${hint}</small></span>
    </label>`;
  return `
    <fieldset class="wallet-choice" data-wallet-choice="${slug}">
      <legend>${ht("wallet.legend")}</legend>
      <p class="muted">${ht("wallet.intro")}</p>
      ${deployed ? option("keep", ht("wallet.keep"), ht("wallet.keepHint"), true) : ""}
      ${option("generate", ht("wallet.generate"), ht("wallet.generateHint"), !deployed)}
      ${option("import", ht("wallet.import"), ht("wallet.importHint"), false)}
      <label class="wallet-phrase" data-wallet-phrase-box hidden>
        <span>${ht("wallet.phraseLabel")}</span>
        <input data-wallet-phrase type="password" autocomplete="off" spellcheck="false" autocapitalize="off" placeholder="${ht("wallet.phrasePlaceholder")}" />
      </label>
    </fieldset>
  `;
}

/**
 * count distinct positions in 1..total, in order, picked with rand. Any rand works,
 * one that returns NaN, a constant or a value outside [0, 1) included: the answer is
 * always count distinct positions in range.
 */
export function pickPositions(total: number, count: number, rand: () => number): number[] {
  const pool = Array.from({ length: total }, (_, i) => i + 1);
  for (let i = pool.length - 1; i > 0; i--) {
    const r = Number(rand());
    const j = Number.isFinite(r) ? Math.min(i, Math.max(0, Math.floor(r * (i + 1)))) : 0;
    [pool[i], pool[j]] = [pool[j], pool[i]];
  }
  return pool.slice(0, Math.min(count, total)).sort((a, b) => a - b);
}

/** "Type words 4, 9 and 11". */
export function positionsPrompt(positions: number[]): string {
  if (positions.length <= 1) return t("wallet.typeWord", { n: positions.join("") });
  return t("wallet.typeWords", { rest: positions.slice(0, -1).join(", "), last: String(positions[positions.length - 1]) });
}

/** Whether typed (one answer per position, in order) matches the words there. */
export function wordsMatch(words: string[], positions: number[], typed: string[]): boolean {
  if (words.length === 0 || positions.length === 0 || typed.length !== positions.length) return false;
  return positions.every((position, i) => {
    const want = words[position - 1];
    return Boolean(want) && phraseWords(typed[i]).join(" ") === want;
  });
}

/** The 12 words, numbered. */
function wordList(words: string[]): string {
  return `<ol class="seed-words">${words.map((word, i) => `<li data-seed-n="${i + 1}"><span class="seed-n">${i + 1}</span> <span class="seed-word">${escapeHtml(word)}</span></li>`).join("")}</ol>`;
}

/** A wallet's public address, with a Copy button. */
export function walletAddressLine(address: string | null | undefined): string {
  if (!address) return "";
  return `
    <p class="wallet-address">${ht("wallet.address")}
      <code class="cell-mono">${escapeHtml(address)}</code>
      <button class="btn btn-small secondary" data-wallet-copy="${escapeHtml(address)}">${ht("wallet.copy")}</button>
    </p>`;
}

export interface PhrasePanelView {
  slug: string;
  words: string[];
  address: string;
  /** show: the words; check: three of them asked back; done: nothing left to show. */
  step: "show" | "check" | "done";
  positions: number[];
  error: string;
}

/** The one-time panel after a deploy that made a new wallet. */
export function renderPhrasePanel(view: PhrasePanelView): string {
  const slug = escapeHtml(view.slug);
  if (view.step === "done") {
    return `
      <div class="wallet-panel" data-wallet-panel-view="done">
        <p>${ht("wallet.done")}</p>
        ${walletAddressLine(view.address)}
      </div>`;
  }
  if (view.step === "check") {
    return `
      <div class="wallet-panel" data-wallet-panel-view="check" role="alertdialog" aria-labelledby="wallet-check-${slug}">
        <p id="wallet-check-${slug}">${view.positions.length <= 1
          ? ht("wallet.checkOne", { n: view.positions.join("") })
          : ht("wallet.checkMany", { rest: view.positions.slice(0, -1).join(", "), last: String(view.positions[view.positions.length - 1]) })}</p>
        <div class="wallet-word-inputs">
          ${view.positions.map((position) => `
            <label><span>${ht("wallet.word", { n: String(position) })}</span>
              <input data-wallet-word="${position}" type="text" autocomplete="off" spellcheck="false" autocapitalize="off" />
            </label>`).join("")}
        </div>
        ${view.error ? `<p class="error-text">${escapeHtml(view.error)}</p>` : ""}
        <div class="actions left">
          <button class="primary" data-wallet-finish="${slug}">${ht("wallet.finish")}</button>
          <button class="secondary" data-wallet-back="${slug}">${ht("wallet.showAgain")}</button>
        </div>
      </div>`;
  }
  return `
    <div class="wallet-panel" data-wallet-panel-view="show" role="alertdialog" aria-labelledby="wallet-show-${slug}">
      <p id="wallet-show-${slug}">${ht("wallet.writeDown")}</p>
      ${wordList(view.words)}
      ${walletAddressLine(view.address)}
      <div class="actions left">
        <button class="primary" data-wallet-written="${slug}">${ht("wallet.written")}</button>
        <button class="secondary" data-wallet-dismiss="${slug}">${ht("wallet.close")}</button>
      </div>
    </div>`;
}

export type RevealView =
  | { kind: "idle" }
  | { kind: "ask"; error?: string; busy?: boolean }
  | { kind: "shown"; words: string[] };

/** The dashboard's "Reveal seed phrase": a button, the name to type, or the words. */
export function renderRevealPanel(slug: string, name: string, view: RevealView): string {
  const s = escapeHtml(slug);
  if (view.kind === "shown") {
    return `
      <div class="wallet-reveal" data-wallet-reveal-view="shown">
        <p>${ht("wallet.revealed")}</p>
        ${wordList(view.words)}
        <button class="secondary" data-wallet-reveal-hide="${s}">${ht("wallet.hide")}</button>
      </div>`;
  }
  if (view.kind === "ask") {
    return `
      <div class="wallet-reveal" data-wallet-reveal-view="ask">
        <label><span>${ht("wallet.typeName", { name })}</span>
          <input data-wallet-reveal-name="${s}" type="text" autocomplete="off" spellcheck="false" autocapitalize="off"${view.busy ? " disabled" : ""} />
        </label>
        ${view.error ? `<p class="error-text">${escapeHtml(view.error)}</p>` : ""}
        <div class="actions left">
          <button class="primary" data-wallet-reveal-show="${s}"${view.busy ? " disabled aria-busy=\"true\"" : ""}>${ht("wallet.show")}</button>
          <button class="secondary" data-wallet-reveal-hide="${s}">${ht("wallet.cancel")}</button>
        </div>
      </div>`;
  }
  return `<button class="btn btn-small secondary" data-wallet-reveal-start="${s}">${ht("wallet.reveal")}</button>`;
}

/** The dashboard's wallet line under a deployed seed-file service. "" for any other. */
export function walletDetail(service: Service | undefined, deployment: Deployment): string {
  if (!service || !seedFileVar(service)) return "";
  return `
    <div class="wallet-detail">
      ${walletAddressLine(deployment.walletAddress) || `<p class="muted">${ht("wallet.addressLater")}</p>`}
      <div data-wallet-reveal-slot="${escapeHtml(service.slug)}">${renderRevealPanel(service.slug, service.name, { kind: "idle" })}</div>
    </div>`;
}
