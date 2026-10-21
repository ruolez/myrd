import { describe, expect, test } from "vitest";
import { vectors } from "../test/vectors";
import {
  DEFAULT_STREAM_PREFS,
  describeAuto,
  editStreamField,
  encodeStreamRequest,
  fpsChoices,
  isStreamMenuHotkey,
  legacyTier,
  parseStreamPrefs,
  parseStreamState,
  relayCapped,
  resolutionLabel,
  selectPreset,
  serializeStreamPrefs,
  streamLabel,
  streamPrefsKey,
  streamView,
} from "./stream";
import type { StreamPrefs, StreamRequestId, StreamState } from "./stream";

const wireState = {
  t: "stream",
  id: 7,
  ok: true,
  preset: "auto",
  fps: null,
  res: null,
  max_kbps: null,
  background: false,
  wallpaper: false,
  resolved: { fps: 60, res: "native", max_kbps: null },
  target_fps: 30,
  max_fps: 60,
  width: 2560,
  height: 1440,
  native_width: 5120,
  native_height: 2880,
  ceil_kbps: 8000,
  relay: false,
  auto: { rung: 2, rungs: 6, reason: "congestion" },
  options: {
    fps: [5, 10, 15, 24, 30, 60],
    res: [
      { id: "native", width: 5120, height: 2880 },
      { id: "1440p", width: 2560, height: 1440 },
      { id: "1080p", width: 1920, height: 1080 },
      { id: "720p", width: 1280, height: 720 },
      { id: "half", width: 2560, height: 1440 },
    ],
  },
};

const parsedState: StreamState = {
  id: 7,
  ok: true,
  preset: "auto",
  fps: null,
  res: null,
  max_kbps: null,
  background: false,
  wallpaper: false,
  resolved: { fps: 60, res: "native", max_kbps: null },
  target_fps: 30,
  max_fps: 60,
  width: 2560,
  height: 1440,
  native_width: 5120,
  native_height: 2880,
  ceil_kbps: 8000,
  relay: false,
  auto: { rung: 2, rungs: 6, reason: "congestion" },
  options: wireState.options as StreamState["options"],
};

const state = (patch: Partial<StreamState> = {}): StreamState => ({ ...parsedState, ...patch });
const prefs = (patch: Partial<StreamPrefs> = {}): StreamPrefs => ({
  ...DEFAULT_STREAM_PREFS,
  ...patch,
});

describe("encodeStreamRequest", () => {
  test.each(vectors.stream_request)("$name matches the shared vector", (v) => {
    const p = { ...DEFAULT_STREAM_PREFS, ...v.prefs } as StreamPrefs;
    expect(encodeStreamRequest(p, v.hidden, v.id as StreamRequestId, v.view ?? null)).toEqual(
      v.json,
    );
  });

  test("the scale preference stays in the browser", () => {
    const json = encodeStreamRequest(prefs({ scale: "actual" }), false, 1 as StreamRequestId, null);
    expect(JSON.parse(json)).toEqual({
      t: "stream",
      id: 1,
      preset: "auto",
      fps: null,
      res: null,
      max_kbps: null,
      background: false,
      wallpaper: false,
    });
  });
});

describe("streamView", () => {
  test.each([
    ["1x", 1600, 900, 1, { w: 1600, h: 900 }],
    ["retina", 1440, 812.5, 2, { w: 2880, h: 1624 }],
    ["fractional scale floors to even device pixels", 1279.5, 719.2, 1.25, { w: 1598, h: 898 }],
    ["odd device pixels", 801, 601, 1, { w: 800, h: 600 }],
    ["collapsed stage", 0, 600, 2, null],
  ])("%s", (_name, w, h, dpr, want) => {
    expect(streamView(w, h, dpr)).toEqual(want);
  });
});

