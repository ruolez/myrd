import { useRef } from "react";
import type { Device } from "../api/types";
import { previewsPossible, usePreview, useWatchPreview } from "../app/previews";
import { useAuth } from "../app/auth";
import { timeAgo } from "../lib/format";
import { OsIcon } from "./icons";

/** Older than this (or from an offline device) the picture is dimmed and dated. */
const STALE_MS = 2 * 60_000;

export const previewReasonText: Record<string, string> = {
  previews_off: "Previews are off on the device",
  paused: "Paused on the device",
  updating: "Updating",
  untrusted: "Connect once from this browser to see previews",
  unpinned: "Connect once to enable previews",
  identity_changed: "Device identity changed",
  bad_key: "Preview key not accepted",
  ask_every_time: "“Ask every time” is on",
  no_permission: "No screen recording permission",
  in_session: "In a session",
  locked: "Locked",
  display_off: "Display asleep",
  capture_failed: "No preview right now",
  unsupported: "This device cannot send previews",
  no_console: "Nobody signed in",
};

/** Why a device shows no preview, from what the dashboard already knows. */
function localReason(d: Device, adminOn: boolean): string | null {
  if (!adminOn) return null;
  if (!d.previews) return "Previews are off for this device";
  if (!d.features.includes("preview")) return d.online ? "Update the agent to see previews" : null;
  return null;
}

/**
 * A device's latest screen preview filling its parent (16:9), or its OS
 * artwork with a short reason when there is none. Watches while on screen.
 */
export function PreviewImage({
  device: d,
  now,
  compact,
}: {
  device: Device;
  now: number;
  compact?: boolean;
}) {
  const { config } = useAuth();
  const adminOn =
    (config?.features?.includes("previews") ?? false) && config?.screen_previews !== false;
  const ref = useRef<HTMLSpanElement>(null);
  const possible = previewsPossible(d, adminOn);
  useWatchPreview(d.id, ref, possible);
  const p = usePreview(d.id);
  const age = p.capturedAt === null ? null : now - p.capturedAt;
  const stale = age !== null && (age > STALE_MS || !d.online);
  const reason = possible
    ? p.reason
      ? (previewReasonText[p.reason] ?? null)
      : null
    : localReason(d, adminOn);
  const showUrl = possible && p.url;

  return (
    <span ref={ref} className="absolute inset-0 block">
      {showUrl ? (
        <img
          src={p.url ?? undefined}
          alt={`Screen of ${d.name}, captured ${timeAgo(new Date(p.capturedAt ?? 0).toISOString(), now)}`}
          className={`h-full w-full object-cover transition-[filter,opacity] motion-reduce:transition-none ${
            stale ? "opacity-60 grayscale" : ""
          }`}
          draggable={false}
        />
      ) : (
        <span
          className={`absolute inset-0 flex items-center justify-center ${d.online ? "text-primary" : "text-muted"}`}
        >
          <OsIcon os={d.os} size={compact ? 32 : 44} />
        </span>
      )}
      {(stale || reason) && !compact && (
        <span className="absolute top-2 right-10 left-10 flex justify-center">
          <span className="max-w-full truncate rounded-full bg-black/60 px-2 py-0.5 text-[11px] text-white">
            {reason ?? `captured ${timeAgo(new Date(p.capturedAt ?? 0).toISOString(), now)}`}
          </span>
        </span>
      )}
    </span>
  );
}
