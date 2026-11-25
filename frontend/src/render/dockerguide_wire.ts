// The wiring of the Docker checklist (render/dockerguide.ts): its buttons, the
// wait for Docker's engine, and the screens around it. First run's runtime step
// and the app's runtime page both mount it.
//
// It lives in src/render so the harness compiles it, but it touches elements, so
// it takes everything that talks to Go as deps and never imports the bindings.

import type { FirmwareRestart, HostCheckResult, RuntimeStatus, SetupResumeView } from "../wails";
import { START_LIMIT_SECONDS, engineAnswers, renderDockerGuide, waitLine, type GuideModel } from "./dockerguide.js";

export interface DockerGuideDeps {
  hostCheck(): Promise<HostCheckResult>;
  checkRuntime(): Promise<RuntimeStatus>;
  startDocker(): Promise<void>;
  openWindowsUpdate(): Promise<void>;
  restartToFirmware(step: string): Promise<FirmwareRestart>;
  cancelSetup(): Promise<void>;
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
    this.model = {
      steps: init.result?.steps ?? null,
      runtime: init.result?.runtime ?? init.runtime,
      view: "list",
      checking: false,
      waiting: null,
      waitResult: "",
      resume: init.resume,
      firmware: null,
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
      this.model.steps = result.steps ?? [];
      this.model.runtime = result.runtime;
      this.deps.checked?.(result);
    } catch (error) {
      this.deps.failed?.(error);
    } finally {
      this.model.checking = false;
      this.render();
    }
  }

  /**
   * Open Docker Desktop (or bring it forward), then wait up to two minutes for its
   * engine, with a line that says how long so far. A check follows either way.
   */
  async startDocker(): Promise<void> {
    if (this.model.waiting !== null) return;
    this.model.waitResult = "";
    try {
      await this.deps.startDocker();
    } catch (error) {
      this.model.waitResult = "failed";
      this.render();
      this.deps.failed?.(error);
      return;
    }
    const sleep = this.deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
    this.model.waiting = 0;
    this.render();
    let up = false;
    while (this.alive && this.model.waiting < START_LIMIT_SECONDS) {
      await sleep(1000);
      this.model.waiting++;
      this.updateWaitLine();
      if (this.model.waiting % POLL_EVERY === 0) {
        try {
          if (engineAnswers(await this.deps.checkRuntime())) {
            up = true;
            break;
          }
        } catch {
          // A failed ask is one missed poll; the next one tries again.
        }
      }
    }
    this.model.waiting = null;
    this.model.waitResult = up ? "ready" : "timeout";
    await this.check();
  }

  private updateWaitLine(): void {
    const line = this.holder.querySelector("[data-guide-wait]");
    if (line) line.outerHTML = waitLine(this.model);
  }

  private async firmwareRestart(step: string): Promise<void> {
    try {
      const result = await this.deps.restartToFirmware(step);
      if (result.started) return; // Windows is restarting.
      this.model.firmware = result;
      this.model.view = "firmware-failed";
    } catch (error) {
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
      b.addEventListener("click", () => void this.act(b.dataset.guideAction || "", b.dataset.guideStep || "")),
    );
  }

  private async act(action: string, step: string): Promise<void> {
    switch (action) {
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
