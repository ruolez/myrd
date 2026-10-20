import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, errorMessage } from "../api/client";
import type { Device } from "../api/types";
import { useInterval } from "../lib/hooks";
import { signaling } from "../lib/signaling";
import type { DeviceUpdateStatus } from "../lib/signaling";
import {
  applyUpdateStatus,
  canUpdate,
  isUpdating,
  updateBadge,
  updateErrorMessage,
} from "../lib/update";
import type { UpdateFields } from "../lib/update";
import { Badge, Modal, useToast } from "./ui";

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

const UPDATE_POLL_MS = 5_000;
const UPDATE_KICK_MS = 60_000;

/**
 * Keeps update progress live: applies device.update_status pushes (owner pages
 * pass setDevices) and refetches every 5 s while an attempt is under way or
 * for a minute after kick(), the fallback when pushes don't arrive.
 */
export function useLiveUpdates(
  devices: Device[],
  reload: () => void,
  setDevices?: (fn: (d: Device[]) => Device[]) => void,
): () => void {
  const kickedUntil = useRef(0);
  useEffect(() => {
    if (!setDevices) return;
    return signaling.on("device.update_status", (m) => {
      const msg = m as DeviceUpdateStatus;
      setDevices((list) => list.map((d) => applyUpdateStatus(d, msg)));
    });
  }, [setDevices]);
  useInterval(() => {
    if (document.hidden) return;
    if (devices.some(isUpdating) || Date.now() < kickedUntil.current) reload();
  }, UPDATE_POLL_MS);
  return useCallback(() => {
    kickedUntil.current = Date.now() + UPDATE_KICK_MS;
  }, []);
}

export function DeviceUpdateBadge({ device }: { device: UpdateFields }) {
  const b = updateBadge(device);
  if (!b) return null;
  return (
    <span title={b.detail || undefined} className="inline-flex max-w-full">
      <Badge tone={b.tone}>{b.label}</Badge>
    </span>
  );
}

/**
 * "Update now". While the device is in a session it asks whether to wait for
 * the session to end or to force the update, which ends the session.
 */
export function UpdateButton({
  device,
  request,
  onRequested,
  className = "btn-text btn-sm",
  label = "Update now",
}: {
  device: UpdateFields & Pick<Device, "name" | "busy">;
  request: (force: boolean) => Promise<{ version: string }>;
  onRequested: () => void;
  className?: string;
  label?: string;
}) {
  const toast = useToast();
  const [asking, setAsking] = useState(false);
  const [sending, setSending] = useState(false);
  if (!canUpdate(device)) return null;

  const send = async (force: boolean) => {
    setAsking(false);
    setSending(true);
    try {
      const { version } = await request(force);
      toast(
        device.busy && !force
          ? `${device.name} installs ${version} when the session ends`
          : `${device.name} is updating to ${version}`,
        "success",
      );
      onRequested();
    } catch (e) {
      toast(
        e instanceof ApiError ? updateErrorMessage(e.code, e.message) : errorMessage(e),
        "error",
      );
    } finally {
      setSending(false);
    }
  };

  return (
    <>
      <button
        type="button"
        className={className}
        disabled={sending}
        onClick={() => (device.busy ? setAsking(true) : void send(false))}
      >
        {label}
      </button>
      {asking && (
        <Modal
          title={`Update ${device.name}?`}
          onClose={() => setAsking(false)}
          footer={
            <>
              <button className="btn-text" onClick={() => setAsking(false)}>
                Cancel
              </button>
              <button className="btn-outline" onClick={() => void send(false)}>
                Update when idle
              </button>
              <button className="btn-danger" onClick={() => void send(true)}>
                Force now (ends the current session)
              </button>
            </>
          }
        >
          <p className="text-muted">
            {device.name} is in a remote session. It can install the update once the session ends,
            or right away, which ends the session for whoever is connected.
          </p>
        </Modal>
      )}
    </>
  );
}

export function PathBadge({ path }: { path: string | null }) {
  if (!path) return <span className="text-muted">—</span>;
  return (
    <Badge tone={path === "relay" ? "relay" : path === "host" ? "success" : "primary"}>
      {path}
    </Badge>
  );
}
