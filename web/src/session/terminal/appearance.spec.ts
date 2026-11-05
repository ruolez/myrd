import { describe, expect, test } from "vitest";
import { DEFAULT_APPEARANCE, parseAppearance } from "./appearance";

describe("parseAppearance", () => {
  test("nothing stored is the default", () => {
    expect(parseAppearance(null)).toEqual(DEFAULT_APPEARANCE);
  });

  test("keeps every valid choice", () => {
    const stored = {
      theme: "nord",
      font: "menlo",
      lineHeight: 1.35,
      cursor: "bar",
      blink: false,
      instantTyping: false,
    };
    expect(parseAppearance(JSON.stringify(stored))).toEqual(stored);
  });

  test("replaces unknown or malformed values with the defaults", () => {
    const stored = { theme: "neon", font: "comic", lineHeight: 7, cursor: "heart", blink: "yes" };
    expect(parseAppearance(JSON.stringify(stored))).toEqual(DEFAULT_APPEARANCE);
  });

  test.each(["{", "[]", "42", '"dark"'])("survives %s", (raw) => {
    expect(parseAppearance(raw)).toEqual(DEFAULT_APPEARANCE);
  });

  test("takes the theme chosen before appearances existed", () => {
    expect(parseAppearance(null, "solarized")).toEqual({
      ...DEFAULT_APPEARANCE,
      theme: "solarized",
    });
  });

  test("a stored appearance wins over the old theme", () => {
    expect(parseAppearance('{"theme":"dracula"}', "light").theme).toBe("dracula");
  });
});
