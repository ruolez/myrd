// Browser side of the `file` data channel (docs/PROTOCOL.md §5 "`file`"):
// browsing requests plus streamed, credit-window flow-controlled transfers.

import {
  ACK_EVERY,
  ackMsg,
  cancelMsg,
  chunkRanges,
  createSha256,
  CreditWindow,
  decodeFileData,
  deleteRequest,
  encodeFileData,
  endMsg,
  FileError,
  getRequest,
  listRequest,
  MAX_FILE_PAYLOAD,
  mkdirRequest,
  parseFileMsg,
  putRequest,
  RateMeter,
  renameRequest,
  rootsRequest,
  SLICE_BYTES,
} from "../lib/files";
import type { Brand } from "../api/types";
import type {
  FileMsg,
  FileRequestId,
  Listing,
  OkReply,
  RootsReply,
  TransferId,
} from "../lib/files";

/** The subset of RTCDataChannel the file channel uses (fakeable in tests). */
export type Chan = {
  send(data: string | Uint8Array<ArrayBuffer>): void;
  readonly readyState: RTCDataChannelState;
  readonly bufferedAmount: number;
  bufferedAmountLowThreshold: number;
  onbufferedamountlow: ((ev: Event) => void) | null;
  onmessage: ((ev: MessageEvent) => void) | null;
  onclose: ((ev: Event) => void) | null;
};

/** The subset of File an upload reads. */
export type FileSource = {
  readonly name: string;
  readonly size: number;
  slice(start: number, end: number): { arrayBuffer(): Promise<ArrayBuffer> };
};

export type SinkKind = "fs" | "stream" | "memory";

/** Where a download is written. `ready` settles before the `get` is sent. */
export type DownloadSink = {
  readonly kind: SinkKind;
  readonly ready?: Promise<void>;
  write(chunk: Uint8Array): Promise<void>;
  close(): Promise<void>;
  abort(reason: string): void;
};

export type TransferStatus =
  "queued" | "waiting" | "active" | "verifying" | "done" | "failed" | "cancelled";

export type BatchId = Brand<number, "BatchId">;

export type Transfer = {
  xfer: TransferId;
  batch: BatchId | null;
  direction: "upload" | "download";
  name: string;
  /** Remote path: the source of a download, the destination of an upload once known. */
  path: string | null;
  size: number;
  bytes: number;
  status: TransferStatus;
  error: { code: string; detail: string } | null;
  speed: number | null;
  eta: number | null;
  sink: SinkKind | null;
};

export type BatchPhase = "scanning" | "asking" | "running" | "failed" | "cancelled";
export type BatchProblem = { name: string; code: string };

/** One user action (a selection, a drop) that may expand into many transfers. */
export type Batch = {
  id: BatchId;
  direction: Transfer["direction"];
  /** What was picked: one name, or "3 items". */
  name: string;
  /** Where it goes, for display. */
  dest: string;
  phase: BatchPhase;
  /** Files and bytes planned once scanning is done. */
  files: number;
  bytes: number;
  skipped: number;
  /** Failures outside transfers: folders that could not be read or created. */
  problems: readonly BatchProblem[];
};

/** Finished: given up, or running with every transfer final. */
export const batchFinished = (b: Batch, transfers: readonly Transfer[]) =>
  b.phase === "failed" ||
  b.phase === "cancelled" ||
  (b.phase === "running" && transfers.every((t) => t.batch !== b.id || isFinal(t)));

export const BUFFER_HIGH = 4 * 1024 * 1024;
/** Transfers running at once per direction; the agent allows 4 in all. */
export const PER_DIRECTION = 2;
/** Requests in flight at once; the agent allows 2. */
export const MAX_REQUESTS = 2;
export const BUFFER_LOW = 1024 * 1024;
const EMIT_EVERY_MS = 150;

const FINAL: readonly TransferStatus[] = ["done", "failed", "cancelled"];
export const isFinal = (t: Transfer) => FINAL.includes(t.status);

type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void };

function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  promise.catch(() => undefined);
  return { promise, resolve, reject };
}

type Job = {
  xfer: TransferId;
  started: boolean;
  aborted: boolean;
  credits: CreditWindow | null;
  hasher: ReturnType<typeof createSha256>;
  meter: RateMeter;
  /** Upload: resolved by `ready`. Download: resolved by `begin`. */
  opened: Deferred<FileMsg>;
  /** Upload: resolved by `done`. Download: resolved after `end` is verified. */
  finished: Deferred<void>;
  file?: FileSource;
  dir?: string | null;
  replace?: boolean;
  source?: string;
  sink?: DownloadSink;
  received: number;
  written: number;
  chain: Promise<void>;
};

