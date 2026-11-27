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
import { ht, t } from "./i18n.js";
import { renderHealthBadge, renderProducerBadge } from "./health.js";
import { customImageBadge } from "./image.js";
import { copyRows, firstCopyLabel } from "./instances.js";
import { renderMystNodes } from "./myst.js";
import { rowButton } from "./actions.js";
import { containerState } from "./attention.js";
import { customBadge } from "./custom.js";
import { outdatedBadge } from "./catalog.js";
import { walletDetail } from "./wallet.js";
import { dockerBlocked, type RuntimeInput } from "./runtime_state.js";

export const SERVICE_COLUMNS = 8;

export interface ServicesTableInput {
  services: Service[];
  deployments: Deployment[];
  earnings: { platform: string; balance: number; currency: string; error?: string }[];
  breakdown?: ServiceEarning[] | null;
  health?: Record<string, HealthScore> | null;
  serviceDetails?: Record<string, string> | null;
  outdated?: string[] | null;
  /** Per slug, how its container differs from what a redeploy would build. */
  drift?: Record<string, string[]> | null;
  producerStates?: Record<string, ProducerReport> | null;
  copies?: Record<string, ServiceCopy[]> | null;
  displayCurrency: string;
  /** The shared Docker status: Start on a container waits for Docker. */
  runtime?: Pick<RuntimeInput, "available" | "reason">;
}

