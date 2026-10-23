import { describe, expect, test } from "vitest";
import { canRestart, POWER_AGENT, powerActions, powerFailure } from "./power";

describe("canRestart", () => {
  test.each([
    ["windows", POWER_AGENT, true],
    ["darwin", "1.0.0", true],
    ["windows", "0.5.3", false],
    ["linux", POWER_AGENT, false],
    ["windows", "", false],
  ])("%s %s → %s", (os, agent_version, want) => {
    expect(canRestart({ os, agent_version })).toBe(want);
  });
});

describe("powerActions", () => {
  test("keeps the known actions the hello lists", () => {
    expect(powerActions(["stream", "power"], ["restart", "shutdown", "restart_agent", 3])).toEqual([
      "restart",
      "restart_agent",
    ]);
  });

  test.each([
    ["no power feature", ["stream"], ["restart"]],
    ["no hello yet", null, ["restart"]],
    ["a malformed list", ["power"], "restart"],
  ])("offers nothing with %s", (_name, features, power) => {
    expect(powerActions(features, power)).toEqual([]);
  });
});

describe("powerFailure", () => {
  test("explains a known code and falls back for an unknown one", () => {
    expect([powerFailure("timeout"), powerFailure("nonsense"), powerFailure(null)]).toEqual([
      "The device did not answer.",
      "The device could not restart.",
      "The device could not restart.",
    ]);
  });
});
