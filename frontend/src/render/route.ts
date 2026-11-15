// The Route box on a service's setup card (internal/services/routes.go).
//
// A service can run through one of the owner's proxies from its first start, so it
// never runs once from this machine's own IP. The box offers this machine's own IP
// (the default) and each proxy. A service the catalog cannot route says why instead,
// and on a machine set to CASHPILOT_PROXIED_ONLY the own-IP choice is gone.
//
// Pure: takes catalog data and the route state, returns HTML strings or values.

import type { ProxiedResult, RouteState, Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/**
 * Render the Route box for one service, or "" when there is nothing to choose.
 *
 * `draft` is what the owner picked in the box before a redraw: a proxy id, null for
 * this computer's own IP, or undefined for no pick (the recorded route is selected).
 * A redraw must keep the pick, or a proxy chosen for an unrouted service would fall
 * back to Direct and the next deploy would show this computer's IP unasked.
 */
export function routeChoiceSection(service: Service, route: RouteState | null | undefined, draft?: number | null): string {
  if (service.manualOnly || service.custom || !route) return "";
  const slug = escapeHtml(service.slug);
  const refusal = route.refusals?.[service.slug] || "";
  const proxies = route.proxies || [];
  if (refusal) {
    return `<p class="muted compact-copy route-hint">Route: this computer's own IP. ${escapeHtml(refusal)}</p>`;
  }
  if (proxies.length === 0 && !route.proxiedOnly) return "";
  const current = draft !== undefined ? draft : route.recorded?.[service.slug];
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
 * The wizard after a click on a button that switches the page (data-view: the tabs and
 * Add service). Each such click leaves the wizard or starts it again at its first step,
 * so the Route box picks of the visit before are dropped: a pick never outlives the
 * visit it was made in, and the next visit starts from the recorded route.
 */
export function wizardAfterNav(next: string, step: number): { step: number; drafts: Record<string, number | null> } {
  return { step: next === "wizard" ? 1 : step, drafts: {} };
}

/**
 * Whether a deploy from this computer's own IP must first be confirmed by the owner:
 * the service was routed through a proxy here, running or not.
 */
export function leavesRoute(slug: string, route: RouteState | null | undefined): boolean {
  return route?.recorded?.[slug] !== undefined;
}

/** What the setup card says after a routed deploy: where it leaves, then the device-link step. */
export function routedDeployText(result: Partial<ProxiedResult>, link: string): string {
  const where = result.exitIp ? ` It leaves from ${result.exitIp}${result.country ? ` (${result.country})` : ""}.` : "";
  return [`Deployed through the proxy.${where}`, result.warning || "", link].filter(Boolean).join("\n\n");
}

/** What a deploy from the setup card ran, or why it ran nothing. */
export type RouteDeployOutcome<D, R> =
  | { kind: "refused"; message: string }
  | { kind: "cancelled" }
  | { kind: "routed"; result: R }
  | { kind: "direct"; deployment: D };

/** The calls a deploy makes; main.ts passes the Wails bindings and window.confirm. */
export interface RouteDeployCalls<D, R> {
  /** Saves the typed credentials. */
  save(): Promise<void>;
  confirm(message: string): boolean;
  routed(proxyId: number): Promise<R>;
  /** leaveRoute is the owner's yes to running a routed service from this computer's own IP. */
  direct(leaveRoute: boolean): Promise<D>;
}

/**
 * Deploy from the setup card. A routed deploy runs the catalog image once, so a chosen
 * image or more than one copy is refused instead of dropped. Every refusal and the
 * owner's confirmation come before the credentials are saved, so a deploy that does
 * not run changes nothing.
 */
export async function runRouteDeploy<D, R>(
  slug: string,
  proxyId: number | null,
  image: string | null,
  instances: number | null,
  route: RouteState | null | undefined,
  calls: RouteDeployCalls<D, R>,
): Promise<RouteDeployOutcome<D, R>> {
  if (proxyId !== null && (image || (instances !== null && instances > 1))) {
    return {
      kind: "refused",
      message: "A service routed through a proxy runs the catalog image, once. Clear the Image box and set Instances to 1, or choose this computer's own IP. Nothing was deployed.",
    };
  }
  const leaving = proxyId === null && leavesRoute(slug, route);
  // From this computer's own IP, the provider sees that IP: only the owner's yes switches it.
  if (leaving && !calls.confirm(`${slug} runs through a proxy on this computer. Running it from this computer's own IP shows that IP to the provider. Switch it?`)) {
    return { kind: "cancelled" };
  }
  await calls.save();
  if (proxyId !== null) return { kind: "routed", result: await calls.routed(proxyId) };
  return { kind: "direct", deployment: await calls.direct(leaving) };
}
