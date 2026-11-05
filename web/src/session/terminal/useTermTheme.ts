// The terminal color theme, one for every terminal in this browser.

import { useSyncExternalStore } from "react";
import { readList, themeKey, writeList } from "../../lib/termHistory";
import { parseThemeId } from "./theme";
import type { TermThemeId } from "./theme";

const listeners = new Set<() => void>();
let current: TermThemeId | null = null;

export function getTermTheme(): TermThemeId {
  current ??= parseThemeId(readList(globalThis.localStorage, themeKey)[0]);
  return current;
}

export function setTermTheme(id: TermThemeId) {
  current = id;
  writeList(globalThis.localStorage, themeKey, [id]);
  for (const f of listeners) f();
}

const subscribe = (f: () => void) => {
  listeners.add(f);
  return () => listeners.delete(f);
};

export const useTermTheme = (): TermThemeId => useSyncExternalStore(subscribe, getTermTheme);
