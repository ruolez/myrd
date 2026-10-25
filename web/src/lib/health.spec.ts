import { describe, expect, test } from "vitest";
import type { DiskInfo, HostInfo } from "../api/types";
import {
  agentMemory,
  diskLabel,
  diskSeverity,
  formatIdle,
  formatUptime,
  infoStale,
  lowestFreeDisk,
  memSeverity,
  tileHealthLine,
} from "./health";

const GB = 1024 ** 3;
const disk = (total: number, free: number, mount = "/x", name = ""): DiskInfo => ({
  name,
  mount,
  fs: "apfs",
  total: total * GB,
  free: free * GB,
});
const info = (over: Partial<HostInfo>): HostInfo => ({
  os_name: "macOS",
  os_version: "15.1",
  os_build: "",
  user: "alice",
  idle_s: 0,
  uptime_s: 0,
  locked: false,
  cpu_pct: null,
  cpu_count: 8,
  mem_total: 16 * GB,
  mem_used: 8 * GB,
  disks: [],
  ...over,
});

describe("formatIdle", () => {
  test.each([
    [null, ""],
    [0, "active"],
    [59, "active"],
    [60, "idle 1m"],
    [3 * 3600 + 5, "idle 3h"],
    [2 * 86_400, "idle 2d"],
  ])("%s → %j", (s, want) => {
    expect(formatIdle(s)).toBe(want);
  });
});

describe("formatUptime", () => {
  test.each([
    [59, "0m"],
    [12 * 60, "12m"],
    [5 * 3600 + 12 * 60, "5h 12m"],
    [3 * 86_400 + 4 * 3600 + 59, "3d 4h"],
  ])("%d → %s", (s, want) => {
    expect(formatUptime(s)).toBe(want);
  });
});

describe("diskSeverity", () => {
  test.each([
    [disk(500, 100), "ok"],
    [disk(500, 40), "warning"],
    [disk(500, 20), "error"],
    [disk(20, 1.5), "error"], // under 2 GB free even though 7.5%
    [disk(100, 10), "ok"], // exactly 10% is still fine
  ])("%#", (d, want) => {
    expect(diskSeverity(d)).toBe(want);
  });
});

describe("memSeverity", () => {
  test.each([
    [8, "ok"],
    [14, "warning"],
    [15.5, "error"],
  ])("%d of 16 GB used → %s", (used, want) => {
    expect(memSeverity(info({ mem_used: used * GB }))).toBe(want);
  });
});

describe("lowestFreeDisk", () => {
  test("picks the smallest free share, ties by free space", () => {
    const a = disk(100, 50, "/a");
    const b = disk(1000, 100, "/b");
    const c = disk(200, 20, "/c");
    expect(lowestFreeDisk([a, b, c])?.mount).toBe("/c");
    expect(lowestFreeDisk([])).toBe(null);
  });
});

describe("tileHealthLine", () => {
  test.each([
    [info({ idle_s: 320 }), "alice · idle 5m"],
    [info({ idle_s: 10 }), "alice · active"],
    [info({ locked: true, idle_s: 900 }), "Locked · alice"],
    [info({ user: "", idle_s: null }), "Nobody signed in"],
    [info({ user: null, idle_s: null, locked: null }), ""],
  ])("%#", (i, want) => {
    expect(tileHealthLine(i)).toBe(want);
  });
});

describe("infoStale", () => {
  const now = Date.parse("2026-10-22T12:00:00Z");
  test.each([
    ["2026-10-22T11:59:00Z", true, false],
    ["2026-10-22T11:50:00Z", true, true],
    ["2026-10-22T11:59:00Z", false, true],
    [null, true, true],
  ])("%s online=%s → %s", (at, online, want) => {
    expect(infoStale(at, online, now)).toBe(want);
  });
});

describe("diskLabel", () => {
  test.each([
    [disk(1, 1, "/", ""), "Startup disk"],
    [disk(1, 1, "C:\\", "Local Disk (C:)"), "Local Disk (C:)"],
    [disk(1, 1, "/data", ""), "/data"],
  ])("%#", (d, want) => {
    expect(diskLabel(d)).toBe(want);
  });
});

describe("agentMemory", () => {
  const MB = 1024 ** 2;
  test.each([
    ["the agent alone", { main: 48 * MB, go_heap: 9 * MB }, "48 MB"],
    [
      "with a capture worker",
      { main: 48 * MB, go_heap: 9 * MB, capture: 260 * MB },
      "308 MB (agent 48 MB · screen capture 260 MB)",
    ],
    [
      "Windows helper, worker and service",
      { main: 40 * MB, go_heap: 8 * MB, capture: 200 * MB, service: 16 * MB },
      "256 MB (agent 40 MB · screen capture 200 MB · service 16 MB)",
    ],
    [
      "Windows idle",
      { main: 40 * MB, go_heap: 8 * MB, service: 16 * MB },
      "56 MB (agent 40 MB · service 16 MB)",
    ],
  ])("%s", (_, mem, want) => {
    expect(agentMemory(mem)).toBe(want);
  });
});
