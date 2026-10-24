import { describe, expect, test } from "vitest";
import {
  bestFitMode,
  groupModes,
  modeLabel,
  parseDisplay,
  parseDisplayPref,
  rememberedMode,
  serializeDisplayPref,
} from "./display";
import type { DisplayInfo, DisplayMode } from "./display";

const m = (width: number, height: number, scale = 1): DisplayMode => ({ width, height, scale });
const MODES = [
  m(2560, 1440),
  m(1920, 1200),
  m(1920, 1080),
  m(1600, 900),
  m(1280, 1024),
  m(1024, 768),
];
const info = (current: DisplayMode, original: DisplayMode | null = null): DisplayInfo => ({
  modes: MODES,
  current,
  original,
});

describe("parseDisplay", () => {
  test("reads modes, current and original", () => {
    const raw = {
      modes: [
        { width: 1920, height: 1080, scale: 1 },
        { width: 1280, height: 720, scale: 2 },
      ],
      current: { width: 1280, height: 720, scale: 2 },
      original: { width: 1920, height: 1080, scale: 1 },
    };
    expect(parseDisplay(raw)).toEqual({
      modes: [m(1920, 1080), m(1280, 720, 2)],
      current: m(1280, 720, 2),
      original: m(1920, 1080),
    });
  });

  test("drops malformed modes and rejects a display without a current mode", () => {
    const modes = [
      { width: 1920, height: 1080, scale: 1 },
      { width: "x" },
      { width: 0, height: 5 },
    ];
    expect(parseDisplay({ modes, current: { width: 1920, height: 1080 }, original: null })).toEqual(
      { modes: [m(1920, 1080)], current: m(1920, 1080), original: null },
    );
    expect(parseDisplay({ modes, current: null })).toBeNull();
    expect(parseDisplay(undefined)).toBeNull();
  });
});

describe("bestFitMode", () => {
  test.each([
    { view: { w: 2560, h: 1440 }, want: m(2560, 1440) },
    { view: { w: 1990, h: 1100 }, want: m(1920, 1080) },
    { view: { w: 1990, h: 965 }, want: m(1600, 900) },
    { view: { w: 1300, h: 1100 }, want: m(1280, 1024) },
  ])("the largest mode inside $view.w×$view.h", ({ view, want }) => {
    expect(bestFitMode(MODES, view)).toEqual(want);
  });

  test("the smallest mode when none fits", () => {
    expect(bestFitMode(MODES, { w: 800, h: 500 })).toEqual(m(1024, 768));
  });

  test("a HiDPI mode fits by its pixels", () => {
    const mac = [m(1920, 1080, 2), m(1440, 900, 2), m(1280, 720, 2)];
    expect(bestFitMode(mac, { w: 3000, h: 1900 })).toEqual(m(1440, 900, 2));
  });

  test("null without modes", () => {
    expect(bestFitMode([], { w: 1920, h: 1080 })).toBeNull();
  });
});

describe("groupModes", () => {
  test("modes in the current shape first, the rest after, order kept", () => {
    expect(groupModes(MODES, m(1920, 1080))).toEqual({
      same: [m(2560, 1440), m(1920, 1080), m(1600, 900)],
      other: [m(1920, 1200), m(1280, 1024), m(1024, 768)],
    });
  });
});

describe("display preference", () => {
  test.each([{ kind: "fit" as const }, { kind: "mode" as const, width: 1600, height: 900 }])(
    "round-trips %o",
    (choice) => {
      expect(parseDisplayPref(serializeDisplayPref(choice))).toEqual(choice);
    },
  );

  test.each([null, "", "{", '{"v":2,"kind":"fit"}', '{"v":1,"kind":"mode","width":-1,"height":5}'])(
    "anything else reads as no choice: %s",
    (raw) => {
      expect(parseDisplayPref(raw)).toBeNull();
    },
  );
});

describe("rememberedMode", () => {
  const view = { w: 1990, h: 965 };

  test("a remembered mode the device offers, unless it already shows it", () => {
    expect(
      rememberedMode({ kind: "mode", width: 1280, height: 1024 }, info(m(1920, 1080)), view),
    ).toEqual(m(1280, 1024));
    expect(
      rememberedMode({ kind: "mode", width: 1920, height: 1080 }, info(m(1920, 1080)), view),
    ).toBeNull();
    expect(
      rememberedMode({ kind: "mode", width: 3840, height: 2160 }, info(m(1920, 1080)), view),
    ).toBeNull();
  });

  test("fit picks the best mode for the view", () => {
    expect(rememberedMode({ kind: "fit" }, info(m(1920, 1080)), view)).toEqual(m(1600, 900));
    expect(rememberedMode({ kind: "fit" }, info(m(1600, 900)), view)).toBeNull();
  });

  test("no choice changes nothing", () => {
    expect(rememberedMode(null, info(m(1920, 1080)), view)).toBeNull();
  });
});

describe("modeLabel", () => {
  test.each([
    { mode: m(1920, 1080), want: "1920 × 1080" },
    { mode: m(1440, 900, 2), want: "1440 × 900 (HiDPI)" },
  ])("$want", ({ mode, want }) => {
    expect(modeLabel(mode)).toBe(want);
  });
});
