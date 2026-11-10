import type { ClientKeyId, Session } from "../api/types";
import { timeAgo } from "./format";

/** The session that keeps a device busy, as GET /v1/devices/{id}/session returns it. */
export type Holder = Pick<
  Session,
  | "client_key_id"
  | "client_key_label"
  | "client_ip"
  | "mode"
  | "state"
  | "created_at"
  | "connected_at"
>;

/** Who holds a busy device, for the take-over prompt. */
export function describeHolder(
  s: Holder,
  ownKey: ClientKeyId | null,
  now = Date.now(),
): { who: string; detail: string } {
  const who =
    s.client_key_id !== null && s.client_key_id === ownKey
      ? "This browser"
      : (s.client_key_label ?? "Another browser");
  const when =
    s.state === "pending"
      ? `connecting since ${timeAgo(s.created_at, now)}`
      : `connected ${timeAgo(s.connected_at ?? s.created_at, now)}`;
  const parts = [s.mode === "terminal" ? "Terminal session" : "Screen session", when];
  if (s.client_ip) parts.push(`from ${s.client_ip}`);
  return { who, detail: parts.join(" · ") };
}
