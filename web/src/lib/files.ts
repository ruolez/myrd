// File channel codec and helpers (docs/PROTOCOL.md §5 "`file`"). Pure: no DOM.

import { sha256 } from "@noble/hashes/sha2.js";
import { bytesToHex } from "@noble/hashes/utils.js";
import type { Brand } from "../api/types";

export type TransferId = Brand<number, "TransferId">;
export type FileRequestId = Brand<number, "FileRequestId">;

export const FILE_DATA = 0x40;
export const MAX_FILE_PAYLOAD = 16384;
export const FILE_WINDOW = 4 * 1024 * 1024;
export const ACK_EVERY = 256 * 1024;
export const SLICE_BYTES = 256 * 1024;

export type Sep = "/" | "\\";
export type RootKind = "home" | "downloads" | "desktop" | "documents" | "volume" | "drive";
export type Root = { name: string; path: string; kind: RootKind };
export type EntryKind = "dir" | "file" | "other";
export type FileEntry = {
  name: string;
  kind: EntryKind;
  link: boolean;
  size: number;
  mtime: number;
  hidden: boolean;
};

export type RootsReply = {
  t: "roots";
  id: FileRequestId;
  roots: Root[];
  default_dir: string;
  sep: Sep;
};
export type Listing = {
  t: "listing";
  id: FileRequestId;
  path: string;
  parent: string;
  entries: FileEntry[];
  offset: number;
  next_offset: number | null;
  total: number;
  truncated: boolean;
};
export type FileErrorMsg = { t: "error"; code: string; detail: string } & (
  { id: FileRequestId } | { xfer: TransferId }
);

/** Messages the agent sends on the `file` channel. */
export type FileMsg =
  | RootsReply
  | Listing
  | { t: "begin"; xfer: TransferId; name: string; size: number; mtime: number; window: number }
  | { t: "ready"; xfer: TransferId; name: string; path: string; window: number }
  | { t: "ack"; xfer: TransferId; bytes: number }
  | { t: "end"; xfer: TransferId; size: number; sha256: string }
  | { t: "done"; xfer: TransferId; name: string; path: string; size: number; sha256: string }
  | { t: "cancel"; xfer: TransferId }
  | FileErrorMsg;

export class FileError extends Error {
  constructor(
    readonly code: string,
    readonly detail = "",
  ) {
    super(detail ? `${code}: ${detail}` : code);
    this.name = "FileError";
  }
}

export function encodeFileData(xfer: TransferId, payload: Uint8Array): Uint8Array {
  if (payload.length < 1 || payload.length > MAX_FILE_PAYLOAD) {
    throw new RangeError(`file payload must be 1..${MAX_FILE_PAYLOAD} bytes`);
  }
  const buf = new Uint8Array(5 + payload.length);
  buf[0] = FILE_DATA;
  new DataView(buf.buffer).setUint32(1, xfer >>> 0, true);
  buf.set(payload, 5);
  return buf;
}

export function decodeFileData(msg: Uint8Array): { xfer: TransferId; payload: Uint8Array } | null {
  if (msg.length < 6 || msg.length > 5 + MAX_FILE_PAYLOAD || msg[0] !== FILE_DATA) return null;
  const xfer = new DataView(msg.buffer, msg.byteOffset, msg.byteLength).getUint32(1, true);
  return { xfer: xfer as TransferId, payload: msg.subarray(5) };
}

// ---------- outgoing (browser → agent) ----------

export const rootsRequest = (id: FileRequestId) => JSON.stringify({ t: "roots", id });
export const listRequest = (id: FileRequestId, path: string, offset: number, hidden: boolean) =>
  JSON.stringify({ t: "list", id, path, offset, hidden });
export const getRequest = (xfer: TransferId, path: string) =>
  JSON.stringify({ t: "get", xfer, path });
export const putRequest = (xfer: TransferId, dir: string, name: string, size: number) =>
  JSON.stringify({ t: "put", xfer, dir, name, size });
export const ackMsg = (xfer: TransferId, bytes: number) =>
  JSON.stringify({ t: "ack", xfer, bytes });
export const endMsg = (xfer: TransferId, sha: string) =>
  JSON.stringify({ t: "end", xfer, sha256: sha });
export const cancelMsg = (xfer: TransferId) => JSON.stringify({ t: "cancel", xfer });

// ---------- incoming (agent → browser) ----------

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const isU = (v: unknown): v is number => Number.isInteger(v) && (v as number) >= 0;
const isStr = (v: unknown): v is string => typeof v === "string";
const ROOT_KINDS: readonly string[] = [
  "home",
  "downloads",
  "desktop",
  "documents",
  "volume",
  "drive",
];
const ENTRY_KINDS: readonly string[] = ["dir", "file", "other"];

