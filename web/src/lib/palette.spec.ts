import { describe, expect, test } from "vitest";
import { fuzzyScore, rankItems } from "./palette";

describe("fuzzyScore", () => {
  test.each([
    ["fd", "Front desk", true],
    ["fdk", "Front desk", true],
    ["kd", "Front desk", false],
    ["", "anything", true],
  ])("%j in %j matches: %s", (q, text, ok) => {
    expect(fuzzyScore(q, text) !== null).toBe(ok);
  });

  test("a prefix beats a match in the middle", () => {
    expect(fuzzyScore("mac", "Mac mini")!).toBeGreaterThan(fuzzyScore("mac", "Old macbook")!);
  });

  test("word starts beat scattered letters", () => {
    expect(fuzzyScore("fd", "Front desk")!).toBeGreaterThan(fuzzyScore("fd", "Fluffed")!);
  });
});

describe("rankItems", () => {
  const items = ["Old macbook", "Mac mini", "Front desk", "Demo"];

  test("orders by score and drops non-matches", () => {
    expect(rankItems(items, "mac", (s) => [s])).toEqual(["Mac mini", "Old macbook"]);
  });

  test("an empty query keeps the input order", () => {
    expect(rankItems(items, "", (s) => [s])).toEqual(items);
  });

  test("the best of several fields counts", () => {
    const devs = [
      { name: "PC-1", host: "accounting" },
      { name: "Accounts", host: "pc-2" },
    ];
    expect(rankItems(devs, "acc", (d) => [d.name, d.host]).map((d) => d.name)).toEqual([
      "Accounts",
      "PC-1",
    ]);
  });
});
