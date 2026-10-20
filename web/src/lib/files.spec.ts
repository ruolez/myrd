import { describe, expect, test } from "vitest";
import { vectors } from "../test/vectors";
import { fromHex, toHex } from "./input";
import {
  ackMsg,
  breadcrumbs,
  cancelMsg,
  chunkRanges,
  contentDisposition,
  createSha256,
  CreditWindow,
  decodeFileData,
  describeFileError,
  encodeFileData,
  endMsg,
  filesAvailability,
  getRequest,
  joinPath,
  listRequest,
  MAX_FILE_PAYLOAD,
  parseFileMsg,
  putRequest,
  RateMeter,
  rootsRequest,
} from "./files";
import type { FileRequestId, Root, TransferId } from "./files";

const vec = (name: string) => {
  const v = vectors.file_messages.find((m) => m.name === name);
  if (!v) throw new Error(`missing vector ${name}`);
  return v.json;
};
const rid = (n: number) => n as FileRequestId;
const xid = (n: number) => n as TransferId;

describe("encodeFileData", () => {
  test.each(vectors.file_data)("$name matches the shared vector", (v) => {
    expect(toHex(encodeFileData(xid(v.xfer), fromHex(v.payload_hex)))).toEqual(v.hex);
  });

  test.each([
    ["an empty payload", 0],
    ["a payload over 16 KiB", MAX_FILE_PAYLOAD + 1],
  ])("refuses %s", (_name, n) => {
    expect(() => encodeFileData(xid(1), new Uint8Array(n))).toThrow(RangeError);
  });
});

describe("decodeFileData", () => {
  test.each(vectors.file_data)("$name decodes", (v) => {
    expect(decodeFileData(fromHex(v.hex))).toEqual({
      xfer: v.xfer,
      payload: fromHex(v.payload_hex),
    });
  });

  test.each(vectors.file_data_invalid)("rejects $name", (v) => {
    expect(decodeFileData(fromHex(v.hex))).toBeNull();
  });

  test("decodes a frame inside a larger buffer", () => {
    const outer = fromHex("ff40070000006869ff");
    expect(decodeFileData(outer.subarray(1, 8))).toEqual({ xfer: 7, payload: fromHex("6869") });
  });
});

describe("outgoing file messages", () => {
  test.each([
    ["roots request", rootsRequest(rid(1))],
    ["list", listRequest(rid(2), "/home/device", 0, false)],
    ["get", getRequest(xid(8), "/home/device/notes.txt")],
    ["put", putRequest(xid(7), "", "a.txt", 5)],
    ["ack", ackMsg(xid(7), 262144)],
    [
      "end upload",
      endMsg(xid(7), "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"),
    ],
    ["cancel", cancelMsg(xid(7))],
  ])("%s matches the shared vector", (name, json) => {
    expect(JSON.parse(json)).toEqual(vec(name));
  });
});

describe("parseFileMsg", () => {
  test.each([
    "roots reply",
    "listing",
    "begin",
    "ready",
    "ack",
    "end download",
    "done",
    "cancel",
    "error for transfer",
    "error for request",
  ])("%s round-trips the shared vector", (name) => {
    expect(parseFileMsg(JSON.stringify(vec(name)))).toEqual(vec(name));
  });

  test.each([
    ["invalid JSON", "{"],
    ["unknown type", JSON.stringify({ t: "rename", xfer: 1 })],
    ["begin without window", JSON.stringify({ t: "begin", xfer: 1, name: "a", size: 1, mtime: 0 })],
    ["ack with negative bytes", JSON.stringify({ t: "ack", xfer: 1, bytes: -1 })],
    ["error without id or xfer", JSON.stringify({ t: "error", code: "io", detail: "" })],
    [
      "listing entry of unknown kind",
      JSON.stringify({ ...vec("listing"), entries: [{ name: "x", kind: "pipe" }] }),
    ],
    ["roots with a bad separator", JSON.stringify({ ...vec("roots reply"), sep: ":" })],
    ["a B→A list request", JSON.stringify(vec("list"))],
  ])("rejects %s", (_name, text) => {
    expect(parseFileMsg(text)).toBeNull();
  });
});

