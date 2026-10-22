import { describe, expect, test } from "vitest";
import type { FolderId, TagId } from "../api/types";
import type { Chip } from "./deviceFilters";
import {
  DEFAULT_PREFS,
  parseDashParams,
  parseDashPrefs,
  parseView,
  serializeDashPrefs,
  toDashParams,
  viewKey,
} from "./dashboardState";
import type { DashParams } from "./dashboardState";

describe("parseView", () => {
  test.each([
    [null, "all"],
    ["favorites", "favorites"],
    ["folder:abc", { folder: "abc" }],
    ["tag:t1", { tag: "t1" }],
    ["folder:", "all"],
    ["nonsense", "all"],
  ])("%j", (s, want) => {
    expect(parseView(s)).toEqual(want);
  });
});

describe("dashboard params", () => {
  test("round-trip keeps every value", () => {
    const s: DashParams = {
      view: { folder: "f1" as FolderId },
      query: "tag:lab front",
      chips: new Set<Chip>(["windows", "online"]),
      sort: "last_connected",
    };
    const back = parseDashParams(toDashParams(s));
    expect({ ...back, chips: [...back.chips].sort() }).toEqual({
      ...s,
      chips: ["online", "windows"],
    });
  });

  test("defaults give an empty query string", () => {
    expect(toDashParams({ view: "all", query: "", chips: new Set(), sort: null }).toString()).toBe(
      "",
    );
  });

  test("unknown chips and sorts are dropped", () => {
    const p = parseDashParams(new URLSearchParams("f=online,bogus&sort=size&view=tag:t9"));
    expect({ ...p, chips: [...p.chips] }).toEqual({
      view: { tag: "t9" as TagId },
      query: "",
      chips: ["online"],
      sort: null,
    });
  });

  test("viewKey matches what parseView reads", () => {
    expect(parseView(viewKey({ tag: "t2" as TagId }))).toEqual({ tag: "t2" });
  });
});

describe("parseDashPrefs", () => {
  test("round-trips", () => {
    const p = { layout: "list", size: "l", sort: "name", collapsed: ["a" as FolderId] } as const;
    expect(parseDashPrefs(serializeDashPrefs({ ...p, collapsed: [...p.collapsed] }))).toEqual(p);
  });

  test.each([null, "", "not json", "42", '{"layout":"huge","size":9,"sort":"x","collapsed":"no"}'])(
    "falls back to defaults for %j",
    (raw) => {
      expect(parseDashPrefs(raw)).toEqual(DEFAULT_PREFS);
    },
  );
});
