import { describe, expect, test } from "vitest";
import type { DeviceId } from "../api/types";
import type { DeviceUpdateStatus } from "./signaling";
import {
  applyUpdateStatus,
  canUpdate,
  isUpdating,
  updateBadge,
  updateErrorMessage,
} from "./update";
import type { UpdateFields } from "./update";

const CURRENT = "0.3.0";
const LATEST = "0.3.1";
const AT = "2026-10-20T12:00:00Z";
const ID = "dev-1" as DeviceId;

const device: UpdateFields = {
  id: ID,
  os: "darwin",
  online: true,
  status: "online",
  agent_version: CURRENT,
  latest_version: LATEST,
  update_available: true,
  update_state: "",
  update_from: "",
  update_version: "",
  update_error: "",
  update_at: null,
};

const attempt = (state: UpdateFields["update_state"], extra: Partial<UpdateFields> = {}) => ({
  ...device,
  update_state: state,
  update_from: CURRENT,
  update_version: LATEST,
  update_at: AT,
  ...extra,
});

describe("updateBadge", () => {
  test.each<[string, UpdateFields, ReturnType<typeof updateBadge>]>([
    ["newer release published", device, { label: `Update available ${LATEST}`, tone: "primary" }],
    [
      "on the newest release",
      { ...device, agent_version: LATEST, update_available: false },
      { label: "Up to date", tone: "success" },
    ],
    ["checking", attempt("checking"), { label: "Checking for updates", tone: "primary" }],
    ["downloading", attempt("downloading"), { label: `Downloading ${LATEST}`, tone: "primary" }],
    [
      "waiting for the session",
      attempt("waiting_idle"),
      {
        label: "Waiting for session to end",
        tone: "warning",
        detail: `Installs ${LATEST} when the current session ends.`,
      },
    ],
    ["installing", attempt("installing"), { label: `Installing ${LATEST}`, tone: "primary" }],
    [
      "installing while the agent restarts offline",
      attempt("installing", { online: false }),
      { label: `Installing ${LATEST}`, tone: "primary" },
    ],
    [
      "failed with an error",
      attempt("failed", { update_error: "sha256 mismatch" }),
      { label: "Update failed: sha256 mismatch", tone: "error", detail: "sha256 mismatch" },
    ],
    [
      "failed without an error",
      attempt("failed"),
      { label: "Update failed", tone: "error", detail: "" },
    ],
    [
      "rolled back",
      attempt("rolled_back", { update_error: "no hello_ack within 5 min" }),
      { label: `Rolled back from ${LATEST}`, tone: "error", detail: "no hello_ack within 5 min" },
    ],
    [
      "agent says up to date",
      attempt("up_to_date", { update_available: false, update_version: "" }),
      { label: "Up to date", tone: "success" },
    ],
  ])("%s", (_name, d, want) => {
    expect(updateBadge(d)).toEqual(want);
  });

  test("a long failure is shortened in the label and kept whole in the detail", () => {
    const error = "x".repeat(80);
    expect(updateBadge(attempt("failed", { update_error: error }))).toEqual({
      label: `Update failed: ${"x".repeat(47)}…`,
      tone: "error",
      detail: error,
    });
  });

  test("an install that reached its version reads as done even before the server clears it", () => {
    const d = attempt("installing", { agent_version: LATEST, update_available: false });
    expect(updateBadge(d)).toEqual({ label: "Up to date", tone: "success" });
  });

  test("a check or download abandoned by an offline agent falls back to availability", () => {
    expect(
      (["checking", "downloading", "waiting_idle"] as const).map((s) =>
        updateBadge(attempt(s, { online: false })),
      ),
    ).toEqual(Array(3).fill({ label: `Update available ${LATEST}`, tone: "primary" }));
  });

  test("no badge without any published release or on the Linux demo agent", () => {
    expect([
      updateBadge({ ...device, latest_version: null, update_available: false }),
      updateBadge({ ...device, os: "linux" }),
    ]).toEqual([null, null]);
  });
});

describe("isUpdating", () => {
  test.each<[UpdateFields["update_state"], boolean, boolean]>([
    ["checking", true, true],
    ["downloading", true, true],
    ["waiting_idle", true, true],
    ["installing", true, true],
    ["installing", false, true],
    ["downloading", false, false],
    ["failed", true, false],
    ["rolled_back", true, false],
    ["up_to_date", true, false],
    ["", true, false],
  ])("%j, online %s → %s", (state, online, want) => {
    expect(isUpdating(attempt(state, { online }))).toBe(want);
  });

  test("an install that already reached its version is over", () => {
    expect(isUpdating(attempt("installing", { agent_version: LATEST }))).toBe(false);
  });
});

describe("canUpdate", () => {
  test.each<[string, UpdateFields, boolean]>([
    ["outdated and online", device, true],
    ["up to date", { ...device, update_available: false }, false],
    ["offline", { ...device, online: false, status: "offline" }, false],
    ["disabled", { ...device, status: "disabled" }, false],
    ["linux demo", { ...device, os: "linux" }, false],
    ["already downloading", attempt("downloading"), false],
    ["already installing", attempt("installing"), false],
    ["waiting for the session, so it can be forced", attempt("waiting_idle"), true],
    ["retry after a failure", attempt("failed", { update_available: false }), true],
    ["retry after a rollback", attempt("rolled_back", { update_available: false }), true],
  ])("%s → %s", (_name, d, want) => {
    expect(canUpdate(d)).toBe(want);
  });
});

describe("applyUpdateStatus", () => {
  const msg = (m: Partial<DeviceUpdateStatus>): DeviceUpdateStatus => ({
    v: 1,
    type: "device.update_status",
    device_id: ID,
    state: "downloading",
    from: CURRENT,
    version: LATEST,
    at: AT,
    ...m,
  });

  test("records the reported attempt on the matching device", () => {
    expect(applyUpdateStatus(device, msg({}))).toEqual(attempt("downloading"));
  });

  test("keeps the error of a failure and clears it on the next state", () => {
    const failed = applyUpdateStatus(device, msg({ state: "failed", error: "disk full" }));
    expect([failed, applyUpdateStatus(failed, msg({ state: "checking" }))]).toEqual([
      attempt("failed", { update_error: "disk full" }),
      attempt("checking"),
    ]);
  });

  test("up_to_date from the agent means nothing newer applies", () => {
    expect(applyUpdateStatus(device, msg({ state: "up_to_date", version: undefined }))).toEqual(
      attempt("up_to_date", { update_version: "", update_available: false }),
    );
  });

  test("ignores other devices and unknown states", () => {
    const other = applyUpdateStatus(device, msg({ device_id: "dev-2" }));
    const unknown = applyUpdateStatus(device, msg({ state: "exploding" }));
    expect([other, unknown]).toEqual([device, device]);
  });
});

describe("updateErrorMessage", () => {
  test.each([
    ["device_offline", "The device is offline. It checks for updates when it reconnects."],
    ["up_to_date", "The device already runs the newest release."],
    ["unsupported", "This device cannot update itself remotely."],
    ["forbidden", "server text"],
  ])("%s", (code, want) => {
    expect(updateErrorMessage(code, "server text")).toBe(want);
  });
});
