// Pure updates of the device list from pushes (PROTOCOL §3 browser extras).

import type { Device, HostInfo } from "../api/types";
import type { SignalMessage } from "./signaling";

/** Applies a device.status push; `busy` is absent on older servers. Unchanged: the same list. */
export function applyStatus(devices: Device[], m: SignalMessage): Device[] {
  const id = m.device_id as string;
  const status = m.status as string;
  const i = devices.findIndex((d) => d.id === id);
  if (i < 0) return devices;
  if (status === "removed") return devices.filter((d) => d.id !== id);
  const d = devices[i] as Device;
  const online = Boolean(m.online);
  const next = {
    online,
    status: status as Device["status"],
    last_seen_at: (m.last_seen_at as string | undefined) ?? d.last_seen_at,
    busy: !online ? false : typeof m.busy === "boolean" ? m.busy : d.busy,
  };
  if ((Object.keys(next) as (keyof typeof next)[]).every((k) => next[k] === d[k])) return devices;
  const out = devices.slice();
  out[i] = { ...d, ...next };
  return out;
}

/** Applies a patch to the listed devices (optimistic edits). */
export function patchDevices(
  devices: Device[],
  ids: ReadonlySet<string>,
  fn: (d: Device) => Device,
): Device[] {
  return devices.map((d) => (ids.has(d.id) ? fn(d) : d));
}

/** Applies a device.info push (PROTOCOL §3). */
export function applyInfo(devices: Device[], m: SignalMessage): Device[] {
  const id = m.device_id as string;
  if (!m.info || typeof m.info !== "object") return devices;
  if (!devices.some((d) => d.id === id)) return devices;
  return devices.map((d) =>
    d.id === id ? { ...d, info: m.info as HostInfo, info_at: (m.at as string) ?? d.info_at } : d,
  );
}

/** What session chrome shows of a device. */
export type DeviceLabel = { name: string; os: Device["os"]; user: string | null };

export function deviceLabels(devices: Device[]): Map<string, DeviceLabel> {
  return new Map(
    devices.map((d) => [d.id, { name: d.name, os: d.os, user: d.info?.user ?? null }]),
  );
}

/** Whether two label maps show the same, so health reports need not re-render session chrome. */
export function sameLabels(a: Map<string, DeviceLabel>, b: Map<string, DeviceLabel>): boolean {
  if (a.size !== b.size) return false;
  for (const [id, l] of a) {
    const o = b.get(id);
    if (!o || o.name !== l.name || o.os !== l.os || o.user !== l.user) return false;
  }
  return true;
}
