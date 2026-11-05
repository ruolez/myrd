import { describe, expect, test } from "vitest";
import { nextTheme, parseThemeId, TERMINAL_THEMES } from "./theme";
import type { TermThemeId } from "./theme";

describe("nextTheme", () => {
  test("visits every theme once before coming back", () => {
    const seen: TermThemeId[] = [];
    let id: TermThemeId = "dark";
    for (let i = 0; i < Object.keys(TERMINAL_THEMES).length; i++) {
      seen.push(id);
      id = nextTheme(id);
    }
    expect([id, [...seen].sort()]).toEqual(["dark", Object.keys(TERMINAL_THEMES).sort()]);
  });
});

describe("parseThemeId", () => {
  test.each([
    ["light", "light"],
    ["solarized", "solarized"],
    ["dark", "dark"],
    ["neon", "dark"],
    [undefined, "dark"],
  ])("%s → %s", (raw, want) => {
    expect(parseThemeId(raw)).toBe(want);
  });
});
