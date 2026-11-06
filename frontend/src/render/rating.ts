// A provider's grade, as the web catalog shows it (app.js gradeBadge and
// ratingBlock in GeiserX/CashPilot#572).
//
// The grade is computed in Go (internal/catalog/rating.go) from the published
// criteria in services/_ratings.yml; nothing here decides a letter. There is no A,
// and a provider without valid evidence says "not rated", never a neutral grade.
//
// Every con and pro carries its source and the date it was checked, opened through
// data-url like every other external link in the app.
//
// Pure: takes catalog data, returns HTML.

import type { RatingItem, RatingResult, Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** The published method, on the web docs. */
export const RATINGS_URL = "https://geiserx.github.io/CashPilot/ratings/";

const GRADES = new Set(["B", "C", "D", "F"]);

/** The letter as a badge; its tooltip is the thin-evidence note and the worst con. */
export function gradeBadge(r: RatingResult | null | undefined): string {
  if (!r || !r.rated || !GRADES.has(r.grade)) return `<span class="muted">not rated</span>`;
  const top = [
    r.thinEvidence ? `Thin evidence: ${r.unknownCount} criteria unchecked.` : "",
    r.topCons?.[0]?.text || "",
  ].filter(Boolean).join(" ");
  return `<span class="grade-badge grade-${r.grade.toLowerCase()}" title="${escapeHtml(top)}">${escapeHtml(r.grade)}</span>`;
}

function cited(item: RatingItem): string {
  const source = item.source.startsWith("https://")
    ? `<button class="text-link" data-url="${escapeHtml(item.source)}">source</button>, `
    : "";
  return `<li>${escapeHtml(item.text)} <span class="rating-src">${source}checked ${escapeHtml(item.checkedOn)}</span></li>`;
}

/** The rating block of a service card. Empty for a service the user added. */
export function ratingBlock(service: Service): string {
  if (service.custom) return "";
  const r = service.ratingResult;
  const how = `<button class="text-link" data-url="${escapeHtml(RATINGS_URL + "#" + encodeURIComponent(service.slug || ""))}">How we grade</button>`;
  if (!r || !r.rated || !GRADES.has(r.grade)) {
    return `
      <div class="detail-block rating-block">
        <strong class="detail-title">Rating</strong>
        <p class="muted">Not rated yet. ${how}</p>
      </div>`;
  }
  const caps = (r.caps || []).map((c) => `<li>${escapeHtml(c.reason)}</li>`).join("");
  const cons = (r.topCons || []).map(cited).join("");
  const pros = (r.pros || []).map(cited).join("");
  return `
    <div class="detail-block rating-block">
      <strong class="detail-title">Rating</strong>
      <div class="rating-head">${gradeBadge(r)} <span class="rating-score">${escapeHtml(r.summary || "")}</span> ${how}</div>
      ${caps ? `<ul class="rating-caps">${caps}</ul>` : ""}
      ${cons ? `<div class="rating-label">Top cons</div><ul class="rating-list">${cons}</ul>` : ""}
      ${pros ? `<details class="rating-pros"><summary>Pros (${(r.pros || []).length})</summary><ul class="rating-list">${pros}</ul></details>` : ""}
    </div>`;
}
