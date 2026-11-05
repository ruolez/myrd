import { describe, expect, test } from "vitest";
import { parseStringList, pushHistory, toggleSnippet } from "./termHistory";

describe("pushHistory", () => {
  test("newest first, a repeat moves to the front, blanks are ignored", () => {
    let h: string[] = [];
    for (const c of ["ls", "  cd /tmp ", "ls", "   "]) h = pushHistory(h, c);
    expect(h).toEqual(["ls", "cd /tmp"]);
  });
  test("keeps the 50 most recent", () => {
    let h: string[] = [];
    for (let i = 0; i < 60; i++) h = pushHistory(h, `cmd ${i}`);
    expect([h.length, h[0], h[49]]).toEqual([50, "cmd 59", "cmd 10"]);
  });
});

describe("parseStringList", () => {
  test.each([
    [null, []],
    ["not json", []],
    ['{"a":1}', []],
    ['["a", 2, "b"]', ["a", "b"]],
  ])("%j → %o", (raw, want) => {
    expect(parseStringList(raw)).toEqual(want);
  });
});

describe("toggleSnippet", () => {
  test("saves a new command first, trimmed", () => {
    expect(toggleSnippet(["make test"], "  git status ")).toEqual(["git status", "make test"]);
  });
  test("forgets a saved command", () => {
    expect(toggleSnippet(["git status", "make test"], "make test")).toEqual(["git status"]);
  });
  test("toggling twice gives back the same commands", () => {
    const list = ["a", "b", "c"];
    for (const cmd of ["x", "b", " y "])
      expect(toggleSnippet(toggleSnippet(list, cmd), cmd).sort()).toEqual([...list].sort());
  });
  test("ignores blank lines", () => {
    expect(toggleSnippet(["a"], "   ")).toEqual(["a"]);
  });
  test("keeps at most 30", () => {
    const full = Array.from({ length: 30 }, (_, i) => `cmd ${i}`);
    expect(toggleSnippet(full, "new")).toEqual(["new", ...full.slice(0, 29)]);
  });
});