describe("legacyTier", () => {
  test.each(vectors.legacy_tier)("$name", (v) => {
    expect(legacyTier(v.prefs as StreamPrefs, v.hidden)).toEqual(v.tier);
  });

  test("never exceeds tier 2 (agents reject 0x20 op 3 arg 3)", () => {
    const invalid = vectors.input_invalid.find((v) => v.name === "quality tier 3");
    const all = (["auto", "speed", "balanced", "quality", "custom"] as const).flatMap((preset) =>
      [null, 1, 5, 30, 31, 60].flatMap((fps) =>
        [false, true].map((hidden) => legacyTier(prefs({ preset, fps }), hidden)),
      ),
    );
    expect({ max: Math.max(...all), invalidHex: invalid?.hex }).toEqual({
      max: 2,
      invalidHex: "200303",
    });
  });
});

describe("parseStreamState", () => {
  test("parses the PROTOCOL example", () => {
    expect(parseStreamState(wireState)).toEqual(parsedState);
  });

  test("reports the wallpaper the agent shows", () => {
    expect(parseStreamState({ ...wireState, wallpaper: true })?.wallpaper).toEqual(true);
  });

  test("defaults optional fields an agent may omit", () => {
    const minimal = {
      t: "stream",
      preset: "speed",
      resolved: { fps: 30, res: "720p", max_kbps: 4000 },
      target_fps: 30,
      max_fps: 30,
      width: 1280,
      height: 720,
      native_width: 1920,
      native_height: 1080,
    };
    expect(parseStreamState(minimal)).toEqual({
      id: null,
      ok: true,
      preset: "speed",
      fps: null,
      res: null,
      max_kbps: null,
      background: false,
      wallpaper: false,
      resolved: { fps: 30, res: "720p", max_kbps: 4000 },
      target_fps: 30,
      max_fps: 30,
      width: 1280,
      height: 720,
      native_width: 1920,
      native_height: 1080,
      ceil_kbps: null,
      relay: false,
      auto: null,
      options: { fps: [], res: [] },
    });
  });

  test("reads the window size and the latency reason", () => {
    const sized = {
      ...wireState,
      resolved: { fps: 60, res: "window", max_kbps: null },
      auto: { rung: 1, rungs: 6, reason: "latency" },
      options: {
        fps: [30, 60],
        res: [{ id: "window", width: 2844, height: 1600 }, ...wireState.options.res],
      },
    };
    const got = parseStreamState(sized);
    expect([got?.resolved.res, got?.auto?.reason, got?.options.res[0]]).toEqual([
      "window",
      "latency",
      { id: "window", width: 2844, height: 1600 },
    ]);
  });

  test("keeps ok:false replies", () => {
    expect(parseStreamState({ ...wireState, ok: false })?.ok).toEqual(false);
  });

  test("drops option entries with unknown resolution ids", () => {
    const withJunk = {
      ...wireState,
      options: {
        fps: [5, "x", 60],
        res: [{ id: "4k", width: 1, height: 1 }, ...wireState.options.res],
      },
    };
    expect(parseStreamState(withJunk)?.options).toEqual({
      fps: [5, 60],
      res: wireState.options.res,
    });
  });

  test.each([
    ["not an object", "stream"],
    ["unknown preset", { ...wireState, preset: "turbo" }],
    ["missing resolved", { ...wireState, resolved: undefined }],
    ["resolved res unknown", { ...wireState, resolved: { fps: 60, res: "4k", max_kbps: null } }],
    ["width not a number", { ...wireState, width: "2560" }],
  ])("rejects %s", (_name, msg) => {
    expect(parseStreamState(msg)).toBeNull();
  });
});

describe("selectPreset", () => {
  test("a named preset clears every override", () => {
    expect(
      selectPreset(
        prefs({ preset: "custom", fps: 15, res: "720p", max_kbps: 3000 }),
        "quality",
        null,
      ),
    ).toEqual(prefs({ preset: "quality" }));
  });

  test("auto clears ceilings when picked again", () => {
    expect(selectPreset(prefs({ fps: 30, res: "1080p" }), "auto", null)).toEqual(
      DEFAULT_STREAM_PREFS,
    );
  });

  test("custom starts from what the agent resolved", () => {
    const s = state({ preset: "balanced", resolved: { fps: 30, res: "1080p", max_kbps: 8000 } });
    expect(selectPreset(prefs({ preset: "balanced" }), "custom", s)).toEqual({
      preset: "custom",
      fps: 30,
      res: "1080p",
      max_kbps: 8000,
      wallpaper: false,
      scale: "fit",
    });
  });

  test.each(["auto", "speed", "custom"] as const)(
    "switching to %s keeps the wallpaper and scale choices",
    (preset) => {
      const shown = prefs({ preset: "quality", wallpaper: true, scale: "actual" });
      const next = selectPreset(shown, preset, state());
      expect([next.wallpaper, next.scale]).toEqual([true, "actual"]);
    },
  );

  test("custom without agent state keeps the current overrides", () => {
    expect(selectPreset(prefs({ preset: "speed", fps: 10 }), "custom", null)).toEqual(
      prefs({ preset: "custom", fps: 10 }),
    );
  });
});

