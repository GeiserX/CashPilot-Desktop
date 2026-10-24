// The Image box under a service's Advanced section, and the badge that marks a
// deployment running an image the user chose instead of the catalog's.
//
// The box has three meanings, and the deploy path depends on telling them apart:
//
//   left as it was   keep whatever the deployment already runs (null to the backend);
//   cleared          go back to the catalog image ("");
//   anything else    run this image instead, on this and every later redeploy.
//
// It is filled with the recorded choice, so "left as it was" is the initial value,
// not an empty box: a redeploy from a card nobody opened must not drop a choice.
//
// Pure: takes catalog data and the recorded choice, returns HTML strings or values.

import type { Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/**
 * Render the Advanced section with the Image box for one service. `customImage` is
 * the image its deployment records ("" or absent when it follows the catalog); the
 * section starts open when there is one, so a choice is never hidden.
 */
export function imageChoiceSection(service: Service, customImage: string | null | undefined): string {
  if (service.manualOnly) return "";
  const catalogImage = service.docker?.image || "";
  const chosen = customImage || "";
  const name = escapeHtml(service.name);
  return `
    <details class="image-choice"${chosen ? " open" : ""}>
      <summary>Advanced</summary>
      <label>
        <span>Image</span>
        <input data-wizard-image="${escapeHtml(service.slug)}" data-initial="${escapeHtml(chosen)}" type="text" spellcheck="false" autocomplete="off" placeholder="${escapeHtml(catalogImage)}" value="${escapeHtml(chosen)}" />
      </label>
      <p class="muted compact-copy">Leave blank to use the catalog image${catalogImage ? ` (<code>${escapeHtml(catalogImage)}</code>)` : ""}.
        A custom image receives your ${name} credentials and runs with exactly what the catalog grants ${name}:
        no extra capabilities, devices or ports. CashPilot keeps it on every redeploy until you clear this box.</p>
    </details>
  `;
}

/**
 * The image to send with a deploy: null when the box is left as it was (the backend
 * then keeps the recorded choice), "" to go back to the catalog image, otherwise the
 * typed reference.
 */
export function imageChoice(value: string, initial: string): string | null {
  const typed = value.trim();
  return typed === initial ? null : typed;
}

/** The badge on a deployment running a chosen image; nothing when it follows the catalog. */
export function customImageBadge(customImage: string | null | undefined): string {
  if (!customImage) return "";
  return ` <span class="badge" title="${escapeHtml(customImage)}">custom image</span>`;
}
