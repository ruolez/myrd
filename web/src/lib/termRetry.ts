// When a dropped terminal session reconnects by itself: for reasons a retry
// can fix, waiting a little longer after each failure (10 s at most, so a
// device that comes back is found quickly), for up to a quarter hour.

/** End reasons a retry can fix (a dropped link, a restarting device or server). */
const TRANSIENT = new Set([
  "ice_failed",
  "peer_disconnected",
  "error",
  "timeout",
  "server_restart",
  "device_offline",
  "device_busy", // the dropped session is still closing on the device
  "agent_update",
  "agent_restart",
  "device_restart",
  "rate_limited",
]);

const DELAYS_MS = [500, 1000, 2000, 3000, 5000, 8000, 10_000];
/** Retries stop this long after the first drop; the session then shows as closed. */
export const GIVE_UP_MS = 15 * 60_000;

/** The wait before retry number `attempt` (0 = the first). */
export const retryDelay = (attempt: number): number =>
  DELAYS_MS[Math.min(Math.max(0, attempt), DELAYS_MS.length - 1)]!;

/** Whether a session that ended for `reason` should reconnect by itself. */
export function shouldRetry(
  reason: string,
  tries: { attempts: number; since: number } | undefined,
  now: number,
): boolean {
  if (!TRANSIENT.has(reason)) return false;
  return !tries || now - tries.since < GIVE_UP_MS;
}