describe("editStreamField", () => {
  test("in auto a field becomes a ceiling and the preset stays", () => {
    expect(editStreamField(prefs(), state(), "fps", 30)).toEqual(prefs({ fps: 30 }));
  });

  test("a named preset switches to custom filled from resolved", () => {
    const s = state({ preset: "speed", resolved: { fps: 60, res: "720p", max_kbps: 4000 } });
    expect(editStreamField(prefs({ preset: "speed" }), s, "res", "1080p")).toEqual({
      preset: "custom",
      fps: 60,
      res: "1080p",
      max_kbps: 4000,
      wallpaper: false,
      scale: "fit",
    });
  });

  test("custom keeps its other values", () => {
    const p = prefs({ preset: "custom", fps: 15, res: "720p", max_kbps: 5000 });
    expect(editStreamField(p, state(), "max_kbps", null)).toEqual({ ...p, max_kbps: null });
  });

  test("without agent state a named preset switches to custom with only the edited field", () => {
    expect(editStreamField(prefs({ preset: "quality" }), null, "fps", 24)).toEqual(
      prefs({ preset: "custom", fps: 24 }),
    );
  });
});

describe("parseStreamPrefs / serializeStreamPrefs", () => {
  test("round-trips custom prefs", () => {
    const p = prefs({ preset: "custom", fps: 15, res: "half", max_kbps: 5500 });
    expect(parseStreamPrefs(serializeStreamPrefs(p))).toEqual(p);
  });

  test("serializes with a version tag", () => {
    expect(JSON.parse(serializeStreamPrefs(prefs({ preset: "speed" })))).toEqual({
      v: 1,
      preset: "speed",
      fps: null,
      res: null,
      max_kbps: null,
      wallpaper: false,
      scale: "fit",
    });
  });

  test("round-trips actual size", () => {
    const p = prefs({ scale: "actual" });
    expect(parseStreamPrefs(serializeStreamPrefs(p))).toEqual(p);
  });

  test("prefs saved before the wallpaper switch keep it hidden", () => {
    const raw = JSON.stringify({ v: 1, preset: "speed", fps: null, res: null, max_kbps: null });
    expect(parseStreamPrefs(raw)).toEqual(prefs({ preset: "speed" }));
  });

  test("round-trips a shown wallpaper", () => {
    const p = prefs({ wallpaper: true });
    expect(parseStreamPrefs(serializeStreamPrefs(p))).toEqual(p);
  });

  test.each([
    ["nothing stored", null],
    ["invalid JSON", "{"],
    ["a different version", JSON.stringify({ v: 2, preset: "speed" })],
    ["an unknown preset", JSON.stringify({ v: 1, preset: "turbo" })],
    ["an array", "[]"],
  ])("falls back to defaults for %s", (_name, raw) => {
    expect(parseStreamPrefs(raw)).toEqual(DEFAULT_STREAM_PREFS);
  });

  test("drops out-of-range fields but keeps the preset", () => {
    const raw = JSON.stringify({ v: 1, preset: "custom", fps: 61, res: "4k", max_kbps: 500 });
    expect(parseStreamPrefs(raw)).toEqual(prefs({ preset: "custom" }));
  });

  test("keys are per device", () => {
    expect(streamPrefsKey("d1")).toEqual("remotectl-stream:d1");
  });
});

