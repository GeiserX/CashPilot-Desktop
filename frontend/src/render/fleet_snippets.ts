// Fleet > Add Worker or Mobile: the two setup snippets and their Copy buttons.
//
// The snippets carry the fleet API key. The page shows it masked and never puts
// it in the HTML, not even in a data attribute: main.ts copies the real snippet
// from memory on click, the way Settings copies a read-only secret.
//
// Pure: takes the snippets the backend sent, returns HTML, touches no global.

import type { FleetState } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts

/** What the page shows in place of the key. */
export const KEY_MASK = "••••";

/** A snippet with the fleet API key replaced by KEY_MASK. */
export function maskFleetKey(snippet: string, apiKey: string): string {
  let out = snippet.replace(/^(CASHPILOT_API_KEY=).+$/m, `$1${KEY_MASK}`);
  if (apiKey) out = out.split(apiKey).join(KEY_MASK);
  return out;
}

/** Which snippet a Copy button copies: the data-fleet-copy value. */
export type FleetSnippet = "worker" | "mobile";

export function renderFleetSnippets(fleet: Pick<FleetState, "workerSnippet" | "mobileSnippet" | "apiKey">): string {
  const block = (which: FleetSnippet, title: string, snippet: string) => `
              <div>
                <div class="snippet-header"><strong>${title}</strong><button type="button" class="secondary compact-btn" data-fleet-copy="${which}">Copy</button></div>
                <code>${escapeHtml(maskFleetKey(snippet, fleet.apiKey))}</code>
              </div>`;
  return `
            <div class="fleet-snippets">${block("worker", "Docker worker", fleet.workerSnippet)}${block("mobile", "Mobile / companion", fleet.mobileSnippet)}
            </div>
            <p class="muted compact-copy">The key is hidden here. Copy puts the real key in your clipboard.</p>`;
}
