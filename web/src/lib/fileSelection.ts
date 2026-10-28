// Multi-select over a file list, file-manager style: click, Ctrl/⌘-click
// toggles, Shift-click and Shift+arrows select a range from the anchor.

import { selectRange, toggleId } from "./selection";

export type Selection = {
  readonly selected: ReadonlySet<string>;
  /** Where a Shift range starts: the last item clicked without Shift. */
  readonly anchor: string | null;
  /** The item the keyboard is on. */
  readonly focus: string | null;
};

export const EMPTY_SELECTION: Selection = { selected: new Set(), anchor: null, focus: null };

export type ClickMods = { shift?: boolean; toggle?: boolean };

export function clickSelect(
  sel: Selection,
  order: readonly string[],
  name: string,
  mods: ClickMods,
): Selection {
  const anchor = sel.anchor !== null && order.includes(sel.anchor) ? sel.anchor : null;
  if (mods.shift && anchor !== null) {
    const base = mods.toggle ? sel.selected : new Set<string>();
    return { selected: selectRange(base, order, anchor, name), anchor, focus: name };
  }
  if (mods.toggle) return { selected: toggleId(sel.selected, name), anchor: name, focus: name };
  return { selected: new Set([name]), anchor: name, focus: name };
}

/** Arrow keys, Home and End move the focus (Shift extends from the anchor); null for other keys. */
export function keySelect(
  sel: Selection,
  order: readonly string[],
  key: string,
  shift: boolean,
): Selection | null {
  if (order.length === 0) return null;
  const at = sel.focus === null ? -1 : order.indexOf(sel.focus);
  let next: number;
  switch (key) {
    case "ArrowDown":
      next = at < 0 ? 0 : Math.min(order.length - 1, at + 1);
      break;
    case "ArrowUp":
      next = at < 0 ? 0 : Math.max(0, at - 1);
      break;
    case "Home":
      next = 0;
      break;
    case "End":
      next = order.length - 1;
      break;
    default:
      return null;
  }
  return clickSelect(sel, order, order[next] as string, { shift });
}

export function selectAll(sel: Selection, order: readonly string[]): Selection {
  return { ...sel, selected: new Set(order) };
}

/** Drops names no longer listed; the same object when nothing changed. */
export function pruneSelection(sel: Selection, order: readonly string[]): Selection {
  const present = new Set(order);
  const gone = [...sel.selected].some((n) => !present.has(n));
  const anchorGone = sel.anchor !== null && !present.has(sel.anchor);
  const focusGone = sel.focus !== null && !present.has(sel.focus);
  if (!gone && !anchorGone && !focusGone) return sel;
  return {
    selected: new Set([...sel.selected].filter((n) => present.has(n))),
    anchor: anchorGone ? null : sel.anchor,
    focus: focusGone ? null : sel.focus,
  };
}
