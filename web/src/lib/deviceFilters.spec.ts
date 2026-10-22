import { describe, expect, test } from "vitest";
import type { Device, DeviceId, Folder, FolderId, Tag, TagId } from "../api/types";
import {
  applyFilter,
  attentionReasons,
  inView,
  parseQuery,
  recent,
  sortDevices,
  viewCounts,
} from "./deviceFilters";
import type { Chip, DashFilter, FilterCtx } from "./deviceFilters";

const NOW = Date.parse("2026-10-20T12:00:00Z");
const daysAgo = (n: number) => new Date(NOW - n * 86_400_000).toISOString();

const device = (over: Omit<Partial<Device>, "id"> & { id: string; name: string }): Device => ({
  hostname: over.name.toLowerCase(),
  os: "darwin",
  arch: "arm64",
  agent_version: "0.3.15",
  public_key: "",
  status: "online",
  online: true,
  busy: false,
  auto_accept: true,
  last_seen_at: null,
  created_at: daysAgo(30),
  monitors: null,
  permissions: { screen: true, input: true },
  encoder: null,
  pinned_clients: null,
  latest_version: null,
  update_available: false,
  update_state: "",
  update_from: "",
  update_version: "",
  update_error: "",
  update_at: null,
  folder_id: null,
  tag_ids: [],
  favorite: false,
  notes: "",
  last_connected_at: null,
  info: null,
  info_at: null,
  features: [],
  previews: true,
  ...over,
  id: over.id as DeviceId,
});

const folders: Folder[] = [
  { id: "office" as FolderId, parent_id: null, name: "Office", position: 0, created_at: "" },
  {
    id: "floor2" as FolderId,
    parent_id: "office" as FolderId,
    name: "Floor 2",
    position: 0,
    created_at: "",
  },
];
const tags: Tag[] = [
  { id: "t-lab" as TagId, name: "Lab", color: 2, created_at: "" },
  { id: "t-win7" as TagId, name: "Windows 7", color: 5, created_at: "" },
];
const ctx: FilterCtx = { folders, tags, now: NOW };

const mac = device({
  id: "mac",
  name: "Mac mini",
  favorite: true,
  folder_id: "office" as FolderId,
  last_connected_at: daysAgo(1),
});
const pc = device({
  id: "pc",
  name: "Front desk",
  os: "windows",
  busy: true,
  folder_id: "floor2" as FolderId,
  tag_ids: ["t-win7" as TagId],
  notes: "Ask Dana before rebooting",
  last_connected_at: daysAgo(0.1),
});
const old = device({
  id: "old",
  name: "Old laptop",
  os: "windows",
  online: false,
  status: "offline",
  last_seen_at: daysAgo(10),
  tag_ids: ["t-lab" as TagId],
});
const demo = device({
  id: "demo",
  name: "Demo",
  os: "linux",
  permissions: { screen: true, input: false },
  update_state: "failed",
});
const all = [mac, pc, old, demo];
const ids = (ds: Device[]) => ds.map((d) => d.id);

describe("parseQuery", () => {
  test("separates tokens from free words and resolves OS aliases", () => {
    expect(parseQuery('front tag:"windows 7" os:win is:busy folder:floor bogus:x')).toEqual({
      text: ["front", "bogus:x"],
      tags: ["windows 7"],
      folders: ["floor"],
      os: ["windows"],
      is: ["busy"],
    });
  });

  test("an unknown is: value stays a free word", () => {
    expect(parseQuery("is:sleepy").text).toEqual(["is:sleepy"]);
  });
});

describe("attentionReasons", () => {
  test.each([
    [old, ["offline_long"]],
    [demo, ["update_failed", "permissions"]],
    [mac, []],
  ])("%#", (d, want) => {
    expect(attentionReasons(d, NOW)).toEqual(want);
  });

  test("a nearly full disk is a reason", () => {
    const GB = 1024 ** 3;
    const full = {
      ...mac,
      info: {
        os_name: "macOS",
        os_version: "",
        os_build: "",
        user: "alice",
        idle_s: 0,
        uptime_s: 0,
        locked: false,
        cpu_pct: null,
        cpu_count: 8,
        mem_total: 0,
        mem_used: 0,
        disks: [{ name: "", mount: "/", fs: "apfs", total: 500 * GB, free: 30 * GB }],
      },
    };
    expect(attentionReasons(full, NOW)).toEqual(["low_disk"]);
  });

  test("offline for exactly the limit is not yet a reason", () => {
    expect(attentionReasons({ ...old, last_seen_at: daysAgo(7) }, NOW)).toEqual([]);
  });
});

describe("inView", () => {
  test.each([
    ["all", ["mac", "pc", "old", "demo"]],
    ["favorites", ["mac"]],
    ["recent", ["pc", "mac"]],
    ["online", ["mac", "pc", "demo"]],
    ["in_session", ["pc"]],
    ["attention", ["old", "demo"]],
    ["unfiled", ["old", "demo"]],
  ] as const)("%s", (view, want) => {
    expect(ids(inView(all, view, ctx))).toEqual(want);
  });

  test("a folder view includes its subfolders", () => {
    expect(ids(inView(all, { folder: "office" as FolderId }, ctx))).toEqual(["mac", "pc"]);
  });

  test("a tag view lists the tagged devices", () => {
    expect(ids(inView(all, { tag: "t-lab" as TagId }, ctx))).toEqual(["old"]);
  });

  test("a session open in this tab counts as in session", () => {
    expect(ids(inView(all, "in_session", { ...ctx, openHere: new Set(["mac"]) }))).toEqual([
      "mac",
      "pc",
    ]);
  });
});

describe("sortDevices", () => {
  test("favorites first, then by status, then by name", () => {
    expect(ids(sortDevices(all, "status"))).toEqual(["mac", "demo", "pc", "old"]);
  });

  test("names compare numerically", () => {
    const list = [device({ id: "b", name: "PC 10" }), device({ id: "a", name: "PC 9" })];
    expect(ids(sortDevices(list, "name"))).toEqual(["a", "b"]);
  });

  test("last connected puts never-connected devices last", () => {
    expect(ids(sortDevices(all, "last_connected"))).toEqual(["mac", "pc", "demo", "old"]);
  });
});

describe("recent", () => {
  test("is newest first and limited", () => {
    expect(ids(recent(all, 1))).toEqual(["pc"]);
  });
});

describe("applyFilter", () => {
  const f = (over: Partial<DashFilter>): DashFilter => ({
    view: "all",
    query: "",
    chips: new Set<Chip>(),
    sort: "name",
    ...over,
  });

  test.each([
    ["dana", ["pc"]],
    ["floor", ["pc"]],
    ["tag:lab", ["old"]],
    ["tag:lab front", []],
    ["os:mac", ["mac"]],
    ["macos", ["mac"]],
    ["folder:office", ["mac", "pc"]],
  ])("query %j", (query, want) => {
    expect(ids(applyFilter(all, f({ query }), ctx))).toEqual(want);
  });

  test("status chips are alternatives, OS chips narrow further", () => {
    const chips = new Set<Chip>(["offline", "in_session", "windows"]);
    expect(ids(applyFilter(all, f({ chips }), ctx))).toEqual(["pc", "old"]);
  });
});

describe("viewCounts", () => {
  test("counts every smart view", () => {
    expect(viewCounts(all, ctx)).toEqual({
      all: 4,
      favorites: 1,
      recent: 2,
      online: 3,
      in_session: 1,
      attention: 2,
      unfiled: 2,
    });
  });
});
