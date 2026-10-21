// Stream settings (docs/PROTOCOL.md §5 "Stream settings"). Pure: no DOM, no storage.

import type { Brand } from "../api/types";

export type StreamPreset = "auto" | "speed" | "balanced" | "quality" | "custom";
export type NamedPreset = Exclude<StreamPreset, "custom">;
export type ResolutionId = "window" | "native" | "1440p" | "1080p" | "720p" | "half";
export type AutoReason =
  "steady" | "warmup" | "congestion" | "loss" | "starved" | "latency" | "probe";
/** fit: the stream follows the window, shown pixel for pixel; actual: native size 1:1, scrolled. */
export type StreamScale = "fit" | "actual";
/** standard: 4:2:0 H.264; full: 4:4:4 (VP9 profile 1) for sharp colored text. */
export type StreamColor = "standard" | "full";
/** The viewer's video area in device pixels (even), sent so the agent encodes exactly that. */
export type StreamView = { w: number; h: number };
export type StreamRequestId = Brand<number, "StreamRequestId">;
export type LegacyTier = 0 | 1 | 2;

export type StreamPrefs = {
  preset: StreamPreset;
  fps: number | null;
  res: ResolutionId | null;
  max_kbps: number | null;
  /** Show the device's wallpaper; hidden by default (cheaper to encode). */
  wallpaper: boolean;
  scale: StreamScale;
  color: StreamColor;
};

export type ResolutionOption = { id: ResolutionId; width: number; height: number };

export type StreamState = {
  id: number | null;
  ok: boolean;
  preset: StreamPreset;
  fps: number | null;
  res: ResolutionId | null;
  max_kbps: number | null;
  background: boolean;
  wallpaper: boolean;
  /** The color the browser asked for; resolved.color is what the device sends. */
  color: StreamColor | null;
  resolved: { fps: number; res: ResolutionId; max_kbps: number | null; color: StreamColor };
  target_fps: number;
  max_fps: number;
  width: number;
  height: number;
  native_width: number;
  native_height: number;
  ceil_kbps: number | null;
  relay: boolean;
  auto: { rung: number; rungs: number; reason: AutoReason } | null;
  options: { fps: number[]; res: ResolutionOption[]; color: StreamColor[] };
};

export const NAMED_PRESETS: readonly NamedPreset[] = ["auto", "speed", "balanced", "quality"];
export const FPS_STEPS: readonly number[] = [5, 10, 15, 24, 30, 60];
export const MIN_KBPS = 1000;
export const MAX_KBPS = 50000;
export const KBPS_STEP = 500;
export const RELAY_CAP_KBPS = 6000;
export const DEFAULT_STREAM_PREFS: StreamPrefs = {
  preset: "auto",
  fps: null,
  res: null,
  max_kbps: null,
  wallpaper: false,
  scale: "fit",
  color: "standard",
};

const PRESETS: readonly StreamPreset[] = [...NAMED_PRESETS, "custom"];
const RESOLUTIONS: readonly ResolutionId[] = ["window", "native", "1440p", "1080p", "720p", "half"];
const REASONS: Record<AutoReason, string> = {
  steady: "Steady",
  warmup: "Warming up",
  congestion: "Lowered: network congestion",
  loss: "Lowered: packet loss",
  starved: "Lowered: not enough bandwidth",
  latency: "Lowered: network delay",
  probe: "Trying higher quality",
};
const PRESET_NAMES: Record<StreamPreset, string> = {
  auto: "Auto",
  speed: "Speed",
  balanced: "Balanced",
  quality: "Quality",
  custom: "Custom",
};
const RES_NAMES: Record<ResolutionId, string> = {
  window: "Match window",
  native: "Native",
  "1440p": "1440p",
  "1080p": "1080p",
  "720p": "720p",
  half: "50%",
};

type Obj = Record<string, unknown>;

const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const isNum = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);
const isPreset = (v: unknown): v is StreamPreset => PRESETS.includes(v as StreamPreset);
const isRes = (v: unknown): v is ResolutionId => RESOLUTIONS.includes(v as ResolutionId);
const isReason = (v: unknown): v is AutoReason => typeof v === "string" && v in REASONS;
const isColor = (v: unknown): v is StreamColor => v === "standard" || v === "full";
const intIn = (v: unknown, lo: number, hi: number): number | null =>
  Number.isInteger(v) && (v as number) >= lo && (v as number) <= hi ? (v as number) : null;