function parseEntry(e: unknown): FileEntry | null {
  if (!isObj(e) || !isStr(e.name) || !ENTRY_KINDS.includes(e.kind as string)) return null;
  if (!isU(e.size) || !Number.isFinite(e.mtime)) return null;
  return {
    name: e.name,
    kind: e.kind as EntryKind,
    link: e.link === true,
    size: e.size,
    mtime: e.mtime as number,
    hidden: e.hidden === true,
  };
}

function parseRoot(r: unknown): Root | null {
  if (!isObj(r) || !isStr(r.name) || !isStr(r.path) || !ROOT_KINDS.includes(r.kind as string)) {
    return null;
  }
  return { name: r.name, path: r.path, kind: r.kind as RootKind };
}

function all<T>(items: unknown, parse: (v: unknown) => T | null): T[] | null {
  if (!Array.isArray(items)) return null;
  const out: T[] = [];
  for (const it of items) {
    const p = parse(it);
    if (!p) return null;
    out.push(p);
  }
  return out;
}

/** Validates one JSON text frame from the agent; null when malformed or not A→B. */
export function parseFileMsg(text: string): FileMsg | null {
  let m: unknown;
  try {
    m = JSON.parse(text);
  } catch {
    return null;
  }
  if (!isObj(m)) return null;
  const xfer = m.xfer as TransferId;
  const id = m.id as FileRequestId;
  switch (m.t) {
    case "roots": {
      const roots = all(m.roots, parseRoot);
      if (!isU(id) || !roots || !isStr(m.default_dir) || (m.sep !== "/" && m.sep !== "\\")) {
        return null;
      }
      return { t: "roots", id, roots, default_dir: m.default_dir, sep: m.sep };
    }
    case "listing": {
      const entries = all(m.entries, parseEntry);
      const next = m.next_offset ?? null;
      if (!isU(id) || !entries || !isStr(m.path) || !isStr(m.parent)) return null;
      if (!isU(m.offset) || !isU(m.total) || (next !== null && !isU(next))) return null;
      return {
        t: "listing",
        id,
        path: m.path,
        parent: m.parent,
        entries,
        offset: m.offset,
        next_offset: next,
        total: m.total,
        truncated: m.truncated === true,
      };
    }
    case "begin":
      if (!isU(xfer) || !isStr(m.name) || !isU(m.size) || !isU(m.window)) return null;
      if (!Number.isFinite(m.mtime)) return null;
      return {
        t: "begin",
        xfer,
        name: m.name,
        size: m.size,
        mtime: m.mtime as number,
        window: m.window,
      };
    case "ready":
      if (!isU(xfer) || !isStr(m.name) || !isStr(m.path) || !isU(m.window)) return null;
      return { t: "ready", xfer, name: m.name, path: m.path, window: m.window };
    case "ack":
      if (!isU(xfer) || !isU(m.bytes)) return null;
      return { t: "ack", xfer, bytes: m.bytes };
    case "end":
      if (!isU(xfer) || !isU(m.size) || !isStr(m.sha256)) return null;
      return { t: "end", xfer, size: m.size, sha256: m.sha256 };
    case "done":
      if (!isU(xfer) || !isStr(m.name) || !isStr(m.path) || !isU(m.size) || !isStr(m.sha256)) {
        return null;
      }
      return { t: "done", xfer, name: m.name, path: m.path, size: m.size, sha256: m.sha256 };
    case "cancel":
      return isU(xfer) ? { t: "cancel", xfer } : null;
    case "error": {
      if (!isStr(m.code)) return null;
      const detail = isStr(m.detail) ? m.detail : "";
      if (isU(xfer)) return { t: "error", xfer, code: m.code, detail };
      if (isU(id)) return { t: "error", id, code: m.code, detail };
      return null;
    }
    default:
      return null;
  }
}

// ---------- flow control, hashing, progress ----------

export function createSha256() {
  const h = sha256.create();
  return {
    update(chunk: Uint8Array) {
      h.update(chunk);
    },
    hex: () => bytesToHex(h.digest()),
  };
}

/**
 * Credit accounting for one transfer direction: at most `window` bytes beyond
 * the receiver's last cumulative ack. Receivers mirror it to detect overruns.
 */
export class CreditWindow {
  sent = 0;
  acked = 0;

  constructor(readonly window: number) {}

  get inFlight() {
    return this.sent - this.acked;
  }

  canSend(n: number) {
    return this.inFlight + n <= this.window;
  }

  recordSent(n: number) {
    this.sent += n;
  }

  /** Cumulative ack; false (and ignored) when it goes backwards or past what was sent. */
  recordAck(bytes: number): boolean {
    if (bytes < this.acked || bytes > this.sent) return false;
    this.acked = bytes;
    return true;
  }
}

/** [start, end) byte ranges of at most `chunk` bytes covering `size`. */
export function* chunkRanges(size: number, chunk: number): Generator<[number, number]> {
  for (let start = 0; start < size; start += chunk) yield [start, Math.min(size, start + chunk)];
}

