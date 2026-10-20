// Per-profile browser signing key (PROTOCOL §4): non-extractable ECDSA P-256 in
// WebCrypto, stored in IndexedDB keyed by user id, public half registered as a
// client_key. It signs every DTLS fingerprint the browser offers.

import { api } from "../api/client";
import type { ClientKeyId, UserId } from "../api/types";
import { b64encode, fingerprintMessage } from "./fingerprint";
import { idbDelete, idbGet, idbPut } from "./idb";

type StoredKey = {
  keyPair: CryptoKeyPair;
  spki: string;
  id: ClientKeyId;
  label: string;
};

const cache = new Map<string, StoredKey>();

export function browserLabel(ua = navigator.userAgent): string {
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /Firefox\//.test(ua)
      ? "Firefox"
      : /Chrome\//.test(ua)
        ? "Chrome"
        : /Safari\//.test(ua)
          ? "Safari"
          : "Browser";
  const os = /Mac OS X|Macintosh/.test(ua)
    ? "macOS"
    : /Windows/.test(ua)
      ? "Windows"
      : /Android/.test(ua)
        ? "Android"
        : /iPhone|iPad/.test(ua)
          ? "iOS"
          : /Linux/.test(ua)
            ? "Linux"
            : "unknown OS";
  return `${browser} on ${os}`;
}

async function generate(userId: UserId): Promise<StoredKey> {
  const keyPair = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, false, [
    "sign",
    "verify",
  ]);
  const spki = b64encode(await crypto.subtle.exportKey("spki", keyPair.publicKey));
  const label = browserLabel();
  const { client_key } = await api.clientKeys.register(spki, label);
  const stored: StoredKey = { keyPair, spki, id: client_key.id, label: client_key.label };
  await idbPut("client_keys", userId, stored);
  return stored;
}

/**
 * Ensures this browser profile holds a registered, non-revoked client key for
 * the user. Re-registration is idempotent server-side, so it also recovers a
 * key whose server row was lost.
 */
export async function ensureClientKey(userId: UserId): Promise<StoredKey> {
  const cached = cache.get(userId);
  if (cached) return cached;
  let stored = await idbGet<StoredKey>("client_keys", userId).catch(() => undefined);
  if (stored) {
    const { items } = await api.clientKeys.list();
    const row = items.find((k) => k.public_key === stored?.spki);
    if (!row || row.revoked_at) {
      if (row?.revoked_at) {
        await idbDelete("client_keys", userId);
        stored = undefined;
      } else {
        const { client_key } = await api.clientKeys.register(stored.spki, stored.label);
        stored = { ...stored, id: client_key.id };
        await idbPut("client_keys", userId, stored);
      }
    } else if (row.id !== stored.id) {
      stored = { ...stored, id: row.id };
      await idbPut("client_keys", userId, stored);
    }
  }
  stored ??= await generate(userId);
  cache.set(userId, stored);
  return stored;
}

export function forgetClientKeyCache(userId?: string) {
  if (userId) cache.delete(userId);
  else cache.clear();
}

export async function currentClientKeyId(userId: UserId): Promise<ClientKeyId | null> {
  const stored =
    cache.get(userId) ?? (await idbGet<StoredKey>("client_keys", userId).catch(() => undefined));
  return stored?.id ?? null;
}

/** ECDSA P-256/SHA-256 signature in P1363 form (r||s), base64. */
export async function signFingerprint(
  key: StoredKey,
  sessionId: string,
  fingerprint: string,
): Promise<string> {
  const msg = new TextEncoder().encode(fingerprintMessage(sessionId, "offer", fingerprint));
  const sig = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    key.keyPair.privateKey,
    msg,
  );
  return b64encode(sig);
}
