// Binary input/cursor protocol (docs/PROTOCOL.md §5). Little-endian, first byte = type.

export const MsgType = {
  MouseMove: 0x01,
  MouseButton: 0x02,
  MouseWheel: 0x03,
  Key: 0x04,
  Text: 0x05,
  MouseMoveRel: 0x06,
  ClipboardBegin: 0x10,
  ClipboardData: 0x11,
  Control: 0x20,
  CursorShape: 0x30,
  CursorPos: 0x31,
} as const;

export const ControlOp = {
  SelectMonitor: 1,
  RequestKeyframe: 2,
  SetQuality: 3,
  ReleaseAll: 4,
} as const;

export const Quality = { Background: 0, Balanced: 1, High: 2 } as const;
export type QualityTier = (typeof Quality)[keyof typeof Quality];

export const Button = { Left: 0, Right: 1, Middle: 2, Back: 3, Forward: 4 } as const;

export const Mime = { Text: 1, Png: 2 } as const;

export const MAX_TEXT_BYTES = 1024;
export const MAX_CLIPBOARD_BYTES = 1024 * 1024;
export const CLIPBOARD_CHUNK_BYTES = 16384;

const clampU16 = (v: number) => Math.max(0, Math.min(65535, Math.round(v)));
const clampI16 = (v: number) => Math.max(-32768, Math.min(32767, Math.round(v)));

function frame(size: number, type: number): [Uint8Array, DataView] {
  const buf = new Uint8Array(size);
  const view = new DataView(buf.buffer);
  view.setUint8(0, type);
  return [buf, view];
}

export function encodeMouseMove(x: number, y: number, monitor: number): Uint8Array {
  const [buf, v] = frame(6, MsgType.MouseMove);
  v.setUint16(1, clampU16(x), true);
  v.setUint16(3, clampU16(y), true);
  v.setUint8(5, monitor & 0xff);
  return buf;
}

export function encodeMouseMoveRel(dx: number, dy: number): Uint8Array {
  const [buf, v] = frame(5, MsgType.MouseMoveRel);
  v.setInt16(1, clampI16(dx), true);
  v.setInt16(3, clampI16(dy), true);
  return buf;
}

export function encodeMouseButton(button: number, down: boolean): Uint8Array {
  const [buf, v] = frame(3, MsgType.MouseButton);
  v.setUint8(1, button & 0xff);
  v.setUint8(2, down ? 1 : 0);
  return buf;
}

export function encodeMouseWheel(dx: number, dy: number): Uint8Array {
  const [buf, v] = frame(5, MsgType.MouseWheel);
  v.setInt16(1, clampI16(dx), true);
  v.setInt16(3, clampI16(dy), true);
  return buf;
}

export function encodeKey(usage: number, down: boolean, mods: number): Uint8Array {
  const [buf, v] = frame(5, MsgType.Key);
  v.setUint16(1, usage & 0xffff, true);
  v.setUint8(3, down ? 1 : 0);
  v.setUint8(4, mods & 0xff);
  return buf;
}

const utf8 = new TextEncoder();

/** Splits text into Text messages of at most MAX_TEXT_BYTES each, never splitting a code point. */
export function encodeText(text: string): Uint8Array[] {
  const out: Uint8Array[] = [];
  let pending = "";
  let pendingBytes = 0;
  const flush = () => {
    if (!pendingBytes) return;
    const body = utf8.encode(pending);
    const buf = new Uint8Array(body.length + 1);
    buf[0] = MsgType.Text;
    buf.set(body, 1);
    out.push(buf);
    pending = "";
    pendingBytes = 0;
  };
  for (const ch of text) {
    const n = utf8.encode(ch).length;
    if (pendingBytes + n > MAX_TEXT_BYTES) flush();
    pending += ch;
    pendingBytes += n;
  }
  flush();
  return out;
}

export function encodeControl(op: number, arg = 0): Uint8Array {
  const [buf, v] = frame(3, MsgType.Control);
  v.setUint8(1, op & 0xff);
  v.setUint8(2, arg & 0xff);
  return buf;
}

