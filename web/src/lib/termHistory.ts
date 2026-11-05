// Remembered terminal state per device, in this browser. Pure helpers plus
// tolerant storage reads (private mode, quota, garbage).

const HISTORY_MAX = 50;

/** Most recent first, without duplicates or blank lines. */
export function pushHistory(list: readonly string[], cmd: string): string[] {
  const c = cmd.trim();
  if (!c) return [...list];
  return [c, ...list.filter((x) => x !== c)].slice(0, HISTORY_MAX);
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
