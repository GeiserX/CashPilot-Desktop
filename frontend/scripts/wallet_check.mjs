#!/usr/bin/env node
// Browser-free checks on a wallet seed kept in a file (Sia's hostd).
//
// THE RULE: the seed phrase is the wallet, and CashPilot keeps no copy. The form
// never asks for it as a field, a new wallet's phrase is shown once and checked
// back, and a reveal shows it only when the backend handed it over.
//
//   node scripts/wallet_check.mjs      # against ./.harness-build

import { serviceFormFields } from "../.harness-build/render/fields.js";
import { renderWizardServiceSetup } from "../.harness-build/render/wizard.js";
import { renderServicesTable } from "../.harness-build/render/services.js";
import {
  phraseWords,
  pickPositions,
  positionsPrompt,
  renderPhrasePanel,
  renderRevealPanel,
  walletChoiceSection,
  wordsMatch,
} from "../.harness-build/render/wallet.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

// The public BIP-39 all-zero test phrase: never a wallet anybody uses.
const PHRASE = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";
const WORDS = PHRASE.split(" ");
const ADDRESS = "a2a3773f76136bdb05a0ff79a0f4fcc2826436794f8db36db6408355c5ca32345002db43c2eb";

const sia = {
  slug: "sia-hostd",
  name: "Sia (hostd)",
  category: "storage",
  description: "",
  docker: {
    image: "ghcr.io/siafoundation/hostd@sha256:2f27",
    env: [
      {
        key: "HOSTD_WALLET_SEED", label: "Wallet seed phrase", required: true, secret: true, description: "BIP-39 seed", default: "",
        seedFile: { path: "/data/hostd.yml", key: "recoveryPhrase", words: 12, generate: ["seed"], phrasePattern: "x", addressPattern: "y" },
      },
      { key: "HOSTD_API_PASSWORD", label: "Dashboard password", required: true, secret: true, description: "pw", default: "" },
    ],
  },
};
const plain = { slug: "honeygain", name: "Honeygain", category: "bandwidth", description: "", docker: { image: "honeygain/honeygain:0.8.1", env: [{ key: "EMAIL", label: "Email", required: true, secret: false, description: "e", default: "" }] } };

// 1. The seed is never a form field.
const keys = serviceFormFields(sia, {}).map((f) => f.key);
check("the seed variable is not a form field", !keys.includes("HOSTD_WALLET_SEED"), keys.join(","));
check("the other secret of the service still is", keys.includes("HOSTD_API_PASSWORD"), keys.join(","));
const card = renderWizardServiceSetup(sia, {}, "box", null, "", null, 1, null, null, null, false);
check("the card has no input for the seed variable", !card.includes('data-wizard-env="HOSTD_WALLET_SEED"'));
check("the card asks for a wallet choice", card.includes("data-wallet-mode"));
check("a service with no seed file gets no wallet choice", !renderWizardServiceSetup(plain, {}, "box").includes("data-wallet-mode"));

// 2. The wallet choice and its default.
const fresh = walletChoiceSection(sia, false);
check("a node not deployed yet defaults to a new wallet", /value="generate"[^>]*checked/.test(fresh), fresh);
check("a node not deployed yet has no keep choice", !fresh.includes('value="keep"'));
check("an existing wallet can be pasted", fresh.includes('value="import"'));
const phraseInput = fresh.match(/<input[^>]*data-wallet-phrase[^>]*>/)?.[0] || "";
check("the pasted phrase is a password box", phraseInput.includes('type="password"'), phraseInput);
check("the pasted phrase is not autocompleted, spellchecked or capitalised",
  phraseInput.includes('autocomplete="off"') && phraseInput.includes('spellcheck="false"') && phraseInput.includes('autocapitalize="off"'), phraseInput);
const redeploy = walletChoiceSection(sia, true);
check("a deployed node defaults to keeping its wallet", /value="keep"[^>]*checked/.test(redeploy), redeploy);
check("a deployed node does not default to a new wallet", !/value="generate"[^>]*checked/.test(redeploy));
check("no wallet choice for a service with no seed file", walletChoiceSection(plain, false) === "");

