import { describe, expect, test } from "vitest";
import { bytes, duration, pct, timeAgo } from "./format";

describe("timeAgo", () => {
  const now = Date.parse("2026-10-19T12:00:00Z");
  test.each([
    [null, "never"],
    ["2026-10-19T11:59:58Z", "just now"],
    ["2026-10-19T11:59:30Z", "30s ago"],
    ["2026-10-19T11:15:00Z", "45m ago"],
    ["2026-10-18T12:00:00Z", "24h ago"],
    ["2026-10-10T12:00:00Z", "9d ago"],
  ])("%s → %s", (ts, out) => {
    expect(timeAgo(ts, now)).toBe(out);
  });
});

describe("duration", () => {
  test.each([
    [null, "—"],
    [7, "7s"],
    [125, "2m 5s"],
    [3725, "1h 2m"],
  ])("%s → %s", (s, out) => {
    expect(duration(s)).toBe(out);
  });
});

describe("bytes", () => {
  test.each([
    [0, "0 B"],
    [512, "512 B"],
    [1536, "1.5 KB"],
    [5 * 1024 * 1024 * 1024, "5.0 GB"],
  ])("%d → %s", (n, out) => {
    expect(bytes(n)).toBe(out);
  });
});

describe("pct", () => {
  test.each([
    [null, "—"],
    [0, "0%"],
    [0.034, "3.4%"],
    [0.25, "25%"],
  ])("%s → %s", (f, out) => {
    expect(pct(f)).toBe(out);
  });
});
