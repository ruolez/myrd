import { describe, expect, test } from "vitest";
import { contentBox, denormalizePoint, normalizePoint } from "./geometry";

describe("contentBox", () => {
  test("pillarboxes a 16:9 video in a 4:3 element", () => {
    expect(contentBox(800, 600, 1920, 1080)).toEqual({ left: 0, top: 75, width: 800, height: 450 });
  });

  test("letterboxes a 4:3 video in a 16:9 element", () => {
    expect(contentBox(1600, 900, 1024, 768)).toEqual({
      left: 200,
      top: 0,
      width: 1200,
      height: 900,
    });
  });

  test("falls back to the element box before video dimensions are known", () => {
    expect(contentBox(640, 480, 0, 0)).toEqual({ left: 0, top: 0, width: 640, height: 480 });
  });
});

describe("normalizePoint", () => {
  const box = { left: 0, top: 75, width: 800, height: 450 };

  test.each([
    [0, 75, 0, 0],
    [800, 525, 65535, 65535],
    [400, 300, 32768, 32768],
  ])("(%d,%d) → (%d,%d)", (px, py, x, y) => {
    expect(normalizePoint(px, py, box)).toEqual({ x, y });
  });

  test("clamps points in the letterbox bars to the edge", () => {
    expect(normalizePoint(-20, 10, box)).toEqual({ x: 0, y: 0 });
  });

  test("denormalize inverts normalize within a pixel", () => {
    const { x, y } = normalizePoint(123, 456, box);
    const back = denormalizePoint(x, y, box);
    expect([Math.round(back.x), Math.round(back.y)]).toEqual([123, 456]);
  });
});
