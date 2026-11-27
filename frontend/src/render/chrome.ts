// The window chrome every screen shares: the custom title bar with its close,
// minimise and maximise buttons.
//
// Pure: returns HTML. main.ts wires the buttons (wireChrome).
//
// The title bar comes LAST in the markup. Tab follows the markup, so a screen's
// own controls come first and the window controls come after them, as in a
// native app. When the title bar came first, a click that redrew the screen
// left focus nowhere, and the next Tab landed on Close: one stray Enter then
// quit the app. The bar is position: fixed, so it still draws at the top.
//
// The window is frameless on every OS, so the bar draws the controls itself, in
// the layout of the OS: on macOS three coloured dots at the top left, on Windows
// (and Linux) Minimise, Maximise and Close at the top right, each 46x32 as
// Windows draws them. On Windows the dots did not read as window controls.

import { ht } from "./i18n.js"; // .js so the emitted ESM resolves in Node

/** Which layout the title bar uses. Linux has no single native layout, so it uses Windows'. */
export type WindowPlatform = "mac" | "windows";

/** The title bar layout for the webview's user agent: macOS gets the dots, every other OS the Windows controls. */
export function windowPlatform(userAgent: string): WindowPlatform {
  return /Macintosh|Mac OS X/.test(userAgent) ? "mac" : "windows";
}

function macControls(): string {
  return `
      <div class="traffic-lights">
        <button class="traffic close" data-window-action="close" aria-label="${ht("chrome.close")}" title="${ht("chrome.close")}"></button>
        <button class="traffic minimise" data-window-action="minimise" aria-label="${ht("chrome.minimise")}" title="${ht("chrome.minimise")}"></button>
        <button class="traffic maximise" data-window-action="maximise" aria-label="${ht("chrome.maximise")}" title="${ht("chrome.maximise")}"></button>
      </div>`;
}

// The glyphs Windows 11 draws, as 10px strokes in the text colour.
const GLYPH = {
  minimise: `<path d="M0 5.5h10" />`,
  maximise: `<rect x="0.5" y="0.5" width="9" height="9" />`,
  close: `<path d="M0.5 0.5l9 9M9.5 0.5l-9 9" />`,
};

/** label is HTML-escaped text: it goes into attributes. */
function winButton(action: keyof typeof GLYPH, label: string): string {
  return `<button class="win-ctl ${action}" type="button" data-window-action="${action}" aria-label="${label}" title="${label}"><svg viewBox="0 0 10 10" aria-hidden="true" focusable="false">${GLYPH[action]}</svg></button>`;
}

function windowsControls(): string {
  return `
      <div class="window-controls">
        ${winButton("minimise", ht("chrome.minimise"))}${winButton("maximise", ht("chrome.maximise"))}${winButton("close", ht("chrome.close"))}
      </div>`;
}

function titlebar(platform: WindowPlatform): string {
  if (platform === "mac") {
    return `
    <div class="titlebar">${macControls()}
      <span>CashPilot Desktop</span>
    </div>
  `;
  }
  return `
    <div class="titlebar titlebar-win">
      <span>CashPilot Desktop</span>${windowsControls()}
    </div>
  `;
}

/** A whole screen: its content, then the title bar. */
export function withChrome(body: string, platform: WindowPlatform = "mac"): string {
  return `${body}${titlebar(platform)}`;
}

/** A first-run screen: the brand bar, then one panel. */
export function gatePage(bar: string, panel: string, opts: { wide?: boolean; platform?: WindowPlatform } = {}): string {
  return withChrome(`
    <div class="gate">
      ${bar}
      <main class="gate-main">
        <section class="gate-panel${opts.wide ? " gate-panel-wide" : ""}">
          ${panel}
        </section>
      </main>
    </div>
  `, opts.platform);
}
