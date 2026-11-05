// Codec of the `term` data channel (docs/PROTOCOL.md §5 "`term`"): binary
// output/input frames per terminal and the JSON requests around them. Pure:
// no DOM, no channel.

import type { Brand } from "../api/types";

export const TERM_OUTPUT = 0x50;
export const TERM_INPUT = 0x51;
export const TERM_MAX_PAYLOAD = 16384;
/** The browser acks rendered output at least this often (bytes). */
export const TERM_ACK_EVERY = 32 * 1024;
export const TERM_MAX_COLS = 1000;
export const TERM_MAX_ROWS = 500;

export type TermId = Brand<number, "TermId">;
export type TermRequestId = Brand<number, "TermRequestId">;
export type TermSourceId = Brand<string, "TermSourceId">;

export type TermSourceKind = "shell" | "tmux" | "screen" | "console" | "app";

/** A terminal the browser can open or take over. */
export type TermSource = {
  id: TermSourceId;
  kind: TermSourceKind;
  title: string;
  detail: string;
  /** Viewers right now, browsers and people at the device. */
  attached: number;
  admin: boolean;
  fidelity: "full" | "mirror";
  /** Unix ms; 0 when unknown. */
  created: number;
};

/** A program a new terminal can start. */
export type TermShell = { id: string; name: string; path: string };

export type TermMsg =
  | {
      t: "sources";
      id: TermRequestId;
      sources: TermSource[];
      shells: TermShell[];
      admin: boolean;
    }
  | { t: "opened"; id: TermRequestId; term: TermId; source: TermSource; window: number }
  | { t: "closed"; term: TermId; reason: string; exitCode: number | null }
  | { t: "title"; term: TermId; title: string }
  | { t: "cwd"; id: TermRequestId; path: string }
  | { t: "error"; id: TermRequestId | null; term: TermId | null; code: string; detail: string };

// ---------- binary frames ----------

export function encodeTermData(
  type: number,
  term: TermId,
  payload: Uint8Array,
): Uint8Array<ArrayBuffer> {
  if (payload.length < 1 || payload.length > TERM_MAX_PAYLOAD) {
    throw new RangeError(`terminal payload must be 1..${TERM_MAX_PAYLOAD} bytes`);
  }
  const buf = new Uint8Array(3 + payload.length);
  buf[0] = type;
  new DataView(buf.buffer).setUint16(1, term, true);
  buf.set(payload, 3);
  return buf;
}

export function decodeTermData(
  type: number,
  msg: Uint8Array,
): { term: TermId; payload: Uint8Array } | null {
  if (msg.length < 4 || msg.length > 3 + TERM_MAX_PAYLOAD || msg[0] !== type) return null;
  const term = new DataView(msg.buffer, msg.byteOffset, msg.byteLength).getUint16(1, true);
  return { term: term as TermId, payload: msg.subarray(3) };
}

/** Input frames for bytes of any length. */
export function inputFrames(term: TermId, bytes: Uint8Array): Uint8Array<ArrayBuffer>[] {
  const out: Uint8Array<ArrayBuffer>[] = [];
  for (let i = 0; i < bytes.length; i += TERM_MAX_PAYLOAD)
    out.push(encodeTermData(TERM_INPUT, term, bytes.subarray(i, i + TERM_MAX_PAYLOAD)));
  return out;
}

// ---------- outgoing JSON ----------

export type TermSize = { cols: number; rows: number };

/** Clamps a size to what the agent accepts. */
export function clampSize(s: TermSize): TermSize {
  const c = (n: number, max: number) => Math.min(max, Math.max(1, Math.floor(n) || 1));
  return { cols: c(s.cols, TERM_MAX_COLS), rows: c(s.rows, TERM_MAX_ROWS) };
}

export const listRequest = (id: TermRequestId) => JSON.stringify({ t: "list", id });

