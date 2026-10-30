// The dashboard's Services table, moved out of main.ts so it can be tested.
//
// The web console's columns: service and image, status, health, balance, CPU,
// memory, how far to the cash-out minimum, then the row actions. A platform
// CashPilot reads a balance for but does not run here (a native app, a browser
// extension, another machine) still gets a row, marked as not running here, so
// no balance disappears from the table.
//
// The rule from the earning chip holds here too: a balance with no reading is an
// em dash, never 0.00, and a figure is shown in the display currency only when a
// live rate converted it.
//
// Pure: takes plain state, returns HTML.

import type { Deployment, HealthScore, ProducerReport, Service, ServiceCopy, ServiceEarning } from "../wails";
import { escapeHtml, formatBalance } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { renderHealthBadge, renderProducerBadge } from "./health.js";
import { customImageBadge } from "./image.js";
import { copyRows, firstCopyLabel } from "./instances.js";
import { renderMystNodes } from "./myst.js";
import { rowButton } from "./actions.js";
import { containerState } from "./attention.js";

export const SERVICE_COLUMNS = 8;

export interface ServicesTableInput {
  services: Service[];
  deployments: Deployment[];
  earnings: { platform: string; balance: number; currency: string; error?: string }[];
  breakdown?: ServiceEarning[] | null;
  health?: Record<string, HealthScore> | null;
  serviceDetails?: Record<string, string> | null;
  outdated?: string[] | null;
  producerStates?: Record<string, ProducerReport> | null;
  copies?: Record<string, ServiceCopy[]> | null;
  displayCurrency: string;
}

/** Status as a square and a word, the web console's form. */
export function statusCell(status: string, extra = ""): string {
  const st = containerState(status);
  const cls = st === "ok" ? "running" : st === "fail" ? "stopped" : "unknown";
  return `<span class="badge badge-${cls}"><span class="status-dot ${cls}"></span>${escapeHtml(status || "unknown")}</span>${extra}`;
}

/**
 * The balance cell. `item` is the summary's view of the platform (converted
 * figure and cash-out progress); `record` is the raw reading. An error, or no
 * reading at all, is an em dash.
 */
export function balanceCell(
  item: ServiceEarning | undefined,
  record: { balance: number; currency: string; error?: string } | undefined,
  displayCurrency: string,
): string {
  const error = item?.error || record?.error;
  if (error) {
    return `<span title="${escapeHtml(error)}">&mdash;</span><div class="cell-sub cell-sub-warn">no reading</div>`;
  }
  if (item) {
    const native = formatBalance(item.balance, item.currency);
    // balanceDisplay of 0 on a convertible platform is a missing rate, not a
    // zero balance: fall back to the native figure and name its currency.
    if (item.convertible && item.balanceDisplay !== 0) {
      const sameUnit = item.currency.toUpperCase() === displayCurrency.toUpperCase();
      return `${escapeHtml(formatBalance(item.balanceDisplay, displayCurrency))}${sameUnit ? "" : `<div class="cell-sub">${escapeHtml(native)}</div>`}`;
    }
    // A balance that is really zero is zero in any currency: no note.
    if (item.convertible && item.balance === 0) return escapeHtml(native);
    return `${escapeHtml(native)}<div class="cell-sub">${item.convertible ? "no rate, shown as is" : "not converted"}</div>`;
  }
  if (record) return escapeHtml(formatBalance(record.balance, record.currency));
  return `<span class="muted">&mdash;</span>`;
}

/** How far the balance is to the cash-out minimum, or a dash when that cannot be said. */
export function payoutCell(item: ServiceEarning | undefined): string {
  const cashout = item?.cashout;
  if (!item || item.error || !cashout || !cashout.comparable || !(cashout.minAmount > 0)) {
    return `<span class="muted">&mdash;</span>`;
  }
  const pct = Math.max(0, Math.min(100, cashout.percent || 0));
  const min = formatBalance(cashout.minAmount, cashout.currency);
  return `
    <div class="payout-progress" title="${pct.toFixed(0)}% of the ${escapeHtml(min)} minimum">
      <div class="payout-progress-bar${cashout.eligible ? " eligible" : ""}" style="width: ${pct.toFixed(1)}%"></div>
    </div>
    <span class="payout-label">${cashout.eligible ? "ready" : `${pct.toFixed(0)}%`}</span>
  `;
}

