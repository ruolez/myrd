// Local zoom of the remote screen on touch devices: a scale z and a
// translation (x, y) applied to the stage's content (origin top-left), so
// content point p is drawn at p·z + (x, y).

import type { Rect } from "./geometry";

export type View = { z: number; x: number; y: number };
export type Pt = { x: number; y: number };

export const MAX_ZOOM = 5;
export const IDENTITY: View = { z: 1, x: 0, y: 0 };

// The stream is re-sized for the zoom only at these steps, so a small pinch
// does not renegotiate the encoder.
const VIEW_STEPS = [1, 1.5, 2, 3, 4, MAX_ZOOM];
const STEP_SLACK = 0.05;

// A fitting picture this close to centered stays put: the layout rounds it
// onto device pixels, and moving it by a fraction would blur it.
const CENTER_SNAP = 1;

/** The translation along one axis: centered when the content fits, else no gap at either edge. */
function clampAxis(
  t: number,
  z: number,
  start: number,
  size: number,
  portStart: number,
  portSize: number,
) {
  const span = size * z;
  if (span <= portSize) {
    const centered = portStart + (portSize - span) / 2 - start * z;
    return Math.abs(centered - t) < CENTER_SNAP ? t : centered;
  }
  const min = portStart + portSize - (start + size) * z;
  const max = portStart - start * z;
  return Math.min(max, Math.max(min, t));
}

/**
 * v with z kept in 1..MAX_ZOOM and the content (its unzoomed rect) covering
 * the port (the visible part of the stage) wherever it is larger than it,
 * centered in it wherever it is smaller.
 */
export function clampView(v: View, content: Rect, port: Rect): View {
  const z = Math.min(MAX_ZOOM, Math.max(1, v.z));
  return {
    z,
    x: clampAxis(v.x, z, content.left, content.width, port.left, port.width),
    y: clampAxis(v.y, z, content.top, content.height, port.top, port.height),
  };
}

/**
 * Two fingers moved from a0/b0 (when the view was start) to a/b: the zoom
 * follows their distance and the content under their first midpoint stays
 * under their midpoint.
 */
export function pinchView(
  start: View,
  a0: Pt,
  b0: Pt,
  a: Pt,
  b: Pt,
  content: Rect,
  port: Rect,
): View {
  const d0 = Math.hypot(a0.x - b0.x, a0.y - b0.y);
  const d = Math.hypot(a.x - b.x, a.y - b.y);
  const z = Math.min(MAX_ZOOM, Math.max(1, d0 > 0 ? (start.z * d) / d0 : start.z));
  const under = {
    x: ((a0.x + b0.x) / 2 - start.x) / start.z,
    y: ((a0.y + b0.y) / 2 - start.y) / start.z,
  };
  const m = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
  return clampView({ z, x: m.x - under.x * z, y: m.y - under.y * z }, content, port);
}

/** v panned just enough that content point p is at least margin inside the port. */
export function revealPoint(v: View, p: Pt, margin: number, content: Rect, port: Rect): View {
  const axis = (t: number, at: number, start: number, size: number) => {
    const s = at * v.z + t;
    const m = Math.min(margin, size / 2);
    if (s < start + m) return t + (start + m - s);
    if (s > start + size - m) return t - (s - (start + size - m));
    return t;
  };
  return clampView(
    {
      z: v.z,
      x: axis(v.x, p.x, port.left, port.width),
      y: axis(v.y, p.y, port.top, port.height),
    },
    content,
    port,
  );
}

/** The zoom the stream is sized for: the step at or above z (within a little slack). */
export function viewZoom(z: number): number {
  return VIEW_STEPS.find((s) => z <= s + STEP_SLACK) ?? MAX_ZOOM;
}
