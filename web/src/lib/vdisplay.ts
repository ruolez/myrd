// Virtual display on Windows devices without a monitor (PROTOCOL §3
// device.info virtual_display): what the device page offers in each state.

import type { HostInfo, VirtualDisplayAction } from "../api/types";

export type VirtualDisplayButton = { action: VirtualDisplayAction; label: string };

export type VirtualDisplayView = {
  text: string;
  warning: boolean;
  /** An install or removal is under way: no buttons, a "Working…" one instead. */
  busy: boolean;
  buttons: VirtualDisplayButton[];
  /** Why the buttons are disabled, or null. */
  disabled: string | null;
};

const install: VirtualDisplayButton = { action: "install", label: "Install" };
const remove: VirtualDisplayButton = { action: "remove", label: "Remove" };

/**
 * The device page row, or null when it is not shown: the driver cannot run
 * there, or a monitor is attached and the driver was never installed.
 * pending is a request sent since the last report.
 */
export function virtualDisplayView(
  info: Pick<HostInfo, "virtual_display"> | null,
  online: boolean,
  pending: VirtualDisplayAction | null,
): VirtualDisplayView | null {
  const vd = info?.virtual_display;
  if (!vd?.supported || (!vd.headless && vd.driver === "absent")) return null;
  const disabled = online ? null : "The device is offline";
  const view = (text: string, buttons: VirtualDisplayButton[], warning = false) => ({
    text,
    warning,
    busy: false,
    buttons,
    disabled,
  });
  const driver =
    pending === "install" ? "installing" : pending === "remove" ? "removing" : vd.driver;
  switch (driver) {
    case "absent":
      return view(
        "No monitor is attached, so Windows limits this device to its basic screen. A virtual display gives it any resolution; pick one from Stream settings → Screen resolution during a session.",
        [install],
      );
    case "installing":
    case "removing":
      return {
        ...view(
          driver === "installing"
            ? "Installing the virtual display…"
            : "Removing the virtual display…",
          [],
        ),
        busy: true,
      };
    case "installed":
      return view(
        vd.active
          ? "Installed. The device's screen is the virtual display; change its resolution from Stream settings during a session."
          : "Installed; waiting for Windows to show it.",
        [remove],
      );
    case "failed":
      return view(
        vd.error ? `The last attempt failed: ${vd.error}` : "The last attempt failed.",
        [{ action: "install", label: "Try again" }, remove],
        true,
      );
  }
}

/** Readable text for the 409s of POST …/devices/{id}/virtual-display. */
export function virtualDisplayErrorMessage(code: string, message: string): string {
  switch (code) {
    case "device_offline":
      return "The device is offline.";
    case "unsupported":
      return "Only Windows devices can have a virtual display.";
    default:
      return message;
  }
}