describe("createSha256", () => {
  test.each(vectors.file_sha256)("$name", (v) => {
    const h = createSha256();
    for (const c of v.chunks_hex) h.update(fromHex(c));
    expect(h.hex()).toEqual(v.sha256);
  });
});

describe("CreditWindow", () => {
  test("allows exactly the window beyond the last ack", () => {
    const w = new CreditWindow(100);
    w.recordSent(60);
    const before = [w.canSend(40), w.canSend(41)];
    w.recordAck(30);
    expect({ before, after: [w.canSend(70), w.canSend(71)], inFlight: w.inFlight }).toEqual({
      before: [true, false],
      after: [true, false],
      inFlight: 30,
    });
  });

  test.each([
    ["going backwards", 50, 40],
    ["acking more than was sent", 50, 61],
  ])("refuses an ack %s", (_name, first, second) => {
    const w = new CreditWindow(100);
    w.recordSent(60);
    w.recordAck(first);
    expect({ ok: w.recordAck(second), acked: w.acked }).toEqual({ ok: false, acked: first });
  });

  test("never lets in-flight bytes exceed the window under any send/ack sequence", () => {
    const w = new CreditWindow(64);
    let maxInFlight = 0;
    let seed = 7;
    const rnd = (n: number) => (seed = (seed * 1103515245 + 12345) % 2 ** 31) % n;
    for (let i = 0; i < 2000; i++) {
      const n = 1 + rnd(32);
      if (w.canSend(n)) w.recordSent(n);
      else w.recordAck(w.acked + rnd(w.inFlight + 1));
      maxInFlight = Math.max(maxInFlight, w.inFlight);
    }
    expect(maxInFlight <= 64).toEqual(true);
  });
});

describe("chunkRanges", () => {
  test.each([
    [0, 4, []],
    [4, 4, [[0, 4]]],
    [
      10,
      4,
      [
        [0, 4],
        [4, 8],
        [8, 10],
      ],
    ],
  ])("size %d by %d", (size, chunk, ranges) => {
    expect([...chunkRanges(size, chunk)]).toEqual(ranges);
  });

  test("ranges are contiguous and cover the whole size", () => {
    for (const size of [1, 15, 16, 17, 1000]) {
      const r = [...chunkRanges(size, 16)];
      const contiguous = r.every(([s], i) => i === 0 || s === r[i - 1]?.[1]);
      expect({ first: r[0]?.[0], last: r.at(-1)?.[1], contiguous }).toEqual({
        first: 0,
        last: size,
        contiguous: true,
      });
    }
  });
});

describe("RateMeter", () => {
  test("a steady rate is reported exactly and gives the ETA", () => {
    const m = new RateMeter(1000);
    for (let s = 0; s <= 5; s++) m.sample(s * 1_000_000, s * 1000);
    expect({ rate: m.bytesPerSecond, eta: m.eta(3_000_000) }).toEqual({ rate: 1_000_000, eta: 3 });
  });

  test("moves halfway towards a new rate after one half-life", () => {
    const m = new RateMeter(1000);
    m.sample(0, 0);
    m.sample(1_000_000, 1000);
    m.sample(3_000_000, 2000);
    expect(m.bytesPerSecond).toEqual(1_500_000);
  });

  test("ignores samples closer together than the minimum interval", () => {
    const m = new RateMeter(1000);
    m.sample(0, 0);
    m.sample(1_000_000, 1000);
    m.sample(9_000_000, 1010);
    expect(m.bytesPerSecond).toEqual(1_000_000);
  });

  test("no rate and no ETA before two samples", () => {
    const m = new RateMeter(1000);
    m.sample(0, 0);
    expect({ rate: m.bytesPerSecond, eta: m.eta(10) }).toEqual({ rate: null, eta: null });
  });
});

