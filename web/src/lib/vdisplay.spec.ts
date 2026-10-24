import { describe, expect, test } from "vitest";
import type { VirtualDisplayInfo } from "../api/types";
import { virtualDisplayErrorMessage, virtualDisplayView } from "./vdisplay";

const headless: VirtualDisplayInfo = {
  supported: true,
  headless: true,
  driver: "absent",
  active: false,
};
const installed: VirtualDisplayInfo = { ...headless, driver: "installed", active: true };
const info = (vd: Partial<VirtualDisplayInfo>) => ({
  virtual_display: { ...headless, ...vd },
});
const OFFLINE = "The device is offline";
const INSTALL = { action: "install", label: "Install" };
const REMOVE = { action: "remove", label: "Remove" };
const ABSENT_TEXT =
  "No monitor is attached, so Windows limits this device to its basic screen. A virtual display gives it any resolution; pick one from Stream settings → Screen resolution during a session.";
const ACTIVE_TEXT =
  "Installed. The device's screen is the virtual display; change its resolution from Stream settings during a session.";

describe("virtualDisplayView", () => {
  test.each([
    ["no host info", null],
    ["an agent that does not report it", {}],
    ["an unsupported device", info({ supported: false })],
    ["a device with a monitor and no driver", info({ headless: false })],
  ])("hides the row for %s", (_name, host) => {
    expect(virtualDisplayView(host, true, null)).toBeNull();
  });

  test("offers to install on a headless device", () => {
    expect(virtualDisplayView(info({}), true, null)).toEqual({
      text: ABSENT_TEXT,
      warning: false,
      busy: false,
      buttons: [INSTALL],
      disabled: null,
    });
  });

  test("keeps the row for an installed driver once a monitor is attached", () => {
    expect(virtualDisplayView(info({ ...installed, headless: false }), true, null)).toEqual({
      text: ACTIVE_TEXT,
      warning: false,
      busy: false,
      buttons: [REMOVE],
      disabled: null,
    });
  });

  test("waits for Windows when the driver is installed but not on the desktop", () => {
    expect(virtualDisplayView(info({ ...installed, active: false }), true, null)).toEqual({
      text: "Installed; waiting for Windows to show it.",
      warning: false,
      busy: false,
      buttons: [REMOVE],
      disabled: null,
    });
  });

  test.each([
    ["installing", "Installing the virtual display…"],
    ["removing", "Removing the virtual display…"],
  ] as const)("is busy while %s", (driver, text) => {
    expect(virtualDisplayView(info({ driver }), true, null)).toEqual({
      text,
      warning: false,
      busy: true,
      buttons: [],
      disabled: null,
    });
  });

  test.each([
    ["install", info({}), "Installing the virtual display…"],
    ["remove", info(installed), "Removing the virtual display…"],
  ] as const)("shows a pending %s before the device reports it", (pending, host, text) => {
    expect(virtualDisplayView(host, true, pending)).toMatchObject({
      text,
      busy: true,
      buttons: [],
    });
  });

  test.each([
    [
      "with the reason",
      "driver signature rejected",
      "The last attempt failed: driver signature rejected",
    ],
    ["without a reason", undefined, "The last attempt failed."],
  ])("offers both actions after a failure %s", (_name, error, text) => {
    expect(virtualDisplayView(info({ driver: "failed", error }), true, null)).toEqual({
      text,
      warning: true,
      busy: false,
      buttons: [{ action: "install", label: "Try again" }, REMOVE],
      disabled: null,
    });
  });

  test("disables the buttons of an offline device", () => {
    expect(virtualDisplayView(info(installed), false, null)).toEqual({
      text: ACTIVE_TEXT,
      warning: false,
      busy: false,
      buttons: [REMOVE],
      disabled: OFFLINE,
    });
  });
});

describe("virtualDisplayErrorMessage", () => {
  test("explains the known refusals and passes others through", () => {
    expect([
      virtualDisplayErrorMessage("device_offline", "device is offline"),
      virtualDisplayErrorMessage("unsupported", "only Windows"),
      virtualDisplayErrorMessage("internal", "server error"),
    ]).toEqual([
      "The device is offline.",
      "Only Windows devices can have a virtual display.",
      "server error",
    ]);
  });
});
