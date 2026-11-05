// Browser side of the `term` data channel (docs/PROTOCOL.md §5 "`term`"):
// list / open / attach requests and one Term per terminal, whose output is
// acked as it is rendered so a busy program can't flood the page.

import {
  ackMsg,
  attachRequest,
  decodeTermData,
  detachMsg,
  inputFrames,
  killMsg,
  listRequest,
  openRequest,
  parseTermMsg,
  resizeMsg,
  TERM_ACK_EVERY,
  TERM_OUTPUT,
  TermError,
} from "../lib/terminal";
import type {
  TermId,
  TermMsg,
  TermRequestId,
  TermShell,
  TermSize,
  TermSource,
  TermSourceId,
} from "../lib/terminal";
import type { Chan } from "./FileChannel";

export type SourcesReply = { sources: TermSource[]; shells: TermShell[]; admin: boolean };

const LIST_TIMEOUT_MS = 15_000;
const OPEN_TIMEOUT_MS = 30_000;
const encoder = new TextEncoder();

type Pending = {
  resolve: (m: TermMsg) => void;
  reject: (e: Error) => void;
  timer: ReturnType<typeof setTimeout>;
};

/** How a terminal ended. */
export type TermEnd = { reason: string; exitCode: number | null };

/** One open terminal on this channel. */
export class Term {
  title: string;
  ended: TermEnd | null = null;
  private dataFns = new Set<(b: Uint8Array) => void>();
  private titleFns = new Set<(t: string) => void>();
  private endFns = new Set<(e: TermEnd) => void>();
  /** Output that arrived before anyone listened. */
  private backlog: Uint8Array[] = [];
  private rendered = 0;
  private acked = 0;

  constructor(
    private readonly ch: TerminalChannel,
    readonly id: TermId,
    readonly source: TermSource,
  ) {
    this.title = source.title;
  }

  /** Output; replays what arrived before the first listener. */
  onData(fn: (b: Uint8Array) => void): () => void {
    this.dataFns.add(fn);
    if (this.backlog.length) {
      const pending = this.backlog;
      this.backlog = [];
      for (const b of pending) fn(b);
    }
    return () => this.dataFns.delete(fn);
  }

  onTitle(fn: (t: string) => void): () => void {
    this.titleFns.add(fn);
    return () => this.titleFns.delete(fn);
  }

  onEnd(fn: (e: TermEnd) => void): () => void {
    this.endFns.add(fn);
    if (this.ended) fn(this.ended);
    return () => this.endFns.delete(fn);
  }

  /** Types text or bytes. false: not sent (the channel is not open, or the terminal ended). */
  write(data: string | Uint8Array): boolean {
    if (this.ended) return false;
    const bytes = typeof data === "string" ? encoder.encode(data) : data;
    if (bytes.length === 0) return true;
    return this.ch.sendFrames(inputFrames(this.id, bytes));
  }

  /** Report that n bytes of output were drawn; acks go out every TERM_ACK_EVERY. */
  consumed(n: number) {
    this.rendered += n;
    if (this.rendered - this.acked >= TERM_ACK_EVERY) {
      this.acked = this.rendered;
      this.ch.sendText(ackMsg(this.id, this.acked));
    }
  }

  resize(size: TermSize) {
    if (!this.ended) this.ch.sendText(resizeMsg(this.id, size));
  }

  /** Stop watching; the program keeps running on the device. */
  detach() {
    if (!this.ended) this.ch.sendText(detachMsg(this.id));
  }

  /** End the program. */
  kill() {
    if (!this.ended) this.ch.sendText(killMsg(this.id));
  }

  /** @internal */
  deliver(b: Uint8Array) {
    if (this.dataFns.size === 0) {
      this.backlog.push(b.slice());
      return;
    }
    for (const fn of this.dataFns) fn(b);
  }

  /** @internal */
  setTitle(t: string) {
    this.title = t;
    for (const fn of this.titleFns) fn(t);
  }

  /** @internal */
  end(e: TermEnd) {
    if (this.ended) return;
    this.ended = e;
    for (const fn of this.endFns) fn(e);
  }
}

export class TerminalChannel {
  private next = 0;
  private requests = new Map<number, Pending>();
  private terms = new Map<number, Term>();
  private disposed = false;
  private listeners = new Set<() => void>();

  constructor(private readonly ch: Chan) {
    ch.onmessage = (ev) => this.onMessage(ev.data);
    ch.onclose = () => this.dispose("closed");
  }

