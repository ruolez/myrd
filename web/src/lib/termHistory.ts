// Remembered terminal state per device, in this browser. Pure helpers plus
// tolerant storage reads (private mode, quota, garbage).

const HISTORY_MAX = 50;
const SNIPPETS_MAX = 30;

/** Most recent first, without duplicates or blank lines. */
export function pushHistory(list: readonly string[], cmd: string): string[] {
  const c = cmd.trim();
  if (!c) return [...list];
  return [c, ...list.filter((x) => x !== c)].slice(0, HISTORY_MAX);
}

/** Saves cmd as a snippet (first), or forgets it when it is one already. */
export function toggleSnippet(list: readonly string[], cmd: string): string[] {
  const c = cmd.trim();
  if (!c) return [...list];
  return list.includes(c) ? list.filter((x) => x !== c) : [c, ...list].slice(0, SNIPPETS_MAX);
}

/** Parses a stored list of strings; anything else is an empty list. */
export function parseStringList(raw: string | null): string[] {
  try {
    const v = JSON.parse(raw ?? "[]") as unknown;
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

export function readList(storage: Storage | undefined, key: string): string[] {
  try {
    return parseStringList(storage?.getItem(key) ?? null);
  } catch {
    return [];
  }
}

export function writeList(storage: Storage | undefined, key: string, list: readonly string[]) {
  try {
    storage?.setItem(key, JSON.stringify(list));
  } catch {
    // private mode / quota: kept for this page only
  }
}

export const historyKey = (deviceId: string) => `remotectl-term-history:${deviceId}`;
/** The terminals a tab had open, reattached after a reconnect (sessionStorage). */
export const openKey = (deviceId: string) => `remotectl-term-open:${deviceId}`;
export const fontKey = (deviceId: string) => `remotectl-term-font:${deviceId}`;
/** Saved commands, for every device (localStorage). */
export const snippetsKey = "remotectl-term-snippets";
/** The terminal color theme (localStorage). */
export const themeKey = "remotectl-term-theme";
