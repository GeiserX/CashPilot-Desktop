// The Activity page: what each earner container does on the network. It
// follows the web console's Activity page (GeiserX/CashPilot, docs/activity.md)
// with one machine instead of a fleet.
//
// Pure: takes the view GetActivity returns (and any expanded rows' details),
// gives back HTML. The rule it keeps: a count with no reading (the first window
// after switching on, a host-network earner's counters, a failed helper) renders
// as an em dash, never 0. A 0 would claim no connections were opened.

import type { ActivityDetail, ActivityRow, ActivityTop, ActivityView } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";
import type { RuntimeInput } from "./runtime_state.js";

export const UNKNOWN = "—";

/** A row status's chip label and its title, or undefined for a status this file does not know. */
function statusText(status: string): [string, string] | undefined {
  switch (status) {
    case "ok": return [t("activity.status.ok"), ""];
    case "host_network": return [t("activity.status.hostNetwork"), t("activity.status.hostNetworkTitle")];
    case "no_network": return [t("activity.status.noNetwork"), t("activity.status.noNetworkTitle")];
    case "sandboxed": return [t("activity.status.sandboxed"), t("activity.status.sandboxedTitle")];
    case "probe_failed": return [t("activity.status.probeFailed"), t("activity.status.probeFailedTitle")];
    case "skipped_budget": return [t("activity.status.skipped"), t("activity.status.skippedTitle")];
    case "native": return [t("activity.status.native"), t("activity.status.nativeTitle")];
    default: return undefined;
  }
}

/** Why a host-network earner could not be told apart, in plain words, or "". */
export function cgroupReason(reason: string): string {
  switch (reason) {
    case "cgroup_v1": return t("activity.cgroup.v1");
    case "cgroup_missing": return t("activity.cgroup.missing");
    case "sock_diag_refused": return t("activity.cgroup.sockDiagRefused");
    case "kernel_no_cgroup_id": return t("activity.cgroup.noCgroupId");
    default: return "";
  }
}

export function hostCgroupNote(): string {
  return t("activity.hostCgroupNote");
}

export function forwardedNote(): string {
  return t("activity.forwardedNote");
}

function classLabel(kind: string): string {
  switch (kind) {
    case "public": return t("activity.class.public");
    case "infra": return t("activity.class.infra");
    case "peer": return t("activity.class.peer");
    case "docker_peer": return t("activity.class.dockerPeer");
    case "lan": return t("activity.class.lan");
    case "tailnet": return t("activity.class.tailnet");
    case "docker_host": return t("activity.class.dockerHost");
    case "metadata": return t("activity.class.metadata");
    case "link_local": return t("activity.class.linkLocal");
    case "multicast": return t("activity.class.multicast");
    default: return "";
  }
}

/** Only outbound rows of these kinds are the earner reaching into your networks. */
const ALARM: Record<string, boolean> = { lan: true, tailnet: true, docker_host: true, docker_peer: true, metadata: true, link_local: true };

/** A count, or the em dash when there was no reading. */
export function num(value: number | null | undefined): string {
  return value === null || value === undefined || !Number.isFinite(value) ? UNKNOWN : String(value);
}

/**
 * An element id for a container's detail box. Injective: every character
 * outside [A-Za-z0-9-], "_" included, becomes "_" and four hex digits, so
 * "a.b" and "a_b" never share a box.
 */
export function domId(container: string): string {
  let out = "activity-detail-";
  for (const ch of container) {
    if (/[A-Za-z0-9-]/.test(ch)) {
      out += ch;
      continue;
    }
    for (let i = 0; i < ch.length; i++) out += "_" + ch.charCodeAt(i).toString(16).padStart(4, "0");
  }
  return out;
}

function isHostNetwork(row: ActivityRow): boolean {
  return row.networkMode === "host" || row.reason === "host_cgroup" || row.status === "host_network" || Boolean(cgroupReason(row.reason));
}

/** The status chip of one row. */
export function statusChip(row: ActivityRow): string {
  if (row.status === "ok" && row.reason === "shared_namespace") {
    return `<span class="activity-chip activity-chip-muted" title="${ht("activity.sharedNamespaceTitle")}">${escapeHtml(
      t("activity.sharedNamespace", { members: (row.members || []).join(", ") }),
    )}</span>`;
  }
  if (row.status === "ok" && row.reason === "host_cgroup") {
    return `<span class="activity-chip activity-chip-info" title="${escapeHtml(forwardedNote())}">${escapeHtml(hostCgroupNote())}</span>`;
  }
  if (row.status === "host_network") {
    const why = cgroupReason(row.reason);
    const text = why ? t("activity.hostNetworkWhy", { why }) : t("activity.status.hostNetwork");
    return `<span class="activity-chip activity-chip-muted" title="${escapeHtml(t("activity.status.hostNetworkTitle"))}">${escapeHtml(text)}</span>`;
  }
  const s = statusText(row.status) || [row.status, ""];
  let text = s[0];
  if (row.members && row.members.length > 1) text = t("activity.sharedWith", { members: row.members.join(", ") });
  if (row.status === "probe_failed" && row.reason) text = t("activity.withReason", { text, reason: row.reason });
  const cls = row.status === "ok" ? "" : ["sandboxed", "no_network", "native"].includes(row.status) ? " activity-chip-muted" : " activity-chip-warn";
  return `<span class="activity-chip${cls}" title="${escapeHtml(s[1])}">${escapeHtml(text)}</span>`;
}

