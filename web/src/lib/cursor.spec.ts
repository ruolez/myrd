import { describe, expect, test } from "vitest";
import { cursorSyntax, cursorValue, echoTolerance, ECHO_GRACE_MS, followsRemote } from "./cursor";

describe("echoTolerance", () => {
  test.each([
    [1920, 137],
    [5120, 64], // never tighter than the floor
    [0, 262140], // unknown width: anything counts as an echo
  ])("native width %d → %d normalized units", (w, want) => {
    expect(echoTolerance(w)).toBe(want);
  });
});

describe("followsRemote", () => {
  const sent = { x: 30000, y: 20000, at: 5000 };
  const tol = 100;
  const settled = sent.at + ECHO_GRACE_MS;

  test("follows the agent before anything was sent", () => {
    expect(followsRemote(null, { x: 0, y: 0 }, 0, tol)).toBe(true);
  });

  test("stays local while our moves are still in flight, wherever the report is", () => {
    expect(followsRemote(sent, { x: 0, y: 0 }, settled - 1, tol)).toBe(false);
  });

  test.each([
    ["the echo of our move", { x: 30000, y: 20000 }, false],
    ["within tolerance", { x: 30000 + tol, y: 20000 - tol }, false],
    ["moved on the device (x)", { x: 30000 + tol + 1, y: 20000 }, true],
    ["moved on the device (y)", { x: 30000, y: 20000 - tol - 1 }, true],
  ])("after the echo window: %s → %s", (_name, reported, want) => {
    expect(followsRemote(sent, reported, settled, tol)).toBe(want);
  });
});

describe("cursorSyntax", () => {
  test.each([
    [["image-set", "-webkit-image-set"], "image-set"],
    [["-webkit-image-set"], "-webkit-image-set"],
    [[], "url"],
  ])("supported %j → %s", (supported, want) => {
    const supports = (_p: string, v: string) => supported.some((s) => v.startsWith(`${s}(`));
    expect(cursorSyntax(supports)).toBe(want);
  });
});

describe("cursorValue", () => {
  test("declares the image density and rounds the hotspot", () => {
    expect(cursorValue("data:x", "image-set", 2, 3.4, 7.6)).toBe(
      'image-set(url("data:x") 2x) 3 8, auto',
    );
  });

  test("plain url carries no density and clamps a negative hotspot", () => {
    expect(cursorValue("data:x", "url", 2, -1, 0)).toBe('url("data:x") 0 0, auto');
  });
});
