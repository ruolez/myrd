// DTLS fingerprint signing (docs/PROTOCOL.md §4).

export type Role = "offer" | "answer";

const fpLine = /^a=fingerprint:sha-256 ([0-9A-F:]+)\s*$/im;

/** First sha-256 fingerprint in an SDP, uppercased; null when absent. */
export function sdpFingerprint(sdp: string): string | null {
  const m = fpLine.exec(sdp);
  return m?.[1] ? m[1].toUpperCase() : null;
}

/** "remotectl-fp-v1\n" + session_id + "\n" + role + "\n" + FINGERPRINT */
export function fingerprintMessage(sessionId: string, role: Role, fingerprint: string): string {
  return `remotectl-fp-v1\n${sessionId}\n${role}\n${fingerprint.toUpperCase()}`;
}

export function b64encode(bytes: ArrayBuffer | Uint8Array): string {
  const u = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  let s = "";
  for (let i = 0; i < u.length; i++) s += String.fromCharCode(u[i] as number);
  return btoa(s);
}

export function b64decode(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function b64urlToBytes(s: string): Uint8Array<ArrayBuffer> {
  const pad = s.length % 4 === 0 ? "" : "=".repeat(4 - (s.length % 4));
  return b64decode(s.replace(/-/g, "+").replace(/_/g, "/") + pad);
}

export function bytesToB64url(bytes: ArrayBuffer | Uint8Array): string {
  return b64encode(bytes).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
