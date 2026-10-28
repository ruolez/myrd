import { describe, expect, test } from "vitest";
import {
  clickSelect,
  keySelect,
  pruneSelection,
  selectAll,
  EMPTY_SELECTION,
} from "./fileSelection";
import type { Selection } from "./fileSelection";

const ORDER = ["a", "b", "c", "d", "e"];
const sel = (selected: string[], anchor: string | null, focus = anchor): Selection => ({
  selected: new Set(selected),
  anchor,
  focus,
});
const view = (s: Selection) => ({
  selected: [...s.selected].sort(),
  anchor: s.anchor,
  focus: s.focus,
});

describe("clickSelect", () => {
  test.each([
    ["a plain click selects only that item", sel(["a", "b"], "a"), "c", {}, sel(["c"], "c")],
    ["ctrl/⌘ toggles an item in", sel(["a"], "a"), "c", { toggle: true }, sel(["a", "c"], "c")],
    ["ctrl/⌘ toggles an item out", sel(["a", "c"], "a"), "c", { toggle: true }, sel(["a"], "c")],
    [
      "shift selects the range from the anchor, replacing the rest",
      sel(["e"], "b"),
      "d",
      { shift: true },
      sel(["b", "c", "d"], "b", "d"),
    ],
    ["shift works upwards", sel(["d"], "d"), "b", { shift: true }, sel(["b", "c", "d"], "d", "b")],
    [
      "shift with ctrl/⌘ adds the range to the selection",
      sel(["e"], "a"),
      "b",
      { shift: true, toggle: true },
      sel(["a", "b", "e"], "a", "b"),
    ],
    [
      "shift without an anchor selects the item",
      EMPTY_SELECTION,
      "c",
      { shift: true },
      sel(["c"], "c"),
    ],
  ])("%s", (_name, before, name, mods, want) => {
    expect(view(clickSelect(before, ORDER, name, mods))).toEqual(view(want));
  });

  test("a range never includes names outside the order", () => {
    const s = clickSelect(sel([], "gone"), ORDER, "b", { shift: true });
    expect(view(s)).toEqual(view(sel(["b"], "b")));
  });
});

describe("keySelect", () => {
  test.each([
    ["ArrowDown moves focus and selects it", sel(["b"], "b"), "ArrowDown", false, sel(["c"], "c")],
    ["ArrowUp at the top stays", sel(["a"], "a"), "ArrowUp", false, sel(["a"], "a")],
    ["Home goes to the first item", sel(["d"], "d"), "Home", false, sel(["a"], "a")],
    ["End goes to the last item", sel(["b"], "b"), "End", false, sel(["e"], "e")],
    [
      "shift+ArrowDown extends from the anchor",
      sel(["b", "c"], "b", "c"),
      "ArrowDown",
      true,
      sel(["b", "c", "d"], "b", "d"),
    ],
    [
      "shift+ArrowUp shrinks back towards the anchor",
      sel(["b", "c", "d"], "b", "d"),
      "ArrowUp",
      true,
      sel(["b", "c"], "b", "c"),
    ],
    [
      "ArrowDown with nothing focused starts at the top",
      EMPTY_SELECTION,
      "ArrowDown",
      false,
      sel(["a"], "a"),
    ],
  ])("%s", (_name, before, key, shift, want) => {
    const out = keySelect(before, ORDER, key, shift);
    expect(out && view(out)).toEqual(view(want));
  });

  test.each([
    ["an unrelated key", "x"],
    ["any key in an empty folder", "ArrowDown"],
  ])("returns null for %s", (_name, key) => {
    const order = key === "x" ? ORDER : [];
    expect(keySelect(sel(["a"], "a"), order, key, false)).toBeNull();
  });
});

describe("selectAll and pruneSelection", () => {
  test("selectAll selects every item and keeps the focus", () => {
    expect(view(selectAll(sel(["b"], "b"), ORDER))).toEqual(view(sel(ORDER, "b")));
  });

  test("pruneSelection drops names that left the listing and an anchor that did", () => {
    const s = pruneSelection(sel(["a", "x", "c"], "x", "c"), ORDER);
    expect(view(s)).toEqual(view(sel(["a", "c"], null, "c")));
  });

  test("pruneSelection returns the same object when nothing changed", () => {
    const s = sel(["a"], "a");
    expect(pruneSelection(s, ORDER)).toBe(s);
  });
});
