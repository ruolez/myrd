// How terminals look in this browser (the Appearance panel): theme, font,
// line spacing, cursor and instant typing. The text size is per device
// (pinch / zoom), kept with the device's terminal history.

import { useSyncExternalStore } from "react";
import { readList, themeKey } from "../../lib/termHistory";
import { parseThemeId, TERMINAL_FONTS } from "./theme";
import type { TermFontId, TermThemeId } from "./theme";

export type CursorShape = "block" | "bar" | "underline";

export type TermAppearance = {
  theme: TermThemeId;
  font: TermFontId;
  /** Line height as a multiple of the font size. */
  lineHeight: number;
  cursor: CursorShape;
  blink: boolean;
  /** Show typing before the device echoes it (termPredict). */
  instantTyping: boolean;
};

export const LINE_HEIGHTS = [
  { value: 1, name: "Compact" },
  { value: 1.15, name: "Normal" },
  { value: 1.35, name: "Relaxed" },
] as const;

export const DEFAULT_APPEARANCE: TermAppearance = {
  theme: "dark",
  font: "system",
  lineHeight: 1.15,
  cursor: "block",
  blink: true,
  instantTyping: true,
};

export const appearanceKey = "remotectl-term-appearance";

/** Reads a stored appearance; anything missing or unknown is the default. */
export function parseAppearance(raw: string | null, legacyTheme?: string): TermAppearance {
  let v: Record<string, unknown> = {};
  try {
    const parsed: unknown = raw ? JSON.parse(raw) : {};
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed))
      v = parsed as Record<string, unknown>;
  } catch {
    // keep the defaults
  }
  const d = DEFAULT_APPEARANCE;
  const pick = <T>(x: unknown, ok: (x: unknown) => x is T, dflt: T): T => (ok(x) ? x : dflt);
  return {
    theme: parseThemeId(typeof v.theme === "string" ? v.theme : legacyTheme),
    font: pick(v.font, (x): x is TermFontId => TERMINAL_FONTS.some((f) => f.id === x), d.font),
    lineHeight: pick(
      v.lineHeight,
      (x): x is number => LINE_HEIGHTS.some((l) => l.value === x),
      d.lineHeight,
    ),
    cursor: pick(
      v.cursor,
      (x): x is CursorShape => x === "block" || x === "bar" || x === "underline",
      d.cursor,
    ),
    blink: pick(v.blink, (x): x is boolean => typeof x === "boolean", d.blink),
    instantTyping: pick(
      v.instantTyping,
      (x): x is boolean => typeof x === "boolean",
      d.instantTyping,
    ),
  };
}

const listeners = new Set<() => void>();
let current: TermAppearance | null = null;

function storage(): Storage | null {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

export function getTermAppearance(): TermAppearance {
  if (!current) {
    const st = storage();
    let raw: string | null;
    try {
      raw = st?.getItem(appearanceKey) ?? null;
    } catch {
      raw = null;
    }
    current = parseAppearance(raw, st ? readList(st, themeKey)[0] : undefined);
  }
  return current;
}

export function setTermAppearance(change: Partial<TermAppearance>) {
  current = { ...getTermAppearance(), ...change };
  try {
    storage()?.setItem(appearanceKey, JSON.stringify(current));
  } catch {
    // private mode: this tab only
  }
  for (const f of listeners) f();
}

const subscribe = (f: () => void) => {
  listeners.add(f);
  return () => listeners.delete(f);
};

export const useTermAppearance = (): TermAppearance =>
  useSyncExternalStore(subscribe, getTermAppearance);