/** Status as a square and a word, the web console's form. */
export function statusCell(status: string, extra = ""): string {
  const st = containerState(status);
  const cls = st === "ok" ? "running" : st === "fail" ? "stopped" : "unknown";
  return `<span class="badge badge-${cls}"><span class="status-dot ${cls}"></span>${escapeHtml(status || t("services.statusUnknown"))}</span>${extra}`;
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
  pool = false,
): string {
  const error = item?.error || record?.error;
  if (error) {
    return `<span title="${escapeHtml(error)}">&mdash;</span><div class="cell-sub cell-sub-warn">${ht("services.noReading")}</div>`;
  }
  if (item) {
    const native = formatBalance(item.balance, item.currency, pool);
    // balanceDisplay of 0 on a convertible platform is a missing rate, not a
    // zero balance: fall back to the native figure and name its currency.
    if (item.convertible && item.balanceDisplay !== 0) {
      const sameUnit = item.currency.toUpperCase() === displayCurrency.toUpperCase();
      return `${escapeHtml(formatBalance(item.balanceDisplay, displayCurrency))}${sameUnit ? "" : `<div class="cell-sub">${escapeHtml(native)}</div>`}`;
    }
    // A balance that is really zero is zero in any currency: no note.
    if (item.convertible && item.balance === 0) return escapeHtml(native);
    return `${escapeHtml(native)}<div class="cell-sub">${item.convertible ? ht("services.noRate") : ht("services.notConverted")}</div>`;
  }
  if (record) return escapeHtml(formatBalance(record.balance, record.currency, pool));
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
    <div class="payout-progress" title="${ht("services.payoutTitle", { pct: pct.toFixed(0), min })}">
      <div class="payout-progress-bar${cashout.eligible ? " eligible" : ""}" style="width: ${pct.toFixed(1)}%"></div>
    </div>
    <span class="payout-label">${cashout.eligible ? ht("services.ready") : `${pct.toFixed(0)}%`}</span>
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
        <p class="empty-state-title">${ht("services.empty.title")}</p>
        <p class="empty-state-text">${ht("services.empty.text")}</p>
        <button class="btn btn-primary" id="open-wizard-empty">${ht("services.empty.add")}</button>
      </div>
    `;
  }
  const outdatedSet = new Set(input.outdated || []);
  const blocked = input.runtime ? dockerBlocked(input.runtime) : "";
  const nameOf = (slug: string) => services.find((svc) => svc.slug === slug)?.name || itemBy.get(slug)?.name || slug;

  const rows = deployments.map((deployment) => {
    const slug = deployment.slug;
    const name = nameOf(slug);
    const service = services.find((svc) => svc.slug === slug);
    const custom = Boolean(service?.custom);
    const copies = input.copies?.[slug];
    const running = deployment.status === "running";
    // The provider refuses the client: starting it again cannot help.
    const disabledAccount = input.producerStates?.[slug]?.state === "account_disabled";
    const nodeDetail = slug === "mysterium" ? renderMystNodes(input.serviceDetails?.["mysterium"]) : walletDetail(service, deployment);
    const tags = [
      renderProducerBadge(input.producerStates?.[slug], deployment.status),
      customBadge(service),
      customImageBadge(deployment.customImage),
      outdatedSet.has(slug) ? outdatedBadge(service) : "",
      input.drift?.[slug]?.length ? `<span class="badge warn" title="${ht("services.drift.title", { diffs: input.drift[slug].join("; ") })}">${ht("services.drift.badge")}</span>` : "",
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
        <td class="c-num c-bal">${custom ? `<span class="muted" title="${ht("services.custom.title")}">&mdash;</span><div class="cell-sub">${ht("services.custom.noCollector")}</div>` : balanceCell(itemBy.get(slug), recordBy.get(slug), displayCurrency, service?.collector?.accrual === "pool")}</td>
        <td class="c-num">${deployment.cpuPercent.toFixed(1)}%</td>
        <td class="c-num">${deployment.memoryMb.toFixed(0)} MB</td>
        <td class="c-pay">${payoutCell(itemBy.get(slug))}</td>
        <td class="c-act">
          <div class="action-btns">
            ${custom ? "" : rowButton("collect", slug, t("services.action.collect"))}
            ${rowButton("logs", slug, t("services.action.logs"))}
            ${running ? rowButton("stop", slug, t("services.action.stop")) : disabledAccount ? "" : rowButton("start", slug, t("services.action.start"), 1, deployment.runtime === "native" ? "" : blocked)}
            ${rowButton("remove", slug, t("services.action.remove"))}
          </div>
        </td>
      </tr>
      ${copyRows(name, copies, disabledAccount, blocked)}
      ${nodeDetail ? `<tr class="detail-row"><td colspan="${SERVICE_COLUMNS}">${nodeDetail}</td></tr>` : ""}
    `;
  });

  for (const slug of elsewhere) {
    rows.push(`
      <tr class="breakdown-row">
        <td class="c-name"><span class="svc-name">${escapeHtml(nameOf(slug))}</span><div class="cell-sub">${ht("services.elsewhere.sub")}</div></td>
        <td><span class="badge badge-external"><span class="status-dot external"></span>${ht("services.elsewhere.badge")}</span></td>
        <td class="c-num"><span class="muted">&mdash;</span></td>
        <td class="c-num c-bal">${balanceCell(itemBy.get(slug), recordBy.get(slug), displayCurrency, services.find((svc) => svc.slug === slug)?.collector?.accrual === "pool")}</td>
        <td class="c-num"><span class="muted">&mdash;</span></td>
        <td class="c-num"><span class="muted">&mdash;</span></td>
        <td class="c-pay">${payoutCell(itemBy.get(slug))}</td>
        <td class="c-act"><div class="action-btns">${rowButton("collect", slug, t("services.action.collect"))}</div></td>
      </tr>
    `);
  }

  return `
    <table class="breakdown-table services-table">
      <thead>
        <tr>
          <th>${ht("services.col.service")}</th>
          <th>${ht("services.col.status")}</th>
          <th class="c-num">${ht("services.col.health")}</th>
          <th class="c-num">${ht("services.col.balance")}</th>
          <th class="c-num">${ht("services.col.cpu")}</th>
          <th class="c-num">${ht("services.col.memory")}</th>
          <th class="c-pay">${ht("services.col.payout")}</th>
          <th class="c-act">${ht("services.col.actions")}</th>
        </tr>
      </thead>
      <tbody>${rows.join("")}</tbody>
    </table>
  `;
}
