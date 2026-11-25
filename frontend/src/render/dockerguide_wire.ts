// The wiring of the Docker checklist (render/dockerguide.ts): its buttons, the
// wait for Docker's engine, and the screens around it. First run's runtime step
// and the app's runtime page both mount it.
//
// A change runs only as: row button, its confirm screen, Go ahead. Cancel there
// runs nothing. While it runs, a progress line counts the seconds. A failure
// goes back to the list with the reason on its row. A change that needs a
// restart or a sign-out shows that screen only after Go answered, which is when
// Go has written the journal and RunOnce; the restart or sign-out itself runs
// only from that screen's button.
//
// It lives in src/render so the harness compiles it, but it touches elements, so
// it takes everything that talks to Go as deps and never imports the bindings.

import type { FirmwareRestart, HostCheckResult, SetupResumeView, SetupStepResult } from "../wails";
import { START_LIMIT_SECONDS, engineAnswers, renderDockerGuide, setupFinished, waitLine, type GuideModel } from "./dockerguide.js";
import { runLine } from "./dockersteps.js";

export interface DockerGuideDeps {
  hostCheck(): Promise<HostCheckResult>;
  /** The app's latest runtime reading, also used by the card and app controls. */
  currentRuntime(): GuideModel["runtime"];
  startDocker(): Promise<void>;
  dockerEngineReady(): Promise<boolean>;
  openWindowsUpdate(): Promise<void>;
  restartToFirmware(step: string): Promise<FirmwareRestart>;
  cancelSetup(): Promise<void>;
  /** Makes a change, from its confirm screen only. */
  runSetupStep(op: string, webDownload: boolean): Promise<SetupStepResult>;
  /** Restarts or signs out, from their own screen only. */
  restartNow(): Promise<void>;
  signOutNow(): Promise<void>;
  openUrl(url: string): void;
  /** The user chose native apps only, on the skip screen. */
  skip(): void | Promise<void>;
  /** An action failed in a way the user should hear about. */
  failed?(error: unknown): void;
  /** A test can advance the wait clock without sleeping. */
  sleep?(ms: number): Promise<void>;
  now?(): number;
}

/** How often to check Docker Desktop's engine while waiting, in milliseconds. */
const POLL_EVERY_MS = 3000;

export class DockerGuide {
  model: GuideModel;
  private alive = true;

  constructor(
    private holder: Element,
    private deps: DockerGuideDeps,
    init: { resume: SetupResumeView | null; result?: HostCheckResult | null; checkFailed?: boolean },
  ) {
    this.model = {
      steps: init.result?.steps ?? null,
      runtime: deps.currentRuntime(),
      view: "list",
      checking: false,
      checkFailed: init.checkFailed,
      waiting: null,
      waitResult: "",
      // This PC's Docker already runs: the setup a restart interrupted is finished.
      resume: init.result && setupFinished(init.result) ? null : init.resume,
      firmware: null,
      restarting: false,
    };
    this.render();
    if (!init.result && !init.checkFailed) void this.check();
  }

  /** Stop drawing: the screen it was on is gone. */
  dispose(): void {
    this.alive = false;
  }

  showResume(resume: SetupResumeView | null): void {
    this.model.resume = resume;
    this.render();
  }

  render(): void {
    if (!this.alive) return;
    this.model.runtime = this.deps.currentRuntime();
    this.holder.innerHTML = renderDockerGuide(this.model);
    this.wire();
  }

  /** Read the PC again and redraw every row. One reading at a time. */
  async check(): Promise<void> {
    if (this.model.checking || this.model.waiting !== null) return;
    await this.readChecklist();
  }

  private async readChecklist(afterWait = false): Promise<void> {
    this.model.checking = true;
    this.render();
    try {
      const result = await this.deps.hostCheck();
      this.model.checkFailed = false;
      this.applyReading(result, afterWait);
    } catch (error) {
      this.model.steps = null;
      this.model.checkFailed = true;
      this.deps.failed?.(error);
    } finally {
      this.model.checking = false;
      if (afterWait) this.model.waiting = null;
      this.render();
    }
  }

