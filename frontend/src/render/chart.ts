// "Earned per day": the daily earnings as bars, like the web console's chart.
//
// Pure: takes the daily points, returns an SVG string. No canvas and no chart
// library, so it renders the same offline and in the Node harness.

import type { DailyPoint } from "../wails";
import { escapeHtml, formatBalance } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** The last `days` points that carry a real number. */
export function chartWindow(points: DailyPoint[] | null | undefined, days: number): DailyPoint[] {
  const data = (points || []).filter((point) => Number.isFinite(point.amount));
  return days > 0 ? data.slice(-days) : data;
}

/** A round axis top at or above `max`: 1, 2, 2.5 or 5 times a power of ten. */
export function niceMax(max: number): number {
  if (!(max > 0)) return 1;
  const pow = 10 ** Math.floor(Math.log10(max));
  for (const step of [1, 2, 2.5, 5, 10]) {
    if (step * pow >= max) return step * pow;
  }
  return 10 * pow;
}

export function renderEarningsBars(points: DailyPoint[] | null | undefined, displayCurrency: string, days: number): string {
  const data = chartWindow(points, days);
  // No day with money in it is "nothing collected yet", not a flat line at
  // zero: a row of empty bars would read as days that earned nothing.
  if (data.length === 0 || !data.some((point) => point.amount > 0)) {
    return `
      <div class="chart-empty">
        <strong>No earnings collected yet</strong>
        <span>Once collectors run, daily earnings in ${escapeHtml(displayCurrency)} appear here.</span>
      </div>
    `;
  }
  const width = 1200;
  const height = 220;
  const left = 52;
  const bottom = 24;
  const top = 8;
  const plotW = width - left;
  const plotH = height - bottom - top;
  const max = niceMax(Math.max(...data.map((point) => point.amount)));
  const slot = plotW / data.length;
  const barW = Math.max(2, Math.min(40, slot * 0.6));
  const labelEvery = Math.max(1, Math.ceil(data.length / 8));
  const y = (amount: number) => top + plotH - (Math.max(0, amount) / max) * plotH;
  const grid = [0, 0.5, 1].map((f) => {
    const gy = y(max * f);
    return `<line class="chart-grid" x1="${left}" y1="${gy.toFixed(1)}" x2="${width}" y2="${gy.toFixed(1)}"/>
      <text class="chart-axis" x="${left - 8}" y="${(gy + 4).toFixed(1)}" text-anchor="end">${escapeHtml(formatBalance(max * f, displayCurrency))}</text>`;
  }).join("");
  const bars = data.map((point, index) => {
    const x = left + index * slot + (slot - barW) / 2;
    const by = y(point.amount);
    const label = index % labelEvery === 0
      ? `<text class="chart-axis" x="${(x + barW / 2).toFixed(1)}" y="${height - 6}" text-anchor="middle">${escapeHtml(point.day)}</text>`
      : "";
    return `<rect class="chart-bar" x="${x.toFixed(1)}" y="${by.toFixed(1)}" width="${barW.toFixed(1)}" height="${(top + plotH - by).toFixed(1)}"><title>${escapeHtml(point.day)}: ${escapeHtml(formatBalance(point.amount, displayCurrency))}</title></rect>${label}`;
  }).join("");
  return `
    <div class="chart-shell">
      <svg class="earnings-chart" viewBox="0 0 ${width} ${height}" role="img" aria-label="Earned per day, last ${data.length} days">
        ${grid}
        ${bars}
      </svg>
    </div>
  `;
}
