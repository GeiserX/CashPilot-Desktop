// A Catalog row opens its service's setup card, the one the Add service wizard
// shows with that service picked. Before this, a row did nothing on click, and a
// service with only a Visit button (a desktop app such as Wipter) could be set up
// only through the wizard.
//
// The row's name is a button (render/catalog.ts), so the keyboard reaches it and
// Enter opens it. A click anywhere else on the row opens it too, except on the
// row's own buttons and links (Add, Manage, Sign up, Visit), which do their own job.

/** The controls inside a row that keep their own click. */
const OWN_CONTROLS = "button, a, input, select, textarea, summary, label";

/** The slug a click on a row opens, or null when the click belongs to one of the row's own controls. */
export function catalogOpenTarget(row: Pick<HTMLElement, "dataset">, target: Pick<Element, "closest"> | null): string | null {
  const control = target?.closest(OWN_CONTROLS);
  if (control && !control.hasAttribute("data-catalog-open-name")) return null;
  return row.dataset.catalogOpen || null;
}

/** Wire every catalog row inside scope to open(slug). */
export function wireCatalogRows(scope: ParentNode, open: (slug: string) => void): void {
  scope.querySelectorAll<HTMLElement>("[data-catalog-open]").forEach((row) => {
    row.addEventListener("click", (event) => {
      const slug = catalogOpenTarget(row, event.target as Element | null);
      if (slug) open(slug);
    });
  });
}
