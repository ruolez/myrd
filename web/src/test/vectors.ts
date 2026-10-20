import { readFileSync } from "node:fs";

type Json = Record<string, unknown>;
export type Vector = { name: string; event: Json; hex: string };

export type Vectors = {
  input: Vector[];
  input_invalid: { name: string; hex: string }[];
  cursor: Vector[];
  fingerprint_message: { session_id: string; role: string; fingerprint: string; utf8: string }[];
};

export const vectors: Vectors = JSON.parse(
  readFileSync(new URL("../../../docs/test-vectors.json", import.meta.url), "utf8"),
) as Vectors;
