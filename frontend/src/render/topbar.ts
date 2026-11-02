// The top bar's navigation and its one action.
//
// The tabs are places you look at. Adding a service is the thing you do, so it
// is a primary button on the right of the bar, not the last tab after Settings.
// On a narrow window the label hides and the button shows "+"; the aria-label
// keeps saying "Add service" either way.
//
// Pure: takes the active view, returns HTML.

import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

const TABS: Array<[string, string]> = [
  ["dashboard", "Dashboard"],
  ["fleet", "Fleet"],
  ["catalog", "Catalog"],
  ["settings", "Settings"],
];

function navButton(view: string, label: string, active: string): string {
  return `<button class="sidebar-link ${active === view ? "active" : ""}" data-view="${view}"${active === view ? ' aria-current="page"' : ""}>${escapeHtml(label)}</button>`;
}

export function renderTopnav(active: string): string {
  return `<nav class="topnav" aria-label="Main">${TABS.map(([view, label]) => navButton(view, label, active)).join("")}</nav>`;
}

/** "+ Add service": opens the wizard at its first step, like the tab it replaces. */
export function renderAddServiceButton(active: string): string {
  const here = active === "wizard";
  return `<button class="btn btn-primary btn-sm topbar-add${here ? " active" : ""}" data-view="wizard" aria-label="Add service"${here ? ' aria-current="page"' : ""}><span aria-hidden="true">+</span><span class="topbar-add-label" aria-hidden="true"> Add service</span></button>`;
}
