import { useEffect, useState } from "react";

export type VisualPort = {
  height: number;
  /** How far iOS scrolled the page to keep the focused field visible. */
  offsetTop: number;
  /** The on-screen keyboard covers part of the window. */
  keyboard: boolean;
};

const read = (): VisualPort => {
  const vv = window.visualViewport;
  const h = vv?.height ?? window.innerHeight;
  return { height: h, offsetTop: vv?.offsetTop ?? 0, keyboard: h < window.innerHeight * 0.8 };
};

/**
 * The part of the window the user sees. With interactive-widget=resizes-visual
 * an on-screen keyboard shrinks only this, not 100dvh, so layouts that must
 * sit right on the keyboard follow it.
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
    const update = () => {
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(() => setPort(read()));
    };
    update();
    vv?.addEventListener("resize", update);
    vv?.addEventListener("scroll", update);
    window.addEventListener("resize", update);
    return () => {
      cancelAnimationFrame(raf);
      vv?.removeEventListener("resize", update);
      vv?.removeEventListener("scroll", update);
      window.removeEventListener("resize", update);
    };
  }, [enabled]);
  return port;
}
