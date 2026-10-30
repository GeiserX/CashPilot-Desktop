// The icon buttons on a services-table row, shared by a service's own row and
// the rows of its copies. Each label is the button's title and accessible name.
//
// Pure: returns HTML.

import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

const ICON = {
  collect: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 11-2.12-9.36L23 10"/></svg>',
  logs: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/></svg>',
  stop: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="1"/></svg>',
  start: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><polygon points="7 4 20 12 7 20 7 4"/></svg>',
  remove: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14a2 2 0 01-2 2H8a2 2 0 01-2-2L5 6"/><path d="M10 11v6M14 11v6M9 6V4h6v2"/></svg>',
};

/** One icon button in a row. The label is the title AND the accessible name. */
export function rowButton(action: "collect" | "logs" | "stop" | "start" | "remove", slug: string, label: string, copy = 1): string {
  const copyAttr = copy > 1 ? ` data-copy="${copy}"` : "";
  const danger = action === "remove" ? " btn-danger-icon" : "";
  return `<button class="btn btn-icon${danger}" data-row-action="${action}" data-slug="${escapeHtml(slug)}"${copyAttr} title="${escapeHtml(label)}" aria-label="${escapeHtml(label)}">${ICON[action]}</button>`;
}