// 3. The one-time phrase panel.
check("phraseWords splits on any spacing and lowercases", phraseWords("  Abandon\tABOUT \n zoo ").join(",") === "abandon,about,zoo");
const shown = renderPhrasePanel({ slug: "sia-hostd", words: WORDS, address: ADDRESS, step: "show", positions: [], error: "" });
const items = shown.match(/<li[\s>]/g) || [];
check("the panel lists 12 words", items.length === 12, String(items.length));
for (let n = 1; n <= 12; n++) check(`word ${n} is numbered`, shown.includes(`data-seed-n="${n}"`));
check("the panel says to write the words down", shown.includes("Write these 12 words down"));
check("the panel says CashPilot keeps no copy", shown.includes("CashPilot keeps no copy"));
check("the panel shows the address", shown.includes(ADDRESS));
const hostile = renderPhrasePanel({ slug: "sia-hostd", words: ["<img src=x onerror=alert(1)>", ...WORDS.slice(1)], address: "", step: "show", positions: [], error: "" });
check("a word is escaped", !hostile.includes("<img") && hostile.includes("&lt;img"));
const asking = renderPhrasePanel({ slug: "sia-hostd", words: WORDS, address: "", step: "check", positions: [2, 7, 11], error: "" });
check("the check step asks for the three positions", asking.includes("Type words 2, 7 and 11"), asking);
check("the check step has one input per position",
  ["2", "7", "11"].every((p) => asking.includes(`data-wallet-word="${p}"`)) && (asking.match(/data-wallet-word=/g) || []).length === 3);
check("the check step does not show the words", !asking.includes(">about<") && !asking.includes(">abandon<"));
const done = renderPhrasePanel({ slug: "sia-hostd", words: [], address: ADDRESS, step: "done", positions: [], error: "" });
check("the finished panel holds no word", !done.includes("abandon") && !done.includes("about"));
check("positionsPrompt joins three positions", positionsPrompt([4, 9, 11]) === "Type words 4, 9 and 11");

// 4. Positions: three distinct, in range, whatever the random source returns.
const sources = [() => 0, () => 0.999999, () => 1, () => -5, () => NaN, () => 0.5];
let seed = 7;
sources.push(() => { seed = (seed * 9301 + 49297) % 233280; return seed / 233280; });
for (const [i, rand] of sources.entries()) {
  for (let round = 0; round < 50; round++) {
    const pos = pickPositions(12, 3, rand);
    const ok = pos.length === 3 && new Set(pos).size === 3 && pos.every((p) => Number.isInteger(p) && p >= 1 && p <= 12);
    check(`source ${i}: three distinct positions in 1..12`, ok, JSON.stringify(pos));
    if (!ok) break;
  }
}
check("positions are in order", (() => { const p = pickPositions(12, 3, Math.random); return p[0] < p[1] && p[1] < p[2]; })());

// 5. The word check.
check("the right words pass, in any case and spacing", wordsMatch(WORDS, [1, 6, 12], [" Abandon ", "ABANDON", "about\n"]));
check("a wrong word fails", !wordsMatch(WORDS, [1, 6, 12], ["abandon", "abandon", "abandon"]));
check("a missing word fails", !wordsMatch(WORDS, [1, 6, 12], ["abandon", "abandon"]));
check("an empty answer fails", !wordsMatch(WORDS, [1, 6, 12], ["", "", ""]));
check("an empty phrase never passes", !wordsMatch([], [1, 2, 3], ["", "", ""]));

// 6. The reveal panel.
const idle = renderRevealPanel("sia-hostd", "Sia (hostd)", { kind: "idle" });
check("the reveal starts as a button", idle.includes("data-wallet-reveal-start") && !idle.includes("abandon"));
const ask = renderRevealPanel("sia-hostd", "Sia (hostd)", { kind: "ask", error: "type the service's name" });
check("the reveal asks for the service's name", ask.includes("data-wallet-reveal-name") && ask.includes("Sia (hostd)"));
check("the reveal shows the backend's refusal", ask.includes("type the service&#039;s name"));
check("asking holds no word", !ask.includes("abandon"));
const revealed = renderRevealPanel("sia-hostd", "Sia (hostd)", { kind: "shown", words: WORDS });
check("a reveal shows 12 numbered words", (revealed.match(/data-seed-n=/g) || []).length === 12);
check("a reveal can be hidden", revealed.includes("data-wallet-reveal-hide"));

// 7. The dashboard row of a deployed seed-file service.
const deployment = { slug: "sia-hostd", containerId: "c", name: "cashpilot-sia-hostd", image: "img", status: "running", runtime: "existing-docker", cpuPercent: 0, memoryMb: 0, customImage: "", instances: 1, walletAddress: ADDRESS };
const table = renderServicesTable({ services: [sia, plain], deployments: [deployment, { ...deployment, slug: "honeygain", walletAddress: "" }], earnings: [], displayCurrency: "USD" });
check("the dashboard shows the wallet address", table.includes(ADDRESS));
check("the dashboard offers to reveal the seed", table.includes('data-wallet-reveal-slot="sia-hostd"'));
check("the address has a copy button", table.includes(`data-wallet-copy="${ADDRESS}"`));
check("no reveal for a service with no seed file", !table.includes('data-wallet-reveal-slot="honeygain"'));
check("the dashboard never shows a word", !table.includes("abandon"));

if (failures > 0) {
  console.error(`\n${failures} of ${checks} wallet checks failed`);
  process.exit(1);
}
console.log(`wallet: ${checks} checks passed`);
