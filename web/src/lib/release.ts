// Release sidecars (<installer>.sig, PROTOCOL §7), checked in the browser
// before an upload. Mirrors proto.ParseSidecar; the server still verifies the
// signature and the uploaded bytes.

import type { ReleaseArch, ReleaseOS } from "../api/types";
import { osName } from "./format";
import { validVersion } from "./version";

export const SIDECAR_FORMAT = "remotectl-release-sig-v2";
export const MAX_SIDECAR_BYTES = 4096;
const RELEASE_OSES: readonly ReleaseOS[] = ["darwin", "windows"];
const RELEASE_ARCHES: readonly ReleaseArch[] = ["amd64", "arm64", "universal", "amd64-win7"];
const EXT: Record<ReleaseOS, string> = { darwin: ".pkg", windows: ".msi" };

export type ReleaseSidecar = {
  format: string;
  version: string;
  os: ReleaseOS;
  arch: ReleaseArch;
  sha256: string;
  size: number;
  filename: string;
  key_id: string;
  signature: string;
};

export type SidecarResult = { ok: true; sidecar: ReleaseSidecar } | { ok: false; error: string };

const BAD_JSON = "signature file is not valid JSON";
const isLowerHex = (s: string, n: number) => s.length === n && /^[0-9a-f]*$/.test(s);
// 64 bytes of standard base64: 86 characters plus "==".
const ED25519_SIG_B64 = /^[A-Za-z0-9+/]{86}==$/;

/** Like Go's json.Unmarshal into a string/int64 field: null is the zero value. */
function decodeFields(raw: Record<string, unknown>): Record<string, string | number> | null {
  const out: Record<string, string | number> = {};
  for (const k of [
    "format",
    "version",
    "os",
    "arch",
    "sha256",
    "filename",
    "key_id",
    "signature",
  ]) {
    const v = raw[k] ?? "";
    if (typeof v !== "string") return null;
    out[k] = v;
  }
  const size = raw.size ?? 0;
  if (typeof size !== "number" || !Number.isSafeInteger(size)) return null;
  out.size = size;
  return out;
}

function validate(s: ReleaseSidecar): string | null {
  const ext = EXT[s.os] ?? "";
  const name = s.filename;
  if (s.format !== SIDECAR_FORMAT)
    return `signature file format "${s.format}", want "${SIDECAR_FORMAT}"`;
  if (!validVersion(s.version)) return `version "${s.version}" is not semantic (x.y.z)`;
  if (!RELEASE_OSES.includes(s.os)) return `os "${s.os}" is not one of ${RELEASE_OSES.join(", ")}`;
  if (!RELEASE_ARCHES.includes(s.arch))
    return `arch "${s.arch}" is not one of ${RELEASE_ARCHES.join(", ")}`;
  if (s.arch === "amd64-win7" && s.os !== "windows") return "arch amd64-win7 is only for windows";
  if (!isLowerHex(s.sha256, 64)) return "sha256 must be 64 lowercase hex digits";
  if (s.size <= 0) return "size must be positive";
  if (!name || /[\\/]/.test(name) || !name.toLowerCase().endsWith(ext))
    return `filename must be a plain ${ext} name`;
  if (!isLowerHex(s.key_id, 16)) return "key_id must be 16 lowercase hex digits";
  if (!ED25519_SIG_B64.test(s.signature)) return "signature must be a base64 Ed25519 signature";
  return null;
}

/** Decodes and validates a sidecar; it does not check the signature. */
export function parseSidecar(text: string): SidecarResult {
  if (new TextEncoder().encode(text).length > MAX_SIDECAR_BYTES)
    return { ok: false, error: "signature file is too large" };
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { ok: false, error: BAD_JSON };
  }
  if (typeof raw !== "object" || raw === null || Array.isArray(raw))
    return { ok: false, error: BAD_JSON };
  const fields = decodeFields(raw as Record<string, unknown>);
  if (!fields) return { ok: false, error: BAD_JSON };
  const sidecar = fields as unknown as ReleaseSidecar;
  const error = validate(sidecar);
  return error ? { ok: false, error } : { ok: true, sidecar };
}

/** Why a chosen installer cannot be the file a sidecar signs (null when it can). */
export function releaseFileMatches(
  s: ReleaseSidecar,
  file: { name: string; size: number },
): string | null {
  const ext = EXT[s.os];
  if (!file.name.toLowerCase().endsWith(ext))
    return `The signature is for a ${osName(s.os)} ${ext}, but ${file.name} is not one.`;
  if (file.size !== s.size)
    return `${file.name} is ${file.size} bytes, but the signature is for ${s.size} bytes. Pick the installer this .sig was made for.`;
  return null;
}

const isInstaller = (name: string) => /\.(pkg|msi)$/i.test(name);
const isSig = (name: string) => /\.sig$/i.test(name);

export type FilePair<F> = { installer: F | null; sig: F | null };

/**
 * Sorts dropped files into installers and their sidecars: a .sig goes with the
 * installer it is named after (`<installer>.sig`). A lone installer and a lone
 * .sig left over are paired anyway; any other leftover is a pair with a
 * missing half. Files that are neither are ignored.
 */
export function pairFiles<F extends { name: string }>(
  files: readonly F[],
): { pairs: FilePair<F>[]; ignored: F[] } {
  const sigs = files.filter((f) => isSig(f.name));
  const pairs: FilePair<F>[] = files
    .filter((f) => isInstaller(f.name))
    .map((installer) => ({
      installer,
      sig: sigs.find((s) => s.name.toLowerCase() === `${installer.name.toLowerCase()}.sig`) ?? null,
    }));
  const loose = sigs.filter((s) => !pairs.some((p) => p.sig === s));
  const alone = pairs.filter((p) => !p.sig);
  if (alone.length === 1 && loose.length === 1) alone[0]!.sig = loose.pop()!;
  for (const sig of loose) pairs.push({ installer: null, sig });
  return { pairs, ignored: files.filter((f) => !isInstaller(f.name) && !isSig(f.name)) };
}

/** `prev` with `added` merged in; a file with the name of one already there replaces it. */
export function addReleaseFiles<F extends { name: string }>(
  prev: readonly F[],
  added: readonly F[],
): F[] {
  const byName = new Map(prev.map((f) => [f.name, f]));
  for (const f of added) byName.set(f.name, f);
  return [...byName.values()];
}

export function formatRollout(pct: number): string {
  if (pct >= 100) return "All devices";
  if (pct <= 0) return "Only devices updated by hand";
  return `${pct}% of devices`;
}

/** Readable text for POST /v1/admin/releases errors (PROTOCOL §2 Admin). */
export function uploadErrorMessage(code: string, message: string): string {
  switch (code) {
    case "bad_signature":
      return `The signature file was not made with this server's release key: ${message}`;
    case "mismatch":
      return `The installer does not match its signature file (${message}). Upload the pair produced by one remotectl-release sign run.`;
    case "conflict":
      return "This version is already uploaded for that OS and architecture. Delete it first to replace it.";
    case "too_large":
      return "The installer is larger than the 512 MB upload limit.";
    default:
      return message;
  }
}
