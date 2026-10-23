import { describe, expect, test } from "vitest";
import type { Device, DeviceId } from "../api/types";
import { applyInfo, applyStatus, deviceLabels, patchDevices, sameLabels } from "./directory";
import type { SignalMessage } from "./signaling";

const dev = (id: string, over: Partial<Device> = {}) =>
  ({
    id: id as DeviceId,
    name: id,
    online: true,
    status: "online",
    busy: false,
    last_seen_at: "2026-10-01T00:00:00Z",
    favorite: false,
    ...over,
  }) as Device;

const push = (fields: Record<string, unknown>) =>
  ({ v: 1, type: "device.status", ...fields }) as SignalMessage;

describe("applyStatus", () => {
  test("takes busy from the push", () => {
    const [d] = applyStatus(
      [dev("a")],
      push({ device_id: "a", online: true, status: "online", busy: true }),
    );
    expect(d).toMatchObject({ online: true, busy: true });
  });

  test("keeps busy when an older server leaves it out", () => {
    const [d] = applyStatus(
      [dev("a", { busy: true })],
      push({ device_id: "a", online: true, status: "online" }),
    );
    expect(d?.busy).toBe(true);
  });

  test("an offline device is never busy and keeps its last seen time when none is sent", () => {
    const [d] = applyStatus(
      [dev("a", { busy: true })],
      push({ device_id: "a", online: false, status: "offline", busy: true }),
    );
    expect(d).toMatchObject({
      online: false,
      status: "offline",
      busy: false,
      last_seen_at: "2026-10-01T00:00:00Z",
    });
  });

  test.each([
    ["an unlisted device", push({ device_id: "zz", online: true, status: "online", busy: false })],
    [
      "a push that changes nothing",
      push({
        device_id: "a",
        online: true,
        status: "online",
        busy: false,
        last_seen_at: "2026-10-01T00:00:00Z",
      }),
    ],
    ["removing an unlisted device", push({ device_id: "zz", status: "removed" })],
  ])("returns the same list for %s", (_, m) => {
    const list = [dev("a"), dev("b")];
    expect(applyStatus(list, m)).toBe(list);
  });

  test("removed drops the device; other devices are untouched", () => {
    const b = dev("b");
    expect(applyStatus([dev("a"), b], push({ device_id: "a", status: "removed" }))).toEqual([b]);
  });
});

describe("patchDevices", () => {
  test("changes only the listed devices", () => {
    const out = patchDevices([dev("a"), dev("b")], new Set(["b"]), (d) => ({
      ...d,
      favorite: true,
    }));
    expect(out.map((d) => d.favorite)).toEqual([false, true]);
  });
});

describe("applyInfo", () => {
  test("stores the report and its time on that device only", () => {
    const info = { os_name: "macOS", disks: [] };
    const out = applyInfo([dev("a"), dev("b")], {
      v: 1,
      type: "device.info",
      device_id: "b",
      info,
      at: "2026-10-22T10:00:00Z",
    } as SignalMessage);
    expect(out.map((d) => [d.info ?? null, d.info_at ?? null])).toEqual([
      [null, null],
      [info, "2026-10-22T10:00:00Z"],
    ]);
  });

  test("ignores a push without a report", () => {
    const list = [dev("a")];
    expect(applyInfo(list, { v: 1, type: "device.info", device_id: "a" } as SignalMessage)).toBe(
      list,
    );
  });
});

describe("applyInfo for devices not listed", () => {
  test("returns the same list", () => {
    const list = [dev("a")];
    const m = { v: 1, type: "device.info", device_id: "zz", info: { disks: [] } } as SignalMessage;
    expect(applyInfo(list, m)).toBe(list);
  });
});

describe("sameLabels", () => {
  const info = (user: string, cpu: number) => ({ user, cpu_pct: cpu, disks: [] }) as never;

  test("a health report that keeps name, OS and user changes nothing", () => {
    const before = deviceLabels([dev("a", { os: "darwin", info: info("ann", 3) })]);
    const after = deviceLabels([dev("a", { os: "darwin", info: info("ann", 97) })]);
    expect(sameLabels(before, after)).toBe(true);
  });

  test.each([
    ["a rename", dev("a", { name: "renamed" })],
    ["another user", dev("a", { info: info("bob", 3) })],
    ["another OS", dev("a", { os: "windows" })],
  ])("%s is a change", (_, changed) => {
    const before = deviceLabels([dev("a", { info: info("ann", 3) })]);
    expect(sameLabels(before, deviceLabels([changed]))).toBe(false);
  });

  test("an added device is a change", () => {
    expect(sameLabels(deviceLabels([dev("a")]), deviceLabels([dev("a"), dev("b")]))).toBe(false);
  });
});