  /** Every open terminal, oldest first. */
  get open(): Term[] {
    return [...this.terms.values()];
  }

  /** Called when the set of open terminals changes. */
  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  list(): Promise<SourcesReply> {
    return this.request((id) => listRequest(id), LIST_TIMEOUT_MS).then((m) => {
      if (m.t !== "sources") throw new TermError("protocol", "unexpected reply");
      return { sources: m.sources, shells: m.shells, admin: m.admin };
    });
  }

  /** Starts a shell ("" = the device's default) and opens it. */
  openShell(size: TermSize, shell?: string, admin?: boolean): Promise<Term> {
    return this.opened(this.request((id) => openRequest(id, size, shell, admin), OPEN_TIMEOUT_MS));
  }

  /** Takes over a terminal from `list`. */
  attach(source: TermSourceId, size: TermSize): Promise<Term> {
    return this.opened(this.request((id) => attachRequest(id, source, size), OPEN_TIMEOUT_MS));
  }

  dispose(reason: string) {
    if (this.disposed) return;
    this.disposed = true;
    for (const p of this.requests.values()) {
      clearTimeout(p.timer);
      p.reject(new TermError(reason, ""));
    }
    this.requests.clear();
    for (const t of this.terms.values()) t.end({ reason: "session_ended", exitCode: null });
    this.terms.clear();
    this.emit();
  }

  /** @internal */
  sendFrames(frames: Uint8Array<ArrayBuffer>[]): boolean {
    if (this.disposed || this.ch.readyState !== "open") return false;
    for (const f of frames) this.ch.send(f);
    return true;
  }

  /** @internal */
  sendText(s: string): boolean {
    if (this.disposed || this.ch.readyState !== "open") return false;
    this.ch.send(s);
    return true;
  }

  private async opened(p: Promise<TermMsg>): Promise<Term> {
    const m = await p;
    if (m.t !== "opened") throw new TermError("protocol", "unexpected reply");
    let t = this.terms.get(m.term);
    if (!t) {
      t = new Term(this, m.term, m.source);
      this.terms.set(m.term, t);
      this.emit();
    }
    return t;
  }

  private request(build: (id: TermRequestId) => string, timeoutMs: number): Promise<TermMsg> {
    if (this.disposed || this.ch.readyState !== "open")
      return Promise.reject(new TermError("closed", "The terminal channel is not open."));
    this.next = (this.next + 1) >>> 0;
    const id = this.next as TermRequestId;
    return new Promise<TermMsg>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.requests.delete(id);
        reject(new TermError("timeout", "The device did not answer."));
      }, timeoutMs);
      this.requests.set(id, { resolve, reject, timer });
      this.ch.send(build(id));
    });
  }

  private onMessage(data: unknown) {
    if (data instanceof ArrayBuffer) {
      const d = decodeTermData(TERM_OUTPUT, new Uint8Array(data));
      if (d) this.terms.get(d.term)?.deliver(d.payload);
      return;
    }
    if (typeof data !== "string") return;
    const m = parseTermMsg(data);
    if (!m) return;
    switch (m.t) {
      case "sources":
      case "opened": {
        // Output can follow `opened` at once: register the terminal before
        // the request's promise callbacks run.
        if (m.t === "opened" && !this.terms.has(m.term)) {
          this.terms.set(m.term, new Term(this, m.term, m.source));
          this.emit();
        }
        this.settle(m.id, m);
        break;
      }
      case "title":
        this.terms.get(m.term)?.setTitle(m.title);
        break;
      case "closed": {
        const t = this.terms.get(m.term);
        if (!t) break;
        this.terms.delete(m.term);
        t.end({ reason: m.reason, exitCode: m.exitCode });
        this.emit();
        break;
      }
      case "error":
        if (m.id !== null && this.requests.has(m.id)) {
          const p = this.requests.get(m.id)!;
          this.requests.delete(m.id);
          clearTimeout(p.timer);
          p.reject(new TermError(m.code, m.detail));
        } else if (m.term !== null) {
          const t = this.terms.get(m.term);
          if (t) {
            this.terms.delete(m.term);
            t.end({ reason: m.code, exitCode: null });
            this.emit();
          }
        }
        break;
    }
  }

  private settle(id: TermRequestId, m: TermMsg) {
    const p = this.requests.get(id);
    if (!p) return;
    this.requests.delete(id);
    clearTimeout(p.timer);
    p.resolve(m);
  }

  private emit() {
    for (const fn of this.listeners) fn();
  }
}
