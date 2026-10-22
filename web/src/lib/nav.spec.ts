import { describe, expect, test } from "vitest";
import { DEFAULT_SIDEBAR, isSidebarToggle, navMode, parseSidebarPrefs } from "./nav";

describe("navMode", () => {
  test.each([
    ["/admin", "admin"],
    ["/admin/users", "admin"],
    ["/admin/settings", "admin"],
    ["/devices", "workspace"],
    ["/devices/abc", "workspace"],
    ["/settings", "workspace"],
    ["/administrator", "workspace"],
    ["/", "workspace"],
  ])("%s", (path, want) => {
    expect(navMode(path)).toBe(want);
  });
});

describe("parseSidebarPrefs", () => {
  test("round-trips every field", () => {
    const prefs = { collapsed: true, devices: false, sessions: false };
    expect(parseSidebarPrefs(JSON.stringify(prefs))).toEqual(prefs);
  });

  test.each([
    ["nothing stored", null],
    ["broken JSON", "{"],
    ["not an object", "[true]"],
    ["wrong types", JSON.stringify({ collapsed: "yes", devices: 1, sessions: null })],
  ])("%s gives the defaults", (_name, raw) => {
    expect(parseSidebarPrefs(raw)).toEqual(DEFAULT_SIDEBAR);
  });

  test("missing fields keep their defaults", () => {
    expect(parseSidebarPrefs(JSON.stringify({ collapsed: true }))).toEqual({
      ...DEFAULT_SIDEBAR,
      collapsed: true,
    });
  });
});

describe("isSidebarToggle", () => {
  const none = { ctrlKey: false, altKey: false, shiftKey: false, metaKey: false };
  test.each([
    ["Ctrl+B", { code: "KeyB", ...none, ctrlKey: true }, true],
    ["Cmd+B", { code: "KeyB", ...none, metaKey: true }, true],
    ["B alone", { code: "KeyB", ...none }, false],
    ["Ctrl+Shift+B", { code: "KeyB", ...none, ctrlKey: true, shiftKey: true }, false],
    ["Ctrl+Alt+B", { code: "KeyB", ...none, ctrlKey: true, altKey: true }, false],
    ["Ctrl+Cmd+B", { code: "KeyB", ...none, ctrlKey: true, metaKey: true }, false],
    ["Ctrl+N", { code: "KeyN", ...none, ctrlKey: true }, false],
  ])("%s", (_name, e, want) => {
    expect(isSidebarToggle(e)).toBe(want);
  });
});