function topList(list: ActivityTop[] | null | undefined, label?: (v: string) => string): string {
  if (!list || !list.length) return `<span class="muted">${UNKNOWN}</span>`;
  return list
    .slice(0, 3)
    .map((t) => `<span class="activity-top">${escapeHtml(label ? label(t.value) : t.value)}</span>`)
    .join(" ");
}

// The last sampling round, which ran up to 5 minutes ago. Its no_runtime says
// Docker was away THEN; the shared Docker status says whether it is NOW.
function round(view: ActivityView, runtime?: Pick<RuntimeInput, "available">): string {
  const r = view.round;
  if (!view.enabled || !r || !r.at) return "";
  if (r.workerStatus === "no_image") return `<span class="activity-chip activity-chip-warn">${ht("activity.noImage")}</span>`;
  if (r.workerStatus === "no_runtime" && runtime?.available) {
    return `<span class="activity-chip activity-chip-muted">${ht("activity.dockerBack")}</span>`;
  }
  if (r.workerStatus === "no_runtime") return `<span class="activity-chip activity-chip-warn">${ht("activity.noDocker")}</span>`;
  if ((r.leftoverProbes || []).length) {
    return `<span class="activity-chip activity-chip-warn" title="${escapeHtml(r.leftoverProbes.join(", "))}">${ht("activity.leftoverProbes", { n: r.leftoverProbes.length })}</span>`;
  }
  return "";
}

/** One row of the containers table, and its detail box when open. */
export function renderActivityRow(row: ActivityRow, detail: ActivityDetail | null | undefined, open: boolean): string {
  const alarm = Object.keys(row.classes || {}).some((k) => ALARM[k]);
  const opens = row.reason === "host_cgroup" || row.status !== "ok" ? null : row.opensLast;
  const fails = row.reason === "host_cgroup" || row.status !== "ok" ? null : row.attemptFailsLast;
  let html =
    `<tr class="activity-row">` +
    `<td><button class="btn btn-link" data-activity-expand="${escapeHtml(row.container)}" aria-expanded="${open ? "true" : "false"}">${escapeHtml(row.container)}</button></td>` +
    `<td>${statusChip(row)}</td>` +
    `<td>${num(opens)}</td>` +
    `<td>${num(row.estMax)}</td>` +
    `<td>${num(fails)}</td>` +
    `<td>${num(row.remotesTotal)}${row.truncated ? "+" : ""}</td>` +
    `<td>${topList(row.topPorts)}</td>` +
    `<td>${topList(row.topAsn)}</td>` +
    `<td>${topList(row.topCc)}</td>` +
    `<td>${row.alerts ? `<span class="activity-chip activity-chip-warn">${row.alerts}</span>` : alarm ? `<span class="activity-chip activity-chip-warn">${ht("activity.lanAlert")}</span>` : ""}</td>` +
    `</tr>`;
  if (open) {
    html += `<tr class="activity-detail-row"><td colspan="10"><div id="${domId(row.container)}" class="activity-detail">${
      detail ? renderActivityDetail(detail, isHostNetwork(row)) : `<p class="muted">${ht("activity.loading")}</p>`
    }</div></td></tr>`;
  }
  return html;
}

function dimTable(title: string, rows: ActivityTop[], label?: (v: string) => string): string {
  if (!rows || !rows.length) return `<div><h3 class="activity-h3">${escapeHtml(title)}</h3><p class="muted">${ht("activity.none")}</p></div>`;
  return `<div><h3 class="activity-h3">${escapeHtml(title)}</h3><table class="activity-dim"><tbody>${rows
    .map((r) => `<tr><td>${escapeHtml(label ? label(r.value) : r.value || t("activity.unknown"))}</td><td>${escapeHtml(String(r.sightings))}</td></tr>`)
    .join("")}</tbody></table></div>`;
}

