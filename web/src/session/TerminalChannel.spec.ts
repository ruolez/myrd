import { describe, expect, test } from "vitest";
import {
  decodeTermData,
  encodeTermData,
  TERM_ACK_EVERY,
  TERM_INPUT,
  TERM_OUTPUT,
} from "../lib/terminal";
import type { TermId, TermSourceId } from "../lib/terminal";
import type { Chan } from "./FileChannel";
import { TerminalChannel } from "./TerminalChannel";

class FakeChan implements Chan {
  readyState: RTCDataChannelState = "open";
  bufferedAmount = 0;
  bufferedAmountLowThreshold = 0;
  onbufferedamountlow: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: Event) => void) | null = null;
  sent: (string | Uint8Array)[] = [];

  send(data: string | Uint8Array<ArrayBuffer>) {
    this.sent.push(data);
  }
  json(): Record<string, unknown>[] {
    return this.sent.filter((s): s is string => typeof s === "string").map((s) => JSON.parse(s));
  }
  binary(): Uint8Array[] {
    return this.sent.filter((s): s is Uint8Array => typeof s !== "string");
  }
  deliver(msg: object) {
    this.onmessage?.({ data: JSON.stringify(msg) } as MessageEvent);
  }
  output(term: number, text: string) {
    const b = encodeTermData(TERM_OUTPUT, term as TermId, new TextEncoder().encode(text));
    this.onmessage?.({ data: b.buffer } as MessageEvent);
  }
}

const shellSource = { id: "shell:1", kind: "shell", title: "zsh", attached: 1, fidelity: "full" };
const size = { cols: 80, rows: 24 };
const flush = () => new Promise((r) => setTimeout(r, 0));

async function opened(ch: FakeChan, tc: TerminalChannel, term = 5) {
  const p = tc.openShell(size);
  const req = ch.json().at(-1)!;
  ch.deliver({ t: "opened", id: req.id, term, source: shellSource, window: 262144 });
  return p;
}

describe("TerminalChannel", () => {
  test("list resolves with the parsed sources", async () => {
    const ch = new FakeChan();
    const tc = new TerminalChannel(ch);
    const p = tc.list();
    expect(ch.json()).toEqual([{ t: "list", id: 1 }]);
    ch.deliver({ t: "sources", id: 1, sources: [shellSource], shells: [], admin: true });
    expect(await p).toEqual({
      sources: [{ ...shellSource, detail: "", admin: false, created: 0 }],
      shells: [],
      admin: true,
    });
  });

  test("output that arrives right after opened is not lost", async () => {
    const ch = new FakeChan();
    const tc = new TerminalChannel(ch);
    const p = tc.openShell(size);
    ch.deliver({ t: "opened", id: 1, term: 5, source: shellSource, window: 262144 });
    ch.output(5, "$ ");
    const t = await p;
    const got: string[] = [];
    t.onData((b) => got.push(new TextDecoder().decode(b)));
    ch.output(5, "ls");
    expect(got).toEqual(["$ ", "ls"]);
  });

  test("write sends input frames for this terminal", async () => {
    const ch = new FakeChan();
    const t = await opened(ch, new TerminalChannel(ch));
    expect(t.write("ls\r")).toBe(true);
    const d = decodeTermData(TERM_INPUT, ch.binary()[0]!);
    expect(d && { term: d.term, text: new TextDecoder().decode(d.payload) }).toEqual({
      term: 5,
      text: "ls\r",
    });
  });

  test("acks once TERM_ACK_EVERY bytes were rendered, cumulatively", async () => {
    const ch = new FakeChan();
    const t = await opened(ch, new TerminalChannel(ch));
    t.consumed(TERM_ACK_EVERY - 1);
    expect(ch.json().filter((m) => m.t === "ack")).toEqual([]);
    t.consumed(1);
    t.consumed(TERM_ACK_EVERY);
    expect(ch.json().filter((m) => m.t === "ack")).toEqual([
      { t: "ack", term: 5, bytes: TERM_ACK_EVERY },
      { t: "ack", term: 5, bytes: 2 * TERM_ACK_EVERY },
    ]);
  });

  test("closed ends the terminal with its reason and exit code", async () => {
    const ch = new FakeChan();
    const tc = new TerminalChannel(ch);
    const t = await opened(ch, tc);
    const ends: unknown[] = [];
    t.onEnd((e) => ends.push(e));
    ch.deliver({ t: "closed", term: 5, reason: "exited", exit_code: 1 });
    expect(ends).toEqual([{ reason: "exited", exitCode: 1 }]);
    expect(tc.open).toEqual([]);
    expect(t.write("x")).toBe(false);
  });

  test("an error for a request rejects it with the code", async () => {
    const ch = new FakeChan();
    const tc = new TerminalChannel(ch);
    const p = tc.attach("tmux:gone" as TermSourceId, size);
    ch.deliver({ t: "error", id: 1, code: "not_found", detail: "no such terminal" });
    await expect(p).rejects.toMatchObject({ code: "not_found" });
  });

  test("closing the channel ends every terminal and rejects requests", async () => {
    const ch = new FakeChan();
    const tc = new TerminalChannel(ch);
    const t = await opened(ch, tc);
    const p = tc.list();
    ch.readyState = "closed";
    ch.onclose?.({} as Event);
    await expect(p).rejects.toMatchObject({ code: "closed" });
    expect(t.ended).toEqual({ reason: "session_ended", exitCode: null });
    await expect(tc.list()).rejects.toMatchObject({ code: "closed" });
  });

  test("title updates reach listeners", async () => {
    const ch = new FakeChan();
    const t = await opened(ch, new TerminalChannel(ch));
    const titles: string[] = [];
    t.onTitle((x) => titles.push(x));
    ch.deliver({ t: "title", term: 5, title: "vim notes.txt" });
    await flush();
    expect([titles, t.title]).toEqual([["vim notes.txt"], "vim notes.txt"]);
  });
});
