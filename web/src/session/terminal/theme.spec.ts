import { describe, expect, test } from "vitest";
import { parseThemeId, TERMINAL_THEMES } from "./theme";

describe("TERMINAL_THEMES", () => {
  const keys = [
    "background",
    "foreground",
    "cursor",
    "black",
    "red",
    "green",
    "yellow",
    "blue",
    "magenta",
    "cyan",
    "white",
    "brightBlack",
    "brightRed",
    "brightGreen",
    "brightYellow",
    "brightBlue",
    "brightMagenta",
    "brightCyan",
    "brightWhite",
  ] as const;
  test.each(Object.entries(TERMINAL_THEMES))("%s sets every color as #rrggbb", (_, { theme }) => {
    const bad = keys.filter((k) => !/^#[0-9a-f]{6}$/i.test(theme[k] ?? ""));
    expect(bad).toEqual([]);
  });
});

describe("parseThemeId", () => {
  test.each([
    ["light", "light"],
    ["solarized", "solarized"],
    ["tokyo-night", "tokyo-night"],
    ["toString", "dark"],
    ["dark", "dark"],
    ["neon", "dark"],
    [undefined, "dark"],
  ])("%s → %s", (raw, want) => {
    expect(parseThemeId(raw)).toBe(want);
  });
});