type Options = { now?: () => number };

export class FileChannel {
  private seq = 0;
  private batchSeq = 0;
  private requests = new Map<number, Deferred<FileMsg>>();
  private waitingRequests: { build: (id: FileRequestId) => string; d: Deferred<FileMsg> }[] = [];
  private jobs = new Map<number, Job>();
  private queues = { upload: [] as Job[], download: [] as Job[] };
  private active = { upload: new Set<Job>(), download: new Set<Job>() };
  private waiters = new Set<() => void>();
  /** Transfers by id in insertion order; `getTransfers` snapshots it when read after a change. */
  private store = new Map<TransferId, Transfer>();
  private snapshot: readonly Transfer[] = [];
  private dirty = false;
  private batches: readonly Batch[] = [];
  private listeners = new Set<() => void>();
  private emitTimer: ReturnType<typeof setTimeout> | null = null;
  private lastEmit = 0;
  private disposed = false;
  private readonly now: () => number;

  constructor(
    private readonly chan: Chan,
    opts: Options = {},
  ) {
    this.now = opts.now ?? (() => Date.now());
    chan.bufferedAmountLowThreshold = BUFFER_LOW;
    chan.onbufferedamountlow = () => this.wake();
    chan.onmessage = (ev) => this.onMessage(ev.data);
    chan.onclose = () => this.dispose("closed");
  }

  // ---------- store (useSyncExternalStore) ----------

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  getTransfers = (): readonly Transfer[] => {
    if (this.dirty) {
      this.snapshot = [...this.store.values()];
      this.dirty = false;
    }
    return this.snapshot;
  };

  getBatches = (): readonly Batch[] => this.batches;

  get activeCount() {
    return this.getTransfers().filter((t) => !isFinal(t)).length;
  }

  /** Removes finished batches and transfers from the list. */
  clearFinished() {
    this.batches = this.batches.filter((b) => !batchFinished(b, this.getTransfers()));
    const kept = new Set(this.batches.map((b) => b.id));
    for (const t of this.getTransfers()) {
      if (isFinal(t) && (t.batch === null || !kept.has(t.batch))) this.store.delete(t.xfer);
    }
    this.dirty = true;
    this.emit(true);
  }

  // ---------- batches ----------

  createBatch(init: Pick<Batch, "direction" | "name" | "dest">): BatchId {
    this.batchSeq++;
    const id = this.batchSeq as BatchId;
    this.batches = [
      ...this.batches,
      { ...init, id, phase: "scanning", files: 0, bytes: 0, skipped: 0, problems: [] },
    ];
    this.emit(true);
    return id;
  }

  updateBatch(id: BatchId, patch: Partial<Omit<Batch, "id">>) {
    this.batches = this.batches.map((b) => (b.id === id ? { ...b, ...patch } : b));
    this.emit(true);
  }

  batch(id: BatchId): Batch | undefined {
    return this.batches.find((b) => b.id === id);
  }

  /** Stops a batch: no more planning, and its running transfers are cancelled. */
  cancelBatch(id: BatchId) {
    const b = this.batch(id);
    if (!b || batchFinished(b, this.getTransfers())) return;
    this.updateBatch(id, { phase: "cancelled" });
    for (const t of this.getTransfers()) if (t.batch === id) this.cancel(t.xfer);
  }

  // ---------- browsing ----------

  roots(): Promise<RootsReply> {
    return this.request((id) => rootsRequest(id)) as Promise<RootsReply>;
  }

  list(path: string, offset = 0, hidden = false): Promise<Listing> {
    return this.request((id) => listRequest(id, path, offset, hidden)) as Promise<Listing>;
  }

  // ---------- file operations (feature "file_ops") ----------

  mkdir(dir: string, name: string, existOk = false): Promise<OkReply> {
    return this.request((id) => mkdirRequest(id, dir, name, existOk)) as Promise<OkReply>;
  }

  rename(path: string, name: string): Promise<OkReply> {
    return this.request((id) => renameRequest(id, path, name)) as Promise<OkReply>;
  }

  /** Deletes a file, link or folder (with everything in it) on the device, permanently. */
  remove(path: string): Promise<OkReply> {
    return this.request((id) => deleteRequest(id, path)) as Promise<OkReply>;
  }

  // ---------- transfers ----------

