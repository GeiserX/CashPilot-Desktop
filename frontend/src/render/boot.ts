// The first draw of the window: wait while the Go side is still starting, then show
// the first screen, or a failed page with a way to try again.
//
// Every bound method answers STILL_STARTING until startCore in app.go has set up the
// config, the store, the catalog and the runtimes. The window can load before that,
// most on a first start, when the PC scans the new program and sets up WebView2. A
// single GetAppState lost that race and drew a dead failed page. bootWhenReady asks
// again with a short backoff, for a bounded time, and only a real error, or a start
// that never ends, reaches the failed page.

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
    ? `CashPilot did not finish starting within ${BOOT_WAIT_MS / 1000} seconds.`
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
}

/**
 * Run load until it succeeds. While it rejects with STILL_STARTING, show the
 * starting page and try again with a backoff. Any other error, or STILL_STARTING
 * after waitMs, shows the failed page. Resolves true when the first screen is drawn.
 */
export async function bootWhenReady(steps: BootSteps): Promise<boolean> {
  const limit = steps.waitMs ?? BOOT_WAIT_MS;
  const start = steps.now();
  let delay = FIRST_DELAY_MS;
  let waiting = false;
  for (;;) {
    try {
      await steps.load();
      return true;
    } catch (error) {
      if (!isStillStarting(error) || steps.now() - start >= limit) {
        steps.showFailed(error);
        return false;
      }
      if (!waiting) {
        steps.showStarting();
        waiting = true;
      }
      await steps.sleep(delay);
      delay = Math.min(delay * 2, MAX_DELAY_MS);
    }
  }
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
