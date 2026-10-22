// Screen preview crypto (PROTOCOL §4.1), the browser half of proto/preview.go:
// verify the device's Ed25519 signature against the pinned device key, then
// ECDH P-256 with this browser's preview key → HKDF-SHA256 → AES-256-GCM.

import { verifyEd25519 } from "./devicePins";
import { b64decode } from "./fingerprint";

/** A preview as relayed by the server (proto.Preview). */
export type SealedPreview = {
  device_id: string;
  client_key_id: string;
  captured_at_ms: number;
  monitor_id: number;
  width: number;
  height: number;
  mime: string;
  eph_pub: string;
  iv: string;
  ciphertext: string;
  sig: string;
  received_at?: string;
};

const utf8 = (s: string) => new TextEncoder().encode(s);

export async function sha256Hex(bytes: Uint8Array<ArrayBuffer>): Promise<string> {
  const d = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return Array.from(d, (b) => b.toString(16).padStart(2, "0")).join("");
}

/** What a client key signs to vouch for its preview key. */
export const previewKeyMessage = (ckSha: string, pkSha: string) =>
  `remotectl-preview-key-v1\n${ckSha}\n${pkSha}`;

/** AES-GCM additional data; the start of the signed message. */
export function previewHeader(p: SealedPreview, ckSha: string, pkSha: string): string {
  return [
    "remotectl-preview-v1",
    p.device_id.toLowerCase(),
    ckSha,
    pkSha,
    String(p.captured_at_ms),
    String(p.monitor_id),
    String(p.width),
    String(p.height),
    p.mime,
    p.eph_pub,
    p.iv,
  ].join("\n");
}

export async function verifyPreview(
  devicePublicKey: string,
  p: SealedPreview,
  ckSha: string,
  pkSha: string,
): Promise<boolean> {
  let ct: Uint8Array<ArrayBuffer>;
  try {
    ct = b64decode(p.ciphertext);
  } catch {
    return false;
  }
  const msg = `${previewHeader(p, ckSha, pkSha)}\n${await sha256Hex(ct)}`;
  return verifyEd25519(devicePublicKey, p.sig, utf8(msg));
}

export async function decryptPreview(
  previewKey: CryptoKey,
  p: SealedPreview,
  ckSha: string,
  pkSha: string,
): Promise<Uint8Array<ArrayBuffer>> {
  const ephPub = b64decode(p.eph_pub);
  const eph = await crypto.subtle.importKey(
    "raw",
    ephPub,
    { name: "ECDH", namedCurve: "P-256" },
    false,
    [],
  );
  const shared = await crypto.subtle.deriveBits({ name: "ECDH", public: eph }, previewKey, 256);
  const hkdf = await crypto.subtle.importKey("raw", shared, "HKDF", false, ["deriveKey"]);
  const info = `remotectl-preview-v1\n${p.device_id.toLowerCase()}\n${ckSha}\n${pkSha}`;
  const key = await crypto.subtle.deriveKey(
    { name: "HKDF", hash: "SHA-256", salt: ephPub, info: utf8(info) },
    hkdf,
    { name: "AES-GCM", length: 256 },
    false,
    ["decrypt"],
  );
  const pt = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: b64decode(p.iv), additionalData: utf8(previewHeader(p, ckSha, pkSha)) },
    key,
    b64decode(p.ciphertext),
  );
  return new Uint8Array(pt);
}

/**
 * Verifies, then decrypts. Rejects a preview of another device, one not
 * signed by devicePublicKey (the pinned key) and anything tampered with.
 */
export async function openPreview(
  deviceId: string,
  devicePublicKey: string,
  previewKey: CryptoKey,
  p: SealedPreview,
  ckSha: string,
  pkSha: string,
): Promise<Uint8Array<ArrayBuffer>> {
  if (p.device_id.toLowerCase() !== deviceId.toLowerCase())
    throw new Error("preview of another device");
  if (!(await verifyPreview(devicePublicKey, p, ckSha, pkSha)))
    throw new Error("bad preview signature");
  return decryptPreview(previewKey, p, ckSha, pkSha);
}
