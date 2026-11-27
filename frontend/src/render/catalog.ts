// The catalog as the web console draws it: one table, the services deployed here
// first, then the rest, one line each with what it pays and where it runs.
//
// Sign up stays on every row that has a sign-up link: for providers with a
// referral programme it is the referral link, disclosed under the table.
//
// The Grade column is the provider's letter from the published criteria
// (render/rating.ts); its tooltip is the worst con.
//
// Pure: takes catalog data, returns HTML.

import type { Deployment, Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";
import { customImageBadge } from "./image.js";
import { containerState } from "./attention.js";
import { customBadge } from "./custom.js";
import { gradeBadge } from "./rating.js";
import { codeEntryCopy, hasAccount } from "./details.js";

/** "$0-5 / device" from the catalog's monthly estimate, or a dash when it has none. */
export function estimateText(service: Service): string {
  const e = service.earnings;
  if (!e || (!(e.monthlyLow > 0) && !(e.monthlyHigh > 0))) return "—";
  const cur = (e.currency || "USD").toUpperCase();
  const range = `${e.monthlyLow || 0}-${e.monthlyHigh || 0}`;
  const money = cur === "USD" ? `$${range}` : `${range} ${cur}`;
  return e.per ? `${money} / ${e.per}` : money;
}

/** A deployed service's square: green only when it is actually running. */
function statusDot(status: string): string {
  const state = containerState(status);
  const cls = state === "ok" ? "running" : state === "fail" ? "stopped" : "unknown";
  return `<span class="status-dot ${cls}" title="${ht("catalog.deployedHere", { status: status || t("catalog.unknown") })}"></span> `;
}

/** A catalog kind's name, or "" for a kind this file does not know. */
export function kindLabel(category: string): string {
  switch (category) {
    case "bandwidth":
      return t("catalog.kind.bandwidth");
    case "depin":
      return t("catalog.kind.depin");
    case "storage":
      return t("catalog.kind.storage");
    case "compute":
      return t("catalog.kind.compute");
    case "mining":
      return t("catalog.kind.mining");
    case "custom":
      return t("catalog.kind.custom");
    default:
      return "";
  }
}

const ALWAYS_LISTED = ["bandwidth", "depin", "storage", "compute"];

/** The catalog's filter tabs, in order. Mining and Custom get a tab only once a
 *  service of that kind exists, so no tab opens on an empty table. */
export function catalogFilterTabs(services: Pick<Service, "category" | "custom">[]): string[] {
  const tabs = ["all", ...ALWAYS_LISTED];
  if (services.some((svc) => svc.category === "mining")) tabs.push("mining");
  if (services.some((svc) => svc.custom)) tabs.push("custom");
  return tabs;
}

/** The wizard's category cards: every catalog kind that has at least one service. */
export function wizardCategoryChoices(services: Pick<Service, "category">[]): string[] {
  return [...ALWAYS_LISTED, "mining"].filter((category) => services.some((svc) => svc.category === category));
}

/** The line under a wizard category card's title. */
export function wizardCategoryCopy(category: string): string {
  switch (category) {
    case "bandwidth":
      return t("catalog.wizard.bandwidth");
    case "depin":
      return t("catalog.wizard.depin");
    case "storage":
      return t("catalog.wizard.storage");
    case "compute":
      return t("catalog.wizard.compute");
    case "mining":
      return t("catalog.wizard.mining");
    default:
      return t("catalog.wizard.other");
  }
}

/** "update available": the catalog changed the image, or the user edited a custom service's. */
export function outdatedBadge(service: Pick<Service, "custom"> | undefined): string {
  const title = service?.custom
    ? ht("catalog.outdated.custom")
    : ht("catalog.outdated.catalog");
  return `<span class="badge warn" title="${title}">${ht("catalog.outdated.badge")}</span>`;
}

export function catalogRow(service: Service, deployment: Deployment | undefined, outdated: boolean): string {
  const deployed = Boolean(deployment);
  const signupUrl = service.referral?.signupUrl || service.website;
  const main = service.manualOnly
    ? signupUrl ? `<button class="btn btn-ghost btn-sm" data-url="${escapeHtml(signupUrl)}">${ht("catalog.visit")}</button>` : ""
    : `<button class="btn ${deployed ? "btn-ghost" : "btn-primary"} btn-sm" data-service="${escapeHtml(service.slug)}">${deployed ? ht("catalog.manage") : ht("catalog.add")}</button>`;
  // No account to sign up for (referral.account: false): the link is the provider's site.
  const signup = signupUrl && !service.manualOnly
    ? `<button class="btn btn-ghost btn-sm" data-url="${escapeHtml(signupUrl)}">${hasAccount(service) ? ht("catalog.signUp") : ht("catalog.visit")}</button>`
    : "";
  // Visit and Sign up open the signup page. Where the signup never reads the code
  // from the link (Dawn's extension), the code sits right beside them, or a user
  // who signs up from this row credits nobody.
  const entry = (service.referral?.codeEntry || "").trim();
  const code = signupUrl && entry ? codeEntryCopy(service, ht("catalog.code", { code: entry })) : "";
  const tags = [
    customBadge(service),
    customImageBadge(deployment?.customImage),
    outdated ? outdatedBadge(service) : "",
  ].filter(Boolean).join(" ");
  // The whole row opens the service's setup card (render/catalog_open.ts). Its
  // name is a real button, so Tab reaches it and Enter or Space opens it too.
  return `
    <tr class="catalog-row" data-catalog-open="${escapeHtml(service.slug)}">
      <td class="c-name">
        <button type="button" class="svc-name svc-open" data-catalog-open-name title="${ht("catalog.openSetup", { name: service.name })}">${deployment ? statusDot(deployment.status) : ""}${escapeHtml(service.name)}</button>
        <div class="cell-sub">${escapeHtml(service.shortDescription || service.description)}</div>
        ${tags ? `<div class="cell-tags">${tags}</div>` : ""}
      </td>
      <td>${escapeHtml(kindLabel(service.category) || service.category)}</td>
      <td class="cell-sub-wrap">${escapeHtml((service.platforms || []).join(", ") || t("catalog.noPlatforms"))}</td>
      <td class="c-num">${escapeHtml(estimateText(service))}</td>
      <td class="c-grade">${gradeBadge(service.ratingResult)}</td>
      <td class="c-act"><div class="action-btns">${main}${signup}${code}</div></td>
    </tr>
  `;
}

export function renderCatalogTable(services: Service[], deployments: Deployment[], outdated: string[] | null | undefined): string {
  const deployedBy = new Map(deployments.map((dep) => [dep.slug, dep]));
  const outdatedSet = new Set(outdated || []);
  const here = services.filter((svc) => deployedBy.has(svc.slug));
  const rest = services.filter((svc) => !deployedBy.has(svc.slug));
  const group = (title: string, list: Service[]) => list.length
    ? `<tr class="group-row"><th colspan="6">${escapeHtml(title)} <span class="count">${list.length}</span></th></tr>
       ${list.map((svc) => catalogRow(svc, deployedBy.get(svc.slug), outdatedSet.has(svc.slug))).join("")}`
    : "";
  if (!services.length) return `<p class="block-empty">${ht("catalog.noMatch")}</p>`;
  return `
    <table class="catalog-table">
      <thead>
        <tr><th>${ht("catalog.col.service")}</th><th>${ht("catalog.col.kind")}</th><th>${ht("catalog.col.runsOn")}</th><th class="c-num">${ht("catalog.col.estimate")}</th><th class="c-grade">${ht("catalog.col.grade")}</th><th class="c-act"></th></tr>
      </thead>
      <tbody>
        ${group(t("catalog.group.here"), here)}
        ${group(t("catalog.group.rest"), rest)}
      </tbody>
    </table>
  `;
}