/** The expanded view of one container. */
export function renderActivityDetail(d: ActivityDetail, hostNetwork: boolean): string {
  const raw = d.raw || [];
  const shown = raw.slice(0, 500);
  return (
    (hostNetwork ? `<p class="muted">${escapeHtml(forwardedNote())}</p>` : "") +
    `<div class="activity-dims">` +
    dimTable(t("activity.ports"), d.ports) +
    dimTable(t("activity.networks"), d.asn) +
    dimTable(t("activity.countryRegistered"), d.cc) +
    dimTable(t("activity.kindOfDestination"), d.classes, (v) => classLabel(v) || v) +
    `</div>` +
    `<details class="activity-raw"><summary>${ht("activity.rawSummary", { hours: d.rawHours, count: raw.length })}</summary>` +
    (shown.length
      ? `<table class="activity-dim"><thead><tr><th>${ht("activity.col.window")}</th><th>${ht("activity.col.direction")}</th><th>${ht("activity.col.address")}</th><th>${ht("activity.col.port")}</th><th>${ht("activity.col.kind")}</th><th>${ht("activity.col.network")}</th><th>${ht("activity.col.country")}</th></tr></thead><tbody>` +
        shown
          .map(
            (r) =>
              `<tr><td>${escapeHtml(new Date(r.windowEnd * 1000).toISOString().slice(11, 16))}</td><td>${r.dir === "in" ? ht("activity.dirFrom") : ht("activity.dirTo")}</td><td>${escapeHtml(r.ip)}</td><td>${escapeHtml(
                `${r.proto}/${r.port}`,
              )}</td><td>${escapeHtml(classLabel(r.class) || r.class)}</td><td>${escapeHtml(r.asn || UNKNOWN)}</td><td>${escapeHtml(r.cc || UNKNOWN)}</td></tr>`,
          )
          .join("") +
        `</tbody></table>` +
        (raw.length > 500 ? `<p class="muted">${ht("activity.newest500", { n: raw.length })}</p>` : "")
      : `<p class="muted">${ht("activity.none")}</p>`) +
    `</details>`
  );
}

/** The whole page body under the title. open maps an expanded container to its detail (null while loading). */
export function renderActivityPage(view: ActivityView, open: Record<string, ActivityDetail | null>, runtime?: Pick<RuntimeInput, "available">): string {
  const nets = view.networks || ({ present: false } as ActivityView["networks"]);
  const days = [1, 7, 30]
    .map((d) => `<option value="${d}"${view.days === d ? " selected" : ""}>${d === 1 ? ht("activity.24hours") : ht("activity.days", { n: d })}</option>`)
    .join("");
  const head =
    `<section class="card activity-intro"><div class="card-header"><span class="card-title">${ht("activity.title")}</span></div>` +
    `<p class="muted">${ht("activity.intro")}</p>` +
    `<div class="activity-controls">` +
    `<label class="activity-switch"><input type="checkbox" id="activity-toggle"${view.enabled ? " checked" : ""}> ${view.enabled ? ht("activity.on") : ht("activity.off")}</label>` +
    `<select id="activity-days">${days}</select>` +
    `<button class="secondary compact-btn" id="activity-delete">${ht("activity.deleteData")}</button>` +
    round(view, runtime) +
    `</div>` +
    `<p class="muted activity-asn">${
      nets.present
        ? `${ht("activity.networkTable", { rows: String(nets.rows ?? UNKNOWN), age: nets.ageSeconds == null ? UNKNOWN : String(Math.round(nets.ageSeconds / 86400)) })} `
        : `${ht("activity.noNetworkTable")} `
    }<button class="btn btn-link" id="activity-networks">${ht("activity.downloadNow")}</button></p>` +
    `</section>`;
  const rows = view.containers || [];
  if (!rows.length) {
    return (
      head +
      `<p class="muted">${
        view.enabled
          ? ht("activity.noWindows")
          : ht("activity.isOff")
      }</p>`
    );
  }
  return (
    head +
    `<section class="card"><div class="card-header"><span class="card-title">${ht("activity.containers")}</span><span class="muted">${ht("activity.containersNote")}</span></div>` +
    `<div class="table-scroll"><table class="activity-table"><thead><tr>` +
    `<th scope="col">${ht("activity.col.container")}</th><th scope="col">${ht("activity.col.status")}</th><th scope="col">${ht("activity.col.opens")}</th><th scope="col">${ht("activity.col.peak")}</th><th scope="col">${ht("activity.col.failed")}</th>` +
    `<th scope="col">${ht("activity.col.destinations")}</th><th scope="col">${ht("activity.col.topPorts")}</th><th scope="col">${ht("activity.col.topNetworks")}</th><th scope="col" title="${ht("activity.col.countryTitle")}">${ht("activity.countryRegistered")}</th><th scope="col">${ht("activity.col.alerts")}</th>` +
    `</tr></thead><tbody>` +
    rows.map((r) => {
      // Own keys only: a container named "constructor" is not an open row.
      const isOpen = Object.hasOwn(open, r.container);
      return renderActivityRow(r, isOpen ? open[r.container] : null, isOpen);
    }).join("") +
    `</tbody></table></div></section>`
  );
}
