// The Route box on a service's setup card (internal/services/routes.go).
//
// A service can run through one of the owner's proxies from its first start, so it
// never runs once from this machine's own IP. The box offers this machine's own IP
// (the default) and each proxy. A service the catalog cannot route says why instead,
// and on a machine set to CASHPILOT_PROXIED_ONLY the own-IP choice is gone.
//
// Pure: takes catalog data and the route state, returns HTML strings or values.

import type { RouteState, Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** Render the Route box for one service, or "" when there is nothing to choose. */
export function routeChoiceSection(service: Service, route: RouteState | null | undefined): string {
  if (service.manualOnly || service.custom || !route) return "";
  const slug = escapeHtml(service.slug);
  const refusal = route.refusals?.[service.slug] || "";
  const proxies = route.proxies || [];
  if (refusal) {
    return `<p class="muted compact-copy route-hint">Route: this computer's own IP. ${escapeHtml(refusal)}</p>`;
  }
  if (proxies.length === 0 && !route.proxiedOnly) return "";
  const current = route.recorded?.[service.slug];
  const options = [
    route.proxiedOnly ? "" : `<option value="">Direct: this computer's own IP</option>`,
    ...proxies.map((p) => {
      const exit = [p.exitIp, p.country ? `(${p.country})` : ""].filter(Boolean).join(" ");
      const label = `Through ${p.name || `proxy ${p.id}`}${exit ? `, exit ${exit}` : ""}`;
      return `<option value="${p.id}"${current === p.id ? " selected" : ""}>${escapeHtml(label)}</option>`;
    }),
  ].join("");
  return `
    <div class="route-choice">
      <label>
        <span>Route</span>
        <select data-wizard-route="${slug}">${options}</select>
      </label>
      <p class="muted compact-copy route-hint">A route through one of your proxies applies from the first start, so ${escapeHtml(service.name)} never runs from this computer's own IP. It hides that IP from the service's traffic, not from the provider's website or your earnings login.${route.proxiedOnly ? " This computer runs services only through a proxy." : ""}</p>
    </div>
  `;
}

/** The proxy id to deploy through, or null for this computer's own IP. */
export function routeChoice(value: string | null | undefined): number | null {
  const id = Number.parseInt(String(value ?? ""), 10);
  return Number.isInteger(id) && id > 0 ? id : null;
}

/**
 * Whether a deploy from this computer's own IP must first be confirmed by the owner:
 * the service was routed through a proxy here, running or not.
 */
export function leavesRoute(slug: string, route: RouteState | null | undefined): boolean {
  return route?.recorded?.[slug] !== undefined;
}
