// What this machine's containers use over time: three small line charts under
// "Earned per day" (CPU, memory, network down and up), with a container picker.
// The design follows the web console's usage graphs (GeiserX/CashPilot#515).
//
// Pure: takes the view GetUsage returns, gives back HTML. No canvas, no chart
// library, so it renders the same offline and in the Node harness.
//
// The rule these charts keep: a missing reading is a GAP in the line, never a
// drop to 0. An hour with no reading (the machine asleep, Docker stopped) did not
// use nothing; we just do not know. And a total that leaves out a container whose
// traffic cannot be read (host networking) is a minimum, shown with ≥.

import type { UsageView } from "../wails";
import { niceMax } from "./chart.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { escapeHtml } from "./format.js";
import { ht, intlLocale, t } from "./i18n.js";

type Series = (number | null)[];

/** "12.5%", where 100% is one core. */
export function formatCpu(percent: number): string {
  return `${percent >= 10 ? percent.toFixed(1) : percent.toFixed(2)}%`;
}

/** "512 MB" or "1.25 GB". */
export function formatMemory(mb: number): string {
  return mb >= 1024 ? `${(mb / 1024).toFixed(2)} GB` : `${mb.toFixed(mb >= 100 ? 0 : 1)} MB`;
}

/** Bytes as "3.4 MB". */
export function formatBytes(bytes: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit++;
  }
  return `${value.toFixed(unit === 0 ? 0 : value >= 100 ? 0 : 1)} ${units[unit]}`;
}

/** Bytes per second as "3.4 MB/s". */
export function formatRate(bytesPerSecond: number): string {
  return `${formatBytes(bytesPerSecond)}/s`;
}

