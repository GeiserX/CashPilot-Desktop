// The Mysterium per-node earnings list, extracted from main.ts so it can be
// TESTED.
//
// Same rule again, at the parsing boundary this time: a blob that is missing,
// unparseable, or not a non-empty array renders NOTHING -- never an empty
// header, never a NaN. The input is a JSON string the backend stashed from a
// third-party API, so "we could not read it" is a state that genuinely occurs
// and must not be dressed up as "you earned 0.0000 MYST".
//
// See CashPilot-Desktop-tft.
//
// Pure: takes the raw JSON string (or undefined), returns an HTML string.

import type { MystNode } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

// renderMystNodes turns the Mysterium per-node earnings blob — a JSON array of
// MystNode the backend stashes under serviceDetails["mysterium"] — into a
// compact per-node list: each node's name (or a shortened identity), an
// online/offline dot in the theme's success/muted colours, and its 30-day and
// lifetime MYST. It returns "" when the blob is missing, unparseable, or not a
// non-empty array, so a Mysterium row with no per-node detail renders nothing
// extra rather than an empty header or a NaN.
export function renderMystNodes(json: string | undefined): string {
  if (!json) return "";
  let nodes: MystNode[];
  try {
    const parsed: unknown = JSON.parse(json);
    if (!Array.isArray(parsed) || parsed.length === 0) return "";
    nodes = parsed as MystNode[];
  } catch {
    return "";
  }
  const items = nodes.map((node) => {
    const label = (node.name || "").trim() || shortenIdentity(node.identity);
    const dotColor = node.online ? "var(--success)" : "var(--text-muted)";
    const dot = `<span title="${node.online ? "online" : "offline"}" style="display:inline-block;width:8px;height:8px;border-radius:50%;flex:0 0 auto;background:${dotColor};"></span>`;
    return `
      <div style="display:flex;align-items:center;gap:0.6rem;font-size:0.82rem;padding:0.15rem 0;">
        <span style="display:flex;align-items:center;gap:0.4rem;flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--text-secondary);">${dot}${escapeHtml(label)}</span>
        <span style="flex:0 0 auto;color:var(--text-muted);" title="Last 30 days">${escapeHtml(formatMyst(node.earnings30dMyst))} · 30d</span>
        <span style="flex:0 0 auto;color:var(--text-secondary);" title="Lifetime">${escapeHtml(formatMyst(node.lifetimeMyst))} lifetime</span>
      </div>
    `;
  }).join("");
  return `
    <div style="display:flex;flex-direction:column;gap:0.1rem;">
      <span style="font-size:0.72rem;letter-spacing:0.06em;text-transform:uppercase;color:var(--text-muted);margin-bottom:0.2rem;">Per-node earnings</span>
      ${items}
    </div>
  `;
}

// formatMyst renders a MYST amount to a few decimals. MYST is a reward token,
// not an ISO currency, so Intl currency formatting can't be used; non-finite
// values degrade to 0 rather than showing NaN.
function formatMyst(value: number) {
  const amount = Number.isFinite(value) ? value : 0;
  return `${amount.toFixed(4)} MYST`;
}

// shortenIdentity collapses a long Mysterium identity (a 0x… hash) to
// "first6…last4" so an unnamed node still shows something readable. Short or
// empty identities are returned as-is (or a neutral placeholder).
function shortenIdentity(identity: string) {
  const id = (identity || "").trim();
  if (!id) return "unknown node";
  if (id.length <= 12) return id;
  return `${id.slice(0, 6)}…${id.slice(-4)}`;
}
