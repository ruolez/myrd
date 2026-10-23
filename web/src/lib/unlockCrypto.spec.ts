import { p256 } from "@noble/curves/nist.js";
import { describe, expect, test } from "vitest";
import { vectors } from "../test/vectors";
import { b64decode } from "./fingerprint";
import { sha256Hex } from "./previewCrypto";
import {
  credentialHeader,
  sealCredential,
  unlockKeyMessage,
  verifyUnlockKey,
} from "./unlockCrypto";

const v = vectors.unlock;
const hex = (s: string) => Uint8Array.from(s.match(/../g) ?? [], (h) => parseInt(h, 16));
const b64url = (b: Uint8Array) =>
  Buffer.from(b).toString("base64").replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

/** A P-256 key pair from the vector's private scalar (tests only make keys from d). */
async function keyPair(dHex: string, alg: "ECDH" | "ECDSA"): Promise<CryptoKeyPair> {
  const d = hex(dHex);
  const pub = p256.getPublicKey(d, false);
  const jwk = {
    kty: "EC",
    crv: "P-256",
    x: b64url(pub.slice(1, 33)),
    y: b64url(pub.slice(33, 65)),
  };
  const usages: KeyUsage[] = alg === "ECDH" ? ["deriveBits"] : ["sign"];
  return {
    privateKey: await crypto.subtle.importKey(
      "jwk",
      { ...jwk, d: b64url(d) },
      { name: alg, namedCurve: "P-256" },
      false,
      usages,
    ),
    publicKey: await crypto.subtle.importKey(
      "jwk",
      jwk,
      { name: alg, namedCurve: "P-256" },
      true,
      alg === "ECDH" ? [] : ["verify"],
    ),
  };
}

const unlockKey = { spki: v.unlock_spki, sha: v.uk_sha };

describe("verifyUnlockKey", () => {
  test("accepts the unlock key the device key signed", async () => {
    expect(unlockKeyMessage(v.uk_sha)).toBe(v.key_message_utf8);
    expect(await verifyUnlockKey(v.device_public_key, v.unlock_spki, v.key_message_sig)).toEqual(
      unlockKey,
    );
  });

  test.each([
    [
      "another device's key",
      Buffer.from(new Uint8Array(32).fill(7)).toString("base64"),
      v.unlock_spki,
    ],
    ["a substituted unlock key", v.device_public_key, v.client_spki],
    ["an unlock key that is not base64", v.device_public_key, "%%"],
  ])("rejects %s", async (_name, deviceKey, spki) => {
    expect(await verifyUnlockKey(deviceKey, spki, v.key_message_sig)).toBeNull();
  });
});

describe("sealCredential", () => {
  test("seals the vector's credential byte for byte and signs it with the client key", async () => {
    const client = await keyPair(v.client_d_hex, "ECDSA");
    const sealed = await sealCredential({
      unlockKey,
      clientSpki: v.client_spki,
      clientKey: client.privateKey,
      userId: v.user_id.toUpperCase(),
      ...JSON.parse(v.plaintext_utf8),
      eph: await keyPair(v.eph_d_hex, "ECDH"),
      iv: b64decode(v.iv),
    });
    expect({ ...sealed, sig: "" }).toEqual({
      client_key: v.client_spki,
      unlock_key: v.uk_sha,
      eph_pub: v.eph_pub,
      iv: v.iv,
      ciphertext: v.ciphertext,
      sig: "",
    });
    expect(credentialHeader(v.user_id, v.ck_sha, v.uk_sha, v.eph_pub, v.iv)).toBe(v.header_utf8);
    const signed = `${v.header_utf8}\n${await sha256Hex(b64decode(sealed.ciphertext))}`;
    const ok = await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      client.publicKey,
      b64decode(sealed.sig),
      new TextEncoder().encode(signed),
    );
    expect(ok).toBe(true);
  });

  test("a fresh seal uses a new ephemeral key and iv each time", async () => {
    const client = await keyPair(v.client_d_hex, "ECDSA");
    const seal = () =>
      sealCredential({
        unlockKey,
        clientSpki: v.client_spki,
        clientKey: client.privateKey,
        userId: v.user_id,
        username: "alice",
        password: "pw",
      });
    const [a, b] = [await seal(), await seal()];
    expect(a.eph_pub === b.eph_pub || a.iv === b.iv || a.ciphertext === b.ciphertext).toBe(false);
  });
});
