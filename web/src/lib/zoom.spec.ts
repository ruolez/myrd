import { describe, expect, test } from "vitest";
import type { Rect } from "./geometry";
import { clampView, IDENTITY, MAX_ZOOM, pinchView, revealPoint, viewZoom } from "./zoom";
import type { Pt, View } from "./zoom";

// A phone in portrait: a 16:9 picture letterboxed in a 400×800 stage.
const PORT: Rect = { left: 0, top: 0, width: 400, height: 800 };
const CONTENT: Rect = { left: 0, top: 287.5, width: 400, height: 225 };

/** Where content point p (unzoomed stage coords) lands on screen. */
const onScreen = (v: View, p: Pt): Pt => ({ x: p.x * v.z + v.x, y: p.y * v.z + v.y });

/** Deterministic pseudo-random numbers (mulberry32). */
function rng(seed: number) {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

describe("clampView", () => {
  test("keeps the unzoomed picture where the layout put it", () => {
    expect(clampView(IDENTITY, CONTENT, PORT)).toEqual(IDENTITY);
  });

  test("keeps a picture the layout rounded onto device pixels instead of re-centering it", () => {
    const rounded = { ...CONTENT, top: CONTENT.top + 0.4 };
    expect(clampView(IDENTITY, rounded, PORT)).toEqual(IDENTITY);
  });

  test("centers a picture shorter than the port in the part above the keyboard", () => {
    const port = { ...PORT, height: 450 };
    expect(clampView(IDENTITY, CONTENT, port)).toEqual({ z: 1, x: 0, y: 112.5 - 287.5 });
  });

  test("a zoomed picture cannot be dragged away from the port's edges", () => {
    const z = 2;
    const far = clampView({ z, x: 500, y: 500 }, CONTENT, PORT);
    const near = clampView({ z, x: -5000, y: -5000 }, CONTENT, PORT);
    // Width 800 > 400: left edge at most 0, right edge at least 400.
    // Height 450 < 800: centered whatever the request.
    const centeredY = (800 - 450) / 2 - 287.5 * z;
    expect([far, near]).toEqual([
      { z, x: 0, y: centeredY },
      { z, x: 400 - 800, y: centeredY },
    ]);
  });

  test("the picture always covers the port along an axis where it is larger", () => {
    const r = rng(7);
    for (let i = 0; i < 500; i++) {
      const v = { z: 1 + r() * (MAX_ZOOM - 1), x: (r() - 0.5) * 4000, y: (r() - 0.5) * 4000 };
      const c = clampView(v, CONTENT, PORT);
      const tl = onScreen(c, { x: CONTENT.left, y: CONTENT.top });
      const br = onScreen(c, {
        x: CONTENT.left + CONTENT.width,
        y: CONTENT.top + CONTENT.height,
      });
      if (CONTENT.width * v.z >= PORT.width) {
        expect(tl.x).toBeLessThanOrEqual(PORT.left + 1e-6);
        expect(br.x).toBeGreaterThanOrEqual(PORT.left + PORT.width - 1e-6);
      }
      if (CONTENT.height * v.z >= PORT.height) {
        expect(tl.y).toBeLessThanOrEqual(PORT.top + 1e-6);
        expect(br.y).toBeGreaterThanOrEqual(PORT.top + PORT.height - 1e-6);
      }
    }
  });

  test("is idempotent", () => {
    const r = rng(11);
    for (let i = 0; i < 200; i++) {
      const v = { z: 1 + r() * 4, x: (r() - 0.5) * 3000, y: (r() - 0.5) * 3000 };
      const once = clampView(v, CONTENT, PORT);
      expect(clampView(once, CONTENT, PORT)).toEqual(once);
    }
  });

  test("zoom stays between 1 and MAX_ZOOM", () => {
    expect([
      clampView({ z: 0.2, x: 0, y: 0 }, CONTENT, PORT).z,
      clampView({ z: 40, x: 0, y: 0 }, CONTENT, PORT).z,
    ]).toEqual([1, MAX_ZOOM]);
  });
});

describe("pinchView", () => {
  // A picture much larger than the port, so no clamping interferes.
  const BIG: Rect = { left: -5000, top: -5000, width: 10400, height: 10800 };

  test("the content point under the fingers' midpoint follows the midpoint", () => {
    const r = rng(3);
    for (let i = 0; i < 300; i++) {
      const start = { z: 1 + r() * 2, x: (r() - 0.5) * 200, y: (r() - 0.5) * 200 };
      const pt = () => ({ x: r() * 400, y: r() * 800 });
      const [a0, b0, a, b] = [pt(), pt(), pt(), pt()];
      const d0 = Math.hypot(a0.x - b0.x, a0.y - b0.y);
      const d = Math.hypot(a.x - b.x, a.y - b.y);
      const z = start.z * (d / d0);
      if (d0 < 20 || z < 1 || z > MAX_ZOOM) continue;
      const m0 = { x: (a0.x + b0.x) / 2, y: (a0.y + b0.y) / 2 };
      const m = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
      const under = { x: (m0.x - start.x) / start.z, y: (m0.y - start.y) / start.z };
      const v = pinchView(start, a0, b0, a, b, BIG, PORT);
      const lands = onScreen(v, under);
      expect(v.z).toBeCloseTo(z, 9);
      expect(lands.x).toBeCloseTo(m.x, 6);
      expect(lands.y).toBeCloseTo(m.y, 6);
    }
  });

  test("spreading two fingers to twice their distance doubles the zoom", () => {
    const v = pinchView(
      IDENTITY,
      { x: 150, y: 400 },
      { x: 250, y: 400 },
      { x: 100, y: 400 },
      { x: 300, y: 400 },
      CONTENT,
      PORT,
    );
    expect(v).toEqual({ z: 2, x: -200, y: (800 - 450) / 2 - 287.5 * 2 });
  });

  test("pinching in below 1x stops at the whole picture", () => {
    const v = pinchView(
      { z: 2, x: -200, y: -400 },
      { x: 100, y: 400 },
      { x: 300, y: 400 },
      { x: 190, y: 400 },
      { x: 210, y: 400 },
      CONTENT,
      PORT,
    );
    expect(v).toEqual(IDENTITY);
  });
});

describe("revealPoint", () => {
  const z = 3;
  const v = clampView({ z, x: 0, y: -287.5 * z }, CONTENT, PORT);
  const MARGIN = 40;

  test("leaves the view alone while the point is inside the margin", () => {
    expect(revealPoint(v, { x: 60, y: 300 }, MARGIN, CONTENT, PORT)).toEqual(v);
  });

  test("pans just enough to bring a point near the right edge inside the margin", () => {
    const p = { x: 200, y: 300 };
    const out = revealPoint(v, p, MARGIN, CONTENT, PORT);
    expect(onScreen(out, p).x).toBeCloseTo(PORT.width - MARGIN, 9);
  });

  test("never pans past the picture's edges", () => {
    const out = revealPoint(v, { x: 400, y: 512.5 }, MARGIN, CONTENT, PORT);
    expect(out).toEqual(clampView(out, CONTENT, PORT));
  });
});

describe("viewZoom", () => {
  test.each([
    [1, 1],
    [1.04, 1],
    [1.2, 1.5],
    [1.5, 1.5],
    [1.9, 2],
    [2.5, 3],
    [3.2, 4],
    [4.5, 5],
    [MAX_ZOOM, 5],
  ])("zoom %f asks for a stream sized for %f", (z, step) => {
    expect(viewZoom(z)).toBe(step);
  });
});