/** Exponentially weighted transfer speed (bytes/s) with a configurable half-life. */
export class RateMeter {
  private last: { bytes: number; t: number } | null = null;
  private rate: number | null = null;

  constructor(
    private readonly halfLifeMs = 2000,
    private readonly minIntervalMs = 200,
  ) {}

  sample(totalBytes: number, nowMs: number) {
    if (!this.last) {
      this.last = { bytes: totalBytes, t: nowMs };
      return;
    }
    const dt = nowMs - this.last.t;
    if (dt < this.minIntervalMs) return;
    const inst = ((totalBytes - this.last.bytes) * 1000) / dt;
    const alpha = 1 - 0.5 ** (dt / this.halfLifeMs);
    this.rate = this.rate === null ? inst : this.rate + alpha * (inst - this.rate);
    this.last = { bytes: totalBytes, t: nowMs };
  }

  get bytesPerSecond(): number | null {
    return this.rate;
  }

  /** Seconds left for `remaining` bytes at the current rate. */
  eta(remaining: number): number | null {
    return this.rate && this.rate > 0 ? remaining / this.rate : null;
  }
}

// ---------- paths ----------

export const joinPath = (dir: string, name: string, sep: Sep) =>
  dir.endsWith(sep) ? dir + name : dir + sep + name;

const within = (path: string, base: string, sep: Sep) =>
  path === base || path.startsWith(base.endsWith(sep) ? base : base + sep);

export type Crumb = { name: string; path: string };

/** Clickable path segments, starting at the deepest root containing `path`. */
export function breadcrumbs(path: string, sep: Sep, roots: readonly Root[]): Crumb[] {
  const root = roots
    .filter((r) => within(path, r.path, sep))
    .sort((a, b) => b.path.length - a.path.length)[0];
  let first: Crumb;
  if (root) first = { name: root.name, path: root.path };
  else if (sep === "\\") first = { name: path.slice(0, 2), path: path.slice(0, 2) + "\\" };
  else first = { name: "/", path: "/" };
  const out = [first];
  let cur = first.path;
  for (const seg of path.slice(first.path.length).split(sep)) {
    if (!seg) continue;
    cur = joinPath(cur, seg, sep);
    out.push({ name: seg, path: cur });
  }
  return out;
}

// ---------- download headers ----------

const stripControls = (s: string) =>
  [...s].filter((c) => (c.codePointAt(0) ?? 0) >= 0x20 && c !== "\u007f").join("");

/** `Content-Disposition: attachment` with an ASCII fallback and RFC 5987 `filename*`. */
export function contentDisposition(name: string): string {
  const clean = stripControls(name) || "download";
  const ascii = [...clean]
    .map((c) => ((c.codePointAt(0) ?? 0) > 0x7e || c === '"' || c === "\\" || c === "%" ? "_" : c))
    .join("");
  const star = encodeURIComponent(clean).replace(
    /['()*]/g,
    (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`,
  );
  return `attachment; filename="${ascii}"; filename*=UTF-8''${star}`;
}

// ---------- feature detection and messages ----------

export type FilesAvailability = "pending" | "ready" | "unsupported";

/** Old agents send no `features` and close the `file` channel; both mean unsupported. */
export function filesAvailability(
  features: readonly string[] | null,
  chan: RTCDataChannelState,
): FilesAvailability {
  if (chan === "closing" || chan === "closed") return "unsupported";
  if (features === null) return "pending";
  if (!features.includes("files")) return "unsupported";
  return chan === "open" ? "ready" : "pending";
}

const ERROR_TEXT: Record<string, string> = {
  not_found: "Not found on the device.",
  denied: "Access to this location is not allowed.",
  os_denied:
    "The device's privacy settings block this folder. On a Mac, allow the RemoteAgent app under System Settings › Privacy & Security › Files and Folders.",
  not_dir: "This is not a folder.",
  not_file: "Only regular files can be downloaded.",
  bad_name: "The file name is not allowed on the device.",
  bad_path: "The path is not allowed on the device.",
  no_space: "The device is out of disk space.",
  io: "The device could not read or write the file.",
  changed: "The file changed on the device while it was being read.",
  checksum: "The checksum did not match; the file was discarded.",
  protocol: "The transfer failed (protocol error).",
  too_many: "The device is busy with other transfers. Try again shortly.",
  no_user: "Nobody is signed in on the device. Files are available once a user signs in.",
  audit_unavailable: "The device cannot record transfers right now, so it refused this one.",
  stalled: "The transfer stalled and was cancelled.",
  cancelled: "Cancelled.",
  closed: "The session's file channel closed.",
  unsupported: "This device's agent does not support file transfer.",
};

export function describeFileError(code: string): string {
  return ERROR_TEXT[code] ?? `The transfer failed (${code}).`;
}
