#!/usr/bin/env node
// Browser-free checks on the checklist's consented changes
// (src/render/dockersteps.ts) and their wiring in src/render/dockerguide_wire.ts.
//
//   1. EVERY CONFIRM SCREEN says what changes, whether Windows shows a permission
//      prompt (naming Windows PowerShell for every admin change), whether a
//      restart or sign-out follows, and how to undo it, and has Cancel.
//   2. THE DOCKER INSTALL says CashPilot never accepts Docker's terms, and no
//      screen offers to accept them. It says what to pick in Docker's own
//      installer: the recommended per-user install with WSL 2, Windows
//      containers off, as the checklist's later steps expect.
//   3. CANCEL RUNS NOTHING, and Go ahead acts only from the confirm screen.
//   4. WHILE IT RUNS, a progress line counts the seconds and names the limit.
//   5. A FAILURE goes back to the list, and the row keeps Needs you with the
//      reason and the code; a WSL Store failure offers the direct download, whose
//      confirm screen says so.
//   6. RESTART NEEDED shows the restart screen only once Go answered (when Go has
//      written the journal and RunOnce); that screen says unsaved work can be
//      lost, and only its button restarts.
//   7. SIGN-OUT NEEDED shows the sign-out screen, with Restart instead; only its
//      buttons act.
//   8. THE RESUME BANNER after a docker-users change asks to sign out while the
//      sign-in has not happened, and welcomes back once RunOnce brought CashPilot
//      back.
//   9. A FALLBACK opens the page Go names.
//
//   node scripts/dockersteps_render_check.mjs      # against ./.harness-build

import { CONSENT, ELEVATED_PROMPT, WEB_DOWNLOAD_NOTE, runLine, stepConfirm, stepRestart, stepSignOut } from "../.harness-build/render/dockersteps.js";
import { renderDockerGuide } from "../.harness-build/render/dockerguide.js";
import { DockerGuide } from "../.harness-build/render/dockerguide_wire.js";

let failures = 0;
let checks = 0;

function check(name, condition, detail = "") {
  checks++;
  if (condition) return;
  failures++;
  console.error(`FAIL  ${name}${detail ? `\n      ${detail}` : ""}`);
}

