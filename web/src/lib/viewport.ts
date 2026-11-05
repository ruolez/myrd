// Phone viewport rules that don't touch the browser: when the on-screen
// keyboard is up, how the viewport meta tag asks the keyboard to behave, and
// whether a touch drag has anything to scroll (everything else is locked in
// the session workspace, so the page itself never pans or bounces).

/** Less than this between the window and what is visible is browser chrome, not a keyboard. */
export const KEYBOARD_MIN_PX = 120;

export type Visible = { height: number; scale: number };

/**
 * The on-screen keyboard covers part of a window layoutHeight tall. A pinch
 * zoom shrinks the visible height too, so it is measured unzoomed.
 */
export function keyboardOpen(visible: Visible, layoutHeight: number): boolean {
  return layoutHeight - visible.height * Math.max(1, visible.scale) >= KEYBOARD_MIN_PX;
}

export type KeyboardWidget = "resizes-visual" | "resizes-content";

/**
 * The viewport meta content with interactive-widget set (or removed with
 * null). resizes-visual keeps the layout still under the keyboard (the
 * session follows window.visualViewport itself); resizes-content, the
 * default, shrinks the page so forms and dialogs stay above it.
 */
export function viewportContent(content: string, widget: KeyboardWidget | null): string {
  const parts = content
    .split(",")
    .map((p) => p.trim())
    .filter((p) => p && !/^interactive-widget\s*=/i.test(p));
  if (widget) parts.push(`interactive-widget=${widget}`);
  return parts.join(", ");
}

/** One element between a touch and the page root, as getComputedStyle and its scroll sizes see it. */
export type ScrollBox = {
  overflowX: string;
  overflowY: string;
  scrollWidth: number;
  clientWidth: number;
  scrollHeight: number;
  clientHeight: number;
};

const scrolls = (overflow: string, size: number, client: number) =>
  (overflow === "auto" || overflow === "scroll") && size > client + 1;

/**
 * Whether a drag over this chain of elements (innermost first) scrolls one
 * of them. When none can, the browser would pan or bounce the page instead.
 */
export function canScrollChain(chain: readonly ScrollBox[], axis: "x" | "y"): boolean {
  return chain.some((b) =>
    axis === "y"
      ? scrolls(b.overflowY, b.scrollHeight, b.clientHeight)
      : scrolls(b.overflowX, b.scrollWidth, b.clientWidth),
  );
}