export function openRequest(
  id: TermRequestId,
  size: TermSize,
  shell?: string,
  admin?: boolean,
): string {
  const { cols, rows } = clampSize(size);
  return JSON.stringify({
    t: "open",
    id,
    ...(shell ? { shell } : {}),
    ...(admin ? { admin: true } : {}),
    cols,
    rows,
  });
}

export function attachRequest(id: TermRequestId, source: TermSourceId, size: TermSize): string {
  const { cols, rows } = clampSize(size);
  return JSON.stringify({ t: "attach", id, source, cols, rows });
}

export function resizeMsg(term: TermId, size: TermSize): string {
  const { cols, rows } = clampSize(size);
  return JSON.stringify({ t: "resize", term, cols, rows });
}

export const ackMsg = (term: TermId, bytes: number) => JSON.stringify({ t: "ack", term, bytes });
export const detachMsg = (term: TermId) => JSON.stringify({ t: "detach", term });
export const killMsg = (term: TermId) => JSON.stringify({ t: "kill", term });
/** Asks for the working directory of a terminal's shell. */
export const cwdRequest = (id: TermRequestId, term: TermId) =>
  JSON.stringify({ t: "cwd", id, term });

// ---------- incoming JSON ----------

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const isU = (v: unknown, max = 0xffffffff): v is number =>
  typeof v === "number" && Number.isInteger(v) && v >= 0 && v <= max;
const isStr = (v: unknown): v is string => typeof v === "string";
const KINDS = new Set<string>(["shell", "tmux", "screen", "console", "app"]);

function parseSource(v: unknown): TermSource | null {
  if (!isObj(v) || !isStr(v.id) || !isStr(v.kind) || !KINDS.has(v.kind) || !isStr(v.title))
    return null;
  return {
    id: v.id as TermSourceId,
    kind: v.kind as TermSourceKind,
    title: v.title,
    detail: isStr(v.detail) ? v.detail : "",
    attached: isU(v.attached) ? v.attached : 0,
    admin: v.admin === true,
    fidelity: v.fidelity === "mirror" ? "mirror" : "full",
    created: isU(v.created, Number.MAX_SAFE_INTEGER) ? v.created : 0,
  };
}

function parseShell(v: unknown): TermShell | null {
  if (!isObj(v) || !isStr(v.id) || !isStr(v.name)) return null;
  return { id: v.id, name: v.name, path: isStr(v.path) ? v.path : "" };
}

const present = <T>(xs: (T | null)[]): T[] => xs.filter((x): x is T => x !== null);

/** Parses an agent → browser JSON frame; null for anything malformed. */
export function parseTermMsg(text: string): TermMsg | null {
  let m: unknown;
  try {
    m = JSON.parse(text);
  } catch {
    return null;
  }
  if (!isObj(m)) return null;
  switch (m.t) {
    case "sources":
      if (!isU(m.id) || !Array.isArray(m.sources)) return null;
      return {
        t: "sources",
        id: m.id as TermRequestId,
        sources: present(m.sources.map(parseSource)),
        shells: Array.isArray(m.shells) ? present(m.shells.map(parseShell)) : [],
        admin: m.admin === true,
      };
    case "opened": {
      const source = parseSource(m.source);
      if (!isU(m.id) || !isU(m.term, 0xffff) || !source) return null;
      return {
        t: "opened",
        id: m.id as TermRequestId,
        term: m.term as TermId,
        source,
        window: isU(m.window) ? m.window : 0,
      };
    }
    case "closed":
      if (!isU(m.term, 0xffff) || !isStr(m.reason)) return null;
      return {
        t: "closed",
        term: m.term as TermId,
        reason: m.reason,
        exitCode: typeof m.exit_code === "number" ? m.exit_code : null,
      };
    case "title":
      if (!isU(m.term, 0xffff) || !isStr(m.title)) return null;
      return { t: "title", term: m.term as TermId, title: m.title };
    case "cwd":
      if (!isU(m.id) || !isStr(m.path) || !m.path) return null;
      return { t: "cwd", id: m.id as TermRequestId, path: m.path };
    case "error":
      if (!isStr(m.code)) return null;
      return {
        t: "error",
        id: isU(m.id) ? (m.id as TermRequestId) : null,
        term: isU(m.term, 0xffff) ? (m.term as TermId) : null,
        code: m.code,
        detail: isStr(m.detail) ? m.detail : "",
      };
  }
  return null;
}

