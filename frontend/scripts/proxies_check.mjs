#!/usr/bin/env node
// Browser-free checks on the Proxies section of the Fleet page.
//
//   1. A PROXY SECRET IS NEVER IN THE HTML. The backend sends only "set: true"; even
//      if a value slipped into the view, neither the card nor the editor prints it.
//      The editor's secret boxes are empty password boxes, and the "remove" box is
//      offered only for a secret that is stored.
//   2. AN EXIT NOBODY HAS SEEN IS AN EM DASH, never an address and never 0.0.0.0.
//   3. A CAPPED PROXY SAYS WHY on its card, and it cannot be picked for a new copy.
//   4. A SERVICE THAT CANNOT RUN BEHIND A PROXY IS DISABLED, with the reason.
//   5. THE FORM'S VALUES: a blank secret is left out (blank keeps the stored one),
//      a typed one is sent as typed, everything else trimmed.
//   6. USER TEXT IS ESCAPED.
//   7. A MISSING TUNNEL IMAGE IS SAID, not hidden.
//   8. A CAPPED PROXY'S COPIES OFFER NO LIVE START.
//   9. THE EDITOR SAYS AN EDIT REACHES A COPY ON ITS NEXT START.
//  10. THE SECTION LINKS TO THE PUBLISHED PROXIES PAGE, not to a repo path.
//  11. A FLEET REDRAW NEVER OVERWRITES ANOTHER VIEW, and keeps the open editor's draft.
//
//   node scripts/proxies_check.mjs      # against ./.harness-build

import { capText, fleetRedrawIsCurrent, formatGB, keepsLiveProxyEditor, PROXIES_DOCS_URL, proxyValuesFrom, renderProxiesSection, renderProxyCard, renderProxyEditor } from "../.harness-build/render/proxies.js";

let failures = 0;
let checks = 0;
function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const cap = { enabled: true, capBytes: 10e9, thresholdPct: 90, resetDay: 5, usedBytes: 2.5e9, periodStart: "2026-11-01", cappedAt: "", capReason: "" };
const proxy = {
  id: 3, name: "Gate <b>x</b>", kind: "socks5", host: "gate.example.net", port: 7000, exitNode: "", loginServer: "", rotating: true,
  secrets: { username: true, password: true, auth_key: false },
  cap, lastExitIp: "", lastCountry: "", lastCheckedAt: "", lastTest: null,
  copies: [
    { slug: "earner", name: "Earner", status: "running", tunnelStatus: "running", exitIp: "203.0.113.7", exitCountry: "NL", stoppedByCap: false },
    { slug: "other", name: "Other", status: "exited", tunnelStatus: "exited", exitIp: "", exitCountry: "", stoppedByCap: true },
  ],
  // A value that must never be printed, as if a backend bug sent it.
  password: "LEAKED-PASSWORD", username: "LEAKED-USER",
};

// 1. Secrets.
const card = renderProxyCard(proxy);
const editor = renderProxyEditor(proxy);
for (const [name, html] of [["card", card], ["editor", editor]]) {
  check(`${name} never prints the password`, !html.includes("LEAKED-PASSWORD"));
  check(`${name} never prints the user`, !html.includes("LEAKED-USER"));
}
const pwBox = editor.match(/<input data-proxy-field="password"[^>]*>/)?.[0] || "";
check("the password box is a password box", pwBox.includes('type="password"'), pwBox);
check("the password box is empty", pwBox.includes('value=""'), pwBox);
check("a stored password says blank keeps it", pwBox.includes("leave blank to keep it"), pwBox);
check("a stored password can be removed", editor.includes('data-proxy-field="clear_password"'));
check("an auth key that is not stored offers no remove box", !editor.includes('data-proxy-field="clear_auth_key"'));
check("the card says which secrets are stored", card.includes("password stored") && card.includes("username stored"), card);

// 2. Unknown exit.
check("an exit nobody saw is an em dash", card.includes("Exit &mdash;"), card);
check("a copy with no exit is an em dash", card.includes("exit &mdash;"), card);
check("a seen exit shows with its country", card.includes("203.0.113.7 (NL)"));

// 3. Cap.
check("cap text", capText(proxy) === "2.5 GB of 10.0 GB used, stops at 90%, resets on day 5", capText(proxy));
check("no cap", capText({ ...proxy, cap: { ...cap, enabled: false } }) === "No traffic cap");
check("a copy the cap stopped says so", card.includes("stopped by the cap"));
const capped = { ...proxy, cap: { ...cap, cappedAt: "2026-11-04T10:00:00Z", capReason: "Gate reached 90% of its 10 GB cap on 2026-11-04" } };
check("a capped proxy says why", renderProxyCard(capped).includes("Stopped: Gate reached 90% of its 10 GB cap on 2026-11-04."));
const state = {
  proxies: [capped, { ...proxy, id: 4, name: "Open", copies: [] }],
  candidates: [
    { slug: "earner", name: "Earner", refusal: "" },
    { slug: "mysterium", name: "Mysterium", refusal: "Mysterium needs a device on the host, which a proxied copy does not get." },
  ],
  tunnelImage: "drumsergio/cashpilot-tunnel:1.66.0", tunnelImageError: "",
};
const section = renderProxiesSection(state, null);
check("a capped proxy cannot be picked", /<option value="3" disabled>/.test(section), section);
check("an open proxy can be picked", /<option value="4" >/.test(section));

