// TOFU pinning of device public keys (like SSH known_hosts) and Ed25519
// verification of the agent's fingerprint signature.

import { ed25519 } from "@noble/curves/ed25519.js";
import { b64decode, fingerprintMessage } from "./fingerprint";
import { idbGet, idbPut } from "./idb";

type Pin = { public_key: string; pinned_at: string };

export type PinCheck =
  { status: "new" } | { status: "match" } | { status: "mismatch"; pinned: string };

export async function checkDevicePin(deviceId: string, publicKey: string): Promise<PinCheck> {
  const pin = await idbGet<Pin>("device_pins", deviceId).catch(() => undefined);
  if (!pin) return { status: "new" };
  return pin.public_key === publicKey
    ? { status: "match" }
    : { status: "mismatch", pinned: pin.public_key };
}

export async function pinDevice(deviceId: string, publicKey: string): Promise<void> {
  await idbPut("device_pins", deviceId, {
    public_key: publicKey,
    pinned_at: new Date().toISOString(),
  });
}

export async function verifyDeviceSignature(
  publicKeyB64: string,
  sigB64: string,
  sessionId: string,
  fingerprint: string,
): Promise<boolean> {
  const msg = new TextEncoder().encode(fingerprintMessage(sessionId, "answer", fingerprint));
  let pub: Uint8Array<ArrayBuffer>;
  let sig: Uint8Array<ArrayBuffer>;
  try {
    pub = b64decode(publicKeyB64);
    sig = b64decode(sigB64);
  } catch {
    return false;
  }
  if (pub.length !== 32 || sig.length !== 64) return false;
  try {
    const key = await crypto.subtle.importKey("raw", pub, { name: "Ed25519" }, false, ["verify"]);
    return await crypto.subtle.verify({ name: "Ed25519" }, key, sig, msg);
  } catch {
    // WebCrypto without Ed25519: fall back to the audited pure-JS implementation.
    try {
      return ed25519.verify(sig, msg, pub);
    } catch {
      return false;
    }
  }
}

/** Short human fingerprint of a key for warning screens. */
export async function keyFingerprint(publicKeyB64: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", b64decode(publicKeyB64)));
  return Array.from(digest.slice(0, 8), (b) => b.toString(16).padStart(2, "0"))
    .join(":")
    .toUpperCase();
}
