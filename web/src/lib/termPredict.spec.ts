import { describe, expect, test } from "vitest";
import { ECHO_TIMEOUT_MS, expire, NO_PREDICT, output, printed, shown, typed } from "./termPredict";
import type { Predict } from "./termPredict";

const T0 = 1_000;

/** Types each piece at `now`, then applies the device's reply to it. */
function session(steps: [kind: "type" | "out", text: string, now?: number][]): Predict {
  let s = NO_PREDICT;
  for (const [kind, text, now = T0] of steps)
    s = kind === "type" ? typed(s, text, now, false) : output(s, text, now);
  return s;
}

describe("typed", () => {
  test("the first character on a line waits for its echo", () => {
    expect(shown(session([["type", "l"]]))).toBe("");
  });

  test("once echoed, the rest of the line shows at once", () => {
    expect(
      shown(
        session([
          ["type", "l"],
          ["out", "l"],
          ["type", "s -la"],
        ]),
      ),
    ).toBe("s -la");
  });

  test("a password prompt never shows what is typed", () => {
    expect(
      shown(
        session([
          ["out", "Password: "],
          ["type", "h"],
          ["type", "unter2"],
        ]),
      ),
    ).toBe("");
  });

  test.each([
    ["Enter", "\r"],
    ["Tab", "\t"],
    ["an arrow", "\x1b[D"],
    ["Ctrl+C", "\x03"],
    ["a paste", "\x1b[200~text\x1b[201~"],
  ])("%s stops predicting until the next echo", (_, key) => {
    const s = session([
      ["type", "a"],
      ["out", "a"],
      ["type", "bc"],
      ["type", key],
      ["type", "d"],
    ]);
    expect([s.trusted, shown(s)]).toEqual([false, ""]);
  });

  test("backspace takes back the last guess", () => {
    expect(
      shown(
        session([
          ["type", "a"],
          ["out", "a"],
          ["type", "bcd\x7f"],
        ]),
      ),
    ).toBe("bc");
  });

  test("backspace over echoed text stops predicting", () => {
    expect(
      session([
        ["type", "a"],
        ["out", "a"],
        ["type", "\x7f"],
      ]).trusted,
    ).toBe(false);
  });

  test("a full-screen program gets no prediction", () => {
    const trusted = session([
      ["type", "a"],
      ["out", "a"],
    ]);
    expect(typed(trusted, "jjj", T0, true)).toEqual(NO_PREDICT);
  });
});

describe("output", () => {
  const trusted = () =>
    session([
      ["type", "a"],
      ["out", "a"],
      ["type", "bcd"],
    ]);

  test("an echo confirms its guesses, in chunks", () => {
    expect([shown(output(trusted(), "b", T0)), shown(output(trusted(), "bcd", T0))]).toEqual([
      "cd",
      "",
    ]);
  });

  test("colors and cursor moves around the echo don't matter", () => {
    expect(shown(output(trusted(), "\x1b[32mb\x1b[0m\x1b[?25h", T0))).toBe("cd");
  });

  test("an autosuggestion after the echo keeps the line trusted", () => {
    const s = output(trusted(), "bcd\x1b[90mef\x1b[39m\b\b", T0);
    expect([s.trusted, shown(s)]).toEqual([true, ""]);
  });

  test("output that contradicts the guesses drops them", () => {
    expect(shown(output(trusted(), "x", T0))).toBe("");
    expect(shown(output(trusted(), "bx", T0))).toBe("");
  });

  test("escape-only output changes nothing", () => {
    expect(output(trusted(), "\x1b[?25l\x1b[?25h", T0)).toEqual(trusted());
  });

  test("a long burst is not taken as an echo", () => {
    const s = session([
      ["type", "a"],
      ["out", "a log line that happens to contain the letter a"],
    ]);
    expect(s.trusted).toBe(false);
  });

  test("a late echo does not make the line trusted", () => {
    const s = session([
      ["type", "a", T0],
      ["out", "a", T0 + ECHO_TIMEOUT_MS + 1],
    ]);
    expect(s.trusted).toBe(false);
  });
});

describe("expire", () => {
  test("guesses never echoed disappear", () => {
    const s = session([
      ["type", "a"],
      ["out", "a"],
      ["type", "bc", T0 + 10],
    ]);
    expect([expire(s, T0 + 500), expire(s, T0 + 10 + ECHO_TIMEOUT_MS + 1)]).toEqual([
      s,
      NO_PREDICT,
    ]);
  });
});

describe("printed", () => {
  test.each([
    ["plain", "ls -la", "ls -la"],
    ["SGR", "\x1b[1;31mred\x1b[0m", "red"],
    ["OSC title", "\x1b]0;user@host: ~\x07$ ", "$ "],
    ["controls", "a\r\n\bb\x07", "ab"],
  ])("%s", (_, input, want) => {
    expect(printed(input)).toBe(want);
  });

  test("never keeps an escape or control character", () => {
    const parts = ["a", "\x1b[0m", "\x1b]2;t\x07", "\r", "\n", "\x1b(B", "é", "\t"];
    let seed = 9;
    for (let i = 0; i < 200; i++) {
      let s = "";
      for (let j = 0; j < 6; j++) {
        seed = (seed * 1103515245 + 12345) % 2147483648;
        s += parts[seed % parts.length];
      }
      // eslint-disable-next-line no-control-regex -- what must never be left
      expect(printed(s)).not.toMatch(/[\x00-\x1f\x7f]/);
    }
  });
});
