// Custom services: an image the user adds from Docker Hub or any registry, for a
// service the catalog does not list. The web console leads (app/static/js/app.js);
// this is its port.
//
// What these functions must never get wrong:
//
//   - a stored secret's value is never in the HTML. The backend already sends it
//     blank; the editor says one is stored and that a blank box keeps it;
//   - every custom service says, wherever it shows, that CashPilot has not reviewed
//     its image;
//   - the deploy card has no Image box and no credential inputs: a custom service
//     runs its saved definition, changed only by editing it.
//
// Pure: takes plain data, returns HTML strings or values.

import type { CustomDefinition, CustomEnv, CustomEnvView, CustomServiceView, Deployment, ScalingLimit, Service } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { instancesSection } from "./instances.js";

/** The words every custom service carries: it is the user's image, not a reviewed one. */
export const UNTRUSTED_TEXT = "Added by you. CashPilot has not reviewed this image.";

/** The badge on a custom service in the catalog and on the dashboard. */
export function customBadge(service: Pick<Service, "custom"> | null | undefined): string {
  if (!service?.custom) return "";
  return `<span class="badge warn" title="${escapeHtml(UNTRUSTED_TEXT)}">custom, not reviewed</span>`;
}

/** What a custom service may do, said once in the editor and on its deploy card. */
function restrictionsNote(): string {
  return `<p class="muted compact-copy">It runs with the restrictions every catalog service gets: all capabilities dropped,
    no-new-privileges, a process and a memory limit, never privileged, no host network, no devices, no ports and no
    host folders. CashPilot cannot read its earnings, so it shows no balance.</p>`;
}

function envRow(env: Partial<CustomEnvView> = {}): string {
  const stored = Boolean(env.secret && env.isSet);
  return `
    <div class="custom-env-row" data-custom-env-row>
      <input data-custom-env-key type="text" spellcheck="false" autocomplete="off" placeholder="NAME" value="${escapeHtml(env.key || "")}" aria-label="Variable name" />
      <input data-custom-env-value type="${env.secret ? "password" : "text"}"${env.secret ? ' autocomplete="new-password"' : ' autocomplete="off"'} spellcheck="false"
        placeholder="${stored ? "stored, leave blank to keep" : "value"}" value="${env.secret ? "" : escapeHtml(env.value || "")}" aria-label="Value" />
      <label class="custom-env-secret"><input data-custom-env-secret type="checkbox"${env.secret ? " checked" : ""} /> Secret</label>
      <button class="btn btn-ghost btn-sm" data-custom-env-remove type="button">Remove</button>
    </div>
  `;
}

/** One empty variable row, for the "Add variable" button. */
export function customEnvRow(): string {
  return envRow();
}

/**
 * The editor for one custom service, or for a new one when `view` is null. Field
 * inputs carry data-custom-field; main.ts reads them and hands the values to
 * customDefinitionFrom.
 */
export function renderCustomEditor(view: CustomServiceView | null): string {
  const v = view || { slug: "", name: "", image: "", env: [], command: "", dataPath: "", memLimit: "", volume: "", warnings: [] };
  const field = (name: string, label: string, value: string, placeholder: string, help = "") => `
    <label>
      <span>${escapeHtml(label)}</span>
      <input data-custom-field="${name}" type="text" spellcheck="false" autocomplete="off" placeholder="${escapeHtml(placeholder)}" value="${escapeHtml(value)}" />
      ${help ? `<small class="muted">${help}</small>` : ""}
    </label>`;
  return `
    <form class="custom-editor panel" data-custom-editor="${escapeHtml(v.slug)}" onsubmit="return false">
      <h3>${view ? `Edit ${escapeHtml(view.name)}` : "Add custom service"}</h3>
      <p class="tip">${escapeHtml(UNTRUSTED_TEXT)} Only add an image you trust: it runs on this computer.</p>
      <div class="credential-grid">
        ${field("name", "Name", v.name, "My service", view ? `Internal name <code>${escapeHtml(view.slug)}</code>, which never changes.` : "")}
        ${field("image", "Image", v.image, "publisher/image:1.2.3", "Docker Hub or any registry, with a tag or digest.")}
        ${field("command", "Command (optional)", v.command, "--flag ${NAME}", "One line. ${NAME} is replaced by the variable's value.")}
        ${field("dataPath", "Data path (optional)", v.dataPath, "/data", v.volume ? `Kept in the volume <code>${escapeHtml(v.volume)}</code>.` : "A path inside the container kept in a volume of its own.")}
        ${field("memLimit", "Memory limit", v.memLimit, "512m", "From 32m to 16g.")}
      </div>
      <h4>Environment variables</h4>
      <div class="custom-env" data-custom-env>
        ${v.env.map((env) => envRow(env)).join("")}
      </div>
      <button class="btn btn-ghost btn-sm" data-custom-env-add type="button">Add variable</button>
      ${restrictionsNote()}
      <div class="actions left">
        <button class="primary" data-custom-save type="button">${view ? "Save changes" : "Add service"}</button>
        <button class="secondary" data-custom-cancel type="button">Cancel</button>
      </div>
      <pre class="output" data-custom-output></pre>
    </form>
  `;
}

