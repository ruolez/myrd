// Multi-select and keyboard movement over the dashboard's ordered tiles.

export function toggleId<T>(sel: ReadonlySet<T>, id: T): Set<T> {
  const out = new Set(sel);
  if (out.has(id)) out.delete(id);
  else out.add(id);
  return out;
}

/** Adds every id between anchor and target (inclusive) in display order. */
export function selectRange<T>(
  sel: ReadonlySet<T>,
  order: readonly T[],
  anchor: T,
  target: T,
): Set<T> {
  const a = order.indexOf(anchor);
  const b = order.indexOf(target);
  const out = new Set(sel);
  if (a < 0 || b < 0) {
    out.add(target);
    return out;
  }
  for (let i = Math.min(a, b); i <= Math.max(a, b); i++) out.add(order[i] as T);
  return out;
}

/** The tile index an arrow key (or Home/End) moves to in a grid, or null. */
export function gridNav(index: number, key: string, columns: number, count: number): number | null {
  if (count === 0) return null;
  const cols = Math.max(1, columns);
  let next: number;
  switch (key) {
    case "ArrowRight":
      next = index + 1;
      break;
    case "ArrowLeft":
      next = index - 1;
      break;
    case "ArrowDown":
      next = index + cols;
      break;
    case "ArrowUp":
      next = index - cols;
      break;
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return null;
  }
  return next >= 0 && next < count ? next : null;
}
