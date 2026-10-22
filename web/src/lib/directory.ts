// Pure updates of the device list from pushes (PROTOCOL §3 browser extras).

import type { Device, HostInfo } from "../api/types";
import type { SignalMessage } from "./signaling";

/** Applies a device.status push; `busy` is absent on older servers. */
export function applyStatus(devices: Device[], m: SignalMessage): Device[] {
  const id = m.device_id as string;
  const status = m.status as string;
  if (status === "removed") return devices.filter((d) => d.id !== id);
  return devices.map((d) => {
    if (d.id !== id) return d;
    const online = Boolean(m.online);
    const busy = !online ? false : typeof m.busy === "boolean" ? m.busy : d.busy;
    return {
      ...d,
      online,
      status: status as Device["status"],
      last_seen_at: (m.last_seen_at as string | undefined) ?? d.last_seen_at,
      busy,
    };
  });
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
  return devices.map((d) =>
    d.id === id ? { ...d, info: m.info as HostInfo, info_at: (m.at as string) ?? d.info_at } : d,
  );
}
