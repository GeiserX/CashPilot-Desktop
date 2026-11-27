// Settings > Language: the picker. Each language shows in its own name.
//
// Pure: takes the state Go sent, returns HTML, touches no global.

import type { LanguageState } from "../wails";
import { escapeHtml } from "./format.js";
import { ht, t } from "./i18n.js";

/** The name of code in its own language, from the list Go sent, or the code. */
export function languageName(state: Pick<LanguageState, "languages">, code: string): string {
  return state.languages.find((l) => l.code === code)?.name ?? code;
}

export function renderLanguageCard(state: LanguageState): string {
  const system = state.system
    ? t("language.system", { language: languageName(state, state.system) })
    : t("language.systemNone");
  const option = (code: string, label: string, lang = "") =>
    `<option value="${escapeHtml(code)}"${lang ? ` lang="${escapeHtml(lang)}"` : ""}${state.preference === code ? " selected" : ""}>${escapeHtml(label)}</option>`;
  return `
    <section class="card" aria-labelledby="language-title">
      <div class="card-header">
        <div>
          <span class="card-title" id="language-title">${ht("language.title")}</span>
          <p class="muted compact-copy">${ht("language.help")}</p>
        </div>
        <select id="language-select" class="language-select" aria-labelledby="language-title">
          ${option("", system)}
          ${state.languages.map((l) => option(l.code, l.name, l.code)).join("")}
        </select>
      </div>
    </section>
  `;
}