  /**
   * Queues an upload into `dir` (null = the agent's default, the user's Downloads).
   * `replace` lets it replace a file of the same name (agents with "file_ops").
   */
  upload(
    file: FileSource,
    dir: string | null,
    opts: { replace?: boolean; batch?: BatchId } = {},
  ): TransferId {
    const job = this.newJob();
    job.file = file;
    job.dir = dir;
    job.replace = opts.replace ?? false;
    return this.enqueue(job, {
      direction: "upload",
      batch: opts.batch ?? null,
      name: file.name,
      path: null,
      size: file.size,
      sink: null,
    });
  }

  /** Queues a download of the regular file at `path` into `sink`. */
  download(
    entry: { path: string; name: string; size: number },
    sink: DownloadSink,
    opts: { batch?: BatchId } = {},
  ): TransferId {
    const job = this.newJob();
    job.source = entry.path;
    job.sink = sink;
    return this.enqueue(job, {
      direction: "download",
      batch: opts.batch ?? null,
      name: entry.name,
      path: entry.path,
      size: entry.size,
      sink: sink.kind,
    });
  }

  cancel(xfer: TransferId) {
    const job = this.jobs.get(xfer);
    const t = this.find(xfer);
    if (!job || !t || isFinal(t)) return;
    if (job.started && this.chan.readyState === "open") this.chan.send(cancelMsg(xfer));
    this.stop(job, "cancelled", new FileError("cancelled"));
  }

  /** Fails everything still running and detaches from the channel. */
  dispose(reason = "closed") {
    if (this.disposed) return;
    this.disposed = true;
    const err = new FileError(reason);
    for (const r of this.requests.values()) r.reject(err);
    for (const r of this.waitingRequests) r.d.reject(err);
    this.requests.clear();
    this.waitingRequests = [];
    for (const job of this.jobs.values()) {
      const t = this.find(job.xfer);
      if (!t || isFinal(t)) continue;
      if (job.started && this.chan.readyState === "open") this.chan.send(cancelMsg(job.xfer));
      this.stop(job, "failed", err);
    }
    this.queues.upload = [];
    this.queues.download = [];
    this.chan.onmessage = null;
    this.chan.onbufferedamountlow = null;
    this.chan.onclose = null;
    if (this.emitTimer) clearTimeout(this.emitTimer);
    this.emitTimer = null;
    this.emit(true);
  }

  // ---------- internals ----------

  private next(): number {
    this.seq = (this.seq + 1) >>> 0 || 1;
    return this.seq;
  }

  private request(build: (id: FileRequestId) => string): Promise<FileMsg> {
    if (this.disposed || this.chan.readyState !== "open") {
      return Promise.reject(new FileError(this.disposed ? "closed" : "unsupported"));
    }
    const d = deferred<FileMsg>();
    this.waitingRequests.push({ build, d });
    this.sendRequests();
    return d.promise;
  }

  private sendRequests() {
    while (this.requests.size < MAX_REQUESTS && !this.disposed) {
      const r = this.waitingRequests.shift();
      if (!r) return;
      const id = this.next();
      this.requests.set(id, r.d);
      this.chan.send(r.build(id as FileRequestId));
    }
  }

  private newJob(): Job {
    return {
      xfer: this.next() as TransferId,
      started: false,
      aborted: false,
      credits: null,
      hasher: createSha256(),
      meter: new RateMeter(),
      opened: deferred<FileMsg>(),
      finished: deferred<void>(),
      received: 0,
      written: 0,
      chain: Promise.resolve(),
    };
  }

  private enqueue(
    job: Job,
    t: Pick<Transfer, "direction" | "batch" | "name" | "path" | "size" | "sink">,
  ): TransferId {
    this.jobs.set(job.xfer, job);
    const transfer: Transfer = {
      ...t,
      xfer: job.xfer,
      bytes: 0,
      status: "queued",
      error: null,
      speed: null,
      eta: null,
    };
    this.store.set(job.xfer, transfer);
    this.dirty = true;
    this.emit(true);
    if (this.disposed) {
      this.stop(job, "failed", new FileError("closed"));
    } else {
      this.queues[t.direction].push(job);
      this.pump();
    }
    return job.xfer;
  }

  private pump() {
    for (const dir of ["upload", "download"] as const) {
      while (this.active[dir].size < PER_DIRECTION && !this.disposed) {
        const job = this.queues[dir].shift();
        if (!job) break;
        this.active[dir].add(job);
        const run = dir === "upload" ? this.runUpload(job) : this.runDownload(job);
        void run
          .catch((e: unknown) => this.fail(job, e))
          .finally(() => {
            this.active[dir].delete(job);
            this.pump();
          });
      }
    }
  }

