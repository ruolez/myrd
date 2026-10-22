import { readFileSync } from "node:fs";

type Json = Record<string, unknown>;
export type Vector = { name: string; event: Json; hex: string };

type VectorPrefs = {
  preset: string;
  fps: number | null;
  res: string | null;
  max_kbps: number | null;
  wallpaper?: boolean;
};

export type PreviewVectors = {
  device_public_key: string;
  client_spki: string;
  ck_sha: string;
  preview_d_hex: string;
  preview_spki: string;
  pk_sha: string;
  key_message_utf8: string;
  key_message_sig: string;
  eph_pub: string;
  iv: string;
  plaintext_hex: string;
  device_id: string;
  captured_at_ms: number;
  monitor_id: number;
  width: number;
  height: number;
  mime: string;
  header_utf8: string;
  ciphertext: string;
  sig: string;
  invalid: {
    name: string;
    device_id?: string;
    captured_at_ms?: number;
    width?: number;
    pk_sha?: string;
    ciphertext?: string;
    sig?: string;
  }[];
};

export type Vectors = {
  input: Vector[];
  input_invalid: { name: string; hex: string }[];
  cursor: Vector[];
  fingerprint_message: { session_id: string; role: string; fingerprint: string; utf8: string }[];
  stream_request: {
    name: string;
    prefs: VectorPrefs;
    hidden: boolean;
    id: number;
    view?: { w: number; h: number };
    json: string;
  }[];
  stream_request_invalid: { name: string; json: string }[];
  legacy_tier: { name: string; prefs: VectorPrefs; hidden: boolean; tier: number }[];
  file_data: { name: string; xfer: number; payload_hex: string; hex: string }[];
  file_data_invalid: { name: string; hex: string }[];
  file_messages: { name: string; json: Json }[];
  file_sha256: { name: string; chunks_hex: string[]; sha256: string }[];
  versions: string[];
  preview: PreviewVectors;
  release: {
    public_key: string;
    key_id: string;
    cases: {
      version: string;
      os: string;
      arch: string;
      sha256: string;
      message: string;
      signature: string;
    }[];
  };
};

export const vectors: Vectors = JSON.parse(
  readFileSync(new URL("../../../docs/test-vectors.json", import.meta.url), "utf8"),
) as Vectors;
