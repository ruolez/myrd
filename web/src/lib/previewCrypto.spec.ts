import { p256 } from "@noble/curves/nist.js";
import { describe, expect, test } from "vitest";
import { vectors } from "../test/vectors";
import { b64decode } from "./fingerprint";
import {
  decryptPreview,
  openPreview,
  previewHeader,
  previewKeyMessage,
  sha256Hex,
  verifyPreview,
} from "./previewCrypto";
import type { SealedPreview } from "./previewCrypto";

const v = vectors.preview;
const hex = (s: string) => Uint8Array.from(s.match(/../g) ?? [], (h) => parseInt(h, 16));
const b64url = (b: Uint8Array) =>
  Buffer.from(b).toString("base64").replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

/** The vector's preview key as a WebCrypto ECDH key (tests only make it from d). */
async function previewKey(): Promise<CryptoKey> {
  const d = hex(v.preview_d_hex);
  const pub = p256.getPublicKey(d, false); // 0x04 ‖ x ‖ y
  const jwk = {
    kty: "EC",
    crv: "P-256",
    d: b64url(d),
    x: b64url(pub.slice(1, 33)),
    y: b64url(pub.slice(33, 65)),
  };
  return crypto.subtle.importKey("jwk", jwk, { name: "ECDH", namedCurve: "P-256" }, false, [
    "deriveBits",
  ]);
}

const sealed: SealedPreview = {
  device_id: v.device_id,
  client_key_id: "k",
  captured_at_ms: v.captured_at_ms,
  monitor_id: v.monitor_id,
  width: v.width,
  height: v.height,
  mime: v.mime,
  eph_pub: v.eph_pub,
  iv: v.iv,
  ciphertext: v.ciphertext,
  sig: v.sig,
};

describe("preview key", () => {
  test("the vector's key hashes and message match", async () => {
    expect(await sha256Hex(b64decode(v.client_spki))).toBe(v.ck_sha);
    expect(await sha256Hex(b64decode(v.preview_spki))).toBe(v.pk_sha);
    expect(previewKeyMessage(v.ck_sha, v.pk_sha)).toBe(v.key_message_utf8);
  });

  test("the client key's signature over it verifies", async () => {
    const key = await crypto.subtle.importKey(
      "spki",
      b64decode(v.client_spki),
      { name: "ECDSA", namedCurve: "P-256" },
      false,
      ["verify"],
    );
    const ok = await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      key,
      b64decode(v.key_message_sig),
      new TextEncoder().encode(v.key_message_utf8),
    );
    expect(ok).toBe(true);
  });
});

describe("openPreview", () => {
  test("the header matches the vector byte for byte", () => {
    expect(previewHeader(sealed, v.ck_sha, v.pk_sha)).toBe(v.header_utf8);
  });

  test("verifies and decrypts the vector", async () => {
    const pt = await openPreview(
      v.device_id,
      v.device_public_key,
      await previewKey(),
      sealed,
      v.ck_sha,
      v.pk_sha,
    );
    expect(Buffer.from(pt).toString("hex")).toBe(v.plaintext_hex);
  });

  test.each(v.invalid.map((c) => [c.name, c] as const))("rejects %s", async (_name, c) => {
    const p: SealedPreview = {
      ...sealed,
      device_id: c.device_id ?? sealed.device_id,
      captured_at_ms: c.captured_at_ms ?? sealed.captured_at_ms,
      width: c.width ?? sealed.width,
      ciphertext: c.ciphertext ?? sealed.ciphertext,
      sig: c.sig ?? sealed.sig,
    };
    const pkSha = c.pk_sha ?? v.pk_sha;
    const key = await previewKey();
    // Opening as the device the preview claims to be (so the device check alone does not decide).
    await expect(
      openPreview(p.device_id, v.device_public_key, key, p, v.ck_sha, pkSha),
    ).rejects.toThrow();
  });

  test("a preview of another device is refused before anything else", async () => {
    await expect(
      openPreview(
        "00000000-0000-0000-0000-000000000000",
        v.device_public_key,
        await previewKey(),
        sealed,
        v.ck_sha,
        v.pk_sha,
      ),
    ).rejects.toThrow("another device");
  });

  test("decrypting is possible without the device key, so the signature is what proves origin", async () => {
    const otherDevice = Buffer.from(new Uint8Array(32).fill(7)).toString("base64");
    expect(await verifyPreview(otherDevice, sealed, v.ck_sha, v.pk_sha)).toBe(false);
    const pt = await decryptPreview(await previewKey(), sealed, v.ck_sha, v.pk_sha);
    expect(Buffer.from(pt).toString("hex")).toBe(v.plaintext_hex);
  });
});
