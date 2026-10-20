import { describe, expect, test } from "vitest";
import { vectors } from "../test/vectors";
import {
  MAX_SIDECAR_BYTES,
  formatRollout,
  parseSidecar,
  pickPair,
  releaseFileMatches,
  uploadErrorMessage,
} from "./release";
import type { ReleaseSidecar } from "./release";

const extFor = (os: string) => (os === "darwin" ? ".pkg" : ".msi");

const sidecarFor = (c: (typeof vectors.release.cases)[number], size = 1234): ReleaseSidecar =>
  ({
    format: "remotectl-release-sig-v2",
    version: c.version,
    os: c.os,
    arch: c.arch,
    sha256: c.sha256,
    size,
    filename: `RemoteAgent-${c.version}${extFor(c.os)}`,
    key_id: vectors.release.key_id,
    signature: c.signature,
  }) as ReleaseSidecar;

const valid = sidecarFor(vectors.release.cases[0]!);
const text = (s: unknown) => JSON.stringify(s);

describe("parseSidecar", () => {
  test.each(vectors.release.cases)("accepts the $os/$arch vector sidecar", (c) => {
    const s = sidecarFor(c);
    expect(parseSidecar(text(s))).toEqual({ ok: true, sidecar: s });
  });

  test("keeps only the sidecar fields, like the Go struct", () => {
    expect(parseSidecar(text({ ...valid, extra: "x" }))).toEqual({ ok: true, sidecar: valid });
  });

  test.each<[string, Partial<Record<keyof ReleaseSidecar, unknown>>, string]>([
    [
      "format",
      { format: "remotectl-release-sig-v1" },
      'signature file format "remotectl-release-sig-v1", want "remotectl-release-sig-v2"',
    ],
    ["version", { version: "dev" }, 'version "dev" is not semantic (x.y.z)'],
    ["os", { os: "linux" }, 'os "linux" is not one of darwin, windows'],
    ["arch", { arch: "386" }, 'arch "386" is not one of amd64, arm64, universal'],
    ["sha256 length", { sha256: valid.sha256.slice(1) }, "sha256 must be 64 lowercase hex digits"],
    [
      "sha256 upper",
      { sha256: valid.sha256.toUpperCase() },
      "sha256 must be 64 lowercase hex digits",
    ],
    ["size zero", { size: 0 }, "size must be positive"],
    ["size fraction", { size: 1.5 }, "signature file is not valid JSON"],
    ["size string", { size: "1234" }, "signature file is not valid JSON"],
    ["filename ext", { filename: "RemoteAgent.msi" }, "filename must be a plain .pkg name"],
    ["filename path", { filename: "../RemoteAgent.pkg" }, "filename must be a plain .pkg name"],
    ["filename backslash", { filename: "a\\b.pkg" }, "filename must be a plain .pkg name"],
    ["filename empty", { filename: "" }, "filename must be a plain .pkg name"],
    ["key id", { key_id: "xyz" }, "key_id must be 16 lowercase hex digits"],
    ["signature", { signature: "not base64!" }, "signature must be a base64 Ed25519 signature"],
    [
      "signature length",
      { signature: valid.signature.slice(4) },
      "signature must be a base64 Ed25519 signature",
    ],
  ])("rejects a bad %s", (_name, edit, error) => {
    expect(parseSidecar(text({ ...valid, ...edit }))).toEqual({ ok: false, error });
  });

  test("accepts an upper-case extension and a v-prefixed version", () => {
    const s = { ...valid, filename: "RemoteAgent.PKG", version: "v0.3.0" };
    expect(parseSidecar(text(s))).toEqual({ ok: true, sidecar: s });
  });

  test.each([
    ["not json", "signature file is not valid JSON"],
    ["[1,2]", "signature file is not valid JSON"],
    ["null", "signature file is not valid JSON"],
    [`{${" ".repeat(MAX_SIDECAR_BYTES)}}`, "signature file is too large"],
  ])("rejects %j", (input, error) => {
    expect(parseSidecar(input)).toEqual({ ok: false, error });
  });
});

describe("releaseFileMatches", () => {
  test("a file with the signed size and extension matches, whatever its name", () => {
    expect(releaseFileMatches(valid, { name: "RemoteAgent-0.3.0 (1).PKG", size: 1234 })).toBe(null);
  });

  test("a different size is not the signed installer", () => {
    expect(releaseFileMatches(valid, { name: valid.filename, size: 1235 })).toBe(
      "RemoteAgent-0.3.0.pkg is 1235 bytes, but the signature is for 1234 bytes. Pick the installer this .sig was made for.",
    );
  });

  test("a Windows installer cannot carry a macOS signature", () => {
    expect(releaseFileMatches(valid, { name: "RemoteAgent.msi", size: 1234 })).toBe(
      "The signature is for a macOS .pkg, but RemoteAgent.msi is not one.",
    );
  });
});

describe("pickPair", () => {
  const f = (name: string) => ({ name });

  test("takes the installer and its sidecar dropped together, in either order", () => {
    const pkg = f("RemoteAgent.pkg");
    const sig = f("RemoteAgent.pkg.sig");
    expect([pickPair([sig, pkg]), pickPair([pkg, sig])]).toEqual([
      { installer: pkg, sig, ignored: [] },
      { installer: pkg, sig, ignored: [] },
    ]);
  });

  test("a single file fills only its own slot", () => {
    const msi = f("RemoteAgent.MSI");
    expect(pickPair([msi])).toEqual({ installer: msi, sig: null, ignored: [] });
  });

  test("among several installers, the one named by the sidecar wins", () => {
    const mac = f("RemoteAgent.pkg");
    const win = f("RemoteAgent.msi");
    const sig = f("RemoteAgent.msi.sig");
    expect(pickPair([mac, sig, win])).toEqual({ installer: win, sig, ignored: [mac] });
  });

  test("without a name match the first of each kind wins and the rest are ignored", () => {
    const a = f("a.pkg");
    const b = f("b.pkg");
    const sig = f("other.sig");
    const txt = f("notes.txt");
    expect(pickPair([txt, a, b, sig])).toEqual({ installer: a, sig, ignored: [txt, b] });
  });
});

describe("formatRollout", () => {
  test.each([
    [100, "All devices"],
    [0, "Only devices updated by hand"],
    [25, "25% of devices"],
  ])("%i%% → %s", (pct, label) => {
    expect(formatRollout(pct)).toBe(label);
  });
});

describe("uploadErrorMessage", () => {
  test.each([
    [
      "bad_signature",
      "signed with a different release key (aaaa, this server trusts bbbb)",
      "The signature file was not made with this server's release key: signed with a different release key (aaaa, this server trusts bbbb)",
    ],
    [
      "mismatch",
      "sha256 differs",
      "The installer does not match its signature file (sha256 differs). Upload the pair produced by one remotectl-release sign run.",
    ],
    [
      "conflict",
      "release exists",
      "This version is already uploaded for that OS and architecture. Delete it first to replace it.",
    ],
    ["too_large", "request too large", "The installer is larger than the 512 MB upload limit."],
    ["bad_request", "missing sig part", "missing sig part"],
  ])("%s", (code, message, want) => {
    expect(uploadErrorMessage(code, message)).toBe(want);
  });
});
