// Saved sign-in crypto (PROTOCOL §4.2), the browser half of proto/unlock.go:
// check the device's unlock key against the pinned device key, then seal a
// username and password to it (ECDH P-256 → HKDF-SHA256 → AES-256-GCM) and
// sign the result with this browser's client key.

import { verifyEd25519 } from "./devicePins";
import { b64decode, b64encode } from "./fingerprint";
import { sha256Hex } from "./previewCrypto";

/** A credential as stored on the server and handed to the device (proto.SealedCredential). */
export type SealedCredential = {
  client_key: string;
  unlock_key: string;
  eph_pub: string;
  iv: string;
  ciphertext: string;
  sig: string;
};

/** A device's unlock key whose signature verified: SPKI (base64) and its hash. */
export type UnlockKey = { spki: string; sha: string };

const utf8 = (s: string) => new TextEncoder().encode(s);

export const unlockKeyMessage = (ukSha: string) => `remotectl-unlock-key-v1\n${ukSha}`;

/** The unlock key from `hello`, or null unless the pinned device key signed it. */
export async function verifyUnlockKey(
  devicePublicKey: string,
  spki: string,
  sig: string,
): Promise<UnlockKey | null> {
  let der: Uint8Array<ArrayBuffer>;
  try {
    der = b64decode(spki);
  } catch {
    return null;
  }
  const sha = await sha256Hex(der);
  return (await verifyEd25519(devicePublicKey, sig, utf8(unlockKeyMessage(sha))))
    ? { spki, sha }
    : null;
}

/** AES-GCM additional data; the start of the signed message. */
export function credentialHeader(
  userId: string,
  ckSha: string,
  ukSha: string,
  ephPub: string,
  iv: string,
): string {
  return ["remotectl-unlock-v1", userId.toLowerCase(), ckSha, ukSha, ephPub, iv].join("\n");
}

type SealInput = {
  unlockKey: UnlockKey;
  /** This browser's client key: SPKI (base64) and its ECDSA private key. */
  clientSpki: string;
  clientKey: CryptoKey;
  userId: string;
  username: string;
  password: string;
  /** Tests only: a fixed ephemeral ECDH key pair and iv. */
  eph?: CryptoKeyPair;
  iv?: Uint8Array<ArrayBuffer>;
};

export async function sealCredential(i: SealInput): Promise<SealedCredential> {
  const eph =
    i.eph ??
    (await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, false, ["deriveBits"]));
  const iv = i.iv ?? crypto.getRandomValues(new Uint8Array(12));
  const ephPub = new Uint8Array(await crypto.subtle.exportKey("raw", eph.publicKey));
  const unlockPub = await crypto.subtle.importKey(
    "spki",
    b64decode(i.unlockKey.spki),
    { name: "ECDH", namedCurve: "P-256" },
    false,
    [],
  );
  const shared = await crypto.subtle.deriveBits(
    { name: "ECDH", public: unlockPub },
    eph.privateKey,
    256,
  );
  const ckSha = await sha256Hex(b64decode(i.clientSpki));
  const userId = i.userId.toLowerCase();
  const hkdf = await crypto.subtle.importKey("raw", shared, "HKDF", false, ["deriveKey"]);
  const key = await crypto.subtle.deriveKey(
    {
      name: "HKDF",
      hash: "SHA-256",
      salt: ephPub,
      info: utf8(`remotectl-unlock-v1\n${userId}\n${ckSha}\n${i.unlockKey.sha}`),
    },
    hkdf,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt"],
  );
  const ephB64 = b64encode(ephPub);
  const ivB64 = b64encode(iv);
  const header = credentialHeader(userId, ckSha, i.unlockKey.sha, ephB64, ivB64);
  const plaintext = utf8(JSON.stringify({ username: i.username, password: i.password }));
  const ct = new Uint8Array(
    await crypto.subtle.encrypt(
      { name: "AES-GCM", iv, additionalData: utf8(header) },
      key,
      plaintext,
    ),
  );
  const sig = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    i.clientKey,
    utf8(`${header}\n${await sha256Hex(ct)}`),
  );
  return {
    client_key: i.clientSpki,
    unlock_key: i.unlockKey.sha,
    eph_pub: ephB64,
    iv: ivB64,
    ciphertext: b64encode(ct),
    sig: b64encode(sig),
  };
}
