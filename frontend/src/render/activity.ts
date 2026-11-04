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

export const UNKNOWN = "—";

const STATUS: Record<string, [string, string]> = {
  ok: ["ok", ""],
  host_network: ["host network", "Shares the machine's network, and its connections could not be told apart from the machine's."],
  no_network: ["no network", "Runs with networking off."],
  sandboxed: ["sandboxed", "Runs under a sandboxed runtime (such as gVisor), whose network a helper cannot join."],
  probe_failed: ["could not sample", "The helper did not produce a reading this round."],
  skipped_budget: ["skipped", "The round ran out of time before reaching this container."],
  native: ["native program", "Runs as a program on this machine, not in a container, so no helper can join its network."],
};

/** Why a host-network earner could not be told apart, in plain words. */
export const CGROUP_REASONS: Record<string, string> = {
  cgroup_v1: "this machine uses cgroup v1; a host on cgroup v2 is needed",
  cgroup_missing: "its cgroup was not found",
  sock_diag_refused: "the kernel refused the socket list",
  kernel_no_cgroup_id: "this kernel does not report a socket's cgroup",
};

export const HOST_CGROUP_NOTE =
  "host network, matched by cgroup: connections already closed (TIME_WAIT) are not seen; connection counters are machine-wide and not shown";
export const FORWARDED_NOTE =
  "Traffic an earner routes for VPN customers is forwarded by the kernel, not opened by the earner, and is not shown.";

const CLASS_LABEL: Record<string, string> = {
  public: "internet",
  infra: "DNS / Docker",
  peer: "another earner",
  docker_peer: "another container",
  lan: "your LAN",
  tailnet: "tailnet",
  docker_host: "Docker host",
  metadata: "cloud metadata",
  link_local: "link-local",
  multicast: "multicast",
};

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
  return row.networkMode === "host" || row.reason === "host_cgroup" || row.status === "host_network" || Boolean(CGROUP_REASONS[row.reason]);
}

