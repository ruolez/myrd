import { describe, expect, test } from "vitest";
import { niceTicks } from "./charts";

describe("niceTicks", () => {
  test.each([
    [1, [0, 1]],
    [4, [0, 1, 2, 3, 4]],
    [9, [0, 2, 4, 6, 8, 10]],
    [37, [0, 10, 20, 30, 40]],
    [180, [0, 100, 200]],
    [700, [0, 200, 400, 600, 800]],
  ])("max %d → %j", (max, ticks) => {
    expect(niceTicks(max)).toEqual(ticks);
  });

  test("the last tick always covers the maximum", () => {
    for (const max of [1, 3, 7, 13, 99, 101, 555, 1234]) {
      expect(niceTicks(max).at(-1)).toBeGreaterThanOrEqual(max);
    }
  });
});
