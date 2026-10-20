import { describe, expect, test, vi } from "vitest";
import { vectors } from "../test/vectors";
import { decodeFileData, encodeFileData, FileError, FILE_WINDOW } from "../lib/files";
import type { TransferId } from "../lib/files";
import { BUFFER_HIGH, FileChannel } from "./FileChannel";
import type { Chan, DownloadSink, FileSource } from "./FileChannel";

const HELLO_SHA = vectors.file_sha256[0]?.sha256 ?? "";
const KiB = 1024;

class FakeChan implements Chan {
  sent: (string | Uint8Array)[] = [];
  readyState: RTCDataChannelState = "open";
  bufferedAmount = 0;
  bufferedAmountLowThreshold = 0;
  onbufferedamountlow: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: Event) => void) | null = null;

  send(data: string | Uint8Array<ArrayBuffer>) {
    this.sent.push(typeof data === "string" ? data : data.slice());
  }

  /** JSON messages sent so far. */
  json(): Record<string, unknown>[] {
    return this.sent
      .filter((s): s is string => typeof s === "string")
      .map((s) => JSON.parse(s) as Record<string, unknown>);
  }

  /** Payload bytes of the 0x40 frames sent so far. */
  dataBytes(): number {
    return this.sent
      .filter((s): s is Uint8Array => typeof s !== "string")
      .reduce((n, f) => n + (decodeFileData(f)?.payload.length ?? 0), 0);
  }

  dataPayload(): Uint8Array {
    const parts = this.sent
      .filter((s): s is Uint8Array => typeof s !== "string")
      .map((f) => decodeFileData(f)?.payload ?? new Uint8Array());
    const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
    let off = 0;
    for (const p of parts) {
      out.set(p, off);
      off += p.length;
    }
    return out;
  }

  deliver(msg: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(msg) } as MessageEvent);
  }

  deliverData(xfer: number, payload: Uint8Array) {
    const frame = encodeFileData(xfer as TransferId, payload);
    this.onmessage?.({ data: frame.slice().buffer } as MessageEvent);
  }

  drain() {
    this.bufferedAmount = 0;
    this.onbufferedamountlow?.(new Event("bufferedamountlow"));
  }

  close() {
    this.readyState = "closed";
    this.onclose?.(new Event("close"));
  }
}

class FakeSink implements DownloadSink {
  readonly kind = "memory" as const;
  chunks: Uint8Array[] = [];
  closed = false;
  aborted: string | null = null;
  constructor(readonly ready?: Promise<void>) {}
  async write(chunk: Uint8Array) {
    this.chunks.push(chunk.slice());
  }
  async close() {
    this.closed = true;
  }
  abort(reason: string) {
    this.aborted = reason;
  }
  text() {
    return this.chunks.map((c) => new TextDecoder().decode(c)).join("");
  }
}

const memFile = (name: string, bytes: Uint8Array): FileSource => ({
  name,
  size: bytes.length,
  slice: (s, e) => ({ arrayBuffer: async () => bytes.slice(s, e).buffer }),
});

const text = (s: string) => new TextEncoder().encode(s);
const settle = async (rounds = 20) => {
  for (let i = 0; i < rounds; i++) await new Promise((r) => setTimeout(r, 0));
};

function setup() {
  const chan = new FakeChan();
  const fc = new FileChannel(chan, { now: () => 0 });
  return { chan, fc };
}

describe("FileChannel requests", () => {
  test("roots() resolves with the agent's reply", async () => {
    const { chan, fc } = setup();
    const p = fc.roots();
    const reply = {
      t: "roots",
      id: 1,
      roots: [{ name: "Downloads", path: "/home/device/Downloads", kind: "downloads" }],
      default_dir: "/home/device/Downloads",
      sep: "/",
    };
    chan.deliver(reply);
    expect({ sent: chan.json(), reply: await p }).toEqual({
      sent: [{ t: "roots", id: 1 }],
      reply,
    });
  });

  test("list() rejects with the agent's error code", async () => {
    const { chan, fc } = setup();
    const p = fc.list("/Users/me/Desktop", 0, true);
    chan.deliver({ t: "error", id: 1, code: "os_denied", detail: "TCC" });
    await expect(p).rejects.toEqual(new FileError("os_denied", "TCC"));
    expect(chan.json()).toEqual([
      { t: "list", id: 1, path: "/Users/me/Desktop", offset: 0, hidden: true },
    ]);
  });

  test("request ids and transfer ids come from one counter", () => {
    const { chan, fc } = setup();
    void fc.roots().catch(() => undefined);
    const xfer = fc.upload(memFile("a.txt", text("a")), null);
    void fc.list("/").catch(() => undefined);
    expect({ xfer, ids: chan.json().map((m) => m.id ?? m.xfer) }).toEqual({
      xfer: 2,
      ids: [1, 2, 3],
    });
  });
});

