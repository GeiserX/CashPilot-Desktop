#!/usr/bin/env node
// Browser-free checks on the frontend's translation runtime (src/render/i18n.ts).
//
// THE RULES: the frontend and Go (internal/i18n) fill a message the same way,
// so the same file of vectors runs against both; a missing translation shows
// English, never a blank; ht() escapes what a translation or an argument could
// smuggle into an innerHTML sink; Arabic draws right to left.
//
//   node scripts/i18n_runtime_check.mjs      # against ./.harness-build

import { readFileSync } from "node:fs";
import { format, ht, matchLocale, raw, setLocale, t, textDirection, currentLocale, LOCALES } from "../.harness-build/render/i18n.js";
import { formatBalance, relativeTime } from "../.harness-build/render/format.js";
import { renderLanguageCard } from "../.harness-build/render/language.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const vectors = JSON.parse(readFileSync(new URL("../../internal/i18n/testdata/vectors.json", import.meta.url), "utf-8"));
check("the shared vectors are there", vectors.format.length > 10 && vectors.match.length > 5);
for (const v of vectors.format) {
  const got = format(v.locale, v.msg, v.args);
  check(`format ${v.locale} ${JSON.stringify(v.msg)} ${JSON.stringify(v.args)}`, got === v.want, `got ${JSON.stringify(got)}, want ${JSON.stringify(v.want)}`);
}
for (const v of vectors.match) {
  const got = matchLocale(v.tags);
  check(`match ${JSON.stringify(v.tags)}`, got === v.want, `got ${JSON.stringify(got)}, want ${JSON.stringify(v.want)}`);
}

// Every shipped locale has a file, and every file names its language.
const en = JSON.parse(readFileSync(new URL("../src/locales/en.json", import.meta.url), "utf-8"));
for (const code of LOCALES) {
  const file = JSON.parse(readFileSync(new URL(`../src/locales/${code}.json`, import.meta.url), "utf-8"));
  check(`${code}.json names its language`, typeof file["language.name"] === "string" && file["language.name"] !== "");
}

// English is the default, so the harness sees the English the other checks assert on.
check("starts in English", currentLocale() === "en" && t("tray.quit") === en["tray.quit"]);

// A missing or empty translation shows English; an unknown locale is English.
setLocale("es", { "tray.quit": "Salir de CashPilot Desktop", "tray.show": "" });
check("a translated key shows the translation", t("tray.quit") === "Salir de CashPilot Desktop");
check("an empty translation shows English", t("tray.show") === en["tray.show"]);
check("a key the locale lacks shows English", t("language.title") === en["language.title"]);
setLocale("es-MX", {});
check("es-MX is read as es", currentLocale() === "es");
setLocale("xx", { "tray.quit": "nope" });
check("an unsupported locale is English", currentLocale() === "en" && t("tray.quit") === en["tray.quit"]);

// ht escapes a hostile translation and argument, keeps the allowed tags.
setLocale("fr", { "language.system": "<img src=x onerror=alert(1)><strong>{language}</strong>" });
const html = ht("language.system", { language: "<script>" });
check("ht escapes markup a translation brings", !html.includes("<img") && html.includes("&lt;img"), html);
check("ht keeps the bare allowed tags", html.includes("<strong>") && html.includes("</strong>"), html);
check("ht escapes an argument", html.includes("&lt;script&gt;") && !html.includes("<script>"), html);
check("ht passes a raw() argument through", ht("language.system", { language: raw("<a href=\"#\">x</a>") }).includes("<a href=\"#\">x</a>"));

setLocale("en", {});
check("ht keeps a plain apostrophe as it is", ht("language.help").includes("computer's"), ht("language.help"));

// Right to left only for Arabic.
setLocale("ar", {});
check("ar is right to left", textDirection() === "rtl");
setLocale("he", {});
check("anything else is left to right", textDirection() === "ltr");

// Money, numbers and times follow the locale.
setLocale("de", {});
check("de money uses a decimal comma", formatBalance(1234.5, "EUR").includes("1.234,50"), formatBalance(1234.5, "EUR"));
setLocale("en", {});
check("en money is unchanged", formatBalance(1234.5, "USD") === "$1,234.50", formatBalance(1234.5, "USD"));
const now = Date.parse("2026-01-01T12:00:00Z");
check("en relative time is unchanged", relativeTime("2026-01-01T11:48:00Z", now) === "12 minutes ago" && relativeTime("2026-01-01T11:59:30Z", now) === "just now" && relativeTime("2026-01-01T11:00:00Z", now) === "1 hour ago");
setLocale("es", { "time.justNow": "justo ahora" });
check("es relative time", relativeTime("2026-01-01T11:48:00Z", now) === "hace 12 minutos" && relativeTime("2026-01-01T11:59:30Z", now) === "justo ahora", relativeTime("2026-01-01T11:48:00Z", now));

// The picker lists every language in its own name and marks the saved one.
setLocale("en", {});
const languages = [{ code: "en", name: "English" }, { code: "es", name: "Español" }, { code: "ar", name: "العربية" }];
const auto = renderLanguageCard({ preference: "", active: "es", system: "es", languages });
check("the picker offers the system language first", /<option value=""[^>]* selected>System language \(Español\)<\/option>/.test(auto), auto);
check("each language shows in its own name, tagged with its lang", auto.includes('<option value="ar" lang="ar">العربية</option>'), auto);
const picked = renderLanguageCard({ preference: "ar", active: "ar", system: "", languages });
check("a saved choice is selected", picked.includes('<option value="ar" lang="ar" selected>'), picked);
check("no system language says English is used", picked.includes(en["language.systemNone"]), picked);

if (failures) {
  console.error(`\n${failures} of ${checks} i18n runtime checks failed`);
  process.exit(1);
}
console.log(`ok  ${checks} i18n runtime checks`);