/** A point's start in local time, "Nov 2 14:00". */
export function pointLabel(unixSeconds: number): string {
  const d = new Date(unixSeconds * 1000);
  const month = new Intl.DateTimeFormat(intlLocale(), { month: "short" }).format(d);
  const time = `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  return t("usage.pointLabel", { month, day: String(d.getDate()), time });
}

/**
 * The SVG path of one line, broken at every null: each run of readings is its
 * own "M ... L ..." segment, so a missing hour is a gap and never a dip to 0.
 * A reading with no neighbour on either side cannot draw a segment, so it is
 * returned in `dots` to be drawn as a dot rather than vanish.
 */
export function linePath(values: Series, x: (index: number) => number, y: (value: number) => number): { path: string; dots: number[] } {
  const parts: string[] = [];
  const dots: number[] = [];
  let run: number[] = [];
  const flush = () => {
    if (run.length === 1) dots.push(run[0]);
    if (run.length > 1) {
      parts.push(run.map((index, n) => `${n === 0 ? "M" : "L"}${x(index).toFixed(1)} ${y(values[index] as number).toFixed(1)}`).join(" "));
    }
    run = [];
  };
  values.forEach((value, index) => {
    if (value === null || value === undefined || !Number.isFinite(value)) {
      flush();
    } else {
      run.push(index);
    }
  });
  flush();
  return { path: parts.join(" "), dots };
}

function lastIndex(values: Series): number {
  for (let i = values.length - 1; i >= 0; i--) {
    if (values[i] !== null && values[i] !== undefined) return i;
  }
  return -1;
}

function peak(values: Series): number | null {
  const known = values.filter((v): v is number => v !== null && v !== undefined && Number.isFinite(v));
  return known.length ? Math.max(...known) : null;
}

interface Line {
  label: string;
  values: Series;
  className: string;
}

interface ChartSpec {
  id: string;
  title: string;
  lines: Line[];
  format: (value: number) => string;
  // Whether point i's value is a minimum (a container left out of the total).
  partial: boolean[];
  // Extra tooltip text for point i, after the values.
  extra?: (index: number) => string;
}

function renderLineChart(view: UsageView, spec: ChartSpec): string {
  const width = 400;
  const height = 150;
  const left = 70;
  const right = 6;
  const top = 8;
  const bottom = 22;
  const plotW = width - left - right;
  const plotH = height - top - bottom;
  const n = view.hours.length;
  const max = niceMax(Math.max(0, ...spec.lines.map((line) => peak(line.values) ?? 0)));
  const x = (index: number) => left + (n > 1 ? (index / (n - 1)) * plotW : plotW / 2);
  const y = (value: number) => top + plotH - (Math.max(0, value) / max) * plotH;
  const mark = (index: number) => (spec.partial[index] ? "≥ " : "");

  const grid = [0, 0.5, 1].map((f) => {
    const gy = y(max * f);
    return `<line class="chart-grid" x1="${left}" y1="${gy.toFixed(1)}" x2="${width - right}" y2="${gy.toFixed(1)}"/>
      <text class="usage-axis" x="${left - 6}" y="${(gy + 4).toFixed(1)}" text-anchor="end">${escapeHtml(spec.format(max * f))}</text>`;
  }).join("");
  const ends = n > 0
    ? `<text class="usage-axis" x="${left}" y="${height - 6}" text-anchor="start">${escapeHtml(pointLabel(view.hours[0]))}</text>
       <text class="usage-axis" x="${width - right}" y="${height - 6}" text-anchor="end">${escapeHtml(pointLabel(view.hours[n - 1]))}</text>`
    : "";
  const lines = spec.lines.map((line) => {
    const { path, dots } = linePath(line.values, x, y);
    return `${path ? `<path class="usage-line ${line.className}" d="${path}"/>` : ""}${dots.map((index) => `<circle class="usage-dot ${line.className}" cx="${x(index).toFixed(1)}" cy="${y(line.values[index] as number).toFixed(1)}" r="2.5"/>`).join("")}`;
  }).join("");

  // One hover column per point that has a reading, with the exact values; a gap
  // gets none, so hovering it says nothing rather than "0".
  const slot = n > 1 ? plotW / (n - 1) : plotW;
  const hovers = view.hours.map((hour, index) => {
    const known = spec.lines.filter((line) => line.values[index] !== null && line.values[index] !== undefined);
    if (!known.length) return "";
    const values = known.map((line) => `${line.label} ${mark(index)}${spec.format(line.values[index] as number)}`).join(", ");
    const extra = spec.extra ? spec.extra(index) : "";
    const tip = `${pointLabel(hour)}: ${values}${extra ? `, ${extra}` : ""}`;
    return `<rect class="usage-hover" x="${(x(index) - slot / 2).toFixed(1)}" y="${top}" width="${slot.toFixed(1)}" height="${plotH}"><title>${escapeHtml(tip)}</title></rect>`;
  }).join("");

  // Caption: the latest reading as text, "—" when there is none.
  const caption = spec.lines.map((line) => {
    const i = lastIndex(line.values);
    // With two lines, the label takes its line's colour: it is the legend.
    const label = spec.lines.length > 1 ? `<span class="usage-key ${line.className}">${escapeHtml(line.label)}</span> ` : "";
    return `${label}${escapeHtml(i < 0 ? "—" : mark(i) + spec.format(line.values[i] as number))}`;
  }).join(" · ");
  const aria = spec.lines.map((line) => {
    const i = lastIndex(line.values);
    const top = peak(line.values);
    return t("usage.lineAria", {
      label: line.label,
      latest: i < 0 ? t("usage.noReading") : mark(i) + spec.format(line.values[i] as number),
      peak: top === null ? t("usage.noReading") : spec.format(top),
    });
  }).join("; ");

  return `
    <figure class="usage-chart" data-usage-chart="${spec.id}">
      <figcaption><span class="usage-title">${escapeHtml(spec.title)}</span> <span class="usage-latest">${caption}</span></figcaption>
      <svg viewBox="0 0 ${width} ${height}" role="img" aria-label="${escapeHtml(t("usage.chartAria", { title: spec.title, days: view.days, aria }))}">
        ${grid}${ends}${lines}${hovers}
      </svg>
    </figure>
  `;
}

/**
 * The whole panel: the picker and the three charts, or one line saying why
 * there is nothing to draw. `view` null means it has not loaded yet.
 */
export function renderUsagePanel(view: UsageView | null): string {
  if (!view) {
    return `<div class="chart-empty"><span>${ht("usage.loading")}</span></div>`;
  }
  const containers = view.containers || [];
  const hasReading = [view.cpuPercent, view.memoryMb, view.rxRate, view.txRate].some((series) => series.some((v) => v !== null && v !== undefined));
  if (!containers.length || !hasReading) {
    return `
      <div class="chart-empty">
        <strong>${ht("usage.empty", { days: view.days })}</strong>
        <span>${ht("usage.emptyHint")}</span>
      </div>
    `;
  }
  const options = [`<option value=""${view.container === "" ? " selected" : ""}>${ht("usage.allContainers")}</option>`]
    .concat(containers.map((c) => {
      const label = c.service && c.service !== c.name ? `${c.service} (${c.name})` : c.name;
      return `<option value="${escapeHtml(c.name)}"${view.container === c.name ? " selected" : ""}>${escapeHtml(label)}</option>`;
    }))
    .join("");
  const moved = (index: number) => {
    const rx = view.rxBytes[index];
    const tx = view.txBytes[index];
    if (rx === null || rx === undefined || tx === null || tx === undefined) return "";
    return `${view.trafficPartial[index] ? "≥ " : ""}${t("usage.moved", { down: formatBytes(rx), up: formatBytes(tx) })}`;
  };
  const picked = containers.find((c) => c.name === view.container);
  const unmeasured = picked ? (picked.noCounters ? [picked] : []) : containers.filter((c) => c.noCounters);
  const note = unmeasured.length
    ? `<p class="block-meta usage-note">${picked
      ? ht("usage.noCounters")
      : ht("usage.leftOut", { names: unmeasured.map((c) => c.service || c.name).join(", ") })}</p>`
    : "";
  return `
    <div class="usage-controls">
      <label class="block-meta" for="usage-container">${ht("usage.container")}</label>
      <select id="usage-container">${options}</select>
    </div>
    <div class="usage-grid">
      ${renderLineChart(view, {
        id: "cpu",
        title: t("usage.cpu"),
        lines: [{ label: t("usage.cpu"), values: view.cpuPercent, className: "usage-line-main" }],
        format: formatCpu,
        partial: view.cpuPartial,
      })}
      ${renderLineChart(view, {
        id: "memory",
        title: t("usage.memory"),
        lines: [{ label: t("usage.memory"), values: view.memoryMb, className: "usage-line-main" }],
        format: formatMemory,
        partial: view.memoryPartial,
      })}
      ${renderLineChart(view, {
        id: "network",
        title: t("usage.network"),
        lines: [
          { label: t("usage.down"), values: view.rxRate, className: "usage-line-main" },
          { label: t("usage.up"), values: view.txRate, className: "usage-line-alt" },
        ],
        format: formatRate,
        partial: view.trafficPartial,
        extra: moved,
      })}
    </div>
    ${note}
  `;
}