describe("fpsChoices", () => {
  test("disables steps above the encoder cap", () => {
    expect(fpsChoices(30)).toEqual([
      { fps: 5, disabled: false },
      { fps: 10, disabled: false },
      { fps: 15, disabled: false },
      { fps: 24, disabled: false },
      { fps: 30, disabled: false },
      { fps: 60, disabled: true },
    ]);
  });

  test("an unknown cap enables everything", () => {
    expect(fpsChoices(null).filter((c) => c.disabled)).toEqual([]);
  });
});

describe("describeAuto", () => {
  test.each([
    ["congestion", "Lowered: network congestion · level 4/6"],
    ["steady", "Steady · level 4/6"],
    ["warmup", "Warming up · level 4/6"],
    ["probe", "Trying higher quality · level 4/6"],
    ["starved", "Lowered: not enough bandwidth · level 4/6"],
    ["loss", "Lowered: packet loss · level 4/6"],
  ] as const)("%s", (reason, text) => {
    expect(describeAuto(state({ auto: { rung: 2, rungs: 6, reason } }))).toEqual(text);
  });

  test("rung 0 is the highest level", () => {
    expect(describeAuto(state({ auto: { rung: 0, rungs: 6, reason: "steady" } }))).toEqual(
      "Steady · level 6/6",
    );
  });

  test("null outside auto", () => {
    expect(describeAuto(state({ preset: "speed", auto: null }))).toBeNull();
  });
});

describe("streamLabel", () => {
  test.each([
    ["auto with state", prefs(), state(), "Auto · 1440p30"],
    ["no state yet", prefs({ preset: "balanced" }), null, "Balanced"],
    [
      "custom portrait",
      prefs({ preset: "custom" }),
      state({ preset: "custom", width: 1080, height: 1920, target_fps: 15 }),
      "Custom · 1080p15",
    ],
  ])("%s", (_name, p, s, label) => {
    expect(streamLabel(p, s)).toEqual(label);
  });
});

describe("resolutionLabel", () => {
  test.each([
    [{ id: "native", width: 5120, height: 2880 }, 2, "Native · 5120×2880"],
    [{ id: "1080p", width: 1920, height: 1080 }, 2, "1080p · 1920×1080"],
    [{ id: "half", width: 2560, height: 1440 }, 2, "50% · 2560×1440 (logical size)"],
    [{ id: "half", width: 2560, height: 1440 }, 1.5, "50% · 2560×1440 (logical 3413×1920)"],
    [{ id: "half", width: 960, height: 540 }, 1, "50% · 960×540"],
  ] as const)("%j at scale %d", (opt, scale, label) => {
    expect(resolutionLabel(opt, scale, { width: 5120, height: 2880 })).toEqual(label);
  });
});

describe("relayCapped", () => {
  const resolved = (max_kbps: number | null) => ({ fps: 30, res: "native" as const, max_kbps });
  test.each([
    ["relay ceiling below the requested cap", true, 6000, resolved(8000), true],
    ["same cap on a direct path", false, 8000, resolved(8000), false],
    ["requested cap within the relay ceiling", true, 6000, resolved(6000), false],
    ["automatic bitrate on relay", true, 6000, resolved(null), false],
  ])("%s", (_name, relay, ceil_kbps, res, capped) => {
    expect(relayCapped(state({ relay, ceil_kbps, resolved: res }))).toEqual(capped);
  });
});

describe("isStreamMenuHotkey", () => {
  const key = (patch: Partial<Parameters<typeof isStreamMenuHotkey>[0]>) => ({
    code: "KeyS",
    ctrlKey: true,
    altKey: true,
    shiftKey: true,
    metaKey: false,
    ...patch,
  });
  test.each([
    ["Ctrl+Alt+Shift+S", key({}), true],
    ["without Shift", key({ shiftKey: false }), false],
    ["without Ctrl", key({ ctrlKey: false }), false],
    ["without Alt", key({ altKey: false }), false],
    ["with Meta as well", key({ metaKey: true }), false],
    ["another key", key({ code: "KeyD" }), false],
  ])("%s → %s", (_name, e, hit) => {
    expect(isStreamMenuHotkey(e)).toEqual(hit);
  });
});
