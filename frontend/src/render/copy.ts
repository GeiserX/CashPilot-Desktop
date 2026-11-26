// The Copy buttons' feedback: "Copied" after a successful copy, then back to
// "Copy", so the button does not keep claiming a copy until the next redraw.
//
// Pure: takes the button and the timer, touches no global. main.ts passes the
// real button; the check passes a plain object and a fake timer.

/**
 * Wire every [data-copy-text] button inside scope to copy its value, once per
 * button, so a second call on the same page adds no second listener. Used by the
 * referral code a user types by hand (details.ts referralNote).
 */
const wiredCopy = new WeakSet<object>();

type CopyButton = Pick<HTMLElement, "dataset" | "addEventListener" | "textContent">;

export function wireCopyText(
  scope: Pick<ParentNode, "querySelectorAll">,
  write: (text: string) => Promise<void>,
  failed: (error: unknown) => void,
): void {
  scope.querySelectorAll<HTMLElement>("[data-copy-text]").forEach((button: CopyButton) => {
    if (wiredCopy.has(button)) return;
    wiredCopy.add(button);
    button.addEventListener("click", async () => {
      try {
        await write(button.dataset.copyText || "");
        flashCopied(button);
      } catch (error) {
        failed(error);
      }
    });
  });
}

/** How long a Copy button says "Copied". */
export const COPIED_MS = 2000;

export function flashCopied(
  button: { textContent: string | null },
  schedule: (reset: () => void, ms: number) => unknown = (reset, ms) => setTimeout(reset, ms),
): void {
  button.textContent = "Copied";
  schedule(() => {
    if (button.textContent === "Copied") button.textContent = "Copy";
  }, COPIED_MS);
}
