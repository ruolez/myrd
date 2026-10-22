// Device health from device.info (PROTOCOL §3): compact wording for tiles and
// thresholds shared by tiles, the list, the device page and "Needs attention".

import type { DiskInfo, HostInfo } from "../api/types";

const GB = 1024 ** 3;

/** "3d", "5h", "12m" (largest unit only). */
function short(s: number): string {
  if (s >= 86_400) return `${Math.floor(s / 86_400)}d`;
  if (s >= 3_600) return `${Math.floor(s / 3_600)}h`;
  return `${Math.max(1, Math.floor(s / 60))}m`;
}

/** "active" within the last minute, else "idle 12m". */
export function formatIdle(s: number | null): string {
  if (s === null) return "";
  return s < 60 ? "active" : `idle ${short(s)}`;
}

/** "3d 4h", "5h 12m", "12m". */
export function formatUptime(s: number): string {
  const d = Math.floor(s / 86_400);
  const h = Math.floor((s % 86_400) / 3_600);
  const m = Math.floor((s % 3_600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}

export type Severity = "ok" | "warning" | "error";

export const freeFraction = (d: DiskInfo) => (d.total > 0 ? d.free / d.total : 1);

/** Red below 5% or 2 GB free (whichever comes first on small disks), amber below 10%. */
export function diskSeverity(d: DiskInfo): Severity {
  const f = freeFraction(d);
  if (f < 0.05 || d.free < 2 * GB) return "error";
  if (f < 0.1) return "warning";
  return "ok";
}

export function memSeverity(info: HostInfo): Severity {
  const f = info.mem_total > 0 ? info.mem_used / info.mem_total : 0;
  return f >= 0.95 ? "error" : f >= 0.85 ? "warning" : "ok";
}

/** The fullest disk (least free share; less free space breaks ties). */
export function lowestFreeDisk(disks: DiskInfo[]): DiskInfo | null {
  let best: DiskInfo | null = null;
  for (const d of disks) {
    if (
      !best ||
      freeFraction(d) < freeFraction(best) ||
      (freeFraction(d) === freeFraction(best) && d.free < best.free)
    )
      best = d;
  }
  return best;
}

/** "alice · idle 5m", "Locked · bob", "Nobody signed in". */
export function tileHealthLine(info: HostInfo): string {
  const parts: string[] = [];
  if (info.locked) parts.push("Locked");
  if (info.user === "") parts.push("Nobody signed in");
  else if (info.user) parts.push(info.user);
  const idle = formatIdle(info.idle_s);
  if (idle && !info.locked && info.user !== "") parts.push(idle);
  return parts.join(" · ");
}

export const INFO_STALE_MS = 5 * 60_000;

/** The report no longer describes the device: offline, or none for 5 minutes. */
export function infoStale(infoAt: string | null, online: boolean, now: number): boolean {
  return !online || !infoAt || now - Date.parse(infoAt) > INFO_STALE_MS;
}

/** "Macintosh HD (/)", "Data (D:)", or the mount point alone. */
export const diskLabel = (d: DiskInfo) => d.name || (d.mount === "/" ? "Startup disk" : d.mount);