  private async runUpload(job: Job) {
    const file = job.file as FileSource;
    job.started = true;
    this.patch(job.xfer, { status: "waiting" }, true);
    this.chan.send(putRequest(job.xfer, job.dir ?? "", file.name, file.size, job.replace));
    const ready = (await job.opened.promise) as Extract<FileMsg, { t: "ready" }>;
    job.credits = new CreditWindow(ready.window);
    this.patch(job.xfer, { status: "active", name: ready.name, path: ready.path }, true);
    job.meter.sample(0, this.now());

    for (const [start, end] of chunkRanges(file.size, SLICE_BYTES)) {
      const slice = new Uint8Array(await file.slice(start, end).arrayBuffer());
      for (let off = 0; off < slice.length; off += MAX_FILE_PAYLOAD) {
        const chunk = slice.subarray(off, off + MAX_FILE_PAYLOAD);
        const credits = job.credits;
        await this.until(
          job,
          () => credits.canSend(chunk.length) && this.chan.bufferedAmount <= BUFFER_HIGH,
        );
        this.chan.send(encodeFileData(job.xfer, chunk) as Uint8Array<ArrayBuffer>);
        credits.recordSent(chunk.length);
        job.hasher.update(chunk);
      }
    }
    this.assertRunning(job);
    this.chan.send(endMsg(job.xfer, job.hasher.hex()));
    this.patch(job.xfer, { status: "verifying" }, true);
    await job.finished.promise;
  }

  private async runDownload(job: Job) {
    const sink = job.sink as DownloadSink;
    this.patch(job.xfer, { status: "waiting" }, true);
    if (sink.ready) {
      try {
        await sink.ready;
      } catch (e) {
        // A dismissed save dialog is a cancel; a sink may say what went wrong.
        if (e instanceof FileError) throw e;
        throw new FileError("cancelled", e instanceof Error ? e.message : "");
      }
      this.patch(job.xfer, { sink: sink.kind }, true);
    }
    this.assertRunning(job);
    job.started = true;
    this.chan.send(getRequest(job.xfer, job.source as string));
    const begin = (await job.opened.promise) as Extract<FileMsg, { t: "begin" }>;
    job.credits = new CreditWindow(begin.window);
    this.patch(job.xfer, { status: "active", name: begin.name, size: begin.size }, true);
    job.meter.sample(0, this.now());
    await job.finished.promise;
  }

  private onMessage(data: unknown) {
    if (this.disposed) return;
    if (data instanceof ArrayBuffer) {
      const frame = decodeFileData(new Uint8Array(data));
      const job = frame && this.jobs.get(frame.xfer);
      if (frame && job) this.onData(job, frame.payload);
      return;
    }
    if (typeof data !== "string") return;
    const m = parseFileMsg(data);
    if (!m) return;
    if (m.t === "roots" || m.t === "listing" || m.t === "ok" || (m.t === "error" && "id" in m)) {
      const req = this.requests.get(m.id);
      if (!req) return;
      this.requests.delete(m.id);
      if (m.t === "error") req.reject(new FileError(m.code, m.detail));
      else req.resolve(m);
      this.sendRequests();
      return;
    }
    const job = this.jobs.get(m.xfer);
    const t = this.find(m.xfer);
    if (!job || !t || isFinal(t)) return;
    switch (m.t) {
      case "ready":
      case "begin":
        job.opened.resolve(m);
        break;
      case "ack":
        if (!job.credits || !job.credits.recordAck(m.bytes)) {
          this.protocolError(job);
          return;
        }
        this.progress(job, m.bytes);
        this.wake();
        break;
      case "end":
        job.chain = job.chain.then(() => this.verifyDownload(job, m.size, m.sha256));
        break;
      case "done":
        this.patch(job.xfer, { status: "done", name: m.name, path: m.path, bytes: m.size }, true);
        job.finished.resolve();
        break;
      case "cancel":
        this.stop(job, "cancelled", new FileError("cancelled"));
        break;
      case "error":
        this.stop(job, "failed", new FileError(m.code, m.detail));
        break;
    }
  }

