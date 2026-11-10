import { describe, expect, test } from "vitest";
import { GIVE_UP_MS, retryDelay, shouldRetry } from "./termRetry";

describe("retryDelay", () => {
  test("backs off, then stays at 10 s", () => {
    expect([0, 1, 2, 3, 4, 5, 6, 7, 50].map(retryDelay)).toEqual([
      500, 1000, 2000, 3000, 5000, 8000, 10_000, 10_000, 10_000,
    ]);
  });

  test("never grows smaller", () => {
    for (let a = 0; a < 20; a++) expect(retryDelay(a + 1)).toBeGreaterThanOrEqual(retryDelay(a));
  });
});

describe("shouldRetry", () => {
  const T = 1_000_000;
  test.each([
    ["ice_failed", true],
    ["peer_disconnected", true],
    ["device_restart", true],
    ["device_busy", false],
    ["taken_over", false],
    ["ended_remotely", false],
    ["user_hangup", false],
    ["agent_kill_switch", false],
    ["not_approved", false],
    ["bad_signature", false],
    ["terminal_disabled", false],
  ])("%s → %s", (reason, want) => {
    expect(shouldRetry(reason, undefined, T)).toBe(want);
  });

  test("waits out a busy device only while retrying a dropped session", () => {
    expect([
      shouldRetry("device_busy", undefined, T),
      shouldRetry("device_busy", { attempts: 1, since: T }, T),
    ]).toEqual([false, true]);
  });

  test("gives up a quarter hour after the first drop", () => {
    expect([
      shouldRetry("ice_failed", { attempts: 9, since: T - GIVE_UP_MS + 1 }, T),
      shouldRetry("ice_failed", { attempts: 9, since: T - GIVE_UP_MS }, T),
    ]).toEqual([true, false]);
  });
});
