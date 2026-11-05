import fc from "fast-check";
import { describe, expect, test } from "vitest";
import { canScrollChain, KEYBOARD_MIN_PX, keyboardOpen, viewportContent } from "./viewport";
import type { ScrollBox } from "./viewport";

describe("keyboardOpen", () => {
  const WINDOW = 844; // iPhone 13 portrait
  test.each([
    ["nothing covers the window", { height: WINDOW, scale: 1 }, false],
    ["a phone keyboard (~336 px)", { height: WINDOW - 336, scale: 1 }, true],
    ["exactly the threshold", { height: WINDOW - KEYBOARD_MIN_PX, scale: 1 }, true],
    ["a collapsing address bar (~60 px)", { height: WINDOW - 60, scale: 1 }, false],
    ["pinch-zoomed 2x without a keyboard", { height: WINDOW / 2, scale: 2 }, false],
    ["pinch-zoomed 2x with a keyboard", { height: (WINDOW - 336) / 2, scale: 2 }, true],
    ["a scale below 1 is read as 1", { height: WINDOW, scale: 0.5 }, false],
  ])("%s", (_name, visible, open) => {
    expect(keyboardOpen(visible, WINDOW)).toBe(open);
  });

  test("never open while the whole window shows, whatever the zoom", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 200, max: 3000 }),
        fc.double({ min: 1, max: 5, noNaN: true }),
        (h, scale) => !keyboardOpen({ height: h / scale, scale }, h),
      ),
    );
  });
});

describe("viewportContent", () => {
  const BASE = "width=device-width, initial-scale=1.0, viewport-fit=cover";
  test.each([
    ["adds the widget", BASE, "resizes-visual", `${BASE}, interactive-widget=resizes-visual`],
    [
      "replaces an existing widget",
      `${BASE}, interactive-widget=resizes-visual`,
      "resizes-content",
      `${BASE}, interactive-widget=resizes-content`,
    ],
    ["removes it with null", `${BASE},interactive-widget=resizes-visual`, null, BASE],
    [
      "keeps other keys in place",
      "width=device-width,interactive-widget=overlays-content,viewport-fit=cover",
      "resizes-visual",
      "width=device-width, viewport-fit=cover, interactive-widget=resizes-visual",
    ],
  ] as const)("%s", (_name, content, widget, want) => {
    expect(viewportContent(content, widget)).toBe(want);
  });

  test("setting the same widget twice is the same as once", () => {
    const once = viewportContent(BASE, "resizes-visual");
    expect(viewportContent(once, "resizes-visual")).toBe(once);
  });
});

describe("canScrollChain", () => {
  const box = (o: Partial<ScrollBox> = {}): ScrollBox => ({
    overflowX: "visible",
    overflowY: "visible",
    scrollWidth: 100,
    clientWidth: 100,
    scrollHeight: 100,
    clientHeight: 100,
    ...o,
  });
  const list = box({ overflowY: "auto", scrollHeight: 900, clientHeight: 300 });
  const keyStrip = box({ overflowX: "auto", scrollWidth: 800, clientWidth: 300 });

  test.each([
    ["no scroll container", [box(), box()], "y", false],
    ["a scrolling list higher up", [box(), list], "y", true],
    ["a list that fits its box", [box({ overflowY: "auto" })], "y", false],
    [
      "overflow hidden never scrolls by touch",
      [box({ overflowY: "hidden", scrollHeight: 900 })],
      "y",
      false,
    ],
    ["a sideways strip, dragged sideways", [keyStrip], "x", true],
    ["a sideways strip, dragged up", [keyStrip], "y", false],
    ["a 1 px rounding overflow", [box({ overflowY: "auto", scrollHeight: 101 })], "y", false],
  ] as const)("%s", (_name, chain, axis, want) => {
    expect(canScrollChain(chain, axis)).toBe(want);
  });
});