// ---------- availability and wording ----------

export type TerminalAvailability = "pending" | "ready" | "unsupported";

/** The agent advertises "terminal" and keeps the channel open; old agents close it. */
export function terminalAvailability(
  features: readonly string[] | null,
  chan: RTCDataChannelState,
): TerminalAvailability {
  if (chan === "closing" || chan === "closed") return "unsupported";
  if (features === null) return "pending";
  if (!features.includes("terminal")) return "unsupported";
  return chan === "open" ? "ready" : "pending";
}

export class TermError extends Error {
  constructor(
    readonly code: string,
    detail: string,
  ) {
    super(detail || code);
  }
}

const ERROR_TEXT: Record<string, string> = {
  not_found: "That terminal is gone.",
  denied: "Not allowed on this device.",
  no_user: "Nobody is signed in on the device.",
  unsupported: "This device can't do that.",
  too_many:
    "Too many terminals: close one here, or end a shell you no longer need in the list (trash button).",
  audit_unavailable:
    "The device can't reach the server's activity log right now, so it won't open terminals.",
  spawn_failed: "The shell didn't start on the device.",
  protocol: "The terminal stopped after a protocol error.",
  session_ended: "The session ended.",
  closed: "The connection closed.",
};

export function describeTermError(code: string): string {
  return ERROR_TEXT[code] ?? `The device refused (${code}).`;
}

/** What the end of a terminal looks like in its window. */
export function closedLine(reason: string, exitCode: number | null): string {
  switch (reason) {
    case "exited":
      return exitCode === null || exitCode === 0
        ? "[Process exited]"
        : exitCode < 0
          ? "[Process ended by a signal]"
          : `[Process exited with code ${exitCode}]`;
    case "killed":
      return "[Process ended]";
    case "detached":
      return "[Detached. The shell keeps running on the device]";
    case "session_ended":
      return "[Disconnected. The shell keeps running on the device]";
  }
  return "[Terminal closed]";
}

/** Short label of a source kind, for badges. */
export function kindLabel(s: Pick<TermSource, "kind" | "fidelity">): string {
  switch (s.kind) {
    case "shell":
      return "Shell";
    case "tmux":
      return "tmux";
    case "screen":
      return "screen";
    case "console":
      return "Console";
    case "app":
      return "Window";
  }
}

// ---------- paths typed into a shell ----------

/** True for a Windows path ("C:\\x", "\\\\server\\share"). */
export const isWindowsPath = (p: string) => /^[A-Za-z]:[\\/]/.test(p) || p.startsWith("\\\\");

/**
 * A path quoted for the shell it is typed into: single quotes for POSIX
 * shells, double quotes for Windows shells (whose paths cannot hold "),
 * nothing when the path needs none.
 */
export function shellQuote(path: string, windows = isWindowsPath(path)): string {
  if (windows) return /^[A-Za-z0-9_.\\:@%+=,-]+$/.test(path) ? path : `"${path}"`;
  return /^[A-Za-z0-9_./:@%+=,-]+$/.test(path) ? path : `'${path.replaceAll("'", `'\\''`)}'`;
}

/** The name to type for an uploaded file: relative to dir when it landed there. */
export function typedPath(path: string, dir: string | null): string {
  if (dir) {
    const windows = isWindowsPath(path);
    const sep = windows ? "\\" : "/";
    const prefix = dir.endsWith(sep) ? dir : dir + sep;
    if (path.startsWith(prefix) && !path.slice(prefix.length).includes(sep))
      return shellQuote(path.slice(prefix.length), windows);
  }
  return shellQuote(path);
}