const decode = (s) => s.replace(/&quot;/g, '"').replace(/&#039;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
const text = (html) => decode(html.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim());
const item = (html, key) => text((html.match(new RegExp(`<li data-consent="${key}">([\\s\\S]*?)</li>`)) || [])[1] || "");

const OPS = ["wsl-install", "wsl-update", "docker-install", "docker-users", "linux-engine", "hypervisor-launch", "lanmanserver-auto"];
const ELEVATED = new Set(["wsl-install", "wsl-update", "docker-users", "hypervisor-launch", "lanmanserver-auto"]);
const RESTART = new Set(["wsl-install", "hypervisor-launch"]);

// RULE 1 and 2
check("RULE 1: every change has a confirm screen", OPS.every((op) => CONSENT[op]) && Object.keys(CONSENT).length === OPS.length, Object.keys(CONSENT).join(" "));
for (const op of OPS) {
  const html = stepConfirm(op, false);
  const all = text(html);
  check(`RULE 1: ${op} says what changes`, item(html, "changes").length > 40, item(html, "changes"));
  check(`RULE 1: ${op} says how to undo it`, /^To (undo|switch back)|cannot roll/.test(item(html, "undo")), item(html, "undo"));
  check(`RULE 1: ${op} says whether a restart or sign-out follows`, /restart|sign out/i.test(item(html, "after")), item(html, "after"));
  if (ELEVATED.has(op)) {
    check(`RULE 1: ${op} warns of the prompt naming Windows PowerShell`, item(html, "prompt") === ELEVATED_PROMPT && all.includes("Windows PowerShell"), item(html, "prompt"));
  } else {
    check(`RULE 1: ${op} says which prompt, if any, shows`, /No Windows permission prompt|asks Windows for permission itself/.test(item(html, "prompt")), item(html, "prompt"));
  }
  if (RESTART.has(op)) {
    check(`RULE 1: ${op} says nothing restarts until the click`, item(html, "after").includes("nothing restarts until you click Restart now"), item(html, "after"));
  }
  check(`RULE 1: ${op} has Go ahead and Cancel`, /data-guide-action="step-go"/.test(html) && /data-guide-action="back"[^>]*>Cancel</.test(html), html);
  check(`RULE 1: ${op} names its time limit`, /stops by itself if it takes longer than \d+ minutes/.test(all), all);
  check(`RULE 2: ${op} never offers to accept a licence`, !/accept-license|accept the terms for you|we accept/i.test(all), all);
}
check("RULE 1: WSL installs no distribution", item(stepConfirm("wsl-install", false), "changes").includes("--no-distribution"));
check("RULE 1: docker-users adds only this account, by its SID", /only yours/.test(item(stepConfirm("docker-users", false), "changes")) && /SID/.test(item(stepConfirm("docker-users", false), "changes")));
check("RULE 1: the hypervisor change asks for a restart only when it changed the boot option", /only if CashPilot changed the boot option/.test(item(stepConfirm("hypervisor-launch", false), "after")) && /already auto, no restart follows/.test(item(stepConfirm("hypervisor-launch", false), "after")), item(stepConfirm("hypervisor-launch", false), "after"));
check("RULE 1: the Linux switch says it affects the user's other Docker work", /all your Docker work/.test(item(stepConfirm("linux-engine", false), "changes")));
check("RULE 2: the Docker install says CashPilot never accepts Docker's terms", item(stepConfirm("docker-install", false), "changes").includes("CashPilot never accepts Docker's terms for you"));
{
  const choices = item(stepConfirm("docker-install", false), "choices");
  check("RULE 2: the Docker install says to keep the recommended per-user install", /keep the recommended Per-user installation/.test(choices), choices);
  check("RULE 2: the Docker install says to pick WSL 2", /with WSL 2/.test(choices) && /WSL 2 backend/.test(choices), choices);
  check("RULE 2: the Docker install says to leave Windows containers off", /Leave Allow Windows Containers off/.test(choices) && /Linux containers/.test(choices), choices);
  // The checklist's own Linux switch is the step that expects this choice.
  check("RULE 2: the advice matches the checklist's Linux containers step", CONSENT["linux-engine"].title.includes("Linux containers"), CONSENT["linux-engine"].title);
  check("RULE 2: only the Docker install gives installer advice", OPS.filter((op) => item(stepConfirm(op, false), "choices")).join(",") === "docker-install");
}
check("RULE 5: the direct-download retry says so on its confirm screen", item(stepConfirm("wsl-install", true), "web-download") === WEB_DOWNLOAD_NOTE && item(stepConfirm("wsl-install", false), "web-download") === "");
check("RULE 1: an unknown change has no screen", stepConfirm("format-c", false) === "");
check("RULE 1: text is escaped", !/<script/.test(stepRestart({ step: "a3", op: "wsl-install", state: "restart", text: "<script>x</script>", reopens: false })));

// RULE 4
const line = text(runLine("wsl-install", 42));
check("RULE 4: the progress line counts and names the limit", line.includes("42 s so far") && line.includes("15 minutes"), line);

// RULE 6 and 7: the screens themselves.
const restartHtml = stepRestart({ step: "a3", op: "wsl-install", state: "restart", text: "Done. Windows needs a restart to finish this change.", reopens: true });
check("RULE 6: the restart screen says unsaved work can be lost", text(restartHtml).includes("Save your work first: work you have not saved can be lost."), text(restartHtml));
check("RULE 6: the restart screen has Restart now and Later", /data-guide-action="restart-go"/.test(restartHtml) && /data-guide-action="back"[^>]*>Later</.test(restartHtml));
check("RULE 6: the restart screen says CashPilot reopens", text(restartHtml).includes("opens this checklist by itself"));
const signHtml = stepSignOut({ step: "a8", op: "docker-users", state: "signout", text: "Your account is now in the docker-users group.", reopens: false });
check("RULE 7: the sign-out screen says unsaved work can be lost", text(signHtml).includes("work you have not saved can be lost"), text(signHtml));
check("RULE 7: the sign-out screen offers Sign out now, Restart instead and Later", /signout-go/.test(signHtml) && /restart-go[^>]*>Restart instead</.test(signHtml) && /back[^>]*>Later</.test(signHtml), signHtml);
check("RULE 7: without RunOnce it says to open CashPilot by hand", text(signHtml).includes("open CashPilot"));

// The wiring.
const none = { kind: "", label: "" };
const steps = [
  { id: "a3", title: "WSL 2 backend: Windows features", state: "needs-you", text: "WSL is off.", action: { kind: "elevated", label: "Turn on WSL", op: "wsl-install", manual: "by hand" } },
  { id: "a5", title: "Docker Desktop installed", state: "needs-you", text: "Not installed.", action: { kind: "run", label: "Install Docker Desktop", op: "docker-install", url: "https://www.docker.com/products/docker-desktop/" } },
  { id: "a8", title: "docker-users group", state: "needs-you", text: "Refused.", action: { kind: "elevated", label: "Add my account to docker-users", op: "docker-users", manual: "by hand" } },
  { id: "a9", title: "Linux containers", state: "needs-you", text: "Windows containers.", action: { kind: "run", label: "Switch to Linux containers", op: "linux-engine", manual: "menu" } },
  { id: "a1", title: "Windows version", state: "done", text: "Fine.", action: none },
];
const runtimeDown = { available: false, reason: "wsl-missing", nativeServices: [] };
const holder = () => ({ innerHTML: "", querySelectorAll: () => [], querySelector: () => null });
const deps = (over = {}) => {
  const calls = { run: [], restart: 0, signOut: 0, urls: [], hostCheck: 0 };
  return {
    calls,
    deps: {
      currentRuntime: () => runtimeDown,
      hostCheck: async () => { calls.hostCheck++; return { facts: { supported: true }, runtime: runtimeDown, steps }; },
      dockerEngineReady: async () => false,
      startDocker: async () => {},
      openWindowsUpdate: async () => {},
      restartToFirmware: async () => ({ started: false, reopens: false }),
      cancelSetup: async () => {},
      runSetupStep: async (op, web) => { calls.run.push(`${op}${web ? "+web" : ""}`); return { step: "a3", op, state: "done", text: "Done.", reopens: false }; },
      restartNow: async () => { calls.restart++; },
      signOutNow: async () => { calls.signOut++; },
      openUrl: (url) => calls.urls.push(url),
      skip: () => {},
      sleep: () => new Promise((r) => setTimeout(r, 0)),
      ...over,
    },
  };
};
const open = (d) => new DockerGuide(holder(), d, { resume: null, result: { facts: { supported: true }, runtime: runtimeDown, steps } });
const rowHtml = (html, id) => (html.match(new RegExp(`<li class="docker-step" data-step="${id}" data-state="([^"]*)">([\\s\\S]*?)</li>`)) || []);

// RULE 3
{
  const { calls, deps: d } = deps();
  const g = open(d);
  await g.act("step-go", "");
  check("RULE 3: Go ahead from the list runs nothing", calls.run.length === 0);
  await g.act("step", "a3", { guideOp: "wsl-install" });
  check("RULE 3: the row's button opens the confirm screen", g.model.view === "step-confirm" && /data-op="wsl-install"/.test(renderDockerGuide(g.model)), g.model.view);
  check("RULE 3: opening it runs nothing", calls.run.length === 0);
  await g.act("back", "");
  check("RULE 3: Cancel goes back to the list", g.model.view === "list");
  await g.act("step-go", "");
  check("RULE 3: Cancel then Go ahead runs nothing", calls.run.length === 0, JSON.stringify(calls.run));
  await g.act("restart-go", "");
  await g.act("signout-go", "");
  check("RULE 6: a restart or sign-out from the list does nothing", calls.restart === 0 && calls.signOut === 0);
  await g.act("step", "a3", { guideOp: "wsl-install" });
  await g.act("step-go", "");
  check("RULE 3: Go ahead on the confirm screen runs the change once", calls.run.join() === "wsl-install", JSON.stringify(calls.run));
  check("RULE 3: a done change reads the PC again and goes back to the list", g.model.view === "list" && calls.hostCheck === 1, `${g.model.view} ${calls.hostCheck}`);
}

// RULE 4: the progress line while it runs, and a second Go ahead does nothing.
{
  let answer;
  const { calls, deps: d } = deps({ runSetupStep: (op) => { calls.run.push(op); return new Promise((r) => { answer = r; }); } });
  const g = open(d);
  await g.act("step", "a3", { guideOp: "wsl-install" });
  const first = g.act("step-go", "");
  await g.act("step-go", "");
  check("RULE 4: it shows the running screen", g.model.view === "step-running" && /data-guide-run/.test(renderDockerGuide(g.model)), g.model.view);
  check("RULE 4: an admin change says the PowerShell prompt is this change", text(renderDockerGuide(g.model)).includes("permission prompt naming Windows PowerShell"));
  check("RULE 4: a second Go ahead runs nothing", calls.run.length === 1, JSON.stringify(calls.run));
  await new Promise((r) => setTimeout(r, 20));
  check("RULE 4: the seconds count up", (g.model.running ?? 0) >= 1, String(g.model.running));
  answer({ step: "a3", op: "wsl-install", state: "done", text: "Done.", reopens: false });
  await first;
  check("RULE 4: the count stops when Go answers", g.model.running === null && g.model.view === "list");
}

// RULE 5
{
  const failed = { step: "a3", op: "wsl-install", state: "failed", text: "WSL could not download through the Microsoft Store (code 0x80072EE7). You can try again with a direct download from Microsoft's GitHub page.", code: "0x80072EE7", retry: "web-download", reopens: false };
  const { calls, deps: d } = deps({ runSetupStep: async (op, web) => { calls.run.push(`${op}${web ? "+web" : ""}`); return failed; } });
  const g = open(d);
  await g.act("step", "a3", { guideOp: "wsl-install" });
  await g.act("step-go", "");
  const html = renderDockerGuide(g.model);
  const [, state, row] = rowHtml(html, "a3");
  check("RULE 5: a failure goes back to the list", g.model.view === "list");
  check("RULE 5: the row stays Needs you", state === "needs-you", state);
  check("RULE 5: the row shows the reason with its code", text(row).includes("0x80072EE7") && /data-outcome="failed"/.test(row), row);
  check("RULE 5: the code is not shown twice", text(row).split("0x80072EE7").length === 2, text(row));
  check("RULE 5: the row offers the direct download", /data-guide-op="wsl-install" data-guide-step="a3" data-guide-web="1"/.test(row), row);
  await g.act("step", "a3", { guideOp: "wsl-install", guideWeb: "1" });
  check("RULE 5: the retry opens the confirm screen with the direct download", g.model.view === "step-confirm" && g.model.stepWeb === true && text(renderDockerGuide(g.model)).includes("Microsoft's GitHub page"));
  await g.act("step-go", "");
  check("RULE 5: the retry runs with --web-download", calls.run.join() === "wsl-install,wsl-install+web", JSON.stringify(calls.run));
  // A done row drops its old reason.
  const doneSteps = steps.map((s) => (s.id === "a3" ? { ...s, state: "done", action: none } : s));
  check("RULE 5: a row that is done shows no old reason", !/data-outcome/.test(rowHtml(renderDockerGuide({ ...g.model, steps: doneSteps }), "a3")[2] || ""));
  // A policy block stops: no retry.
  const policy = { step: "a3", op: "wsl-install", state: "policy", text: "A policy on this PC does not allow this.", code: "0x20000001", reopens: false };
  const { deps: d2 } = deps({ runSetupStep: async () => policy });
  const g2 = open(d2);
  await g2.act("step", "a3", { guideOp: "wsl-install" });
  await g2.act("step-go", "");
  const row2 = rowHtml(renderDockerGuide(g2.model), "a3")[2];
  check("RULE 5: a policy block explains and offers no retry", /data-outcome="policy"/.test(row2) && !/data-guide-web/.test(row2), row2);
  // A thrown error is a failure on the row, never a crash.
  const { deps: d3 } = deps({ runSetupStep: async () => { throw new Error("app is still starting"); } });
  const g3 = open(d3);
  await g3.act("step", "a12", { guideOp: "lanmanserver-auto" });
  await g3.act("step-go", "");
  check("RULE 5: an error is kept as the row's reason", g3.model.outcomes?.a12?.state === "failed" && g3.model.view === "list");
}

// RULE 6
{
  let goWrote = false;
  let answer;
  const { calls, deps: d } = deps({
    runSetupStep: (op) => { calls.run.push(op); return new Promise((r) => { answer = r; }); },
  });
  const g = open(d);
  await g.act("step", "a3", { guideOp: "wsl-install" });
  const run = g.act("step-go", "");
  check("RULE 6: no restart screen while Go has not answered", g.model.view === "step-running" && !/restart-go/.test(renderDockerGuide(g.model)));
  goWrote = true; // Go writes the journal and RunOnce, then answers.
  answer({ step: "a3", op: "wsl-install", state: "restart", text: "Done. Windows needs a restart to finish this change.", reopens: true });
  await run;
  const html = renderDockerGuide(g.model);
  check("RULE 6: the restart screen comes after Go wrote the journal", goWrote && g.model.view === "step-restart" && /restart-go/.test(html), g.model.view);
  check("RULE 6: nothing restarted by itself", calls.restart === 0);
  check("RULE 6: the banner waits for the restart", g.model.resume?.restarted === "no" && g.model.resume?.step === "a3");
  await g.act("signout-go", "");
  check("RULE 6: the restart screen has no sign-out", calls.signOut === 0);
  await g.act("back", "");
  const list = renderDockerGuide(g.model);
  check("RULE 6: Later keeps a 'Restart to finish' banner", /data-resume="same-boot"/.test(list) && /data-guide-action="finish-restart"/.test(list), list);
  await g.act("restart-go", "");
  check("RULE 6: Restart now from the list does nothing", calls.restart === 0);
  await g.act("finish-restart", "");
  await g.act("restart-go", "");
  check("RULE 6: Restart now on its screen restarts once", calls.restart === 1);
  check("RULE 6: the button stays busy while Windows restarts", g.model.finishing === true && /Restarting…/.test(renderDockerGuide(g.model)));
}

// RULE 7 and 8
{
  const { calls, deps: d } = deps({ runSetupStep: async (op) => ({ step: "a8", op, state: "signout", text: "Your account is now in the docker-users group.", reopens: true }) });
  const g = open(d);
  await g.act("step", "a8", { guideOp: "docker-users" });
  await g.act("step-go", "");
  check("RULE 7: a docker-users change asks for a sign-out", g.model.view === "step-signout" && g.model.resume?.finish === "signout");
  await g.act("back", "");
  const list = renderDockerGuide(g.model);
  check("RULE 8: the banner asks to sign out", /data-resume="sign-out"/.test(list) && /finish-signout/.test(list), list);
  await g.act("finish-signout", "");
  await g.act("signout-go", "");
  check("RULE 7: Sign out now on its screen signs out once", calls.signOut === 1 && calls.restart === 0);
  const back = renderDockerGuide({ ...g.model, view: "list", resume: { active: true, flow: "docker", step: "a8", restarted: "no", exeMoved: false, attempts: 1, fromRunOnce: true, finish: "signout" } });
  check("RULE 8: after the sign-in RunOnce brought back, it welcomes back", /data-resume="signed-in"/.test(back) && !/Restart to finish/.test(text(back)), text(back).slice(0, 300));
  const byHand = renderDockerGuide({ ...g.model, view: "list", resume: { active: true, flow: "docker", step: "a8", restarted: "no", exeMoved: false, attempts: 1, fromRunOnce: false, finish: "signout", signedIn: true } });
  check("RULE 8: opened by hand with the group in the token, it asks for no sign-out", /data-resume="signed-in"/.test(byHand) && !/finish-signout/.test(byHand) && !/Restart to finish/.test(text(byHand)), text(byHand).slice(0, 300));
  const failing = deps({ runSetupStep: async (op) => ({ step: "a8", op, state: "signout", text: "x", reopens: false }), signOutNow: async () => { throw new Error("refused"); } });
  const g2 = open(failing.deps);
  await g2.act("step", "a8", { guideOp: "docker-users" });
  await g2.act("step-go", "");
  await g2.act("signout-go", "");
  check("RULE 7: a sign-out Windows refused frees the button", g2.model.finishing === false && g2.model.view === "step-signout");
}

// RULE 9
{
  const { calls, deps: d } = deps({ runSetupStep: async (op) => ({ step: "a5", op, state: "fallback", text: "winget is not on this PC.", url: "https://www.docker.com/products/docker-desktop/", reopens: false }) });
  const g = open(d);
  await g.act("step", "a5", { guideOp: "docker-install" });
  check("RULE 9: the install says no Windows PowerShell prompt of its own", !text(renderDockerGuide(g.model)).includes(ELEVATED_PROMPT));
  await g.act("step-go", "");
  check("RULE 9: no winget opens the download page", calls.urls.join() === "https://www.docker.com/products/docker-desktop/", JSON.stringify(calls.urls));
  check("RULE 9: and the row says why", /data-outcome="fallback"/.test(rowHtml(renderDockerGuide(g.model), "a5")[2] || ""));
  check("RULE 9: the install row also offers the download page itself", /data-guide-url="https:\/\/www.docker.com\/products\/docker-desktop\/"/.test(rowHtml(renderDockerGuide(g.model), "a5")[2] || ""));
}

if (failures > 0) {
  console.error(`\n${failures} of ${checks} consented-change checks failed`);
  process.exit(1);
}
console.log(`dockersteps: ${checks} checks passed`);
