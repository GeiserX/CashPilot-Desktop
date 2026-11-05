#!/usr/bin/env node
// Browser-free checks on how a provider's grade is shown.
//
// The grade is computed in Go from the published criteria. These checks hold the
// last hop: the catalog row and the service card show that letter and nothing else,
// a provider without valid evidence says "not rated" (never a letter, never blank),
// a service the user added gets no rating block at all, every con keeps its source
// and checked date, and text from the catalog is escaped.
//
//   node scripts/rating_check.mjs      # against ./.harness-build

import { gradeBadge, ratingBlock, RATINGS_URL } from "../.harness-build/render/rating.js";
import { renderCatalogTable } from "../.harness-build/render/catalog.js";
import { renderWizardServiceSetup } from "../.harness-build/render/wizard.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const con = (text, n) => ({ text, criterion: "payout_record", kind: "user_reports", source: `https://example.com/thread/${n}`, checkedOn: "2026-11-04" });
const graded = {
  rated: true, grade: "C", bandGrade: "B", score: 15, max: 20, percent: 75,
  caps: [{ key: "poor_payout_or_safety", maxGrade: "C", reason: "A poor payout record or poor account safety caps the grade at C." }],
  unknownCount: 1, thinEvidence: false, reviewedOn: "2026-11-04", verdicts: [], notes: {},
  cons: [con("Payouts held for review for weeks.", 1), con("Second con.", 2), con("Third con.", 3), con("Fourth con.", 4)],
  pros: [{ text: "Pays by PayPal.", criterion: "payout_terms", kind: "terms", source: "https://example.com/terms", checkedOn: "2026-11-03" }],
  topCons: [con("Payouts held for review for weeks.", 1), con("Second con.", 2), con("Third con.", 3)],
  summary: "C, 15 of 20",
};
const svc = (slug, o = {}) => ({
  slug, name: slug, category: "bandwidth", shortDescription: `${slug} desc`, description: "", website: `https://${slug}.example`,
  referral: { signupUrl: `https://${slug}.example/ref/CODE`, code: "", program: null, bonus: { referrer: "", referee: "" } },
  platforms: ["docker"], earnings: { monthlyLow: 0, monthlyHigh: 5, currency: "USD", per: "device", notes: "" },
  docker: { env: [] }, collector: { type: "", notes: "", credentialHint: "", perNodeEarnings: false },
  cashout: { method: "", dashboardUrl: "", minAmount: 0, currency: "", notes: "" },
  payment: { methods: [], cryptoToken: "", minimumPayout: "", currency: "", frequency: "" },
  disclosure: { sells: "", thirdPartyTraffic: "", dataCollected: "", ispRisk: "", accountRules: "" },
  requirements: {}, manualOnly: false, ratingResult: graded, ...o,
});

// The badge.
const badge = gradeBadge(graded);
check("a graded provider shows its letter", />C<\/span>/.test(badge) && badge.includes("grade-c"), badge);
check("the badge tooltip is the worst con", badge.includes('title="Payouts held for review for weeks."'), badge);
const thin = gradeBadge({ ...graded, grade: "F", thinEvidence: true, unknownCount: 7 });
check("thin evidence says so in the tooltip", thin.includes("Thin evidence: 7 criteria unchecked."), thin);
for (const [name, r] of [["null", null], ["undefined", undefined], ["not rated", { rated: false, grade: "", summary: "Not rated yet" }]]) {
  const out = gradeBadge(r);
  check(`${name} reads "not rated", never a letter`, out.includes("not rated") && !/grade-badge/.test(out), out);
}
// There is no A: a letter outside B to F is never drawn, whatever the backend says.
const forgedA = gradeBadge({ ...graded, grade: "A" });
check("an A is never drawn", !/>A</.test(forgedA) && forgedA.includes("not rated"), forgedA);
check("catalog text in a con is escaped", !gradeBadge({ ...graded, topCons: [con('"><img src=x>', 9)] }).includes("<img"));

// The catalog table: a Grade column, one cell per row, header and cells aligned.
const table = renderCatalogTable([svc("alpha"), svc("beta", { ratingResult: { rated: false, grade: "", summary: "Not rated yet" } })], [], []);
check("the catalog has a Grade column", table.includes(">Grade</th>"), table);
const heads = (table.match(/<th[ >]/g) || []).length - (table.match(/colspan/g) || []).length;
for (const row of table.split("</tr>").filter((r) => r.includes("<td"))) {
  const n = (row.match(/<td[ >]/g) || []).length;
  check("every catalog row has as many cells as the header", n === heads, `${n} vs ${heads}`);
}
const groupSpan = Number((table.match(/colspan="(\d+)"/) || [])[1]);
check("the group row spans every column", groupSpan === heads, `${groupSpan} vs ${heads}`);
const alphaRow = table.split("</tr>").find((r) => r.includes("alpha desc")) || "";
const betaRow = table.split("</tr>").find((r) => r.includes("beta desc")) || "";
check("a graded row shows the letter", alphaRow.includes("grade-badge grade-c"), alphaRow);
check("an unrated row says not rated", betaRow.includes("not rated") && !betaRow.includes("grade-badge"), betaRow);

// The service card.
const block = ratingBlock(svc("alpha"));
check("the card shows the grade and the score", block.includes("grade-badge grade-c") && block.includes("C, 15 of 20"), block);
check("a ceiling that lowered the grade is explained", block.includes("caps the grade at C"), block);
check("the card shows the top three cons, not the fourth", block.includes("Third con.") && !block.includes("Fourth con."), block);
check("every con keeps its source and checked date", [1, 2, 3].every((n) => block.includes(`data-url="https://example.com/thread/${n}"`)) && block.split("checked 2026-11-04").length - 1 === 3, block);
check("pros are there, folded", block.includes("<summary>Pros (1)</summary>") && block.includes("Pays by PayPal."), block);
check("the method is one click away", block.includes(`data-url="${RATINGS_URL}#alpha"`), block);
const unrated = ratingBlock(svc("beta", { ratingResult: null }));
check("an unrated provider says Not rated yet", unrated.includes("Not rated yet") && !unrated.includes("grade-badge"), unrated);
check("a service the user added has no rating block", ratingBlock(svc("custom-x", { custom: true })) === "");
const hostile = ratingBlock(svc("alpha", { ratingResult: { ...graded, topCons: [{ ...con("<script>x</script>", 1), source: "javascript:alert(1)" }] } }));
check("con text is escaped and a non-https source gets no link", !hostile.includes("<script>") && !hostile.includes("javascript:"), hostile);

// The wizard card carries the block for a catalog service.
const card = renderWizardServiceSetup(svc("alpha"), {});
check("the setup card shows the rating", card.includes("rating-block") && card.includes("C, 15 of 20"), card.slice(0, 400));

console.log(`${checks - failures}/${checks} rating checks passed`);
if (failures) process.exit(1);
