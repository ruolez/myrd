import { describe, expect, test } from "vitest";
import { vectors } from "../test/vectors";
import {
  b64decode,
  b64encode,
  b64urlToBytes,
  bytesToB64url,
  fingerprintMessage,
  sdpFingerprint,
} from "./fingerprint";
import type { Role } from "./fingerprint";

describe("sdpFingerprint", () => {
  test("extracts and uppercases the first sha-256 fingerprint", () => {
    const sdp = "v=0\r\na=fingerprint:sha-256 ab:CD:01:23\r\na=fingerprint:sha-256 FF:FF\r\n";
    expect(sdpFingerprint(sdp)).toBe("AB:CD:01:23");
  });

  test("returns null without a sha-256 fingerprint", () => {
    expect(sdpFingerprint("v=0\r\na=fingerprint:sha-1 AA:BB\r\n")).toBeNull();
  });
});

describe("fingerprintMessage", () => {
  test.each(vectors.fingerprint_message)("matches the shared canonical form", (v) => {
    expect(fingerprintMessage(v.session_id, v.role as Role, v.fingerprint)).toBe(v.utf8);
  });
});

describe("base64 helpers", () => {
  test("std and url round-trip arbitrary bytes", () => {
    const bytes = new Uint8Array([0, 251, 255, 62, 63, 1]);
    expect([b64decode(b64encode(bytes)), b64urlToBytes(bytesToB64url(bytes))]).toEqual([
      bytes,
      bytes,
    ]);
  });

  test("url form has no padding or +/ characters", () => {
    expect(bytesToB64url(new Uint8Array([251, 255]))).toBe("-_8");
  });
});
