import { useEffect } from "react";
import type { Device } from "../api/types";
import { signaling } from "../lib/signaling";
import { Badge } from "./ui";

/** Applies live device.status pushes (PROTOCOL §3 browser extras) to a device list. */
export function useLiveDeviceStatus(
  setDevices: (fn: (d: Device[]) => Device[]) => void,
  onRemoved?: () => void,
) {
  useEffect(
    () =>
      signaling.on("device.status", (m) => {
        const id = m.device_id as string;
        const status = m.status as string;
        if (status === "removed") {
          setDevices((list) => list.filter((d) => d.id !== id));
          onRemoved?.();
          return;
        }
        setDevices((list) =>
          list.map((d) =>
            d.id === id
              ? {
                  ...d,
                  online: Boolean(m.online),
                  status: status as Device["status"],
                  last_seen_at: (m.last_seen_at as string | undefined) ?? d.last_seen_at,
                  busy: m.online ? d.busy : false,
                }
              : d,
          ),
        );
      }),
    [setDevices, onRemoved],
  );
}

export function DeviceStatusBadge({
  device,
}: {
  device: Pick<Device, "status" | "online" | "busy">;
}) {
  if (device.status === "disabled") return <Badge tone="error">Disabled</Badge>;
  if (!device.online) return <Badge dot>Offline</Badge>;
  if (device.busy)
    return (
      <Badge tone="warning" dot>
        In session
      </Badge>
    );
  return (
    <Badge tone="success" dot>
      Online
    </Badge>
  );
}

export function SessionStateBadge({ state }: { state: string }) {
  const tone =
    state === "active"
      ? "success"
      : state === "pending"
        ? "warning"
        : state === "failed"
          ? "error"
          : "neutral";
  return <Badge tone={tone}>{state}</Badge>;
}

export function PathBadge({ path }: { path: string | null }) {
  if (!path) return <span className="text-muted">—</span>;
  return (
    <Badge tone={path === "relay" ? "relay" : path === "host" ? "success" : "primary"}>
      {path}
    </Badge>
  );
}
