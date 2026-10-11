/* Phone navigation for /jazz/: below the sidebar breakpoint the sidebar folds
 * away and the sticky top bar gets a menu button that slides the same nav in
 * as a drawer. It closes on a link, the scrim, Esc, a view change, or when the
 * window grows back to the sidebar layout. */
(() => {
  "use strict";
  const button = document.getElementById("jazz-menu-button");
  const nav = document.getElementById("jazz-nav");
  const scrim = document.getElementById("jazz-nav-scrim");
  if (!button || !nav || !scrim) return;

  const isOpen = () => document.body.classList.contains("jazz-nav-open");
  function setOpen(open, { focus = true } = {}) {
    if (open === isOpen()) return;
    document.body.classList.toggle("jazz-nav-open", open);
    scrim.hidden = !open;
    button.setAttribute("aria-expanded", String(open));
    button.setAttribute("aria-label", open ? "Close menu" : "Open menu");
    if (open) {
      if (focus) (nav.querySelector(".virtuoso-nav-links .active") || nav.querySelector("a"))?.focus({ preventScroll: true });
    } else if (focus && nav.contains(document.activeElement)) {
      button.focus({ preventScroll: true });
    }
  }

  button.addEventListener("click", () => setOpen(!isOpen()));
  scrim.addEventListener("click", () => setOpen(false));
  nav.addEventListener("click", (event) => { if (event.target.closest("a")) setOpen(false, { focus: false }); });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && isOpen()) { event.preventDefault(); setOpen(false); }
  });
  addEventListener("hashchange", () => setOpen(false, { focus: false }));
  // The drawer only exists while the top bar does; a rotate to landscape brings the sidebar back.
  addEventListener("resize", () => { if (isOpen() && !button.getClientRects().length) setOpen(false, { focus: false }); });
})();
