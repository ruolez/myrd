import { describe, expect, test } from "vitest";
import {
  afterKey,
  ctrlChar,
  diffInput,
  DOUBLE_TAP_MS,
  editBytes,
  keySequence,
  pasteBytes,
  SENTINEL,
  tapMod,
  withMods,
} from "./termKeys";

// A small seeded generator for property checks (no extra dependency).
function* strings(seed: number, n: number): Generator<string> {
  let x = seed;
  const next = () => (x = (x * 1103515245 + 12345) % 2 ** 31);
  const alphabet = ["a", "b", " ", "é", "😀", "\n", "\x1b", "Z", "1"];
  for (let i = 0; i < n; i++) {
    let s = "";
    for (let len = next() % 8; len > 0; len--) s += alphabet[next() % alphabet.length];
    yield s;
  }
}

describe("keySequence", () => {
  test.each([
    ["up", {}, false, "\x1b[A"],
    ["up", {}, true, "\x1bOA"],
    ["left", { ctrl: true }, true, "\x1b[1;5D"],
    ["right", { alt: true }, false, "\x1b[1;3C"],
    ["home", {}, false, "\x1b[H"],
    ["end", {}, true, "\x1bOF"],
    ["pageUp", {}, false, "\x1b[5~"],
    ["delete", { ctrl: true }, false, "\x1b[3;5~"],
    ["f1", {}, false, "\x1bOP"],
    ["f5", {}, false, "\x1b[15~"],
    ["f12", { shift: true }, false, "\x1b[24;2~"],
    ["tab", { shift: true }, false, "\x1b[Z"],
    ["esc", {}, false, "\x1b"],
    ["backspace", {}, false, "\x7f"],
    ["backspace", { alt: true }, false, "\x1b\x7f"],
    ["enter", {}, false, "\r"],
  ] as const)("%s %o appCursor=%s", (key, mods, app, want) => {
    expect(keySequence(key, { ctrl: false, alt: false, ...mods }, app)).toBe(want);
  });
});

describe("ctrlChar", () => {
  test("letters map to C0 controls regardless of case", () => {
    expect(["a", "C", "z"].map(ctrlChar)).toEqual(["\x01", "\x03", "\x1a"]);
  });
  test.each([
    [" ", "\x00"],
    ["[", "\x1b"],
    ["\\", "\x1c"],
    ["]", "\x1d"],
    ["_", "\x1f"],
    ["?", "\x7f"],
  ])("%j", (ch, want) => {
    expect(ctrlChar(ch)).toBe(want);
  });
  test("characters without a control form", () => {
    expect([ctrlChar("é"), ctrlChar("ab"), ctrlChar("!")]).toEqual([null, null, null]);
  });
});

describe("withMods", () => {
  test.each([
    ["c", { ctrl: true, alt: false }, "\x03"],
    ["b", { ctrl: false, alt: true }, "\x1bb"],
    ["x", { ctrl: true, alt: true }, "\x1b\x18"],
    ["cd", { ctrl: true, alt: false }, "\x03d"],
    ["é", { ctrl: true, alt: false }, "é"],
    ["", { ctrl: true, alt: false }, ""],
  ])("%j %o", (text, mods, want) => {
    expect(withMods(text, mods)).toBe(want);
  });
  test("without modifiers text is unchanged", () => {
    for (const s of strings(1, 200)) expect(withMods(s, { ctrl: false, alt: false })).toBe(s);
  });
});

describe("tapMod / afterKey", () => {
  test("tap arms, a quick second tap locks, a third turns it off", () => {
    const armed = tapMod("off", 9999);
    const locked = tapMod(armed, DOUBLE_TAP_MS - 1);
    expect([armed, locked, tapMod(locked, 10)]).toEqual(["armed", "locked", "off"]);
  });
  test("a slow second tap turns it off", () => {
    expect(tapMod("armed", DOUBLE_TAP_MS + 1)).toBe("off");
  });
  test("a key spends an armed modifier but not a locked one", () => {
    expect([afterKey("armed"), afterKey("locked"), afterKey("off")]).toEqual([
      "off",
      "locked",
      "off",
    ]);
  });
});

describe("diffInput / editBytes", () => {
  test("typing after the sentinel", () => {
    expect(diffInput(SENTINEL, SENTINEL + "ls")).toEqual({ deleted: 0, inserted: "ls" });
  });
  test("backspace into the sentinel", () => {
    expect(diffInput(SENTINEL, SENTINEL.slice(1))).toEqual({ deleted: 1, inserted: "" });
  });
  test("autocorrect replacing a word", () => {
    expect(diffInput(SENTINEL + "teh", SENTINEL + "the ")).toEqual({ deleted: 2, inserted: "he " });
  });
  test("an emoji replaced by another is one character out, one in", () => {
    expect(diffInput(SENTINEL + "😀", SENTINEL + "😃")).toEqual({ deleted: 1, inserted: "😃" });
  });
  test("emoji count as one deleted character", () => {
    expect(diffInput(SENTINEL + "a😀", SENTINEL + "a")).toEqual({ deleted: 1, inserted: "" });
  });
  test("applying the diff to before gives after", () => {
    const as = [...strings(7, 150)];
    const bs = [...strings(11, 150)];
    as.forEach((a, i) => {
      const b = i % 3 === 0 ? a + bs[i] : bs[i]!;
      const { deleted, inserted } = diffInput(a, b);
      expect([...a].slice(0, [...a].length - deleted).join("") + inserted).toBe(b);
    });
  });
  test("edit bytes: backspaces, newlines as Enter, Ctrl on the first key", () => {
    expect(editBytes(2, "a\nb", { ctrl: true, alt: false })).toBe("\x7f\x7f\x01\rb");
  });
});

describe("pasteBytes", () => {
  test("newlines become Enter", () => {
    expect(pasteBytes("a\nb\r\nc", false)).toBe("a\rb\rc");
  });
  test("bracketed paste wraps and strips a smuggled end marker", () => {
    expect(pasteBytes("x\x1b[201~rm -rf ~\n", true)).toBe("\x1b[200~xrm -rf ~\r\x1b[201~");
  });
});