/**
 * The definition to send from what the editor holds. A row with neither a name nor
 * a value is a blank row the user never filled and is dropped; a secret row with a
 * name and a blank value is kept, because blank means "keep the stored one".
 */
export function customDefinitionFrom(fields: Record<string, string>, rows: CustomEnv[]): CustomDefinition {
  return {
    name: (fields.name || "").trim(),
    image: (fields.image || "").trim(),
    command: (fields.command || "").trim(),
    dataPath: (fields.dataPath || "").trim(),
    memLimit: (fields.memLimit || "").trim(),
    env: rows
      .map((row) => ({ key: row.key.trim(), value: row.value, secret: Boolean(row.secret) }))
      .filter((row) => row.key !== "" || row.value !== ""),
  };
}

/**
 * Whether a redraw of the catalog keeps the editor the user is typing in. The values
 * typed there live only in the DOM, so main.ts carries the live form over in place of
 * the redrawn one when both are for the same service: liveSlug and freshSlug are the
 * data-custom-editor attribute of each ("" for a new service, null for no editor).
 */
export function keepsLiveEditor(liveSlug: string | null | undefined, freshSlug: string | null | undefined): boolean {
  return liveSlug != null && freshSlug != null && liveSlug === freshSlug;
}

/** The "Your custom services" block above the catalog: the list, and the editor when one is open. */
export function renderCustomServicesSection(
  views: CustomServiceView[] | null,
  deployments: Deployment[],
  editing: CustomServiceView | "new" | null,
  loadError = "",
): string {
  const deployed = new Set(deployments.map((dep) => dep.slug));
  const list = views || [];
  const rows = list.map((view) => `
    <tr>
      <td class="c-name">
        <span class="svc-name">${escapeHtml(view.name)}</span>
        <div class="cell-sub cell-mono">${escapeHtml(view.image)}</div>
        <div class="cell-tags">
          ${customBadge({ custom: true })}
          ${(view.warnings || []).map((w) => `<span class="badge warn" title="${escapeHtml(w)}">no version tag</span>`).join(" ")}
          ${deployed.has(view.slug) ? `<span class="badge">deployed here</span>` : ""}
        </div>
      </td>
      <td class="c-act"><div class="action-btns">
        <button class="btn btn-primary btn-sm" data-service="${escapeHtml(view.slug)}">${deployed.has(view.slug) ? "Manage" : "Deploy"}</button>
        <button class="btn btn-ghost btn-sm" data-custom-edit="${escapeHtml(view.slug)}">Edit</button>
        <button class="btn btn-ghost btn-sm" data-custom-delete="${escapeHtml(view.slug)}"${deployed.has(view.slug) ? ' disabled title="Remove it from this computer first."' : ""}>Delete</button>
      </div></td>
    </tr>
  `).join("");
  const editor = editing === "new" ? renderCustomEditor(null) : editing ? renderCustomEditor(editing) : "";
  return `
    <section class="catalog-block custom-services">
      <div class="split">
        <h3>Your custom services</h3>
        ${editing ? "" : `<button class="btn btn-ghost btn-sm" data-custom-new>Add custom service</button>`}
      </div>
      <p class="muted compact-copy">Run an image the catalog does not list, from Docker Hub or any registry. ${escapeHtml(UNTRUSTED_TEXT)}</p>
      ${loadError ? `<p class="tip">${escapeHtml(loadError)}</p>` : ""}
      ${editor}
      ${rows ? `<div class="table-scroll"><table class="catalog-table"><tbody>${rows}</tbody></table></div>` : ""}
    </section>
  `;
}

/**
 * A custom service's card in the wizard's deploy step. No credential inputs, no
 * Image box, no sign-up or collector: it runs its saved definition.
 */
export function renderCustomServiceCard(service: Service, scaling?: ScalingLimit | null, instances?: number | null): string {
  return `
    <article class="wizard-setup-card" data-form-slug="${escapeHtml(service.slug)}">
      <div class="split">
        <div>
          <h3>${escapeHtml(service.name)}</h3>
          <p class="muted cell-mono">${escapeHtml(service.docker?.image || "")}</p>
        </div>
        ${customBadge(service)}
      </div>
      <p class="tip">${escapeHtml(UNTRUSTED_TEXT)} Its image and variables are changed by editing it under Catalog, Your custom services.</p>
      ${restrictionsNote()}
      ${instancesSection(service, scaling, instances)}
      <div class="actions left">
        <button class="primary" data-wizard-action="deploy" data-slug="${escapeHtml(service.slug)}">Deploy</button>
      </div>
      <pre class="output wizard-output" data-output-slug="${escapeHtml(service.slug)}"></pre>
    </article>
  `;
}
