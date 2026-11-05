import { describe, expect, test } from "vitest";
import { vectors } from "../test/vectors";
import {
  attachRequest,
  cwdRequest,
  clampSize,
  closedLine,
  decodeTermData,
  describeTermError,
  encodeTermData,
  inputFrames,
  listRequest,
  openRequest,
  parseTermMsg,
  resizeMsg,
  shellQuote,
  typedPath,
  TERM_INPUT,
  TERM_MAX_PAYLOAD,
  TERM_OUTPUT,
  terminalAvailability,
} from "./terminal";
import type { TermId, TermRequestId, TermSourceId } from "./terminal";

const hex = (b: Uint8Array) => Buffer.from(b).toString("hex");
const unhex = (s: string) => new Uint8Array(Buffer.from(s, "hex"));

describe("encodeTermData / decodeTermData", () => {
  for (const v of vectors.term_data) {
    test(`vector: ${v.name}`, () => {
      const payload = unhex(v.payload_hex);
      expect(hex(encodeTermData(v.type, v.term as TermId, payload))).toBe(v.hex);
      const d = decodeTermData(v.type, unhex(v.hex));
      expect(d && { term: d.term, payload: hex(d.payload) }).toEqual({
        term: v.term,
        payload: v.payload_hex,
      });
    });
  }
  for (const v of vectors.term_data_invalid) {
    test(`invalid: ${v.name}`, () => {
      expect(decodeTermData(TERM_OUTPUT, unhex(v.hex))).toBeNull();
    });
  }
  test("rejects an empty payload when encoding", () => {
    expect(() => encodeTermData(TERM_INPUT, 1 as TermId, new Uint8Array())).toThrow(RangeError);
  });
});

describe("inputFrames", () => {
  test("splits at the payload limit and keeps every byte in order", () => {
    const bytes = new Uint8Array(2 * TERM_MAX_PAYLOAD + 5).map((_, i) => i % 251);
    const frames = inputFrames(7 as TermId, bytes);
    const back = frames.map((f) => decodeTermData(TERM_INPUT, f));
    expect(back.map((d) => d?.payload.length)).toEqual([TERM_MAX_PAYLOAD, TERM_MAX_PAYLOAD, 5]);
    expect(Buffer.concat(back.map((d) => d!.payload))).toEqual(Buffer.from(bytes));
  });
});

describe("requests", () => {
  // The browser's builders produce messages the vectors call valid.
  const byName = Object.fromEntries(vectors.term_messages.map((v) => [v.name, v.json]));
  test("open default shell", () => {
    expect(JSON.parse(openRequest(2 as TermRequestId, { cols: 80, rows: 24 }))).toEqual(
      byName["open default shell"],
    );
  });
  test("open admin PowerShell", () => {
    expect(
      JSON.parse(openRequest(3 as TermRequestId, { cols: 120, rows: 40 }, "powershell", true)),
    ).toEqual(byName["open admin PowerShell"]);
  });
  test("attach tmux", () => {
    expect(
      JSON.parse(
        attachRequest(4 as TermRequestId, "tmux:main" as TermSourceId, { cols: 44, rows: 20 }),
      ),
    ).toEqual(byName["attach tmux"]);
  });
  test.each([
    ["list", 1, false],
    ["list asking again", 9, true],
  ])("%s", (name, id, ask) => {
    expect(JSON.parse(listRequest(id as TermRequestId, ask))).toEqual(byName[name]);
  });
  test("cwd", () => {
    expect(JSON.parse(cwdRequest(8 as TermRequestId, 1 as TermId))).toEqual(byName["cwd"]);
  });
  test("resize clamps to the largest accepted size", () => {
    expect(JSON.parse(resizeMsg(1 as TermId, { cols: 5000, rows: 900 }))).toEqual(byName["resize"]);
  });
});

describe("describeTermError", () => {
  test.each([
    ["timeout", "The device didn't answer in time. Try again."],
    ["teleport_failed", "The device refused (teleport_failed)."],
  ])("%s", (code, text) => {
    expect(describeTermError(code)).toBe(text);
  });
});

describe("clampSize", () => {
  test.each([
    [
      { cols: 0, rows: 0 },
      { cols: 1, rows: 1 },
    ],
    [
      { cols: 44.9, rows: 20.2 },
      { cols: 44, rows: 20 },
    ],
    [
      { cols: Number.NaN, rows: 3000 },
      { cols: 1, rows: 500 },
    ],
  ])("%o → %o", (input, want) => {
    expect(clampSize(input)).toEqual(want);
  });
});

