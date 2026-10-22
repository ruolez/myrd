import { describe, expect, test } from "vitest";
import type { DeviceId } from "../api/types";
import {
  afterClose,
  byIndex,
  emptyWorkspace,
  lruVictim,
  neighbor,
  workspaceReducer,
} from "./workspaceState";
import type { WsAction, WsState } from "./workspaceState";

const id = (s: string) => s as DeviceId;
const run = (actions: WsAction[], s: WsState = emptyWorkspace) =>
  actions.reduce(workspaceReducer, s);
const open = (x: string, now: number): WsAction => ({
  t: "open",
  id: id(x),
  forceRelay: false,
  now,
});
const focus = (x: string | null, now: number): WsAction => ({
  t: "focus",
  id: x ? id(x) : null,
  now,
});

// a, b, c opened in order; focused a (t=10), c (t=20), b (t=30).
const three = run([
  open("a", 1),
  open("b", 2),
  open("c", 3),
  focus("a", 10),
  focus("c", 20),
  focus("b", 30),
]);
const allLive = () => true;

describe("workspaceReducer", () => {
  test("opening twice is a no-op (StrictMode effects run twice)", () => {
    const once = run([open("a", 1)]);
    expect(workspaceReducer(once, open("a", 5))).toBe(once);
  });

  test("focus records when, and unknown ids are ignored", () => {
    expect(three.focused).toBe("b");
    expect(three.entries[id("b")]?.lastFocusedAt).toBe(30);
    expect(workspaceReducer(three, focus("zzz", 40))).toBe(three);
  });

  test("closing the focused session leaves nothing focused", () => {
    const s = workspaceReducer(three, { t: "close", id: id("b") });
    expect({ order: s.order, focused: s.focused, has: id("b") in s.entries }).toEqual({
      order: ["a", "c"],
      focused: null,
      has: false,
    });
  });

  test("replace bumps the generation and keeps the rest unless given", () => {
    const s = run(
      [
        { t: "replace", id: id("a"), forceRelay: true },
        { t: "replace", id: id("a"), trustKey: "k" },
      ],
      three,
    );
    expect(s.entries[id("a")]).toMatchObject({ gen: 2, forceRelay: true, trustKey: "k" });
  });

  test("reorder moves within bounds", () => {
    expect(workspaceReducer(three, { t: "reorder", id: id("c"), to: 0 }).order).toEqual([
      "c",
      "a",
      "b",
    ]);
    expect(workspaceReducer(three, { t: "reorder", id: id("a"), to: 99 }).order).toEqual([
      "b",
      "c",
      "a",
    ]);
  });
});

describe("selectors", () => {
  test("lruVictim skips the focused one and ended sessions", () => {
    expect(lruVictim(three, allLive)).toBe("a");
    expect(lruVictim(three, (x) => x !== "a")).toBe("c");
  });

  test.each([
    ["a", 1, "b"],
    ["c", 1, "a"],
    ["a", -1, "c"],
    [null, 1, "a"],
    [null, -1, "c"],
  ] as const)("neighbor of %s by %d is %s", (from, dir, want) => {
    expect(neighbor(three, from ? id(from) : null, dir)).toBe(want);
  });

  test("byIndex is 1-based", () => {
    expect([byIndex(three, 1), byIndex(three, 3), byIndex(three, 4)]).toEqual(["a", "c", null]);
  });

  test.each([
    ["b", "c"],
    ["c", "b"],
    ["a", "b"],
  ])("closing %s goes to %s", (x, want) => {
    expect(afterClose(three, id(x))).toBe(want);
  });

  test("closing the only session goes to the dashboard", () => {
    expect(afterClose(run([open("a", 1)]), id("a"))).toBe(null);
  });
});