const fpsOrNull = (v: unknown) => intIn(v, 1, 60);
const kbpsOrNull = (v: unknown) => intIn(v, MIN_KBPS, MAX_KBPS);
const resOrNull = (v: unknown) => (isRes(v) ? v : null);

/**
 * The B→A `stream` message; key order matches the shared vectors. `color` is
 * left out when standard, `view` when null: at actual size, or before the
 * stage was measured.
 */
export function encodeStreamRequest(
  prefs: StreamPrefs,
  hidden: boolean,
  id: StreamRequestId,
  view: StreamView | null,
): string {
  return JSON.stringify({
    t: "stream",
    id,
    preset: prefs.preset,
    fps: prefs.fps,
    res: prefs.res,
    max_kbps: prefs.max_kbps,
    background: hidden,
    wallpaper: prefs.wallpaper,
    color: prefs.color === "full" ? "full" : undefined,
    view: view ?? undefined,
  });
}

/** The stage's size in device pixels, rounded down to even: what the agent may encode at. */
export function streamView(cssWidth: number, cssHeight: number, dpr: number): StreamView | null {
  const even = (v: number) => Math.floor(v * dpr) & ~1;
  const w = even(cssWidth);
  const h = even(cssHeight);
  return w >= 2 && h >= 2 ? { w, h } : null;
}

/** `0x20 op 3` argument for agents without the `stream` feature. */
export function legacyTier(prefs: StreamPrefs, hidden: boolean): LegacyTier {
  if (hidden) return 0;
  const thirtyFps = prefs.preset === "balanced" || prefs.preset === "quality";
  const fps = prefs.fps ?? (thirtyFps ? 30 : 60);
  return fps <= 30 ? 1 : 2;
}

/** Validates an A→B `stream` message; null when it is unusable. */
export function parseStreamState(m: unknown): StreamState | null {
  if (!isObj(m) || !isPreset(m.preset) || !isObj(m.resolved)) return null;
  const r = m.resolved;
  if (!isNum(r.fps) || !isRes(r.res)) return null;
  const sizes = ["target_fps", "max_fps", "width", "height", "native_width", "native_height"];
  if (!sizes.every((k) => isNum(m[k]))) return null;
  const auto =
    isObj(m.auto) && isNum(m.auto.rung) && isNum(m.auto.rungs) && isReason(m.auto.reason)
      ? { rung: m.auto.rung, rungs: m.auto.rungs, reason: m.auto.reason }
      : null;
  const opts = isObj(m.options) ? m.options : {};
  const fpsOpts = Array.isArray(opts.fps) ? opts.fps.filter(isNum) : [];
  const colorOpts = Array.isArray(opts.color) ? opts.color.filter(isColor) : [];
  const resOpts = Array.isArray(opts.res)
    ? opts.res
        .filter((o): o is Obj => isObj(o) && isRes(o.id) && isNum(o.width) && isNum(o.height))
        .map((o) => ({
          id: o.id as ResolutionId,
          width: o.width as number,
          height: o.height as number,
        }))
    : [];
  return {
    id: isNum(m.id) ? m.id : null,
    ok: m.ok !== false,
    preset: m.preset,
    fps: fpsOrNull(m.fps),
    res: resOrNull(m.res),
    max_kbps: kbpsOrNull(m.max_kbps),
    background: m.background === true,
    wallpaper: m.wallpaper === true,
    color: isColor(m.color) ? m.color : null,
    resolved: {
      fps: r.fps,
      res: r.res,
      max_kbps: isNum(r.max_kbps) ? r.max_kbps : null,
      color: isColor(r.color) ? r.color : "standard",
    },
    target_fps: m.target_fps as number,
    max_fps: m.max_fps as number,
    width: m.width as number,
    height: m.height as number,
    native_width: m.native_width as number,
    native_height: m.native_height as number,
    ceil_kbps: isNum(m.ceil_kbps) ? m.ceil_kbps : null,
    relay: m.relay === true,
    auto,
    options: { fps: fpsOpts, res: resOpts, color: colorOpts },
  };
}

/**
 * Picking a named preset drops every override; picking Custom starts from what
 * the agent resolved for the current settings. The wallpaper, scale and color
 * choices stay.
 */
