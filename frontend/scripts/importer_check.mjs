#!/usr/bin/env node
// Browser-free checks on the import page (bring logins over from another tool).
//
//   1. A SECRET'S VALUE IS NEVER IN THE HTML. The backend never sends one; even if a
//      value slipped through, a secret field shows "set" and nothing else, and its
//      input is a password box.
//   2. A ROW THAT CANNOT COME OVER HAS NO TICK; one turned off in the old tool comes
//      unticked; a ready one comes ticked.
//   3. EVERYTHING FROM THE FILES IS ESCAPED: names, keys, values, reasons, notices.
//   4. THE CHOICES SENT ARE THE TICKED ROWS, with typed values only for those rows.
//   5. THE FILE LIMITS ARE CHECKED BEFORE ANYTHING IS SENT.
//   6. A SAVED SERVICE OFFERS DEPLOY, and says which values it kept.
//
//   node scripts/importer_check.mjs      # against ./.harness-build

import { importChoices, importFilesProblem, renderImportPreview, renderImportSaved } from "../.harness-build/render/importer.js";

let failures = 0;
let checks = 0;
function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const preview = {
  source: "money4band",
  sourceName: "money4band",
  notices: ["Stop money4band first <b>now</b>"],
  notImported: [{ key: "<script>k</script>", reason: "CashPilot has no such service." }],
  rows: [
    {
      slug: "honeygain", name: "Honeygain", app: "honeygain", selected: true, offInSource: false, state: "ready", reason: "",
      existing: { configured: true, deployed: false },
      fields: [
        { key: "HONEYGAIN_EMAIL", label: "Email", secret: false, required: true, state: "imported", pattern: "", value: "hg@<example>.com" },
        // A value that should never be sent; the render must not print it either.
        { key: "HONEYGAIN_PASSWORD", label: "Password", secret: true, required: true, state: "imported", pattern: "", value: "LEAKED-SECRET" },
      ],
    },
    {
      slug: "iproyal", name: "IPRoyal", app: "iproyalpawns", selected: true, offInSource: false, state: "missing_fields", reason: "Fill in the fields marked below.",
      existing: { configured: false, deployed: false },
      fields: [{ key: "IPROYALPAWNS_PASSWORD", label: "Password", secret: true, required: true, state: "retype", pattern: "", value: "LEAKED-RETYPE" }],
    },
    {
      slug: "earnfm", name: "Earn.fm", app: "earnfm", selected: false, offInSource: true, state: "ready", reason: "Turned off in the old tool.",
      existing: { configured: false, deployed: false }, fields: [],
    },
    {
      slug: "grass", name: "Grass<img src=x>", app: "grass", selected: false, offInSource: false, state: "unusable", reason: "Grass runs as a browser extension.",
      existing: { configured: false, deployed: false }, fields: [],
    },
  ],
};

const html = renderImportPreview(preview);

// 1
check("a secret's value is not in the HTML", !html.includes("LEAKED-SECRET") && !html.includes("LEAKED-RETYPE"));
check("an imported secret shows as set", /<strong>Password<\/strong>: set /.test(html), html);
check("the box for a secret to retype is a password box",
  /type="password"[^>]*data-import-slug="iproyal"[^>]*data-import-key="IPROYALPAWNS_PASSWORD"/s.test(html));
check("control: a non-secret value is shown, escaped", html.includes("<code>hg@&lt;example&gt;.com</code>"));

// 2
const tick = (slug) => new RegExp(`class="import-pick" data-import-slug="${slug}"( checked)?`).exec(html);
check("a ready row comes ticked", tick("honeygain")?.[1] === " checked");
check("a row turned off in the old tool comes unticked", tick("earnfm") && !tick("earnfm")[1]);
check("an unusable row has no tick", !tick("grass"));
check("an unusable row still says why", html.includes("Grass runs as a browser extension."));
check("a service already set up says its values are kept", html.includes("values already set are kept"));

// 3
check("a notice is escaped", html.includes("&lt;b&gt;now&lt;/b&gt;") && !html.includes("<b>now</b>"));
check("a skipped key is escaped", html.includes("&lt;script&gt;k&lt;/script&gt;") && !html.includes("<script>k"));
check("a service name is escaped", html.includes("Grass&lt;img src=x&gt;") && !html.includes("<img src=x>"));

// 4
const choices = importChoices(
  [{ slug: "honeygain", checked: true }, { slug: "iproyal", checked: true }, { slug: "earnfm", checked: false }],
  [
    { slug: "iproyal", key: "IPROYALPAWNS_PASSWORD", value: "  typed  " },
    { slug: "earnfm", key: "EARNFM_TOKEN", value: "not-ticked" },
    { slug: "iproyal", key: "OTHER", value: "   " },
  ],
);
check("only ticked rows are sent", JSON.stringify(choices.slugs) === '["honeygain","iproyal"]', JSON.stringify(choices));
check("typed values go only with ticked rows, trimmed, blanks dropped",
  JSON.stringify(choices.extra) === '{"iproyal":{"IPROYALPAWNS_PASSWORD":"typed"}}', JSON.stringify(choices.extra));

// 5
check("no files is refused", importFilesProblem([]) !== "");
check("five files are refused", importFilesProblem(Array.from({ length: 5 }, (_, i) => ({ name: `${i}`, content: "A=1" }))) !== "");
check("over 64 KiB is refused", importFilesProblem([{ name: "big", content: "x".repeat(64 * 1024 + 1) }]) !== "");
check("control: one small file is fine", importFilesProblem([{ name: ".env", content: "A=1" }]) === "");

// 6
const saved = renderImportSaved({ saved: ["honeygain", "earnfm"], kept: { honeygain: ["HONEYGAIN_PASSWORD"] } }, preview, ["earnfm"]);
check("a saved service offers Deploy", saved.includes('data-import-deploy="honeygain"'));
check("a deployed one no longer does", !saved.includes('data-import-deploy="earnfm"') && saved.includes("deployed"));
check("kept values are named by label", saved.includes("left as it was: Password."));
check("the saved list holds no secret", !saved.includes("LEAKED-SECRET"));

if (failures) {
  console.error(`\n${failures} of ${checks} import checks failed`);
  process.exit(1);
}
console.log(`importer_check: ${checks} checks passed`);
