// Remote screen resolution (control `display`, docs/PROTOCOL.md §5): the
// device's monitor modes from the stream state, the "best fit" choice and the
// per-device remembered choice.

/** A mode as the device's display settings name it; scale > 1 is a HiDPI macOS mode. */
export type DisplayMode = { width: number; height: number; scale: number };
export type DisplayInfo = {
  modes: DisplayMode[];
  current: DisplayMode;
  /** The mode before a session changed it; null while unchanged. */
  original: DisplayMode | null;
};
/** What the user picked, remembered per device. */
export type DisplayChoice = { kind: "fit" } | { kind: "mode"; width: number; height: number };

const isPos = (v: unknown): v is number => Number.isInteger(v) && (v as number) > 0;

function parseMode(v: unknown): DisplayMode | null {
  if (typeof v !== "object" || v === null) return null;
  const o = v as Record<string, unknown>;
  if (!isPos(o.width) || !isPos(o.height)) return null;
  const scale = typeof o.scale === "number" && o.scale > 0 ? o.scale : 1;
  return { width: o.width, height: o.height, scale };
}

export function parseDisplay(v: unknown): DisplayInfo | null {
  if (typeof v !== "object" || v === null) return null;
  const o = v as Record<string, unknown>;
  const current = parseMode(o.current);
  if (!current) return null;
  const modes = Array.isArray(o.modes)
    ? o.modes.map(parseMode).filter((x): x is DisplayMode => x !== null)
    : [];
  return { modes, current, original: parseMode(o.original) };
}

export const sameSize = (a: DisplayMode, b: { width: number; height: number }) =>
  a.width === b.width && a.height === b.height;

/**
 * The largest mode whose picture (in pixels) fits the view, so the stream
 * shows it pixel for pixel; the smallest mode when none fits.
 */
export function bestFitMode(
  modes: DisplayMode[],
  view: { w: number; h: number },
): DisplayMode | null {
  let best: DisplayMode | null = null;
  let smallest: DisplayMode | null = null;
  const area = (x: DisplayMode) => x.width * x.scale * x.height * x.scale;
  for (const x of modes) {
    if (!smallest || area(x) < area(smallest)) smallest = x;
    const fits = x.width * x.scale <= view.w && x.height * x.scale <= view.h;
    if (fits && (!best || area(x) > area(best))) best = x;
  }
  return best ?? smallest;
}

/** Modes shaped like the current one (aspect within 2%) first, then the rest; order kept. */
export function groupModes(
  modes: DisplayMode[],
  current: DisplayMode,
): { same: DisplayMode[]; other: DisplayMode[] } {
  const ratio = current.width / current.height;
  const same: DisplayMode[] = [];
  const other: DisplayMode[] = [];
  for (const x of modes) (Math.abs(x.width / x.height / ratio - 1) <= 0.02 ? same : other).push(x);
  return { same, other };
}

export const displayPrefKey = (deviceId: string) => `remotectl-display:${deviceId}`;

export function serializeDisplayPref(choice: DisplayChoice): string {
  return JSON.stringify({ v: 1, ...choice });
}

export function parseDisplayPref(raw: string | null): DisplayChoice | null {
  if (!raw) return null;
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof v !== "object" || v === null) return null;
  const o = v as Record<string, unknown>;
  if (o.v !== 1) return null;
  if (o.kind === "fit") return { kind: "fit" };
  if (o.kind === "mode" && isPos(o.width) && isPos(o.height))
    return { kind: "mode", width: o.width, height: o.height };
  return null;
}

/** The mode a remembered choice asks this device for; null when it shows it already or does not offer it. */
export function rememberedMode(
  choice: DisplayChoice | null,
  info: DisplayInfo,
  view: { w: number; h: number },
): DisplayMode | null {
  if (!choice) return null;
  const want =
    choice.kind === "fit"
      ? bestFitMode(info.modes, view)
      : (info.modes.find((x) => sameSize(x, choice)) ?? null);
  return want && !sameSize(want, info.current) ? want : null;
}

export function modeLabel(mode: DisplayMode): string {
  return `${mode.width} × ${mode.height}${mode.scale > 1 ? " (HiDPI)" : ""}`;
}