export function selectPreset(
  prefs: StreamPrefs,
  preset: StreamPreset,
  state: StreamState | null,
): StreamPrefs {
  const { wallpaper, scale, color } = prefs;
  if (preset !== "custom") return { ...DEFAULT_STREAM_PREFS, preset, wallpaper, scale, color };
  if (prefs.preset === "custom") return prefs;
  const base = state?.resolved ?? prefs;
  return {
    preset: "custom",
    fps: base.fps,
    res: base.res,
    max_kbps: base.max_kbps,
    wallpaper,
    scale,
    color,
  };
}

type Field = "fps" | "res" | "max_kbps";

/** In Auto an edit sets a ceiling; any other preset becomes Custom, filled from `resolved`. */
export function editStreamField<K extends Field>(
  prefs: StreamPrefs,
  state: StreamState | null,
  field: K,
  value: StreamPrefs[K],
): StreamPrefs {
  if (prefs.preset === "auto" || prefs.preset === "custom") return { ...prefs, [field]: value };
  return { ...selectPreset(prefs, "custom", state), [field]: value };
}

export const streamPrefsKey = (deviceId: string) => `remotectl-stream:${deviceId}`;

export function serializeStreamPrefs(prefs: StreamPrefs): string {
  return JSON.stringify({ v: 1, ...prefs });
}

export function parseStreamPrefs(raw: string | null): StreamPrefs {
  if (!raw) return DEFAULT_STREAM_PREFS;
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return DEFAULT_STREAM_PREFS;
  }
  if (!isObj(v) || v.v !== 1 || !isPreset(v.preset)) return DEFAULT_STREAM_PREFS;
  return {
    preset: v.preset,
    fps: fpsOrNull(v.fps),
    res: resOrNull(v.res),
    max_kbps: kbpsOrNull(v.max_kbps),
    wallpaper: v.wallpaper === true,
    scale: v.scale === "actual" ? "actual" : "fit",
    color: v.color === "full" ? "full" : "standard",
  };
}

/** The device and this browser can both do full color (4:4:4). */
export const fullColorAvailable = (state: StreamState | null) =>
  state?.options.color.includes("full") ?? false;

export function fpsChoices(maxFps: number | null): { fps: number; disabled: boolean }[] {
  return FPS_STEPS.map((fps) => ({ fps, disabled: maxFps !== null && fps > maxFps }));
}

/** Auto's reason and quality level; rung 0 is the top rung, so level = rungs - rung. */
export function describeAuto(state: StreamState | null): string | null {
  if (!state?.auto) return null;
  const { rung, rungs, reason } = state.auto;
  return `${REASONS[reason]} · level ${rungs - rung}/${rungs}`;
}

/** Toolbar label, e.g. "Auto · 1440p30". */
export function streamLabel(prefs: StreamPrefs, state: StreamState | null): string {
  const name = PRESET_NAMES[prefs.preset];
  if (!state) return name;
  return `${name} · ${Math.min(state.width, state.height)}p${state.target_fps}`;
}

export const presetName = (p: StreamPreset) => PRESET_NAMES[p];

export function resolutionLabel(
  opt: ResolutionOption,
  scale: number,
  native: { width: number; height: number } | null,
): string {
  const base = `${RES_NAMES[opt.id]} · ${opt.width}×${opt.height}`;
  if (opt.id !== "half" || scale <= 1 || !native) return base;
  const lw = Math.round(native.width / scale);
  const lh = Math.round(native.height / scale);
  const same = Math.abs(lw - opt.width) <= 1 && Math.abs(lh - opt.height) <= 1;
  return `${base} (${same ? "logical size" : `logical ${lw}×${lh}`})`;
}

/** True when the relay ceiling is below the cap the settings asked for. */
export function relayCapped(state: StreamState | null): boolean {
  if (!state?.relay || state.ceil_kbps === null || state.resolved.max_kbps === null) return false;
  return state.ceil_kbps < state.resolved.max_kbps;
}

type HotkeyEvent = {
  code: string;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  metaKey: boolean;
};

/** Ctrl+Alt+Shift+S opens the stream menu instead of reaching the remote. */
export function isStreamMenuHotkey(e: HotkeyEvent): boolean {
  return e.code === "KeyS" && e.ctrlKey && e.altKey && e.shiftKey && !e.metaKey;
}
