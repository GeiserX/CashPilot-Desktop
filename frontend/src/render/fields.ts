// The credential inputs one service's wizard card renders, extracted from main.ts
// so it can be TESTED.
//
// A service's form is its container contract (catalog docker.env) PLUS whatever its
// earnings collector needs on top, and the two are frequently different credentials:
// Repocket's container authenticates with an API key from the dashboard while its
// collector signs in with the account password, and a cookie-driven collector
// (EarnApp, Salad, PacketStream) needs a browser cookie the container never sees.
//
// The collector half now arrives from the backend (AppState.collectorFields, built
// from internal/collectors) rather than a table kept in the frontend. That table went
// stale in exactly the way this file exists to stop: the web catalog renamed
// Repocket's env keys to RP_EMAIL/RP_API_KEY, no frontend entry was added, and the
// form stopped asking for the only credentials its collector can use.
//
// Pure: takes the service and the backend's map, returns the fields to render.

import type { CollectorField, Service } from "../wails";

/**
 * earnings marks a field only the earnings collector reads. The deploy never
 * needs it, so the card never marks it * and Deploy never waits for it.
 */
export type ServiceField = CollectorField & { default?: string; earnings?: boolean };

/** Whether Deploy needs this field filled: required by the container, no default. */
export function deployNeeds(field: ServiceField): boolean {
  return Boolean(field.required) && !field.earnings && !field.default;
}

/**
 * The mark after a field's label: * for what the deploy needs, "(for earnings)"
 * for a login only the earnings collector needs. A * the deploy does not wait for
 * would promise a check that never comes.
 */
export function fieldMark(field: ServiceField): string {
  if (deployNeeds(field)) return " *";
  return field.required && field.earnings ? " (for earnings)" : "";
}

/** Strip HTML tags from a catalog description before it goes into a placeholder. */
export function stripHtml(value: string): string {
  return value.replace(/<[^>]+>/g, "");
}

/**
 * serviceFormFields returns the inputs to render for a service, in order: every
 * docker.env variable the container declares, then every collector-only field the
 * backend declares for that slug which docker.env does not already cover. A
 * collector-only field carries earnings: the deploy does not read it.
 *
 * docker.env wins on a shared key because it carries the deploy-time extras (a
 * default value, the container's own label) and saving one value under one key is
 * what both the deploy and the collect path then read.
 */
export function serviceFormFields(
  service: Service,
  collectorFields: Record<string, CollectorField[]> | null | undefined,
): ServiceField[] {
  // A wallet seed lives in a file in the node's own volume (render/wallet.ts): it is
  // never a field, so it is never typed, saved or sent back.
  const collector = (collectorFields || {})[service.slug] || [];
  const extraSecrets = new Set([
    ...collector.filter((item) => item.secret).map((item) => item.key),
    ...(service.native?.env || []).filter((item) => item.secret).map((item) => item.key),
  ]);
  const env = (service.docker?.env || []).filter((item) => !item.seedFile).map((item) => ({
    key: item.key,
    label: item.label || item.key,
    description: stripHtml(item.description || item.key),
    secret: item.secret || extraSecrets.has(item.key),
    required: item.required,
    default: item.default,
    pattern: item.pattern,
  }));
  const envKeys = new Set(env.map((item) => item.key));
  return [...env, ...collector.filter((item) => !envKeys.has(item.key)).map((item) => ({
    ...item, secret: extraSecrets.has(item.key), earnings: true,
  }))];
}
