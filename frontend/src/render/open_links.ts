// External links. Every button with data-url opens its URL in the system browser.
//
// A page used to wire them twice: once in wireShellNav and once in its own render
// function (Catalog, Dashboard, the setup wizard). Each click then ran two
// listeners and opened the browser twice. Now one function wires them, and it
// skips a button it has already wired, so a second call on the same page adds
// nothing.

const wired = new WeakSet<object>();

type LinkButton = Pick<HTMLElement, "dataset" | "addEventListener">;

/** Wire every [data-url] button inside scope to open(url), once per button. */
export function wireOpenLinks(scope: Pick<ParentNode, "querySelectorAll">, open: (url: string) => void): void {
  scope.querySelectorAll<HTMLElement>("[data-url]").forEach((button: LinkButton) => {
    if (wired.has(button)) return;
    wired.add(button);
    button.addEventListener("click", () => {
      const url = button.dataset.url;
      if (url) open(url);
    });
  });
}
