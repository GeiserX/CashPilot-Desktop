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

function titlebar(): string {
  return `
    <div class="titlebar">
      <div class="traffic-lights">
        <button class="traffic close" data-window-action="close" aria-label="Close" title="Close"></button>
        <button class="traffic minimise" data-window-action="minimise" aria-label="Minimise" title="Minimise"></button>
        <button class="traffic maximise" data-window-action="maximise" aria-label="Maximise" title="Maximise"></button>
      </div>
      <span>CashPilot Desktop</span>
    </div>
  `;
}

/** A whole screen: its content, then the title bar. */
export function withChrome(body: string): string {
  return `${body}${titlebar()}`;
}

/** A first-run screen: the brand bar, then one panel. */
export function gatePage(bar: string, panel: string, opts: { wide?: boolean } = {}): string {
  return withChrome(`
    <div class="gate">
      ${bar}
      <main class="gate-main">
        <section class="gate-panel${opts.wide ? " gate-panel-wide" : ""}">
          ${panel}
        </section>
      </main>
    </div>
  `);
}