describe("joinPath", () => {
  test.each([
    ["/home/device", "a.txt", "/", "/home/device/a.txt"],
    ["/", "etc", "/", "/etc"],
    ["C:\\", "Users", "\\", "C:\\Users"],
    ["C:\\Users", "bob", "\\", "C:\\Users\\bob"],
  ] as const)("%s + %s", (dir, name, sep, out) => {
    expect(joinPath(dir, name, sep)).toEqual(out);
  });
});

describe("breadcrumbs", () => {
  const roots: Root[] = [
    { name: "Home", path: "/home/device", kind: "home" },
    { name: "Downloads", path: "/home/device/Downloads", kind: "downloads" },
  ];

  test("starts at the deepest root that contains the path", () => {
    expect(breadcrumbs("/home/device/Downloads/a/b", "/", roots)).toEqual([
      { name: "Downloads", path: "/home/device/Downloads" },
      { name: "a", path: "/home/device/Downloads/a" },
      { name: "b", path: "/home/device/Downloads/a/b" },
    ]);
  });

  test("a root itself is one crumb", () => {
    expect(breadcrumbs("/home/device", "/", roots)).toEqual([
      { name: "Home", path: "/home/device" },
    ]);
  });

  test("a root prefix that is not a parent directory does not match", () => {
    expect(breadcrumbs("/home/device2/x", "/", roots)).toEqual([
      { name: "/", path: "/" },
      { name: "home", path: "/home" },
      { name: "device2", path: "/home/device2" },
      { name: "x", path: "/home/device2/x" },
    ]);
  });

  test("Windows paths outside every root start at the drive", () => {
    expect(breadcrumbs("D:\\Media\\Films", "\\", [])).toEqual([
      { name: "D:", path: "D:\\" },
      { name: "Media", path: "D:\\Media" },
      { name: "Films", path: "D:\\Media\\Films" },
    ]);
  });

  test("Windows drive roots match with their trailing separator", () => {
    const drive: Root[] = [{ name: "Local Disk (C:)", path: "C:\\", kind: "drive" }];
    expect(breadcrumbs("C:\\Users", "\\", drive)).toEqual([
      { name: "Local Disk (C:)", path: "C:\\" },
      { name: "Users", path: "C:\\Users" },
    ]);
  });
});

describe("contentDisposition", () => {
  test.each([
    ["report.pdf", `attachment; filename="report.pdf"; filename*=UTF-8''report.pdf`],
    [
      "résumé (1).pdf",
      `attachment; filename="r_sum_ (1).pdf"; filename*=UTF-8''r%C3%A9sum%C3%A9%20%281%29.pdf`,
    ],
    [
      `say "hi"\\*.txt`,
      `attachment; filename="say _hi__*.txt"; filename*=UTF-8''say%20%22hi%22%5C%2A.txt`,
    ],
    ["line\r\nbreak.txt", `attachment; filename="linebreak.txt"; filename*=UTF-8''linebreak.txt`],
    ["", `attachment; filename="download"; filename*=UTF-8''download`],
  ])("%j", (name, header) => {
    expect(contentDisposition(name)).toEqual(header);
  });
});

describe("filesAvailability", () => {
  test.each([
    ["before hello", null, "open", "pending"],
    ["old agent without features", [], "open", "unsupported"],
    ["agent without files", ["stream"], "open", "unsupported"],
    ["files and open channel", ["stream", "files"], "open", "ready"],
    ["files while the channel connects", ["files"], "connecting", "pending"],
    ["channel closed by the agent", ["files"], "closed", "unsupported"],
    ["channel closed before hello", null, "closed", "unsupported"],
  ] as const)("%s", (_name, features, chan, out) => {
    expect(filesAvailability(features, chan)).toEqual(out);
  });
});

describe("describeFileError", () => {
  test.each([
    ["os_denied", /privacy/i],
    ["no_user", /signed in/i],
    ["checksum", /checksum/i],
    ["something_new", /something_new/],
  ])("%s", (code, re) => {
    expect(describeFileError(code)).toMatch(re);
  });
});