export function encodeClipboardBegin(xfer: number, mime: number, total: number): Uint8Array {
  const [buf, v] = frame(8, MsgType.ClipboardBegin);
  v.setUint16(1, xfer & 0xffff, true);
  v.setUint8(3, mime);
  v.setUint32(4, total, true);
  return buf;
}

export function encodeClipboardData(xfer: number, bytes: Uint8Array): Uint8Array {
  const buf = new Uint8Array(3 + bytes.length);
  const v = new DataView(buf.buffer);
  v.setUint8(0, MsgType.ClipboardData);
  v.setUint16(1, xfer & 0xffff, true);
  buf.set(bytes, 3);
  return buf;
}

/** Full clipboard transfer: one Begin then Data chunks of ≤ 16 KiB. Throws over 1 MiB. */
export function encodeClipboard(xfer: number, mime: number, bytes: Uint8Array): Uint8Array[] {
  if (bytes.length > MAX_CLIPBOARD_BYTES) throw new Error("clipboard over 1 MiB");
  const out = [encodeClipboardBegin(xfer, mime, bytes.length)];
  for (let off = 0; off < bytes.length; off += CLIPBOARD_CHUNK_BYTES) {
    out.push(encodeClipboardData(xfer, bytes.subarray(off, off + CLIPBOARD_CHUNK_BYTES)));
  }
  return out;
}

/** Reassembles clipboard frames arriving on the control channel. */
export class ClipboardAssembler {
  private xfer = -1;
  private mime = 0;
  private buf: Uint8Array | null = null;
  private got = 0;

  /** Returns a completed {mime, bytes} or null. Invalid frames reset the assembler. */
  push(msg: Uint8Array): { mime: number; bytes: Uint8Array } | null {
    if (msg.length < 1) return null;
    const v = new DataView(msg.buffer, msg.byteOffset, msg.byteLength);
    const type = msg[0];
    if (type === MsgType.ClipboardBegin) {
      if (msg.length !== 8) return this.reset();
      const total = v.getUint32(4, true);
      if (total > MAX_CLIPBOARD_BYTES) return this.reset();
      this.xfer = v.getUint16(1, true);
      this.mime = v.getUint8(3);
      this.buf = new Uint8Array(total);
      this.got = 0;
      return total === 0 ? this.finish() : null;
    }
    if (type === MsgType.ClipboardData) {
      if (msg.length < 4 || !this.buf || v.getUint16(1, true) !== this.xfer) return this.reset();
      const chunk = msg.subarray(3);
      if (this.got + chunk.length > this.buf.length) return this.reset();
      this.buf.set(chunk, this.got);
      this.got += chunk.length;
      return this.got === this.buf.length ? this.finish() : null;
    }
    return null;
  }

  private finish() {
    const out = { mime: this.mime, bytes: this.buf ?? new Uint8Array(0) };
    this.reset();
    return out;
  }

  private reset(): null {
    this.xfer = -1;
    this.buf = null;
    this.got = 0;
    return null;
  }
}

export type CursorMessage =
  | { type: "shape"; id: number; w: number; h: number; hotX: number; hotY: number; png: Uint8Array }
  | { type: "pos"; x: number; y: number; visible: boolean };

export function decodeCursor(msg: Uint8Array): CursorMessage | null {
  if (msg.length < 1) return null;
  const v = new DataView(msg.buffer, msg.byteOffset, msg.byteLength);
  if (msg[0] === MsgType.CursorPos && msg.length === 6) {
    return {
      type: "pos",
      x: v.getUint16(1, true),
      y: v.getUint16(3, true),
      visible: v.getUint8(5) !== 0,
    };
  }
  if (msg[0] === MsgType.CursorShape && msg.length >= 13) {
    return {
      type: "shape",
      id: v.getUint32(1, true),
      w: v.getUint16(5, true),
      h: v.getUint16(7, true),
      hotX: v.getUint16(9, true),
      hotY: v.getUint16(11, true),
      png: msg.slice(13),
    };
  }
  return null;
}

export const toHex = (b: Uint8Array) =>
  Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");

export const fromHex = (h: string) =>
  new Uint8Array((h.match(/../g) ?? []).map((x) => parseInt(x, 16)));
