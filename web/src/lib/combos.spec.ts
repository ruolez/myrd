import { describe, expect, test } from "vitest";
import { combosFor, comboInput } from "./combos";

// Key message: type 0x04, usage u16 LE, down, modifier byte (docs/PROTOCOL.md §5).
const key = (usage: number, down: boolean, mods: number) => [
  0x04,
  usage & 0xff,
  usage >> 8,
  down ? 1 : 0,
  mods,
];

describe("comboInput", () => {
  test("Ctrl+Alt+Del: downs in order, ups in reverse, modifier byte after each key", () => {
    expect(comboInput(["ControlLeft", "AltLeft", "Delete"]).map((m) => [...m])).toEqual([
      key(0xe0, true, 0x01),
      key(0xe2, true, 0x05),
      key(0x4c, true, 0x05),
      key(0x4c, false, 0x05),
      key(0xe2, false, 0x01),
      key(0xe0, false, 0x00),
    ]);
  });

  test("a lone key goes down and up with no modifiers", () => {
    expect(comboInput(["PrintScreen"]).map((m) => [...m])).toEqual([
      key(0x46, true, 0),
      key(0x46, false, 0),
    ]);
  });

  test("an unknown key name is an error, not a silent partial combo", () => {
    expect(() => comboInput(["ControlLeft", "NoSuchKey"])).toThrow("no HID usage for NoSuchKey");
  });
});

describe("combosFor", () => {
  test.each(["windows", "darwin", "linux", null])(
    "every %s combo maps to keys and releases what it pressed",
    (os) => {
      for (const c of combosFor(os)) {
        const msgs = comboInput(c.keys);
        expect(msgs).toHaveLength(c.keys.length * 2);
        expect(msgs.at(-1)?.[4]).toBe(0);
      }
    },
  );

  test("Ctrl+Alt+Del leads the Windows list; macOS gets its own keys", () => {
    expect(combosFor("windows")[0]?.keys).toEqual(["ControlLeft", "AltLeft", "Delete"]);
    expect(combosFor(null)).toBe(combosFor("windows"));
    expect(combosFor("darwin").some((c) => c.keys.includes("Delete"))).toBe(false);
  });
});