describe("parseTermMsg", () => {
  test("sources keeps valid entries only", () => {
    const m = parseTermMsg(
      JSON.stringify({
        t: "sources",
        id: 1,
        sources: [
          { id: "shell:1", kind: "shell", title: "zsh", attached: 0, fidelity: "full", created: 5 },
          { id: "x", kind: "teleport", title: "?" },
        ],
        shells: [{ id: "/bin/zsh", name: "zsh", path: "/bin/zsh" }, { nope: 1 }],
        admin: false,
      }),
    );
    expect(m).toEqual({
      t: "sources",
      id: 1,
      sources: [
        {
          id: "shell:1",
          kind: "shell",
          title: "zsh",
          detail: "",
          attached: 0,
          admin: false,
          fidelity: "full",
          created: 5,
        },
      ],
      shells: [{ id: "/bin/zsh", name: "zsh", path: "/bin/zsh" }],
      admin: false,
      blocked: [],
    });
  });
  test("sources keeps the valid blocked apps", () => {
    const m = parseTermMsg(
      JSON.stringify({
        t: "sources",
        id: 2,
        sources: [],
        shells: [],
        admin: false,
        blocked: [
          { app: "Terminal", reason: "denied" },
          { app: "iTerm2", reason: "asking" },
          { app: "Hyper", reason: "maybe" },
          { reason: "denied" },
        ],
      }),
    );
    expect(m).toEqual({
      t: "sources",
      id: 2,
      sources: [],
      shells: [],
      admin: false,
      blocked: [
        { app: "Terminal", reason: "denied" },
        { app: "iTerm2", reason: "asking" },
      ],
    });
  });
  test("opened, closed, title and error", () => {
    const src = { id: "tmux:main", kind: "tmux", title: "main", attached: 2, fidelity: "full" };
    expect(
      parseTermMsg(JSON.stringify({ t: "opened", id: 4, term: 9, source: src, window: 262144 })),
    ).toEqual({
      t: "opened",
      id: 4,
      term: 9,
      source: { ...src, detail: "", admin: false, created: 0 },
      window: 262144,
    });
    expect(parseTermMsg('{"t":"closed","term":9,"reason":"exited","exit_code":2}')).toEqual({
      t: "closed",
      term: 9,
      reason: "exited",
      exitCode: 2,
    });
    expect(parseTermMsg('{"t":"title","term":9,"title":"vim"}')).toEqual({
      t: "title",
      term: 9,
      title: "vim",
    });
    expect(parseTermMsg('{"t":"error","id":3,"code":"not_found","detail":"gone"}')).toEqual({
      t: "error",
      id: 3,
      term: null,
      code: "not_found",
      detail: "gone",
    });
  });
  test.each([
    ["not json", "{"],
    ["array", "[]"],
    ["unknown type", '{"t":"party"}'],
    ["term out of range", '{"t":"title","term":70000,"title":"x"}'],
    ["opened without a source", '{"t":"opened","id":1,"term":2}'],
    ["closed without reason", '{"t":"closed","term":2}'],
  ])("rejects %s", (_, text) => {
    expect(parseTermMsg(text)).toBeNull();
  });
});

describe("terminalAvailability", () => {
  test.each([
    [null, "connecting", "pending"],
    [["terminal"], "connecting", "pending"],
    [["terminal"], "open", "ready"],
    [["files"], "open", "unsupported"],
    [["terminal"], "closed", "unsupported"],
    [null, "closing", "unsupported"],
  ] as const)("features %o, channel %s → %s", (features, chan, want) => {
    expect(terminalAvailability(features, chan)).toBe(want);
  });
});

describe("closedLine", () => {
  test.each([
    ["exited", 0, "[Process exited]"],
    ["exited", 3, "[Process exited with code 3]"],
    ["exited", -1, "[Process ended by a signal]"],
    ["killed", -1, "[Process ended]"],
    ["detached", null, "[Detached. The shell keeps running on the device]"],
    ["whatever", null, "[Terminal closed]"],
  ] as const)("%s %s", (reason, code, want) => {
    expect(closedLine(reason, code)).toBe(want);
  });
});

describe("shellQuote", () => {
  test.each([
    ["/home/dev/report.pdf", "/home/dev/report.pdf"],
    ["/home/dev/my report.pdf", "'/home/dev/my report.pdf'"],
    ["/tmp/it's here", "'/tmp/it'\\''s here'"],
    ["/tmp/$HOME`x`", "'/tmp/$HOME`x`'"],
    ["/tmp/back\\slash", "'/tmp/back\\slash'"],
    ["C:\\Users\\Ann\\a.txt", "C:\\Users\\Ann\\a.txt"],
    ["C:\\Users\\Ann Lee\\a b.txt", '"C:\\Users\\Ann Lee\\a b.txt"'],
  ])("%s", (path, want) => {
    expect(shellQuote(path)).toBe(want);
  });
});

describe("typedPath", () => {
  test.each([
    ["/home/dev/a b.txt", "/home/dev", "'a b.txt'"],
    ["/home/dev/a.txt", "/home/dev/", "a.txt"],
    ["/home/dev/sub/a.txt", "/home/dev", "/home/dev/sub/a.txt"],
    ["/home/devx/a.txt", "/home/dev", "/home/devx/a.txt"],
    ["/Users/a/Downloads/a.txt", null, "/Users/a/Downloads/a.txt"],
    ["C:\\Users\\Ann\\a.txt", "C:\\Users\\Ann", "a.txt"],
    ["C:\\Users\\Ann\\a b.txt", "C:\\Users\\Ann", '"a b.txt"'],
  ])("%s in %s", (path, dir, want) => {
    expect(typedPath(path, dir)).toBe(want);
  });
});
