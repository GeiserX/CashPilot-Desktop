// The first-run screens main.ts draws, whole: the brand bar, the step bar, the
// step's panel and the window's title bar. main.ts puts the result in #app and
// wires the buttons, so a check on these strings checks the screen a person sees.
//
// Pure: returns HTML.

import { gatePage, type WindowPlatform } from "./chrome.js"; // .js so the emitted ESM resolves in Node
import { ht } from "./i18n.js";
import { firstRunSteps, renderGateSteps, renderJoinForm, renderModeChoice, renderWelcome, type Mode } from "./mode.js";

export type FirstRunScreen = "welcome" | "mode" | "join";

export interface FirstRunView {
  /** The brand bar at the top (main.ts's gateBar: a logo and the name, no control). */
  bar: string;
  /** The path chosen so far, which decides the steps the bar shows. */
  path: Mode;
  platform: WindowPlatform;
}

/** One first-run screen, as main.ts draws it. */
export function firstRunScreen(screen: FirstRunScreen, view: FirstRunView): string {
  const steps = renderGateSteps(firstRunSteps(view.path), screen);
  if (screen === "welcome") {
    return gatePage(view.bar, `
    ${steps}
    ${renderWelcome()}
  `, { platform: view.platform });
  }
  if (screen === "mode") {
    return gatePage(view.bar, `
    ${steps}
    ${renderModeChoice()}
  `, { wide: true, platform: view.platform });
  }
  return gatePage(view.bar, `
    ${steps}
    <h1 class="gate-title">${ht("firstrun.joinTitle")}</h1>
    ${renderJoinForm()}
  `, { wide: true, platform: view.platform });
}
