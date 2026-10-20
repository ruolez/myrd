// Where downloads from the device are written, best first:
// 1. File System Access save picker (Chromium), streamed straight to disk.
// 2. Service-worker streamed download (public/dl-sw.js), pull-based backpressure.
// 3. An in-memory Blob, saved when complete (large files may exhaust memory).

import { contentDisposition } from "../lib/files";
import type { DownloadSink, SinkKind } from "./FileChannel";

type SaveFilePicker = (opts: { suggestedName?: string }) => Promise<FileSystemFileHandle>;

const SW_URL = "/dl-sw.js";
const SW_SCOPE = "/__dl/";
const KEEPALIVE_MS = 10_000;
const BATCH_BYTES = 256 * 1024;

const picker = () =>
  (window as Window & { showSaveFilePicker?: SaveFilePicker }).showSaveFilePicker;

/**
 * Must be called synchronously from the click handler: the save picker needs
 * the user activation of that click.
 */
export function createDownloadSink(name: string): DownloadSink {
  const show = picker();
  if (typeof show === "function") return fsAccessSink(show, name);
  if (canStreamDownloads()) return new StreamSink(name);
  return new BlobSink(name);
}

/** Registers the download worker ahead of the first click so streaming starts at once. */
export function warmDownloadWorker() {
  if (typeof picker() !== "function" && canStreamDownloads()) {
    void registerWorker().catch(() => undefined);
  }
}

function canStreamDownloads() {
  return window.isSecureContext && "serviceWorker" in navigator;
}

function fsAccessSink(show: SaveFilePicker, name: string): DownloadSink {
  let writable: FileSystemWritableFileStream | null = null;
  const ready = show.call(window, { suggestedName: name }).then(async (handle) => {
    writable = await handle.createWritable();
  });
  ready.catch(() => undefined);
  const w = () => {
    if (!writable) throw new Error("The save location is not ready.");
    return writable;
  };
  return {
    kind: "fs",
    ready,
    write: (chunk) => w().write(chunk as Uint8Array<ArrayBuffer>),
    close: () => w().close(),
    abort: (reason) => void writable?.abort(reason).catch(() => undefined),
  };
}

let registration: Promise<ServiceWorker> | null = null;

function registerWorker(): Promise<ServiceWorker> {
  registration ??= navigator.serviceWorker
    .register(SW_URL, { scope: SW_SCOPE })
    .then((reg) => activeWorker(reg))
    .catch((e: unknown) => {
      registration = null;
      throw e;
    });
  return registration;
}

// navigator.serviceWorker.ready never settles here: this page is outside the scope.
function activeWorker(reg: ServiceWorkerRegistration): Promise<ServiceWorker> {
  if (reg.active) return Promise.resolve(reg.active);
  const sw = reg.installing ?? reg.waiting;
  if (!sw) return Promise.reject(new Error("The download worker did not install."));
  return new Promise((resolve, reject) => {
    sw.addEventListener("statechange", () => {
      if (sw.state === "activated") resolve(sw);
      else if (sw.state === "redundant") reject(new Error("The download worker was replaced."));
    });
  });
}

/** Streams through public/dl-sw.js; falls back to memory if the worker cannot start. */
class StreamSink implements DownloadSink {
  readonly ready: Promise<void>;
  private port: MessagePort | null = null;
  private credits = 0;
  private wake: (() => void) | null = null;
  private failed: string | null = null;
  private batch: Uint8Array[] = [];
  private batched = 0;
  private iframe: HTMLIFrameElement | null = null;
  private keepalive: ReturnType<typeof setInterval> | null = null;
  private fallback: BlobSink | null = null;

  constructor(private readonly name: string) {
    this.ready = this.open().catch(() => {
      this.cleanup(0);
      this.fallback = new BlobSink(name);
    });
  }

  get kind(): SinkKind {
    return this.fallback ? "memory" : "stream";
  }

  private async open() {
    const worker = await registerWorker();
    const id = crypto.randomUUID();
    const { port1, port2 } = new MessageChannel();
    this.port = port1;
    const registered = new Promise<void>((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error("The download worker did not answer.")),
        5000,
      );
      port1.onmessage = (ev: MessageEvent<{ type?: string; reason?: string }>) => {
        const t = ev.data?.type;
        if (t === "registered") {
          clearTimeout(timer);
          resolve();
        } else if (t === "pull") {
          this.credits++;
          this.notify();
        } else if (t === "cancel") {
          this.failed = ev.data.reason ?? "cancelled";
          this.notify();
        }
      };
    });
    worker.postMessage({ type: "register", id, disposition: contentDisposition(this.name) }, [
      port2,
    ]);
    await registered;
    const iframe = document.createElement("iframe");
    iframe.hidden = true;
    iframe.src = `${SW_SCOPE}${id}/${encodeURIComponent(this.name)}`;
    document.body.appendChild(iframe);
    this.iframe = iframe;
    this.keepalive = setInterval(() => worker.postMessage({ type: "keepalive" }), KEEPALIVE_MS);
  }

  async write(chunk: Uint8Array) {
    if (this.fallback) return this.fallback.write(chunk);
    this.batch.push(chunk.slice());
    this.batched += chunk.length;
    if (this.batched >= BATCH_BYTES) await this.flush();
  }

  async close() {
    if (this.fallback) return this.fallback.close();
    await this.flush();
    this.port?.postMessage({ type: "end" });
    this.cleanup(KEEPALIVE_MS);
  }

  abort(reason: string) {
    if (this.fallback) return this.fallback.abort();
    this.failed ??= reason;
    this.port?.postMessage({ type: "abort", reason });
    this.notify();
    this.cleanup(KEEPALIVE_MS);
  }

  private async flush() {
    if (this.batched === 0) return;
    while (this.credits === 0 && !this.failed) {
      await new Promise<void>((r) => (this.wake = r));
    }
    if (this.failed) throw new Error(`The browser cancelled the download (${this.failed}).`);
    this.credits--;
    const out = new Uint8Array(this.batched);
    let off = 0;
    for (const c of this.batch) {
      out.set(c, off);
      off += c.length;
    }
    this.batch = [];
    this.batched = 0;
    this.port?.postMessage({ type: "chunk", chunk: out.buffer }, [out.buffer]);
  }

  private notify() {
    const w = this.wake;
    this.wake = null;
    w?.();
  }

  private cleanup(delayMs: number) {
    if (this.keepalive) clearInterval(this.keepalive);
    this.keepalive = null;
    const iframe = this.iframe;
    const port = this.port;
    this.iframe = null;
    // Leave the iframe in place a little so the browser finishes the download.
    setTimeout(() => {
      iframe?.remove();
      port?.close();
    }, delayMs);
  }
}

class BlobSink implements DownloadSink {
  readonly kind = "memory" as const;
  private parts: Uint8Array<ArrayBuffer>[] = [];

  constructor(private readonly name: string) {}

  async write(chunk: Uint8Array) {
    this.parts.push(chunk.slice());
  }

  async close() {
    const url = URL.createObjectURL(new Blob(this.parts, { type: "application/octet-stream" }));
    this.parts = [];
    const a = document.createElement("a");
    a.href = url;
    a.download = this.name;
    a.hidden = true;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  }

  abort() {
    this.parts = [];
  }
}
