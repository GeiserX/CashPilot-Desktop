// The top bar's navigation and its one action.
//
// The tabs are places you look at. Adding a service is the thing you do, so it
// is a primary button on the right of the bar, not the last tab after Settings.
// On a narrow window the label hides and the button shows "+"; the aria-label
// keeps saying "Add service" either way.
//
// Pure: takes the active view, returns HTML.

import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";

function tabs(): Array<[string, string]> {
  return [
    ["dashboard", t("topbar.dashboard")],
    ["fleet", t("topbar.fleet")],
    ["activity", t("topbar.activity")],
    ["catalog", t("topbar.catalog")],
    ["security", t("topbar.security")],
    ["settings", t("topbar.settings")],
  ];
}

function navButton(view: string, label: string, active: string): string {
  return `<button class="sidebar-link ${active === view ? "active" : ""}" data-view="${view}"${active === view ? ' aria-current="page"' : ""}>${escapeHtml(label)}</button>`;
}

export function renderTopnav(active: string): string {
  return `<nav class="topnav" aria-label="${ht("topbar.main")}">${tabs().map(([view, label]) => navButton(view, label, active)).join("")}</nav>`;
}

/** "+ Add service": opens the wizard at its first step, like the tab it replaces. */
export function renderAddServiceButton(active: string): string {
  const here = active === "wizard";
  return `<button class="btn btn-primary btn-sm topbar-add${here ? " active" : ""}" data-view="wizard" aria-label="${ht("topbar.addService")}"${here ? ' aria-current="page"' : ""}><span aria-hidden="true">+</span><span class="topbar-add-label" aria-hidden="true"> ${ht("topbar.addService")}</span></button>`;
}