describe("FileChannel uploads", () => {
  test("streams the file and ends with the SHA-256 of the shared vector", async () => {
    const { chan, fc } = setup();
    const xfer = fc.upload(memFile("a.txt", text("hello")), null);
    chan.deliver({
      t: "ready",
      xfer,
      name: "a (1).txt",
      path: "/d/a (1).txt",
      window: FILE_WINDOW,
    });
    await vi.waitFor(() => expect(chan.json().at(-1)?.t).toEqual("end"));
    chan.deliver({ t: "ack", xfer, bytes: 5 });
    chan.deliver({
      t: "done",
      xfer,
      name: "a (1).txt",
      path: "/d/a (1).txt",
      size: 5,
      sha256: HELLO_SHA,
    });
    expect({
      json: chan.json(),
      payload: new TextDecoder().decode(chan.dataPayload()),
      transfer: fc.getTransfers()[0],
    }).toEqual({
      json: [
        { t: "put", xfer, dir: "", name: "a.txt", size: 5 },
        { t: "end", xfer, sha256: HELLO_SHA },
      ],
      payload: "hello",
      transfer: expect.objectContaining({
        status: "done",
        name: "a (1).txt",
        path: "/d/a (1).txt",
        bytes: 5,
      }),
    });
  });

  test("never sends more than the advertised window beyond the last ack", async () => {
    const { chan, fc } = setup();
    const window = 64 * KiB;
    const xfer = fc.upload(memFile("big.bin", new Uint8Array(200 * KiB)), "/dst");
    chan.deliver({ t: "ready", xfer, name: "big.bin", path: "/dst/big.bin", window });
    await vi.waitFor(() => expect(chan.dataBytes()).toEqual(window));
    await settle();
    const stalled = chan.dataBytes();
    chan.deliver({ t: "ack", xfer, bytes: 32 * KiB });
    await vi.waitFor(() => expect(chan.dataBytes()).toEqual(96 * KiB));
    await settle();
    expect({ stalled, afterAck: chan.dataBytes() }).toEqual({
      stalled: window,
      afterAck: 96 * KiB,
    });
  });

  test("waits while the channel buffers more than 4 MiB", async () => {
    const { chan, fc } = setup();
    chan.bufferedAmount = BUFFER_HIGH + 1;
    const xfer = fc.upload(memFile("a.bin", new Uint8Array(40 * KiB)), null);
    chan.deliver({ t: "ready", xfer, name: "a.bin", path: "/d/a.bin", window: FILE_WINDOW });
    await settle();
    const whileFull = chan.dataBytes();
    chan.drain();
    await vi.waitFor(() => expect(chan.dataBytes()).toEqual(40 * KiB));
    expect(whileFull).toEqual(0);
  });

  test("an ack beyond what was sent is a protocol error", async () => {
    const { chan, fc } = setup();
    const xfer = fc.upload(memFile("a.txt", text("hello")), null);
    chan.deliver({ t: "ready", xfer, name: "a.txt", path: "/d/a.txt", window: FILE_WINDOW });
    await vi.waitFor(() => expect(chan.json().at(-1)?.t).toEqual("end"));
    chan.deliver({ t: "ack", xfer, bytes: 6 });
    expect({ last: chan.json().at(-1), error: fc.getTransfers()[0]?.error }).toEqual({
      last: { t: "cancel", xfer },
      error: { code: "protocol", detail: "" },
    });
  });

  test("runs one upload at a time", async () => {
    const { chan, fc } = setup();
    const a = fc.upload(memFile("a.txt", text("a")), null);
    fc.upload(memFile("b.txt", text("b")), null);
    const putsBefore = chan.json().filter((m) => m.t === "put").length;
    chan.deliver({ t: "error", xfer: a, code: "no_space", detail: "disk full" });
    await vi.waitFor(() => expect(chan.json().filter((m) => m.t === "put").length).toEqual(2));
    expect({ putsBefore, first: fc.getTransfers()[0]?.error }).toEqual({
      putsBefore: 1,
      first: { code: "no_space", detail: "disk full" },
    });
  });

  test("cancel() tells the agent and marks the transfer cancelled", async () => {
    const { chan, fc } = setup();
    const xfer = fc.upload(memFile("big.bin", new Uint8Array(200 * KiB)), null);
    chan.deliver({ t: "ready", xfer, name: "big.bin", path: "/d/big.bin", window: 32 * KiB });
    await vi.waitFor(() => expect(chan.dataBytes()).toEqual(32 * KiB));
    fc.cancel(xfer);
    chan.deliver({ t: "ack", xfer, bytes: 32 * KiB });
    await settle();
    expect({
      last: chan.json().at(-1),
      status: fc.getTransfers()[0]?.status,
      sent: chan.dataBytes(),
    }).toEqual({ last: { t: "cancel", xfer }, status: "cancelled", sent: 32 * KiB });
  });
});

