// The Copy buttons' feedback: "Copied" after a successful copy, then back to
// "Copy", so the button does not keep claiming a copy until the next redraw.
//
// Pure: takes the button and the timer, touches no global. main.ts passes the
// real button; the check passes a plain object and a fake timer.

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
