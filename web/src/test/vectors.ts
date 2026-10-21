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
