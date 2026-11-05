// The terminal tab strip: every terminal on the device is a tab, attached or
// not, in the order it was first seen.

import type { TermSource, TermSourceId } from "./terminal";

/** A tab: an attached terminal (`tab`), the device's listing of it (`source`), or both. */
export type StripItem<T> = { id: TermSourceId; tab: T | null; source: TermSource | null };

/** The tabs in `order` (ids first seen earlier first), then any new ones;
 * terminals closed here (`hidden`) and not attached are left out. */
export function stripItems<T>(
  order: readonly TermSourceId[],
  tabs: readonly T[],
  idOf: (t: T) => TermSourceId,
  sources: readonly TermSource[],
  hidden: ReadonlySet<TermSourceId>,
): StripItem<T>[] {
  const live = new Map(tabs.map((t) => [idOf(t), t]));
  const onDevice = new Map(sources.map((s) => [s.id, s]));
  const ids = [...new Set([...order, ...live.keys(), ...onDevice.keys()])];
  return ids.flatMap((id) => {
    const tab = live.get(id) ?? null;
    const source = onDevice.get(id) ?? null;
    if (!tab && (!source || hidden.has(id))) return [];
    return [{ id, tab, source }];
  });
}