/** The status chip of one row. */
export function statusChip(row: ActivityRow): string {
  if (row.status === "ok" && row.reason === "shared_namespace") {
    return `<span class="activity-chip activity-chip-muted" title="This network belongs to a container that is not an earner (a VPN or Tailscale sidecar), which other apps may share. Only the counters are kept; no destinations and no alerts.">${escapeHtml(
      "network of this container, used by " + (row.members || []).join(", "),
    )}</span>`;
  }
  if (row.status === "ok" && row.reason === "host_cgroup") {
    return `<span class="activity-chip activity-chip-info" title="${escapeHtml(FORWARDED_NOTE)}">${escapeHtml(HOST_CGROUP_NOTE)}</span>`;
  }
  if (row.status === "host_network") {
    const why = CGROUP_REASONS[row.reason];
    const text = why ? `host network: cannot be told apart (${why})` : "host network";
    return `<span class="activity-chip activity-chip-muted" title="${escapeHtml(STATUS.host_network[1])}">${escapeHtml(text)}</span>`;
  }
  const s = STATUS[row.status] || [row.status, ""];
  let text = s[0];
  if (row.members && row.members.length > 1) text = "shared with " + row.members.join(", ");
  if (row.status === "probe_failed" && row.reason) text += ` (${row.reason})`;
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

function round(view: ActivityView): string {
  const r = view.round;
  if (!view.enabled || !r || !r.at) return "";
  if (r.workerStatus === "no_image") return `<span class="activity-chip activity-chip-warn">cannot sample: the helper image could not be pulled</span>`;
  if (r.workerStatus === "no_runtime") return `<span class="activity-chip activity-chip-warn">cannot sample: Docker is not reachable</span>`;
  if ((r.leftoverProbes || []).length) {
    return `<span class="activity-chip activity-chip-warn" title="${escapeHtml(r.leftoverProbes.join(", "))}">${r.leftoverProbes.length} helper(s) Docker would not remove</span>`;
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
    `<td>${row.alerts ? `<span class="activity-chip activity-chip-warn">${row.alerts}</span>` : alarm ? `<span class="activity-chip activity-chip-warn">LAN</span>` : ""}</td>` +
    `</tr>`;
  if (open) {
    html += `<tr class="activity-detail-row"><td colspan="10"><div id="${domId(row.container)}" class="activity-detail">${
      detail ? renderActivityDetail(detail, isHostNetwork(row)) : `<p class="muted">Loading</p>`
    }</div></td></tr>`;
  }
  return html;
}

function dimTable(title: string, rows: ActivityTop[], label?: (v: string) => string): string {
  if (!rows || !rows.length) return `<div><h3 class="activity-h3">${escapeHtml(title)}</h3><p class="muted">none</p></div>`;
  return `<div><h3 class="activity-h3">${escapeHtml(title)}</h3><table class="activity-dim"><tbody>${rows
    .map((r) => `<tr><td>${escapeHtml(label ? label(r.value) : r.value || "unknown")}</td><td>${escapeHtml(String(r.sightings))}</td></tr>`)
    .join("")}</tbody></table></div>`;
}

/** The expanded view of one container. */
export function renderActivityDetail(d: ActivityDetail, hostNetwork: boolean): string {
  const raw = d.raw || [];
  const shown = raw.slice(0, 500);
  return (
    (hostNetwork ? `<p class="muted">${escapeHtml(FORWARDED_NOTE)}</p>` : "") +
    `<div class="activity-dims">` +
    dimTable("Ports", d.ports) +
    dimTable("Networks", d.asn) +
    dimTable("Country (registered)", d.cc) +
    dimTable("Kind of destination", d.classes, (v) => CLASS_LABEL[v] || v) +
    `</div>` +
    `<details class="activity-raw"><summary>Destinations of the last ${escapeHtml(String(d.rawHours))} hours (${raw.length})</summary>` +
    (shown.length
      ? `<table class="activity-dim"><thead><tr><th>Window</th><th>Direction</th><th>Address</th><th>Port</th><th>Kind</th><th>Network</th><th>Country</th></tr></thead><tbody>` +
        shown
          .map(
            (r) =>
              `<tr><td>${escapeHtml(new Date(r.windowEnd * 1000).toISOString().slice(11, 16))}</td><td>${r.dir === "in" ? "from" : "to"}</td><td>${escapeHtml(r.ip)}</td><td>${escapeHtml(
                `${r.proto}/${r.port}`,
              )}</td><td>${escapeHtml(CLASS_LABEL[r.class] || r.class)}</td><td>${escapeHtml(r.asn || UNKNOWN)}</td><td>${escapeHtml(r.cc || UNKNOWN)}</td></tr>`,
          )
          .join("") +
        `</tbody></table>` +
        (raw.length > 500 ? `<p class="muted">Showing the newest 500 of ${raw.length} rows.</p>` : "")
      : `<p class="muted">none</p>`) +
    `</details>`
  );
}

/** The whole page body under the title. open maps an expanded container to its detail (null while loading). */
export function renderActivityPage(view: ActivityView, open: Record<string, ActivityDetail | null>): string {
  const nets = view.networks || ({ present: false } as ActivityView["networks"]);
  const days = [1, 7, 30]
    .map((d) => `<option value="${d}"${view.days === d ? " selected" : ""}>${d === 1 ? "24 hours" : `${d} days`}</option>`)
    .join("");
  const head =
    `<section class="card activity-intro"><div class="card-header"><span class="card-title">What each earner talks to</span></div>` +
    `<p class="muted">Every 5 minutes a short-lived helper reads which addresses and ports each earner is connected to, from the kernel's socket tables. It never reads what is inside the traffic. Remote addresses are kept 24 hours; counts and totals 35 days. Switching off deletes the addresses at once.</p>` +
    `<div class="activity-controls">` +
    `<label class="activity-switch"><input type="checkbox" id="activity-toggle"${view.enabled ? " checked" : ""}> ${view.enabled ? "on" : "off"}</label>` +
    `<select id="activity-days">${days}</select>` +
    `<button class="secondary compact-btn" id="activity-delete">Delete data</button>` +
    round(view) +
    `</div>` +
    `<p class="muted activity-asn">${
      nets.present
        ? `Network table: ${escapeHtml(String(nets.rows ?? UNKNOWN))} ranges, ${Math.round((nets.ageSeconds || 0) / 86400)} days old. `
        : "Network table not downloaded, so networks and countries read as unknown. "
    }<button class="btn btn-link" id="activity-networks">Download now</button></p>` +
    `</section>`;
  const rows = view.containers || [];
  if (!rows.length) {
    return (
      head +
      `<p class="muted">${
        view.enabled
          ? "No windows yet. The first one arrives within 5 minutes of switching on; connection counts start with the second."
          : "Activity is off."
      }</p>`
    );
  }
  return (
    head +
    `<section class="card"><div class="card-header"><span class="card-title">Containers</span><span class="muted">Opens and failures are per 5 minutes. Peak is the most connections open at once.</span></div>` +
    `<div class="table-scroll"><table class="activity-table"><thead><tr>` +
    `<th scope="col">Container</th><th scope="col">Status</th><th scope="col">Opens</th><th scope="col">Peak open</th><th scope="col">Failed</th>` +
    `<th scope="col">Destinations</th><th scope="col">Top ports</th><th scope="col">Top networks</th><th scope="col" title="Country where the network is registered">Country (registered)</th><th scope="col">Alerts</th>` +
    `</tr></thead><tbody>` +
    rows.map((r) => renderActivityRow(r, open[r.container], r.container in open)).join("") +
    `</tbody></table></div></section>`
  );
}
