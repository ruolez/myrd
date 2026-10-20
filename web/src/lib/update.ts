// Agent update state shown on device pages (PROTOCOL §1 Device update_*, §7).

import type { Device, UpdateState } from "../api/types";
import type { DeviceUpdateStatus } from "./signaling";
import { compareVersions } from "./version";

export type UpdateFields = Pick<
  Device,
  | "id"
  | "os"
  | "online"
  | "status"
  | "agent_version"
  | "latest_version"
  | "update_available"
  | "update_state"
  | "update_from"
  | "update_version"
  | "update_error"
  | "update_at"
>;

export type UpdateBadge = {
  label: string;
  tone: "neutral" | "primary" | "success" | "error" | "warning";
  detail?: string;
};

const STATES: readonly UpdateState[] = [
  "checking",
  "downloading",
  "waiting_idle",
  "installing",
  "up_to_date",
  "failed",
  "rolled_back",
];
// Steps the agent abandons when it disconnects; installing restarts it by design.
const LIVE_ONLY: readonly string[] = ["checking", "downloading", "waiting_idle"];
const IN_PROGRESS: readonly string[] = [...LIVE_ONLY, "installing"];
const MAX_ERROR_LABEL = 47;

/** The attempt state still worth showing: "" once the agent runs the target or gave up offline. */
function currentState(d: UpdateFields): UpdateState | "" {
  const s = d.update_state;
  if (!s || s === "up_to_date") return s;
  if (d.update_version && compareVersions(d.agent_version, d.update_version) >= 0) return "";
  if (!d.online && LIVE_ONLY.includes(s)) return "";
  return s;
}

export function updateBadge(d: UpdateFields): UpdateBadge | null {
  if (d.os === "linux") return null;
  const v = d.update_version;
  const error = d.update_error ?? "";
  switch (currentState(d)) {
    case "checking":
      return { label: "Checking for updates", tone: "primary" };
    case "downloading":
      return { label: `Downloading ${v}`, tone: "primary" };
    case "waiting_idle":
      return {
        label: "Waiting for session to end",
        tone: "warning",
        detail: `Installs ${v} when the current session ends.`,
      };
    case "installing":
      return { label: `Installing ${v}`, tone: "primary" };
    case "failed": {
      const short = error.length > MAX_ERROR_LABEL ? `${error.slice(0, MAX_ERROR_LABEL)}…` : error;
      return {
        label: short ? `Update failed: ${short}` : "Update failed",
        tone: "error",
        detail: error,
      };
    }
    case "rolled_back":
      return { label: v ? `Rolled back from ${v}` : "Rolled back", tone: "error", detail: error };
  }
  if (!d.latest_version) return null;
  if (d.update_available) return { label: `Update available ${d.latest_version}`, tone: "primary" };
  return { label: "Up to date", tone: "success" };
}

/** An attempt is under way (the page polls while any device is in this state). */
export function isUpdating(d: UpdateFields): boolean {
  return IN_PROGRESS.includes(currentState(d));
}

/** "Update now" applies: online, self-updating, and outdated or retrying a failed attempt. */
export function canUpdate(d: UpdateFields): boolean {
  if (d.os === "linux" || !d.online || d.status === "disabled") return false;
  const s = currentState(d);
  if (s === "checking" || s === "downloading" || s === "installing") return false;
  return d.update_available || s === "waiting_idle" || s === "failed" || s === "rolled_back";
}

/** Applies a device.update_status push to one device. */
export function applyUpdateStatus<D extends UpdateFields>(d: D, m: DeviceUpdateStatus): D {
  if (m.device_id !== d.id || !STATES.includes(m.state as UpdateState)) return d;
  const state = m.state as UpdateState;
  return {
    ...d,
    update_state: state,
    update_from: m.from ?? "",
    update_version: m.version ?? "",
    update_error: m.error ?? "",
    update_at: m.at || d.update_at,
    update_available: state === "up_to_date" ? false : d.update_available,
  };
}

/** Readable text for the 409s of POST …/devices/{id}/update. */
export function updateErrorMessage(code: string, message: string): string {
  switch (code) {
    case "device_offline":
      return "The device is offline. It checks for updates when it reconnects.";
    case "up_to_date":
      return "The device already runs the newest release.";
    case "unsupported":
      return "This device cannot update itself remotely.";
    default:
      return message;
  }
}