// 4. Refusals.
check("a refused service is disabled", /<option value="mysterium" disabled>/.test(section));
check("with its reason", section.includes("needs a device on the host"));
check("an allowed service is offered", /<option value="earner" >/.test(section));

// 5. Values.
const values = proxyValuesFrom({ name: "  Gate ", password: "", username: " spaced user ", auth_key: "", port: " 7000 ", clear_password: "true" });
check("a blank password is left out", !("password" in values), JSON.stringify(values));
check("a typed secret is sent as typed", values.username === " spaced user ", JSON.stringify(values));
check("other fields are trimmed", values.name === "Gate" && values.port === "7000", JSON.stringify(values));
check("clear flags pass", values.clear_password === "true");

// 6. Escaping.
check("names are escaped", card.includes("Gate &lt;b&gt;x&lt;/b&gt;") && !card.includes("<b>x</b>"));

// 7. Tunnel image.
const noImage = renderProxiesSection({ ...state, tunnelImage: "", tunnelImageError: "This build has no tunnel image pinned yet" }, null);
check("a missing tunnel image is said", noImage.includes("This build has no tunnel image pinned yet"));
check("formatGB", formatGB(150e9) === "150 GB" && formatGB(0) === "0.0 GB");
check("an empty state still renders", renderProxiesSection({ proxies: [], candidates: [], tunnelImage: "", tunnelImageError: "" }, "new").includes("Add a proxy"));

// 8. A capped proxy refuses every start, so its stopped copies offer no live Start.
const cappedCard = renderProxyCard({ ...proxy, cap: { ...cap, cappedAt: "2026-11-05T00:00:00Z", capReason: "Gate reached 90%" } });
check("a capped proxy's copy has no live Start", !cappedCard.includes('data-proxy-copy-start="3"'), cappedCard);
check("its Start is disabled", /<button[^>]*disabled[^>]*>Start<\/button>/.test(cappedCard), cappedCard);
check("an open proxy's stopped copy can be started", card.includes('data-proxy-copy-start="3"'), card);

// 9. An edit says when it reaches copies that exist.
check("the editor of a proxy with copies says edits apply on the next start", editor.includes("next time it starts"), editor);
check("the add form says nothing about copies", !renderProxyEditor(null).includes("next time it starts"));

// 10. The help link.
check("the help link is the published proxies page", PROXIES_DOCS_URL === "https://geiserx.github.io/CashPilot/proxies/", String(PROXIES_DOCS_URL));
const helpSection = renderProxiesSection({ proxies: [], candidates: [], tunnelImage: "x", tunnelImageError: "" }, null);
check("the section opens it through a data-url button", helpSection.includes(`data-url="${PROXIES_DOCS_URL}"`), helpSection);

// 11. Redraws.
check("a redraw still on Fleet and the newest writes the page", fleetRedrawIsCurrent(4, 4, "fleet"));
check("a redraw after the owner opened another view is dropped", !fleetRedrawIsCurrent(4, 4, "catalog"));
check("a redraw a newer one overtook is dropped", !fleetRedrawIsCurrent(3, 4, "fleet"));
const editorId = (html) => (html.match(/data-proxy-editor="([^"]*)"/) || [])[1];
const sectionState = { proxies: [proxy], candidates: [], tunnelImage: "x", tunnelImageError: "" };
const editId = editorId(renderProxiesSection(sectionState, proxy));
const addId = editorId(renderProxiesSection(sectionState, "new"));
const closedId = editorId(renderProxiesSection(sectionState, null));
check("the edit form carries the proxy id", editId === "3", String(editId));
check("the add form carries id 0", addId === "0", String(addId));
check("a redraw of the open Edit form keeps the typed values", keepsLiveProxyEditor(editId, editId));
check("a redraw of the open Add form keeps the typed values", keepsLiveProxyEditor(addId, addId));
check("opening another proxy's editor drops the old draft", !keepsLiveProxyEditor(addId, editId));
check("a closed editor brings nothing back", !keepsLiveProxyEditor(editId, closedId));

if (failures) {
  console.error(`\n${failures} of ${checks} proxies checks failed`);
  process.exit(1);
}
console.log(`proxies: ${checks} checks passed`);
