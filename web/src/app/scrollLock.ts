// Keeping the page itself still on phones. useScrollLock stops the page
// behind a dialog or drawer from scrolling; useTouchLock, in the session
// workspace, lets a drag scroll only something that really scrolls (a list,
// the key bar) and never pans, bounces or pinch-zooms the page, which on iOS
// would also slide the toolbar and key bar out from under the finger.

import { useEffect } from "react";
import { canScrollChain } from "../lib/viewport";
import type { ScrollBox } from "../lib/viewport";

let locks = 0;
let saved = { html: "", body: "" };

/** Stops the page from scrolling while active (counted, so nested dialogs work). */
export function useScrollLock(active: boolean) {
  useEffect(() => {
    if (!active) return;
    const html = document.documentElement;
    const body = document.body;
    if (locks++ === 0) {
      saved = { html: html.style.overflow, body: body.style.overflow };
      html.style.overflow = "hidden";
      body.style.overflow = "hidden";
    }
    return () => {
      if (--locks === 0) {
        html.style.overflow = saved.html;
        body.style.overflow = saved.body;
      }
    };
  }, [active]);
}

const scrollChain = (target: EventTarget | null): ScrollBox[] => {
  const chain: ScrollBox[] = [];
  for (let el = target instanceof Element ? target : null; el && el !== document.body;) {
    const s = getComputedStyle(el);
    chain.push({
      overflowX: s.overflowX,
      overflowY: s.overflowY,
      scrollWidth: el.scrollWidth,
      clientWidth: el.clientWidth,
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
    });
    el = el.parentElement;
  }
  return chain;
};

const inField = (target: EventTarget | null) =>
  target instanceof Element && !!target.closest("input, textarea, select, [contenteditable]");

/**
 * While active, a one-finger drag that has nothing to scroll and every
 * two-finger gesture outside the app's own touch layers is cancelled, so the
 * browser never moves or zooms the page. Layers with touch-action: none get
 * pointer events as before.
 */
export function useTouchLock(active: boolean) {
  useEffect(() => {
    if (!active) return;
    let start: { x: number; y: number } | null = null;
    let lock: boolean | null = null;
    const onStart = (e: TouchEvent) => {
      const t = e.touches[0];
      start = e.touches.length === 1 && t ? { x: t.clientX, y: t.clientY } : null;
      lock = null;
    };
    const onMove = (e: TouchEvent) => {
      if (!e.cancelable) return;
      if (e.touches.length > 1) {
        e.preventDefault();
        return;
      }
      if (lock === null) {
        // Decided once per drag: the browser picks what scrolls on the first move.
        const t = e.touches[0];
        const dx = t && start ? Math.abs(t.clientX - start.x) : 0;
        const dy = t && start ? Math.abs(t.clientY - start.y) : 0;
        lock = !inField(e.target) && !canScrollChain(scrollChain(e.target), dx > dy ? "x" : "y");
      }
      if (lock) e.preventDefault();
    };
    // Safari's own pinch zoom of the page.
    const onGesture = (e: Event) => e.preventDefault();
    const opts = { passive: false, capture: true } as const;
    const watch = { passive: true, capture: true } as const;
    document.addEventListener("touchstart", onStart, watch);
    document.addEventListener("touchmove", onMove, opts);
    document.addEventListener("gesturestart", onGesture, opts);
    document.addEventListener("gesturechange", onGesture, opts);
    return () => {
      document.removeEventListener("touchstart", onStart, watch);
      document.removeEventListener("touchmove", onMove, opts);
      document.removeEventListener("gesturestart", onGesture, opts);
      document.removeEventListener("gesturechange", onGesture, opts);
    };
  }, [active]);
}
