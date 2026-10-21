import { describe, expect, test } from "vitest";
import { vectors } from "../test/vectors";
import {
  ClipboardAssembler,
  CLIPBOARD_CHUNK_BYTES,
  decodeCursor,
  encodeClipboard,
  encodeClipboardBegin,
  encodeClipboardData,
  encodeControl,
  encodeKey,
  encodeMouseButton,
  encodeMouseMove,
  encodeMouseMoveRel,
  encodeMouseMoveSeq,
  encodeMouseWheel,
  encodeText,
  fromHex,
  MAX_CLIPBOARD_BYTES,
  MAX_TEXT_BYTES,
  Mime,
  toHex,
} from "./input";

type E = Record<string, number | string | boolean>;

function encodeVector(e: E): Uint8Array {
  const n = (k: string) => e[k] as number;
  switch (e.type) {
    case "move":
      return encodeMouseMove(n("x"), n("y"), n("monitor"));
    case "move_seq":
      return encodeMouseMoveSeq(n("seq"), n("x"), n("y"), n("monitor"));
    case "move_rel":
      return encodeMouseMoveRel(n("dx"), n("dy"));
    case "button":
      return encodeMouseButton(n("button"), e.down as boolean);
    case "wheel":
      return encodeMouseWheel(n("dx"), n("dy"));
    case "key":
      return encodeKey(n("usage"), e.down as boolean, n("mods"));
    case "text": {
      const [only] = encodeText(e.text as string);
      return only as Uint8Array;
    }
    case "clip_begin":
      return encodeClipboardBegin(n("xfer"), n("mime"), n("total"));
    case "clip_data":
      return encodeClipboardData(n("xfer"), new TextEncoder().encode(e.text as string));
    case "control":
      return encodeControl(n("op"), n("arg"));
    default:
      throw new Error(`unknown vector type ${String(e.type)}`);
  }
}

describe("input encoders", () => {
  test.each(vectors.input)("$name matches the shared vector", ({ event, hex }) => {
    expect(toHex(encodeVector(event as E))).toBe(hex);
  });

  test("move clamps coordinates into 0..65535", () => {
    expect(toHex(encodeMouseMove(-10, 70000, 0))).toBe("010000ffff00");
  });

  test("move seq wraps the sequence number into u16", () => {
    expect(toHex(encodeMouseMoveSeq(65536 + 3, 1, 2, 0))).toBe(
      toHex(encodeMouseMoveSeq(3, 1, 2, 0)),
    );
  });

  test("wheel clamps deltas into i16", () => {
    expect(toHex(encodeMouseWheel(-99999, 99999))).toBe("030080ff7f");
  });
});

describe("encodeText", () => {
  test("splits long text into ≤ 1024-byte messages without breaking code points", () => {
    const text = "é".repeat(MAX_TEXT_BYTES); // 2 bytes each
    const msgs = encodeText(text);
    const decoded = msgs.map((m) => new TextDecoder().decode(m.subarray(1))).join("");
    expect({ count: msgs.length, maxLen: Math.max(...msgs.map((m) => m.length)), decoded }).toEqual(
      { count: 2, maxLen: MAX_TEXT_BYTES + 1, decoded: text },
    );
  });

  test("empty text produces no messages", () => {
    expect(encodeText("")).toEqual([]);
  });
});

describe("clipboard", () => {
  test("round-trips a multi-chunk payload through ClipboardAssembler", () => {
    const bytes = new Uint8Array(CLIPBOARD_CHUNK_BYTES * 2 + 17).map((_, i) => i % 251);
    const frames = encodeClipboard(3, Mime.Png, bytes);
    const asm = new ClipboardAssembler();
    const results = frames.map((f) => asm.push(f));
    expect({ frames: frames.length, last: results.at(-1), early: results.slice(0, -1) }).toEqual({
      frames: 4,
      last: { mime: Mime.Png, bytes },
      early: [null, null, null],
    });
  });

  test("an empty clipboard completes on Begin", () => {
    const asm = new ClipboardAssembler();
    expect(asm.push(encodeClipboardBegin(1, Mime.Text, 0))).toEqual({
      mime: Mime.Text,
      bytes: new Uint8Array(0),
    });
  });

  test("data for an unknown transfer is dropped", () => {
    const asm = new ClipboardAssembler();
    asm.push(encodeClipboardBegin(1, Mime.Text, 2));
    expect(asm.push(encodeClipboardData(2, new Uint8Array([1, 2])))).toBeNull();
  });

  test("refuses payloads over 1 MiB", () => {
    expect(() => encodeClipboard(1, Mime.Text, new Uint8Array(MAX_CLIPBOARD_BYTES + 1))).toThrow();
  });
});

describe("decodeCursor", () => {
  test("decodes the shared cursor position vector", () => {
    const pos = vectors.cursor.find((v) => v.event.type === "pos");
    expect(decodeCursor(fromHex(pos?.hex ?? ""))).toEqual({
      type: "pos",
      x: 100,
      y: 200,
      visible: true,
    });
  });

  test("decodes the shared cursor shape vector", () => {
    const shape = vectors.cursor.find((v) => v.event.type === "shape");
    expect(decodeCursor(fromHex(shape?.hex ?? ""))).toEqual({
      type: "shape",
      id: 9,
      w: 2,
      h: 1,
      hotX: 0,
      hotY: 0,
      png: fromHex("89504e47"),
    });
  });

  test.each(["", "31640000", "300900", "7f0000000000"])("rejects malformed %s", (hex) => {
    expect(decodeCursor(fromHex(hex))).toBeNull();
  });
});
