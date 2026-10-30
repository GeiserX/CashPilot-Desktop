// The catalog as the web console draws it: one table, the services deployed here
// first, then the rest, one line each with what it pays and where it runs.
//
// Sign up stays on every row that has a sign-up link: for providers with a
// referral programme it is the referral link, disclosed under the table.
//
// Pure: takes catalog data, returns HTML.

import type { Deployment, Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { customImageBadge } from "./image.js";
import { containerState } from "./attention.js";

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
  return `<span class="status-dot ${cls}" title="Deployed here: ${escapeHtml(status || "unknown")}"></span> `;
}

const KIND: Record<string, string> = { bandwidth: "Bandwidth", depin: "DePIN", storage: "Storage", compute: "Compute" };

export function catalogRow(service: Service, deployment: Deployment | undefined, outdated: boolean): string {
  const deployed = Boolean(deployment);
  const signupUrl = service.referral?.signupUrl || service.website;
  const main = service.manualOnly
    ? signupUrl ? `<button class="btn btn-ghost btn-sm" data-url="${escapeHtml(signupUrl)}">Visit</button>` : ""
    : `<button class="btn ${deployed ? "btn-ghost" : "btn-primary"} btn-sm" data-service="${escapeHtml(service.slug)}">${deployed ? "Manage" : "Deploy"}</button>`;
  const signup = signupUrl && !service.manualOnly
    ? `<button class="btn btn-ghost btn-sm" data-url="${escapeHtml(signupUrl)}">Sign up</button>`
    : "";
  const tags = [
    customImageBadge(deployment?.customImage),
    outdated ? `<span class="badge warn" title="The provider changed this service's image. Re-deploy from the catalog to keep earning.">update available</span>` : "",
  ].filter(Boolean).join(" ");
  return `
    <tr>
      <td class="c-name">
        <span class="svc-name">${deployment ? statusDot(deployment.status) : ""}${escapeHtml(service.name)}</span>
        <div class="cell-sub">${escapeHtml(service.shortDescription || service.description)}</div>
        ${tags ? `<div class="cell-tags">${tags}</div>` : ""}
      </td>
      <td>${escapeHtml(KIND[service.category] || service.category)}</td>
      <td class="cell-sub-wrap">${escapeHtml((service.platforms || []).join(", ") || "none listed")}</td>
      <td class="c-num">${escapeHtml(estimateText(service))}</td>
      <td class="c-act"><div class="action-btns">${main}${signup}</div></td>
    </tr>
  `;
}

export function renderCatalogTable(services: Service[], deployments: Deployment[], outdated: string[] | null | undefined): string {
  const deployedBy = new Map(deployments.map((dep) => [dep.slug, dep]));
  const outdatedSet = new Set(outdated || []);
  const here = services.filter((svc) => deployedBy.has(svc.slug));
  const rest = services.filter((svc) => !deployedBy.has(svc.slug));
  const group = (title: string, list: Service[]) => list.length
    ? `<tr class="group-row"><th colspan="5">${escapeHtml(title)} <span class="count">${list.length}</span></th></tr>
       ${list.map((svc) => catalogRow(svc, deployedBy.get(svc.slug), outdatedSet.has(svc.slug))).join("")}`
    : "";
  if (!services.length) return `<p class="block-empty">No service matches.</p>`;
  return `
    <table class="catalog-table">
      <thead>
        <tr><th>Service</th><th>Kind</th><th>Runs on</th><th class="c-num">Est. per month</th><th class="c-act"></th></tr>
      </thead>
      <tbody>
        ${group("Deployed here", here)}
        ${group("Not running", rest)}
      </tbody>
    </table>
  `;
}
