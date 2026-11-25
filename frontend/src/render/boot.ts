// The first draw of the window: wait while the Go side is still starting, then show
// the first screen, or a failed page with a way to try again.
//
// Every bound method answers STILL_STARTING until startCore in app.go has set up the
// config, the store, the catalog and the runtimes. The window can load before that,
// most on a first start, when the PC scans the new program and sets up WebView2. A
// single GetAppState lost that race and drew a dead failed page. bootWhenReady asks
// again with a short backoff, for a bounded time, and only a real error, or a start
// that never ends, reaches the failed page. Behind a failed page for a slow start
// it keeps asking, so the window moves on by itself when the start does finish.
//
// createBoot holds the whole boot main.ts runs: the starting and failed pages in
// #app, the title bar's wiring, and Try again. main.ts passes only its bindings and
// its draw functions, so the checks run the boot the window runs.

import { escapeHtml } from "./format.js";

/**
 * The exact text of errStillStarting in app.go. A Wails binding rejects with an Error
 * whose message is the Go error's text, so this text is the signal. A Go test
 * (TestStillStartingMatchesFrontend) keeps the two equal.
 */
export const STILL_STARTING = "app is still starting";

/** How long the window waits for the Go side to finish starting. */
export const BOOT_WAIT_MS = 60_000;

const FIRST_DELAY_MS = 200;
const MAX_DELAY_MS = 2_000;

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** Whether a rejected binding call means the Go side has not finished starting. */
export function isStillStarting(error: unknown): boolean {
  return errorText(error) === STILL_STARTING;
}

/** The calm page shown while the Go side starts. titlebar is the window's own bar. */
export function renderStarting(titlebar: string): string {
  return `${titlebar}<main class="center" data-boot="starting"><section class="panel"><h1>Starting CashPilot…</h1><p>The first start on this computer can take a minute.</p></section></main>`;
}

/** The failed page, with the error and a Try again button. */
export function renderFailed(titlebar: string, error: unknown): string {
  const text = isStillStarting(error)
    ? `CashPilot did not finish starting within ${BOOT_WAIT_MS / 1000} seconds. It keeps checking: this page moves on by itself when CashPilot is ready.`
    : errorText(error);
  return `${titlebar}<main class="center" data-boot="failed"><section class="panel"><h1>CashPilot failed to start</h1><pre>${escapeHtml(text)}</pre><div class="gate-actions"><button class="btn btn-primary" type="button" data-boot-retry>Try again</button></div></section></main>`;
}

export interface BootSteps {
  /** Read the state and draw the first screen. Rejects as the bindings do. */
  load(): Promise<void>;
  /** Draw the starting page. Called once, on the first STILL_STARTING. */
  showStarting(): void;
  /** Draw the failed page for error. */
  showFailed(error: unknown): void;
  sleep(ms: number): Promise<void>;
  now(): number;
  /** The wait before the failed page; BOOT_WAIT_MS when unset. */
  waitMs?: number;
  /** False once a newer boot (Try again) has taken over: this one then stops. */
  isCurrent?: () => boolean;
}

/**
 * Run load until it succeeds. While it rejects with STILL_STARTING, show the
 * starting page and try again with a backoff. Any other error shows the failed
 * page and stops. STILL_STARTING after waitMs shows the failed page once and keeps
 * asking behind it, so a start that finishes later still reaches the first screen.
 * Resolves true when the first screen is drawn, false when it stops.
 */
export async function bootWhenReady(steps: BootSteps): Promise<boolean> {
  const limit = steps.waitMs ?? BOOT_WAIT_MS;
  const current = steps.isCurrent ?? (() => true);
  const start = steps.now();
  let delay = FIRST_DELAY_MS;
  let waiting = false;
  let failedShown = false;
  for (;;) {
    if (!current()) return false;
    try {
      await steps.load();
      return true;
    } catch (error) {
      if (!current()) return false;
      if (!isStillStarting(error)) {
        steps.showFailed(error);
        return false;
      }
      if (steps.now() - start >= limit) {
        if (!failedShown) {
          steps.showFailed(error);
          failedShown = true;
        }
      } else if (!waiting) {
        steps.showStarting();
        waiting = true;
      }
      await steps.sleep(delay);
      delay = Math.min(delay * 2, MAX_DELAY_MS);
    }
  }
}

/** What createBoot needs from main.ts. */
export interface BootPage {
  /** #app: the boot's pages go here. */
  root: ParentNode & { innerHTML: string };
  /** The window's title bar around a page (main.ts's withChrome). */
  chrome(body: string): string;
  /** Wire the title bar's buttons after a page is drawn. */
  wireChrome(): void;
  /** Read the state and draw the first screen. Rejects as the bindings do. */
  load(): Promise<void>;
  sleep(ms: number): Promise<void>;
  now(): number;
  waitMs?: number;
}

/**
 * The window's boot: returns boot(), which draws the starting page while the Go
 * side starts, the failed page with Try again when it fails, and the first screen
 * when it is ready. Try again calls boot() again, and a newer boot stops the older
 * one, so two never poll at once.
 */
export function createBoot(page: BootPage): () => Promise<boolean> {
  let generation = 0;
  const boot = (): Promise<boolean> => {
    const mine = ++generation;
    return bootWhenReady({
      load: page.load,
      showStarting: () => {
        page.root.innerHTML = page.chrome(renderStarting(""));
        page.wireChrome();
      },
      showFailed: (error) => {
        page.root.innerHTML = page.chrome(renderFailed("", error));
        page.wireChrome();
        wireBootRetry(page.root, () => void boot());
      },
      sleep: page.sleep,
      now: page.now,
      waitMs: page.waitMs,
      isCurrent: () => mine === generation,
    });
  };
  return boot;
}

/** Wire the failed page's Try again button inside scope. */
export function wireBootRetry(scope: ParentNode, retry: () => void): void {
  scope.querySelectorAll<HTMLButtonElement>("[data-boot-retry]").forEach((button) => {
    button.addEventListener("click", () => {
      button.disabled = true;
      retry();
    });
  });
}
