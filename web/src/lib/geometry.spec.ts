import { describe, expect, test } from "vitest";
import { denormalizePoint, displayRect, normalizePoint, relativeRect } from "./geometry";

describe("displayRect", () => {
  test("pillarboxes a 16:9 stream in a 4:3 stage", () => {
    expect(displayRect(800, 600, 1920, 1080, 1, "fit")).toEqual({
      left: 0,
      top: 75,
      width: 800,
      height: 450,
    });
  });

  test("letterboxes a 4:3 stream in a 16:9 stage", () => {
    expect(displayRect(1600, 900, 1024, 768, 1, "fit")).toEqual({
      left: 200,
      top: 0,
      width: 1200,
      height: 900,
    });
  });

  test("a window-sized stream on a 2x screen is drawn 1:1 in device pixels", () => {
    expect(displayRect(1440, 812, 2880, 1620, 2, "fit")).toEqual({
      left: 0,
      top: 1,
      width: 1440,
      height: 810,
    });
  });

  test("snaps an enlargement under 2% to 1:1, centered on device pixels", () => {
    // 1600×900 into a 1625×910 stage would scale by 1.011.
    expect(displayRect(1625, 910, 1600, 900, 1, "fit")).toEqual({
      left: 13,
      top: 5,
      width: 1600,
      height: 900,
    });
  });

  test("still enlarges a stream much smaller than the stage", () => {
    expect(displayRect(1920, 1080, 1280, 720, 1, "fit")).toEqual({
      left: 0,
      top: 0,
      width: 1920,
      height: 1080,
    });
  });

  test("never snaps a stream larger than the stage (it would be cropped)", () => {
    expect(displayRect(1590, 900, 1600, 900, 1, "fit").width).toBeCloseTo(1590, 6);
  });

  test("actual size centers a smaller stream", () => {
    expect(displayRect(1000, 800, 1600, 1000, 2, "actual")).toEqual({
      left: 100,
      top: 150,
      width: 800,
      height: 500,
    });
  });

  test("actual size overflows a smaller stage from its top-left", () => {
    expect(displayRect(800, 600, 2560, 1440, 1.25, "actual")).toEqual({
      left: 0,
      top: 0,
      width: 2048,
      height: 1152,
    });
  });

  test("offsets are whole device pixels at fractional scales", () => {
    const r = displayRect(1279.2, 700.8, 1598, 898, 1.25, "fit");
    expect([r.left * 1.25, r.top * 1.25].map((v) => Math.abs(v - Math.round(v)) < 1e-9)).toEqual([
      true,
      true,
    ]);
  });

  test("falls back to the stage before the stream size is known", () => {
    expect(displayRect(640, 480, 0, 0, 2, "fit")).toEqual({
      left: 0,
      top: 0,
      width: 640,
      height: 480,
    });
  });
});

describe("relativeRect", () => {
  test("offsets the inner box by the outer origin", () => {
    expect(
      relativeRect(
        { left: 10, top: 50, width: 900, height: 600 },
        { left: 30, top: 60, width: 800, height: 450 },
      ),
    ).toEqual({ left: 20, top: 10, width: 800, height: 450 });
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