export function renderServicesTable(input: ServicesTableInput): string {
  const { services, deployments, displayCurrency } = input;
  const recordBy = new Map(input.earnings.map((record) => [record.platform, record]));
  const itemBy = new Map((input.breakdown || []).map((item) => [item.platform, item]));
  const deployed = new Set(deployments.map((dep) => dep.slug));
  const elsewhere = [...new Set([...itemBy.keys(), ...recordBy.keys()])].filter((slug) => !deployed.has(slug));

  if (deployments.length === 0 && elsewhere.length === 0) {
    return `
      <div class="empty-state">
        <p class="empty-state-title">No services deployed yet</p>
        <p class="empty-state-text">Use Add services to choose a provider, create an account, enter credentials, and deploy.</p>
        <button class="btn btn-primary" id="open-wizard-empty">Add services</button>
      </div>
    `;
  }
  const outdatedSet = new Set(input.outdated || []);
  const nameOf = (slug: string) => services.find((svc) => svc.slug === slug)?.name || itemBy.get(slug)?.name || slug;

  const rows = deployments.map((deployment) => {
    const slug = deployment.slug;
    const name = nameOf(slug);
    const copies = input.copies?.[slug];
    const running = deployment.status === "running";
    const nodeDetail = slug === "mysterium" ? renderMystNodes(input.serviceDetails?.["mysterium"]) : "";
    const tags = [
      renderProducerBadge(input.producerStates?.[slug], deployment.status),
      customImageBadge(deployment.customImage),
      outdatedSet.has(slug) ? `<span class="badge warn" title="The provider changed this service's image. Re-deploy from the catalog to keep earning.">update available</span>` : "",
    ].filter(Boolean).join(" ");
    const instances = copies && copies.length ? ` <span class="badge-instances">${copies.length + 1}x</span>` : "";
    return `
      <tr class="breakdown-row">
        <td class="c-name">
          <span class="svc-name">${escapeHtml(name)}${firstCopyLabel(copies)}</span>
          <div class="cell-sub cell-mono c-name-image" title="${escapeHtml(deployment.image)}">${escapeHtml(deployment.image)}</div>
        </td>
        <td>${statusCell(deployment.status, instances)}${tags ? `<div class="cell-tags">${tags}</div>` : ""}</td>
        <td class="c-num">${renderHealthBadge(input.health?.[slug]) || `<span class="muted">&mdash;</span>`}</td>
        <td class="c-num c-bal">${balanceCell(itemBy.get(slug), recordBy.get(slug), displayCurrency)}</td>
        <td class="c-num">${deployment.cpuPercent.toFixed(1)}%</td>
        <td class="c-num">${deployment.memoryMb.toFixed(0)} MB</td>
        <td class="c-pay">${payoutCell(itemBy.get(slug))}</td>
        <td class="c-act">
          <div class="action-btns">
            ${rowButton("collect", slug, "Collect balance now")}
            ${rowButton("logs", slug, "Logs")}
            ${running ? rowButton("stop", slug, "Stop") : rowButton("start", slug, "Start")}
            ${rowButton("remove", slug, "Remove")}
          </div>
        </td>
      </tr>
      ${copyRows(name, copies)}
      ${nodeDetail ? `<tr class="detail-row"><td colspan="${SERVICE_COLUMNS}">${nodeDetail}</td></tr>` : ""}
    `;
  });

  for (const slug of elsewhere) {
    rows.push(`
      <tr class="breakdown-row">
        <td class="c-name"><span class="svc-name">${escapeHtml(nameOf(slug))}</span><div class="cell-sub">not run by this machine</div></td>
        <td><span class="badge badge-external"><span class="status-dot external"></span>Elsewhere</span></td>
        <td class="c-num"><span class="muted">&mdash;</span></td>
        <td class="c-num c-bal">${balanceCell(itemBy.get(slug), recordBy.get(slug), displayCurrency)}</td>
        <td class="c-num"><span class="muted">&mdash;</span></td>
        <td class="c-num"><span class="muted">&mdash;</span></td>
        <td class="c-pay">${payoutCell(itemBy.get(slug))}</td>
        <td class="c-act"><div class="action-btns">${rowButton("collect", slug, "Collect balance now")}</div></td>
      </tr>
    `);
  }

  return `
    <table class="breakdown-table services-table">
      <thead>
        <tr>
          <th>Service</th>
          <th>Status</th>
          <th class="c-num">Health</th>
          <th class="c-num">Balance</th>
          <th class="c-num">CPU</th>
          <th class="c-num">Memory</th>
          <th class="c-pay">Payout</th>
          <th class="c-act">Actions</th>
        </tr>
      </thead>
      <tbody>${rows.join("")}</tbody>
    </table>
  `;
}
