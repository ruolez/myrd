import { describe, expect, test } from "vitest";
import {
  cellAt,
  clickBytes,
  coast,
  defaultFontSize,
  MAX_FONT,
  MIN_FONT,
  pinchFont,
  scrollBytes,
  scrollLines,
  wordAt,
} from "./termGestures";
import type { MouseModes } from "./termGestures";

const plain: MouseModes = { tracking: "none", alt: false, appCursor: false };

describe("defaultFontSize", () => {
  test.each([
    [320, 12],
    [375, 13],
    [414, 13],
    [768, 14],
  ])("%i px → %i", (w, want) => {
    expect(defaultFontSize(w)).toBe(want);
  });
});

describe("pinchFont", () => {
  const a0 = { x: 100, y: 100 };
  const b0 = { x: 200, y: 100 };
  test("spreading the fingers by half grows the font by half, in half steps", () => {
    expect(pinchFont(12, a0, b0, { x: 75, y: 100 }, { x: 225, y: 100 })).toBe(18);
  });
  test("clamped to the readable range", () => {
    expect([
      pinchFont(12, a0, b0, { x: 0, y: 100 }, { x: 900, y: 100 }),
      pinchFont(12, a0, b0, { x: 140, y: 100 }, { x: 150, y: 100 }),
    ]).toEqual([MAX_FONT, MIN_FONT]);
  });
});

describe("scrollLines", () => {
  test("small moves accumulate into whole lines", () => {
    let carry = 0;
    const lines: number[] = [];
    for (const dy of [6, 6, 6, 6]) {
      const r = scrollLines(carry, dy, 16);
      carry = r.carry;
      lines.push(r.lines);
    }
    expect(lines).toEqual([0, 0, -1, 0]);
  });
  test("the carry never reaches a whole line", () => {
    for (const dy of [-50, -3, 0, 7, 33.3])
      expect(Math.abs(scrollLines(0.9, dy, 16).carry)).toBeLessThan(1);
  });
});

describe("cellAt", () => {
  const screen = { left: 10, top: 20, width: 800, height: 480 };
  test.each([
    [
      { x: 10, y: 20 },
      { col: 1, row: 1 },
    ],
    [
      { x: 809, y: 499 },
      { col: 80, row: 24 },
    ],
    [
      { x: 410, y: 260 },
      { col: 41, row: 13 },
    ],
    [
      { x: -50, y: 9999 },
      { col: 1, row: 24 },
    ],
  ])("%o → %o", (p, want) => {
    expect(cellAt(p, screen, 80, 24)).toEqual(want);
  });
});

describe("scrollBytes", () => {
  const at = { col: 3, row: 7 };
  test("at a prompt the local scrollback moves", () => {
    expect(scrollBytes(-2, at, plain)).toBeNull();
  });
  test("mouse-tracking programs get SGR wheel events at the finger", () => {
    expect(scrollBytes(-2, at, { ...plain, tracking: "vt200", alt: true })).toBe(
      "\x1b[<64;3;7M\x1b[<64;3;7M",
    );
    expect(scrollBytes(1, at, { ...plain, tracking: "any" })).toBe("\x1b[<65;3;7M");
  });
  test("other full-screen programs get arrow keys, in their cursor mode", () => {
    expect(scrollBytes(2, at, { ...plain, alt: true })).toBe("\x1b[B\x1b[B");
    expect(scrollBytes(-1, at, { ...plain, alt: true, appCursor: true })).toBe("\x1bOA");
  });
  test("a fast fling sends at most 20 at once", () => {
    expect(scrollBytes(-500, at, { ...plain, alt: true })).toBe("\x1b[A".repeat(20));
  });
});

describe("clickBytes", () => {
  test("press and release where the finger tapped", () => {
    expect(clickBytes({ col: 12, row: 4 }, { ...plain, tracking: "x10" })).toBe(
      "\x1b[<0;12;4M\x1b[<0;12;4m",
    );
  });
  test("nothing for programs that don't track the mouse", () => {
    expect(clickBytes({ col: 1, row: 1 }, plain)).toBeNull();
  });
});

describe("wordAt", () => {
  const line = "cd ~/src/remote-control && make test";
  test.each([
    [5, [3, 23]],
    [0, [0, 2]],
    [2, [2, 3]],
    [33, [32, 36]],
    [99, [99, 100]],
  ] as const)("column %i → %o", (col, want) => {
    expect(wordAt(line, col)).toEqual(want);
  });
});

describe("coast", () => {
  test("the speed decays and the distance approaches v·τ", () => {
    const r = coast(1, 1e9);
    expect([Math.round(r.d), r.v]).toEqual([325, 0]);
  });
  test("two half steps travel as far as one whole step", () => {
    const a = coast(2, 50);
    const b = coast(a.v, 50);
    expect(a.d + b.d).toBeCloseTo(coast(2, 100).d, 9);
  });
});