describe("FileChannel downloads", () => {
  const entry = { path: "/home/device/notes.txt", name: "notes.txt", size: 5 };

  async function begin(chan: FakeChan, fc: FileChannel, sink: FakeSink, window = FILE_WINDOW) {
    const xfer = fc.download(entry, sink);
    await vi.waitFor(() =>
      expect(chan.json().at(-1)).toEqual({ t: "get", xfer, path: entry.path }),
    );
    chan.deliver({ t: "begin", xfer, name: "notes.txt", size: 5, mtime: 0, window });
    return xfer;
  }

  test("writes every chunk, verifies the checksum and acks at the end", async () => {
    const { chan, fc } = setup();
    const sink = new FakeSink();
    const xfer = await begin(chan, fc, sink);
    chan.deliverData(xfer, text("hel"));
    chan.deliverData(xfer, text("lo"));
    chan.deliver({ t: "end", xfer, size: 5, sha256: HELLO_SHA });
    await vi.waitFor(() => expect(fc.getTransfers()[0]?.status).toEqual("done"));
    expect({
      text: sink.text(),
      closed: sink.closed,
      aborted: sink.aborted,
      last: chan.json().at(-1),
    }).toEqual({ text: "hello", closed: true, aborted: null, last: { t: "ack", xfer, bytes: 5 } });
  });

  test("aborts the sink when the checksum does not match", async () => {
    const { chan, fc } = setup();
    const sink = new FakeSink();
    const xfer = await begin(chan, fc, sink);
    chan.deliverData(xfer, text("hellO"));
    chan.deliver({ t: "end", xfer, size: 5, sha256: HELLO_SHA });
    await vi.waitFor(() => expect(fc.getTransfers()[0]?.status).toEqual("failed"));
    expect({
      closed: sink.closed,
      aborted: sink.aborted,
      last: chan.json().at(-1),
      error: fc.getTransfers()[0]?.error,
    }).toEqual({
      closed: false,
      aborted: "checksum",
      last: { t: "cancel", xfer },
      error: { code: "checksum", detail: "" },
    });
  });

  test("acks every 256 KiB after the sink has written", async () => {
    const { chan, fc } = setup();
    const sink = new FakeSink();
    const xfer = await begin(chan, fc, sink);
    for (let i = 0; i < 20; i++) chan.deliverData(xfer, new Uint8Array(16 * KiB));
    await vi.waitFor(() => expect(sink.chunks.length).toEqual(20));
    expect(chan.json().filter((m) => m.t === "ack")).toEqual([
      { t: "ack", xfer, bytes: 256 * KiB },
    ]);
  });

  test("data beyond the window is a protocol error", async () => {
    const { chan, fc } = setup();
    const sink = new FakeSink();
    const xfer = await begin(chan, fc, sink, 16 * KiB);
    chan.deliverData(xfer, new Uint8Array(16 * KiB));
    chan.deliverData(xfer, new Uint8Array(1));
    expect({
      error: fc.getTransfers()[0]?.error,
      aborted: sink.aborted,
      last: chan.json().at(-1),
    }).toEqual({
      error: { code: "protocol", detail: "" },
      aborted: "protocol",
      last: { t: "cancel", xfer },
    });
  });

  test("a sink whose save dialog was dismissed never sends a get", async () => {
    const { chan, fc } = setup();
    const sink = new FakeSink(Promise.reject(new Error("The user aborted a request.")));
    fc.download(entry, sink);
    await vi.waitFor(() => expect(fc.getTransfers()[0]?.status).toEqual("cancelled"));
    expect(chan.json()).toEqual([]);
  });

  test("an agent error aborts the sink", async () => {
    const { chan, fc } = setup();
    const sink = new FakeSink();
    const xfer = fc.download(entry, sink);
    await vi.waitFor(() => expect(chan.json().length).toEqual(1));
    chan.deliver({ t: "error", xfer, code: "not_file", detail: "" });
    expect({ aborted: sink.aborted, status: fc.getTransfers()[0]?.status }).toEqual({
      aborted: "not_file",
      status: "failed",
    });
  });
});

describe("FileChannel closing", () => {
  test("an old agent closing the channel fails requests and transfers as closed", async () => {
    const { chan, fc } = setup();
    const roots = fc.roots();
    fc.upload(memFile("a.txt", text("a")), null);
    chan.close();
    await expect(roots).rejects.toEqual(new FileError("closed"));
    expect({
      transfer: fc.getTransfers()[0]?.error,
      active: fc.activeCount,
      later: await fc.list("/").catch((e: FileError) => e.code),
    }).toEqual({ transfer: { code: "closed", detail: "" }, active: 0, later: "closed" });
  });

  test("dispose() cancels running transfers on the agent", async () => {
    const { chan, fc } = setup();
    const sink = new FakeSink();
    const xfer = fc.download({ path: "/x", name: "x", size: 1 }, sink);
    await vi.waitFor(() => expect(chan.json().length).toEqual(1));
    fc.dispose("session_ended");
    expect({ last: chan.json().at(-1), aborted: sink.aborted }).toEqual({
      last: { t: "cancel", xfer },
      aborted: "session_ended",
    });
  });

  test("subscribers are notified of status changes", () => {
    const { fc } = setup();
    const seen: string[] = [];
    fc.subscribe(() =>
      seen.push(
        fc
          .getTransfers()
          .map((t) => t.status)
          .join(","),
      ),
    );
    fc.upload(memFile("a.txt", text("a")), null);
    expect(seen).toEqual(["queued", "waiting"]);
  });
});
