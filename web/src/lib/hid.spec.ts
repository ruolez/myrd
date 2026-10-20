import { describe, expect, test } from "vitest";
import { hidUsage, KeyState, modifierBit } from "./hid";

describe("hidUsage", () => {
  test.each([
    ["KeyA", 0x04],
    ["KeyZ", 0x1d],
    ["Digit1", 0x1e],
    ["Digit0", 0x27],
    ["Enter", 0x28],
    ["Escape", 0x29],
    ["Backspace", 0x2a],
    ["Tab", 0x2b],
    ["Space", 0x2c],
    ["Minus", 0x2d],
    ["Backquote", 0x35],
    ["Slash", 0x38],
    ["CapsLock", 0x39],
    ["F1", 0x3a],
    ["F12", 0x45],
    ["F13", 0x68],
    ["F24", 0x73],
    ["Delete", 0x4c],
    ["ArrowRight", 0x4f],
    ["ArrowUp", 0x52],
    ["NumLock", 0x53],
    ["NumpadEnter", 0x58],
    ["Numpad1", 0x59],
    ["Numpad9", 0x61],
    ["Numpad0", 0x62],
    ["IntlBackslash", 0x64],
    ["ControlLeft", 0xe0],
    ["MetaRight", 0xe7],
    ["OSLeft", 0xe3],
  ])("%s → 0x%s", (code, usage) => {
    expect(hidUsage(code)).toBe(usage);
  });

  test("unknown codes map to undefined", () => {
    expect(hidUsage("Unidentified")).toBeUndefined();
  });
});

describe("KeyState", () => {
  test("tracks modifier bits per HID modifier byte", () => {
    const ks = new KeyState();
    ks.down(0xe1); // LShift
    const mods = ks.down(0xe4); // RCtrl
    expect(mods).toBe(modifierBit(0xe1) | modifierBit(0xe4));
    expect(mods).toBe(0b0001_0010);
  });

  test("releaseAll returns every pressed key and clears state", () => {
    const ks = new KeyState();
    ks.down(0x04);
    ks.down(0xe0);
    ks.up(0x04);
    ks.down(0x05);
    expect({ released: ks.releaseAll().sort((a, b) => a - b), mods: ks.modifiers }).toEqual({
      released: [0x05, 0xe0],
      mods: 0,
    });
  });
});
