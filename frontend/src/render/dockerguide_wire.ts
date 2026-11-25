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

import type { FirmwareRestart, HostCheckResult, RuntimeStatus, SetupResumeView, SetupStepResult } from "../wails";
import { START_LIMIT_SECONDS, engineAnswers, renderDockerGuide, setupFinished, waitLine, type GuideModel } from "./dockerguide.js";
import { runLine } from "./dockersteps.js";

export interface DockerGuideDeps {
  hostCheck(): Promise<HostCheckResult>;
  startDocker(): Promise<void>;
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
  /** A reading landed: the caller updates what it shows around the checklist. */
  checked?(result: HostCheckResult): void;
  /** An action failed in a way the user should hear about. */
  failed?(error: unknown): void;
  /** Waits ms; a test can make it instant. */
  sleep?(ms: number): Promise<void>;
}

/** How often the engine is asked while waiting, in seconds. */
const POLL_EVERY = 3;

export class DockerGuide {
  model: GuideModel;
  private alive = true;

  constructor(
    private holder: Element,
    private deps: DockerGuideDeps,
    init: { runtime: RuntimeStatus; resume: SetupResumeView | null; result?: HostCheckResult | null },
  ) {
    const runtime = init.result?.runtime ?? init.runtime;
    this.model = {
      steps: init.result?.steps ?? null,
      runtime,
      view: "list",
      checking: false,
      waiting: null,
      waitResult: "",
      // This PC's Docker already runs: the setup a restart interrupted is finished.
      resume: init.result && setupFinished(init.result) ? null : init.resume,
      firmware: null,
      restarting: false,
    };
    this.render();
    if (!init.result) void this.check();
  }

  /** Stop drawing: the screen it was on is gone. */
  dispose(): void {
    this.alive = false;
  }

  render(): void {
    if (!this.alive) return;
    this.holder.innerHTML = renderDockerGuide(this.model);
    this.wire();
  }

  /** Read the PC again and redraw every row. One reading at a time. */
  async check(): Promise<void> {
    if (this.model.checking) return;
    this.model.checking = true;
    this.render();
    try {
      const result = await this.deps.hostCheck();
      this.applyReading(result);
    } catch (error) {
      this.deps.failed?.(error);
    } finally {
      this.model.checking = false;
      this.render();
    }
  }

  private applyReading(result: HostCheckResult): void {
    this.model.steps = result.steps ?? [];
    this.model.runtime = result.runtime;
    if (setupFinished(result)) this.model.resume = null;
    this.deps.checked?.(result);
  }

  /**
   * Open Docker Desktop (or bring it forward), then wait up to two minutes for its
   * engine, with a line that says how long so far. A check follows either way.
   */
  async startDocker(): Promise<void> {
    if (this.model.waiting !== null) return;
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
    let result: HostCheckResult | null = null;
    while (this.alive && this.model.waiting < START_LIMIT_SECONDS) {
      await sleep(1000);
      this.model.waiting++;
      this.updateWaitLine();
      if (this.model.waiting % POLL_EVERY === 0) {
        try {
          const started = Date.now();
          result = await this.deps.hostCheck();
          this.model.waiting += Math.floor((Date.now() - started) / 1000);
          if (engineAnswers(result.steps)) break;
        } catch {
          // A failed ask is one missed poll; the next one tries again.
        }
      }
    }
    this.model.waiting = null;
    if (!this.alive) return;
    this.model.waitResult = result && engineAnswers(result.steps) ? "ready" : "timeout";
    if (result) {
      this.applyReading(result);
      this.render();
    } else {
      await this.check();
    }
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