  private applyReading(result: HostCheckResult, afterWait: boolean): void {
    this.model.steps = result.steps ?? [];
    const { steps, waitResult } = this.model;
    const answers = engineAnswers(steps);
    if (waitResult === "timeout" && answers) {
      this.model.waitResult = "checklist-answered";
    } else if (!answers && (waitResult === "answered" || waitResult === "checklist-answered")) {
      // Keep the pipe answer as history if the check at the end of the wait was inconclusive.
      const inconclusive = afterWait && steps.some((step) => step.id === "a6" && step.state === "unknown");
      if (!inconclusive) this.model.waitResult = "";
    }
    if (setupFinished(result)) this.model.resume = null;
  }

  /**
   * Open Docker Desktop (or bring it forward), then wait up to two minutes for its
   * engine, with a line that says how long so far. A check follows either way.
   */
  async startDocker(): Promise<void> {
    if (this.model.waiting !== null || this.model.checking) return;
    // Busy before the first await: a second click while Windows starts Docker
    // Desktop finds the wait under way and starts nothing.
    this.model.waitResult = "";
    this.model.waiting = 0;
    this.render();
    try {
      await this.deps.startDocker();
    } catch (error) {
      this.model.waiting = null;
      this.model.waitResult = "failed";
      this.render();
      this.deps.failed?.(error);
      return;
    }
    await this.waitForEngine();
  }

  /** Wait up to two minutes for Docker's engine, then read the PC again. */
  private async waitForEngine(): Promise<void> {
    const sleep = this.sleeper();
    this.model.waitResult = "";
    this.model.waiting = 0;
    this.render();
    const now = this.deps.now ?? (() => performance.now());
    const started = now();
    const deadline = started + START_LIMIT_SECONDS * 1000;
    let nextPoll = started + POLL_EVERY_MS;
    let answered = false;
    while (this.alive) {
      const remaining = deadline - now();
      if (remaining <= 0) break;
      await sleep(Math.min(1000, remaining));
      if (!this.alive) break;

      const tick = now();
      this.model.waiting = Math.floor((tick - started) / 1000);
      this.updateWaitLine();
      if (tick >= deadline) break;
      if (tick < nextPoll) continue;

      nextPoll = tick + POLL_EVERY_MS;
      try {
        answered = await this.deps.dockerEngineReady();
        if (answered) break;
      } catch {
        // A failed probe can be retried at the next poll.
      }
    }
    if (!this.alive) {
      this.model.waiting = null;
      return;
    }
    this.model.waitResult = answered ? "answered" : "timeout";
    await this.readChecklist(true);
  }

  private sleeper(): (ms: number) => Promise<void> {
    return this.deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  }

  /**
   * Run the change the confirm screen is about. One at a time, and only from
   * that screen.
   */
  private async runStep(): Promise<void> {
    if (this.model.view !== "step-confirm" || this.model.running != null || !this.model.stepOp || !this.model.stepId) return;
    const op = this.model.stepOp;
    const step = this.model.stepId;
    const web = !!this.model.stepWeb;
    this.model.view = "step-running";
    this.model.running = 0;
    this.render();
    const sleep = this.sleeper();
    let done = false;
    void (async () => {
      while (!done && this.alive) {
        await sleep(1000);
        if (done) return;
        this.model.running = (this.model.running ?? 0) + 1;
        const line = this.holder.querySelector("[data-guide-run]");
        if (line) line.outerHTML = runLine(op, this.model.running);
      }
    })();
    let result: SetupStepResult;
    try {
      result = await this.deps.runSetupStep(op, web);
    } catch (error) {
      result = { step, op, state: "failed", text: `CashPilot could not run this change: ${String(error)}`, reopens: false };
    }
    done = true;
    this.model.running = null;
    this.model.result = result;
    const outcomes = { ...(this.model.outcomes ?? {}) };
    delete outcomes[step];
    if (result.state === "restart" || result.state === "signout") {
      // Go wrote the journal and RunOnce before it answered.
      this.model.outcomes = outcomes;
      this.model.resume = {
        active: true, flow: "docker", step, restarted: "no", exeMoved: false, attempts: 1, fromRunOnce: false,
        finish: result.state === "signout" ? "signout" : "restart",
      };
      this.model.view = result.state === "restart" ? "step-restart" : "step-signout";
      this.render();
      return;
    }
    if (result.state === "fallback" && result.url) this.deps.openUrl(result.url);
    if (result.state !== "done") outcomes[step] = result;
    this.model.outcomes = outcomes;
    this.model.view = "list";
    if (op === "linux-engine" && result.state === "done") {
      await this.waitForEngine();
      return;
    }
    await this.check();
  }

