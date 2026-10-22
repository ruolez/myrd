import { describe, expect, test } from "vitest";
import { gridNav, selectRange, toggleId } from "./selection";

describe("toggleId", () => {
  test("adds then removes", () => {
    const once = toggleId(new Set<string>(), "a");
    expect([...once]).toEqual(["a"]);
    expect([...toggleId(once, "a")]).toEqual([]);
  });
});

describe("selectRange", () => {
  const order = ["a", "b", "c", "d", "e"];
  test.each([
    ["b", "d", ["x", "b", "c", "d"]],
    ["d", "b", ["x", "b", "c", "d"]],
    ["c", "c", ["x", "c"]],
    ["gone", "e", ["x", "e"]],
  ])("from %s to %s", (anchor, target, want) => {
    expect([...selectRange(new Set(["x"]), order, anchor, target)].sort()).toEqual(want.sort());
  });
});

describe("gridNav", () => {
  // 7 tiles in 3 columns:  0 1 2 / 3 4 5 / 6
  test.each([
    [4, "ArrowRight", 5],
    [2, "ArrowRight", 3],
    [6, "ArrowRight", null],
    [0, "ArrowLeft", null],
    [1, "ArrowDown", 4],
    [4, "ArrowDown", null],
    [3, "ArrowDown", 6],
    [4, "ArrowUp", 1],
    [1, "ArrowUp", null],
    [4, "Home", 0],
    [1, "End", 6],
    [1, "Enter", null],
  ])("%d %s → %s", (index, key, want) => {
    expect(gridNav(index, key, 3, 7)).toBe(want);
  });

  test("no tiles, no movement", () => {
    expect(gridNav(0, "End", 3, 0)).toBe(null);
  });
});
