import { describe, expect, test } from "vitest";
import { vectors } from "../test/vectors";
import { compareVersions, validVersion } from "./version";

describe("validVersion", () => {
  test.each([
    ["0.3.0", true],
    ["v1.2.3", true],
    ["0.0.9-dev", true],
    ["0.3.0-dev.12", true],
    ["1.0.0-rc.1+build.5", true],
    ["10.20.30", true],
    ["0.0.0-abcdef12", true],
    ["", false],
    ["1.2", false],
    ["1.2.3.4", false],
    ["01.2.3", false],
    ["1.2.3-", false],
    ["1.2.3-a..b", false],
    ["1.2.3-01", false],
    ["1.2.3+", false],
    ["1.2.x", false],
    ["1.2.3-ü", false],
    ["dev", false],
  ])("%j → %s (same table as proto/version_test.go)", (v, ok) => {
    expect(validVersion(v)).toBe(ok);
  });
});

describe("compareVersions", () => {
  test.each([
    ["0.3.0", "0.3.0", 0],
    ["v0.3.0", "0.3.0", 0],
    ["0.3.0+build.1", "0.3.0+build.2", 0],
    ["0.10.0", "0.9.9", 1],
    ["0.3.0-dev", "0.3.0", -1],
    ["0.3.0-dev.2", "0.3.0-dev.10", -1],
    ["0.3.0-alpha", "0.3.0-alpha.1", -1],
    ["0.3.0-1", "0.3.0-alpha", -1],
    ["0.3.0-beta", "0.3.0-alpha", 1],
    ["0.0.10-dev", "0.0.9-dev", 1],
    ["1.0.0", "0.99.99", 1],
    ["dev", "0.0.1", -1],
    ["dev", "nightly", 0],
  ] as const)("%s vs %s → %i, antisymmetric", (a, b, want) => {
    expect([compareVersions(a, b), compareVersions(b, a)]).toEqual([want, want === 0 ? 0 : -want]);
  });

  test("sorts the shared versions vector into its listed order", () => {
    expect([...vectors.versions].reverse().sort(compareVersions)).toEqual(vectors.versions);
  });

  test("orders every pair of the versions vector by index", () => {
    const v = vectors.versions;
    expect(v.map((a) => v.map((b) => compareVersions(a, b)))).toEqual(
      v.map((_, i) => v.map((_, j) => Math.sign(i - j) || 0)),
    );
  });
});