  private onData(job: Job, payload: Uint8Array) {
    const t = this.find(job.xfer);
    if (!t || isFinal(t) || job.aborted) return;
    const credits = job.credits;
    if (!credits || !job.sink || !credits.canSend(payload.length)) {
      this.protocolError(job);
      return;
    }
    credits.recordSent(payload.length);
    job.received += payload.length;
    job.hasher.update(payload);
    const sink = job.sink;
    job.chain = job.chain.then(async () => {
      if (job.aborted) return;
      try {
        await sink.write(payload);
      } catch (e) {
        this.writeFailed(job, e);
        return;
      }
      job.written += payload.length;
      if (job.written - credits.acked >= ACK_EVERY) this.sendAck(job);
      this.progress(job, job.written);
    });
  }

  private async verifyDownload(job: Job, size: number, sha: string) {
    if (job.aborted) return;
    const sink = job.sink as DownloadSink;
    if (job.received !== size || job.hasher.hex() !== sha) {
      this.chan.send(cancelMsg(job.xfer));
      this.stop(job, "failed", new FileError("checksum"));
      return;
    }
    this.sendAck(job, true);
    this.patch(job.xfer, { status: "verifying" }, true);
    try {
      await sink.close();
    } catch (e) {
      this.stop(job, "failed", new FileError("io", e instanceof Error ? e.message : ""));
      return;
    }
    this.patch(job.xfer, { status: "done", bytes: job.written, speed: null, eta: 0 }, true);
    job.finished.resolve();
  }

  /** Cumulative ack after writing; `final` also acks an empty file so the agent sees one at the end. */
  private sendAck(job: Job, final = false) {
    if (!job.credits) return;
    const unchanged = job.written === job.credits.acked;
    if (unchanged && !(final && job.written === 0)) return;
    job.credits.recordAck(job.written);
    if (this.chan.readyState === "open") this.chan.send(ackMsg(job.xfer, job.written));
  }

  private writeFailed(job: Job, e: unknown) {
    if (this.chan.readyState === "open") this.chan.send(cancelMsg(job.xfer));
    const detail = e instanceof Error ? e.message : String(e);
    this.stop(job, "failed", new FileError("io", detail));
  }

  private protocolError(job: Job) {
    if (this.chan.readyState === "open") this.chan.send(cancelMsg(job.xfer));
    this.stop(job, "failed", new FileError("protocol"));
  }

  private fail(job: Job, e: unknown) {
    const t = this.find(job.xfer);
    if (!t || isFinal(t)) return;
    const err = e instanceof FileError ? e : new FileError("io", String(e));
    this.stop(job, err.code === "cancelled" ? "cancelled" : "failed", err);
  }

  /** Moves a job to a final state, aborting its sink and releasing any waiter. */
  private stop(job: Job, status: "failed" | "cancelled", err: FileError) {
    const t = this.find(job.xfer);
    if (!t || isFinal(t)) return;
    job.aborted = true;
    this.queues.upload = this.queues.upload.filter((j) => j !== job);
    this.queues.download = this.queues.download.filter((j) => j !== job);
    job.sink?.abort(err.code);
    this.patch(
      job.xfer,
      { status, error: { code: err.code, detail: err.detail }, speed: null, eta: null },
      true,
    );
    job.opened.reject(err);
    job.finished.reject(err);
    this.wake();
  }

  private assertRunning(job: Job) {
    if (job.aborted || this.disposed) throw new FileError("cancelled");
  }

  private async until(job: Job, cond: () => boolean) {
    while (!cond()) {
      this.assertRunning(job);
      await new Promise<void>((r) => this.waiters.add(r));
    }
    this.assertRunning(job);
  }

  private wake() {
    const ws = [...this.waiters];
    this.waiters.clear();
    for (const w of ws) w();
  }

  private progress(job: Job, bytes: number) {
    const t = this.find(job.xfer);
    if (!t) return;
    job.meter.sample(bytes, this.now());
    const speed = job.meter.bytesPerSecond;
    this.patch(job.xfer, { bytes, speed, eta: job.meter.eta(Math.max(0, t.size - bytes)) });
  }

  private find(xfer: TransferId) {
    return this.store.get(xfer);
  }

  private patch(xfer: TransferId, p: Partial<Transfer>, force = false) {
    const t = this.store.get(xfer);
    if (!t) return;
    this.store.set(xfer, { ...t, ...p });
    this.dirty = true;
    this.emit(force);
  }

  private emit(force: boolean) {
    const now = this.now();
    if (!force && now - this.lastEmit < EMIT_EVERY_MS) {
      this.emitTimer ??= setTimeout(() => {
        this.emitTimer = null;
        this.emit(true);
      }, EMIT_EVERY_MS);
      return;
    }
    this.lastEmit = now;
    for (const l of this.listeners) l();
  }
}