  /** Restart now, or Sign out now: only from their own screen, one click at a time. */
  private async finish(how: "restart" | "signout"): Promise<void> {
    const from = this.model.view;
    if (this.model.finishing) return;
    if (how === "restart" && from !== "step-restart" && from !== "step-signout") return;
    if (how === "signout" && from !== "step-signout") return;
    this.model.finishing = true;
    this.render();
    try {
      await (how === "restart" ? this.deps.restartNow() : this.deps.signOutNow());
      return; // Windows is closing the session; the button stays busy.
    } catch (error) {
      this.deps.failed?.(error);
    }
    this.model.finishing = false;
    this.render();
  }

  private updateWaitLine(): void {
    const line = this.holder.querySelector("[data-guide-wait]");
    if (line) line.outerHTML = waitLine(this.model);
  }

  /**
   * One restart at a time: a second click on Restart now, while Go writes the
   * journal and asks Windows, is ignored, and the button shows it is busy.
   */
  private async firmwareRestart(step: string): Promise<void> {
    if (this.model.restarting) return;
    this.model.restarting = true;
    this.render();
    try {
      const result = await this.deps.restartToFirmware(step);
      if (result.started) return; // Windows is restarting; the button stays busy.
      this.model.restarting = false;
      this.model.firmware = result;
      this.model.view = "firmware-failed";
    } catch (error) {
      this.model.restarting = false;
      this.deps.failed?.(error);
      this.model.view = "list";
    }
    this.render();
  }

  private wire(): void {
    const h = this.holder;
    h.querySelectorAll<HTMLButtonElement>("[data-guide-url]").forEach((b) =>
      b.addEventListener("click", () => this.deps.openUrl(b.dataset.guideUrl || "")),
    );
    h.querySelectorAll<HTMLButtonElement>("[data-guide-check]").forEach((b) => b.addEventListener("click", () => void this.check()));
    h.querySelectorAll<HTMLButtonElement>("[data-guide-action]").forEach((b) =>
      b.addEventListener("click", () => void this.act(b.dataset.guideAction || "", b.dataset.guideStep || "", b.dataset)),
    );
  }

  private async act(action: string, step: string, data: DOMStringMap = {}): Promise<void> {
    switch (action) {
      case "step":
        // A row's change, or its direct-download retry: its confirm screen first.
        if (this.model.running != null || this.model.waiting !== null || !data.guideOp || !step) return;
        this.model.view = "step-confirm";
        this.model.stepOp = data.guideOp;
        this.model.stepId = step;
        this.model.stepWeb = data.guideWeb === "1";
        break;
      case "step-go":
        await this.runStep();
        return;
      case "restart-go":
        await this.finish("restart");
        return;
      case "signout-go":
        await this.finish("signout");
        return;
      case "finish-restart":
        this.model.result = null;
        this.model.view = "step-restart";
        break;
      case "finish-signout":
        this.model.result = null;
        this.model.view = "step-signout";
        break;
      case "windows-update":
        await this.deps.openWindowsUpdate().catch((e) => this.deps.failed?.(e));
        return;
      case "start-docker":
      case "show-docker":
        await this.startDocker();
        return;
      case "firmware":
        this.model.view = "firmware-confirm";
        this.model.firmwareStep = step || "a2";
        break;
      case "firmware-go":
        // Only the confirm screen has this button.
        if (this.model.view !== "firmware-confirm") return;
        await this.firmwareRestart(this.model.firmwareStep || "a2");
        return;
      case "skip":
        this.model.view = "skip";
        break;
      case "skip-go":
        if (this.model.view !== "skip") return;
        await this.deps.cancelSetup().catch((e) => this.deps.failed?.(e));
        await this.deps.skip();
        return;
      case "cancel-setup":
        await this.deps.cancelSetup().catch((e) => this.deps.failed?.(e));
        this.model.resume = null;
        break;
      case "back":
        this.model.view = "list";
        break;
      default:
        return;
    }
    this.render();
  }
}
