// This browser's preview key (PROTOCOL §4.1): a second non-extractable
// WebCrypto key, ECDH P-256, that agents seal screen previews to. The client
// key signs it, so agents know it belongs to the browser they approved.

import { api } from "../api/client";
import type { ClientKeyId, UserId } from "../api/types";
import { ensureClientKey } from "./clientKey";
import { b64decode, b64encode } from "./fingerprint";
import { idbGet, idbPut } from "./idb";
import { previewKeyMessage, sha256Hex } from "./previewCrypto";

export type PreviewKey = {
  privateKey: CryptoKey;
  spki: string;
  pkSha: string;
  clientKeyId: ClientKeyId;
  ckSha: string;
};

type Stored = PreviewKey & { keyPair: CryptoKeyPair };

const cache = new Map<string, Promise<PreviewKey>>();

async function create(userId: UserId): Promise<PreviewKey> {
  const ck = await ensureClientKey(userId);
  const ckSha = await sha256Hex(b64decode(ck.spki));
  let stored = await idbGet<Stored>("preview_keys", userId).catch(() => undefined);
  if (!stored || stored.clientKeyId !== ck.id || stored.ckSha !== ckSha) {
    const keyPair = (await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, false, [
      "deriveBits",
    ])) as CryptoKeyPair;
    const spki = b64encode(await crypto.subtle.exportKey("spki", keyPair.publicKey));
    stored = {
      keyPair,
      privateKey: keyPair.privateKey,
      spki,
      pkSha: await sha256Hex(b64decode(spki)),
      clientKeyId: ck.id,
      ckSha,
    };
    await idbPut("preview_keys", userId, stored);
  }
  // Register when the server does not have this key (new key, or a server
  // that lost it); the server checks the client key's signature.
  const { items } = await api.clientKeys.list();
  if (items.find((k) => k.id === ck.id)?.preview_key !== stored.spki) {
    const sig = await crypto.subtle.sign(
      { name: "ECDSA", hash: "SHA-256" },
      ck.keyPair.privateKey,
      new TextEncoder().encode(previewKeyMessage(ckSha, stored.pkSha)),
    );
    await api.clientKeys.setPreviewKey(ck.id, stored.spki, b64encode(sig));
  }
  const { privateKey, spki, pkSha, clientKeyId } = stored;
  return { privateKey, spki, pkSha, clientKeyId, ckSha };
}

/** The registered preview key of this browser for the user (created once). */
export function ensurePreviewKey(userId: UserId): Promise<PreviewKey> {
  let p = cache.get(userId);
  if (!p) {
    p = create(userId);
    cache.set(userId, p);
    p.catch(() => cache.delete(userId));
  }
  return p;
}

export function forgetPreviewKeyCache() {
  cache.clear();
}
