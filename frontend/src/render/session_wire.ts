// The wiring of the desktop-app risk disclosure (render/session.ts): the tickbox,
// the accept button and the Withdraw buttons.
//
// This is the one file in src/render that touches elements. It lives here so the
// harness compiles it and scripts/session_disclosure_render_check.mjs runs it against
// the real rendered markup. The gate is in these few lines: the accept button stays
// disabled until the tick, and a click is ignored while the box is not ticked.
//
// It finds the tickbox and the accept button of one disclosure through that
// disclosure's own element, never by slug across the document, so it needs no
// selector escaping and two disclosures on one page never touch each other.

export interface SessionConsentActions {
  /** Record the consent for slug, at the version of the text the user read. */
  accept(slug: string, textVersion: string): void;
  /** Delete the consent for slug. */
  revoke(slug: string): void;
}

/** Wire every disclosure, accept and Withdraw button inside scope. */
export function wireSessionConsent(scope: ParentNode, actions: SessionConsentActions): void {
  scope.querySelectorAll<HTMLInputElement>("[data-session-tick]").forEach((tick) => {
    tick.addEventListener("change", () => {
      const button = tick.closest("[data-session-disclosure]")?.querySelector<HTMLButtonElement>("[data-session-accept]");
      if (button) button.disabled = !tick.checked;
    });
  });
  scope.querySelectorAll<HTMLButtonElement>("[data-session-accept]").forEach((button) => {
    button.addEventListener("click", () => {
      // The button is disabled until the tick, but the tick is the consent: check it again.
      const tick = button.closest("[data-session-disclosure]")?.querySelector<HTMLInputElement>("[data-session-tick]");
      if (!tick?.checked) return;
      actions.accept(button.dataset.sessionAccept || "", button.dataset.sessionVersion || "");
    });
  });
  scope.querySelectorAll<HTMLButtonElement>("[data-session-revoke]").forEach((button) => {
    button.addEventListener("click", () => actions.revoke(button.dataset.sessionRevoke || ""));
  });
}

/**
 * A queue that runs each call after the one before it has finished.
 *
 * Each Accept or Withdraw returns the whole consent map, and the app replaces its
 * copy with it. Two calls in flight could answer out of order, and the older map
 * would undo the newer change on screen. A call that fails does not stop the next.
 */
export function oneAtATime(): (call: () => Promise<void>) => Promise<void> {
  let last: Promise<void> = Promise.resolve();
  return (call) => {
    last = last.then(call, call);
    return last;
  };
}
