// Touch input on the session stage (lib/touch.ts gestures): mouse, scroll and
// the local zoom of the picture. The zoom is a transform on the video's
// wrapper, applied here directly so a pinch never re-renders the pane.

import { useCallback, useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { denormalizePoint, normalizePoint } from "../lib/geometry";
import type { Rect } from "../lib/geometry";
import { Button, encodeMouseButton, encodeMouseWheel } from "../lib/input";
import { holdDeadline, initialTouch, touchStep } from "../lib/touch";
import type { TouchAction, TouchInput, TouchMode, TouchState } from "../lib/touch";
import { clampView, IDENTITY, pinchView, revealPoint } from "../lib/zoom";
import type { View } from "../lib/zoom";
import type { RemoteSession } from "./RemoteSession";
import { videoBox } from "./videoBox";

/** A phone or tablet: no hover, a finger as the main pointer. */
export const touchFirst = () => window.matchMedia("(hover: none) and (pointer: coarse)").matches;

/** Fired on the surface when the zoom changes, so the cursor layer redraws. */
export const VIEW_EVENT = "rc-view";

// Wheel pixels per remote pixel of finger travel: a 100 px notch is about
// three lines, so the content roughly follows the finger.
const WHEEL_PER_PX = 1.5;
// Coasting after a fling: speed decays with this time constant (ms).
const FLING_TAU_MS = 325;
const FLING_STOP = 0.03;
// Trackpad: slow strokes move the pointer 1:1 for precision, fast ones up to
// this much further.
const MAX_GAIN = 2.5;
// Trackpad on a zoomed view: keep the pointer this far inside the edges.
const REVEAL_MARGIN = 48;
const SETTLE_MS = 80;
// Trackpad: cursor reports this soon after our own moves are echoes of them,
// already stale; later ones mean the pointer moved on the device.
const ECHO_MS = 500;

type Refs = {
  session: RefObject<RemoteSession | null>;
  surface: RefObject<HTMLDivElement | null>;
  video: RefObject<HTMLVideoElement | null>;
  /** The wrapper the zoom transform goes on. */
  zoom: RefObject<HTMLDivElement | null>;
  /** The stage's scroll container ("actual" size scrolls instead of zooming). */
  scroller: RefObject<HTMLDivElement | null>;
};

export type TouchLayout = {
  /** Where the picture is laid out in the stage, unzoomed. */
  content: Rect;
  stage: { w: number; h: number };
  /** Remote monitor width in pixels (for scroll distances). */
  nativeWidth: number;
  zoomable: boolean;
};

/** The part of the stage not covered by the on-screen keyboard (stage coordinates). */
function visiblePort(el: HTMLElement, stage: { w: number; h: number }): Rect {
  const vv = window.visualViewport;
  if (!vv) return { left: 0, top: 0, width: stage.w, height: stage.h };
  const top = el.getBoundingClientRect().top;
  const bottom = vv.offsetTop + vv.height - top;
  return { left: 0, top: 0, width: stage.w, height: Math.max(1, Math.min(stage.h, bottom)) };
}

export function useTouchInput(
  { session, surface, video, zoom, scroller }: Refs,
  enabled: boolean,
  monitor: number,
  mode: TouchMode,
  layout: TouchLayout,
) {
  const [zoomLevel, setZoomLevel] = useState(1);
  const view = useRef<View>(IDENTITY);
  const layoutRef = useRef(layout);
  layoutRef.current = layout;
  const monitorRef = useRef(monitor);
  monitorRef.current = monitor;

  const applyView = useCallback(
    (v: View, settled: boolean) => {
      view.current = v;
      const w = zoom.current;
      if (w)
        w.style.transform =
          v.z === 1 && !v.x && !v.y ? "" : `translate(${v.x}px, ${v.y}px) scale(${v.z})`;
      surface.current?.dispatchEvent(new Event(VIEW_EVENT));
      if (settled) setZoomLevel(v.z);
    },
    [zoom, surface],
  );

  /** v kept inside the current layout and the part of the stage above the keyboard. */
  const fitted = useCallback(
    (v: View): View => {
      const el = scroller.current;
      const { content, stage, zoomable } = layoutRef.current;
      return el && zoomable && stage.w > 0
        ? clampView(v, content, visiblePort(el, stage))
        : IDENTITY;
    },
    [scroller],
  );

  const reclamp = useCallback(() => {
    const v = fitted(view.current);
    const cur = view.current;
    if (v.z !== cur.z || v.x !== cur.x || v.y !== cur.y) applyView(v, true);
  }, [fitted, applyView]);

  const resetZoom = useCallback(() => {
    const w = zoom.current;
    if (w) {
      w.style.transition = "transform 160ms ease-out";
      setTimeout(() => (w.style.transition = ""), 200);
    }
    applyView(fitted(IDENTITY), true);
  }, [zoom, fitted, applyView]);

  // A new layout (rotation, stream size, keyboard) keeps the zoom inside it.
  const { content, stage, zoomable } = layout;
  useEffect(reclamp, [
    reclamp,
    content.left,
    content.top,
    content.width,
    content.height,
    stage.w,
    stage.h,
    zoomable,
  ]);
  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;
    vv.addEventListener("resize", reclamp);
    vv.addEventListener("scroll", reclamp);
    return () => {
      vv.removeEventListener("resize", reclamp);
      vv.removeEventListener("scroll", reclamp);
    };
  }, [reclamp]);

  useEffect(() => {
    const el = surface.current;
    const sc = scroller.current;
    const s0 = session.current;
    if (!enabled || !el || !sc || !s0) return;
    const send = (b: Uint8Array) => session.current?.sendInput(b);

    let state: TouchState = initialTouch;
    let holdTimer: ReturnType<typeof setTimeout> | null = null;
    let settle: ReturnType<typeof setTimeout> | null = null;
    let pinchBase: { view: View; left: number; top: number } | null = null;
    // Trackpad pointer, 0..65535 of the monitor; follows the device between strokes.
    let cursor = { x: 32768, y: 32768 };
    let movedAt = -Infinity;
    let wheel = { x: 0, y: 0 };
    let wheelRaf = 0;
    let fling: { vx: number; vy: number; at: number } | null = null;
    let flingRaf = 0;

    const box = () => {
      const r = el.getBoundingClientRect();
      return { r, box: videoBox(r, video.current) };
    };

    const moveTo = (x: number, y: number, reliable: boolean) => {
      cursor = { x, y };
      movedAt = performance.now();
      if (settle) clearTimeout(settle);
      settle = reliable
        ? null
        : setTimeout(() => {
            settle = null;
            session.current?.sendMove(x, y, monitorRef.current, true);
          }, SETTLE_MS);
      session.current?.sendMove(x, y, monitorRef.current, reliable);
    };

    const offCursor = s0.onCursor((m) => {
      if (m.type === "pos" && performance.now() - movedAt > ECHO_MS) cursor = { x: m.x, y: m.y };
    });

    const flushWheel = () => {
      wheelRaf = 0;
      const dx = Math.trunc(wheel.x);
      const dy = Math.trunc(wheel.y);
      if (!dx && !dy) return;
      wheel = { x: wheel.x - dx, y: wheel.y - dy };
      send(encodeMouseWheel(dx, dy));
    };
    // Fingers moved (dx, dy) on screen; the content follows, so the wheel goes the other way.
    const scrollBy = (dx: number, dy: number) => {
      const { box: b } = box();
      const native = layoutRef.current.nativeWidth || video.current?.videoWidth || b.width;
      const k = b.width > 0 ? (native / b.width) * WHEEL_PER_PX : WHEEL_PER_PX;
      wheel = { x: wheel.x - dx * k, y: wheel.y - dy * k };
      if (!wheelRaf) wheelRaf = requestAnimationFrame(flushWheel);
    };

    const stopFling = () => {
      fling = null;
      if (flingRaf) cancelAnimationFrame(flingRaf);
      flingRaf = 0;
    };
    const coast = (now: number) => {
      flingRaf = 0;
      if (!fling) return;
      const dt = Math.min(50, now - fling.at);
      scrollBy(fling.vx * dt, fling.vy * dt);
      const decay = Math.exp(-dt / FLING_TAU_MS);
      fling = { vx: fling.vx * decay, vy: fling.vy * decay, at: now };
      if (Math.hypot(fling.vx, fling.vy) < FLING_STOP) fling = null;
      else flingRaf = requestAnimationFrame(coast);
    };

    const nudge = (dx: number, dy: number, dt: number) => {
      const { box: b } = box();
      if (b.width <= 0 || b.height <= 0) return;
      const speed = Math.hypot(dx, dy) / Math.max(1, dt);
      const gain = Math.min(MAX_GAIN, 1 + Math.max(0, speed - 0.3) * 1.2);
      const clamp = (v: number) => Math.max(0, Math.min(65535, v));
      moveTo(
        clamp(cursor.x + ((dx * gain) / b.width) * 65535),
        clamp(cursor.y + ((dy * gain) / b.height) * 65535),
        false,
      );
      const { content: c, stage: st, zoomable: z } = layoutRef.current;
      if (z && view.current.z > 1) {
        const p = denormalizePoint(cursor.x, cursor.y, c);
        applyView(revealPoint(view.current, p, REVEAL_MARGIN, c, visiblePort(sc, st)), false);
      }
    };

    const onView = (a: Extract<TouchAction, { t: "view" }>) => {
      const { content: c, stage: st, zoomable: z } = layoutRef.current;
      pinchBase ??= { view: view.current, left: sc.scrollLeft, top: sc.scrollTop };
      if (!z) {
        // Native size: two fingers scroll the stage instead.
        const m0 = { x: (a.a0.x + a.b0.x) / 2, y: (a.a0.y + a.b0.y) / 2 };
        const m = { x: (a.a.x + a.b.x) / 2, y: (a.a.y + a.b.y) / 2 };
        sc.scrollTo(pinchBase.left - (m.x - m0.x), pinchBase.top - (m.y - m0.y));
        return;
      }
      applyView(pinchView(pinchBase.view, a.a0, a.b0, a.a, a.b, c, visiblePort(sc, st)), false);
    };

    const perform = (out: TouchAction[]) => {
      const clicks = out.some((a) => a.t === "button");
      for (const a of out) {
        switch (a.t) {
          case "point": {
            const { box: b } = box();
            const p = normalizePoint(a.p.x, a.p.y, b);
            moveTo(p.x, p.y, clicks);
            break;
          }
          case "nudge":
            nudge(a.dx, a.dy, a.dt);
            break;
          case "button":
            // Trackpad clicks land where the pointer is, not where a lost move left it.
            if (a.down && mode === "trackpad") moveTo(cursor.x, cursor.y, true);
            send(encodeMouseButton(a.button === "left" ? Button.Left : Button.Right, a.down));
            break;
          case "scroll":
            scrollBy(a.dx, a.dy);
            break;
          case "fling":
            fling = { vx: a.vx, vy: a.vy, at: performance.now() };
            if (!flingRaf) flingRaf = requestAnimationFrame(coast);
            break;
          case "view":
            onView(a);
            break;
          case "viewEnd":
            pinchBase = null;
            applyView(view.current, true);
            break;
          case "hold":
            navigator.vibrate?.(10);
            break;
        }
      }
    };

    const step = (e: TouchInput) => {
      const r = touchStep(state, e, mode);
      state = r.s;
      perform(r.out);
      if (holdTimer) clearTimeout(holdTimer);
      holdTimer = null;
      const due = holdDeadline(state);
      if (due !== null)
        holdTimer = setTimeout(
          () => step({ t: "hold", at: performance.now() }),
          Math.max(0, due - performance.now()),
        );
    };

    const point = (e: PointerEvent) => {
      const r = el.getBoundingClientRect();
      return { x: e.clientX - r.left, y: e.clientY - r.top };
    };
    const onDown = (e: PointerEvent) => {
      if (e.pointerType !== "touch") return;
      e.preventDefault();
      stopFling();
      step({ t: "down", id: e.pointerId, p: point(e), at: performance.now() });
    };
    const onMove = (e: PointerEvent) => {
      if (e.pointerType !== "touch") return;
      e.preventDefault();
      step({ t: "move", id: e.pointerId, p: point(e), at: performance.now() });
    };
    const onUp = (e: PointerEvent) => {
      if (e.pointerType !== "touch") return;
      e.preventDefault();
      const at = performance.now();
      step(
        e.type === "pointercancel"
          ? { t: "cancel", id: e.pointerId, at }
          : { t: "up", id: e.pointerId, p: point(e), at },
      );
    };

    el.addEventListener("pointerdown", onDown);
    el.addEventListener("pointermove", onMove);
    el.addEventListener("pointerup", onUp);
    el.addEventListener("pointercancel", onUp);
    return () => {
      // A gesture cut short (mode switch, session ends) must not leave a button down.
      if (state.k === "drag") send(encodeMouseButton(Button.Left, false));
      if (holdTimer) clearTimeout(holdTimer);
      if (settle) clearTimeout(settle);
      if (wheelRaf) cancelAnimationFrame(wheelRaf);
      stopFling();
      offCursor();
      el.removeEventListener("pointerdown", onDown);
      el.removeEventListener("pointermove", onMove);
      el.removeEventListener("pointerup", onUp);
      el.removeEventListener("pointercancel", onUp);
    };
  }, [enabled, mode, session, surface, video, scroller, applyView]);

  return { zoom: zoomLevel, resetZoom };
}
