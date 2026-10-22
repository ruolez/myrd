import { describe, expect, test } from "vitest";
import { workspaceHotkey } from "./shortcuts";

const chord = { ctrlKey: true, altKey: true, shiftKey: true, metaKey: false };
const none = { ctrlKey: false, altKey: false, shiftKey: false, metaKey: false };

describe("workspaceHotkey", () => {
  test.each([
    ["Digit1", { t: "focus", n: 1 }],
    ["Digit9", { t: "focus", n: 9 }],
    ["KeyS", { t: "streamMenu" }],
    ["KeyK", { t: "palette" }],
    ["KeyD", { t: "dashboard" }],
    ["ArrowLeft", { t: "prev" }],
    ["ArrowRight", { t: "next" }],
    ["Digit0", null],
    ["KeyA", null],
  ])("Ctrl+Alt+Shift+%s", (code, want) => {
    expect(workspaceHotkey({ code, ...chord })).toEqual(want);
  });

  test.each([
    ["without Shift", { ...chord, shiftKey: false }],
    ["with Meta", { ...chord, metaKey: true }],
    ["without Ctrl", { ...chord, ctrlKey: false }],
  ])("Digit1 %s goes to the remote", (_name, mods) => {
    expect(workspaceHotkey({ code: "Digit1", ...mods })).toBe(null);
  });

  test("Ctrl+Tab switches only while the keyboard is locked", () => {
    const tab = { code: "Tab", ...none, ctrlKey: true };
    expect(workspaceHotkey(tab, false)).toBe(null);
    expect(workspaceHotkey(tab, true)).toEqual({ t: "next" });
    expect(workspaceHotkey({ ...tab, shiftKey: true }, true)).toEqual({ t: "prev" });
  });

  test("plain Tab and Alt+Tab stay with the remote even when locked", () => {
    expect(workspaceHotkey({ code: "Tab", ...none }, true)).toBe(null);
    expect(workspaceHotkey({ code: "Tab", ...none, altKey: true }, true)).toBe(null);
  });
});
