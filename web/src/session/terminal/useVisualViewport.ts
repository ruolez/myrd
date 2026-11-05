import { useEffect, useState } from "react";
import { keyboardOpen } from "../../lib/viewport";

export type VisualPort = {
  height: number;
  /** How far iOS scrolled the page to keep the focused field visible. */
  offsetTop: number;
  /** The on-screen keyboard covers part of the window. */
  keyboard: boolean;
};

const NON_TEXT = new Set([
  "button",
  "checkbox",
  "color",
  "file",
  "hidden",
  "image",
  "radio",
  "range",
  "reset",
  "submit",
]);

/** A field that raises the on-screen keyboard has focus. */
export const typingFocused = (el: Element | null = document.activeElement) =>
  el instanceof HTMLTextAreaElement
    ? !el.readOnly
    : el instanceof HTMLInputElement
      ? !NON_TEXT.has(el.type) && !el.readOnly
      : el instanceof HTMLElement && el.isContentEditable;

const read = (): VisualPort => {
  const vv = window.visualViewport;
  const layout = Math.max(window.innerHeight, document.documentElement.clientHeight);
  const height = vv?.height ?? window.innerHeight;
  return {
    height,
    offsetTop: vv?.offsetTop ?? 0,
    keyboard: keyboardOpen({ height, scale: vv?.scale ?? 1 }, layout),
  };
};

// iOS reports the viewport late (and in standalone apps sometimes not at
// all) after its keyboard closes: measure again a few times.
const SETTLE_MS = [80, 250, 600];

/**
 * The part of the window the user sees, while a text field has focus (null
 * otherwise: the layout then simply fills the window). With
 * interactive-widget=resizes-visual an on-screen keyboard shrinks only this,
 * not 100dvh, so layouts that must sit right on the keyboard follow it. Only
 * following it while typing keeps a viewport that iOS left shrunk or offset
 * after the keyboard went away from squeezing the page.
 */
export function useVisualViewport(enabled: boolean): VisualPort | null {
  const [port, setPort] = useState<VisualPort | null>(null);
  useEffect(() => {
    if (!enabled) {
      setPort(null);
      return;
    }
    const vv = window.visualViewport;
    let raf = 0;
    let timers: ReturnType<typeof setTimeout>[] = [];
    const update = () => {
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(() => {
        if (!typingFocused()) return setPort(null);
        const next = read();
        setPort((p) =>
          p &&
          p.height === next.height &&
          p.offsetTop === next.offsetTop &&
          p.keyboard === next.keyboard
            ? p
            : next,
        );
      });
    };
    const settle = () => {
      timers.forEach(clearTimeout);
      timers = SETTLE_MS.map((ms) => setTimeout(update, ms));
      update();
    };
    const onFocusOut = () => {
      settle();
      // iOS can leave the page scrolled after its keyboard closes.
      requestAnimationFrame(() => {
        if (!typingFocused() && (window.scrollY || window.scrollX)) window.scrollTo(0, 0);
      });
    };
    update();
    vv?.addEventListener("resize", update);
    vv?.addEventListener("scroll", update);
    window.addEventListener("resize", settle);
    document.addEventListener("focusin", settle);
    document.addEventListener("focusout", onFocusOut);
    return () => {
      cancelAnimationFrame(raf);
      timers.forEach(clearTimeout);
      vv?.removeEventListener("resize", update);
      vv?.removeEventListener("scroll", update);
      window.removeEventListener("resize", settle);
      document.removeEventListener("focusin", settle);
      document.removeEventListener("focusout", onFocusOut);
    };
  }, [enabled]);
  return port;
}
