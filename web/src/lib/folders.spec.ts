import { describe, expect, test } from "vitest";
import type { Folder, FolderId } from "../api/types";
import {
  buildTree,
  canMoveFolder,
  descendants,
  deviceCounts,
  flattenTree,
  folderPath,
} from "./folders";

const fid = (s: string) => s as FolderId;
const folder = (id: string, parent: string | null, name = id, position = 0): Folder => ({
  id: fid(id),
  parent_id: parent ? fid(parent) : null,
  name,
  position,
  created_at: "2026-10-01T00:00:00Z",
});

// office ─┬─ floor1 ── desk
//         └─ floor2
// lab
const folders = [
  folder("floor2", "office", "Floor 2", 1),
  folder("lab", null, "Lab", 1),
  folder("desk", "floor1", "Desk"),
  folder("office", null, "Office", 0),
  folder("floor1", "office", "Floor 1", 0),
];

const shape = (nodes: ReturnType<typeof buildTree>): unknown =>
  nodes.map((n) => [n.folder.id, n.depth, shape(n.children)]);

describe("buildTree", () => {
  test("nests by parent and orders siblings by position", () => {
    expect(shape(buildTree(folders))).toEqual([
      [
        "office",
        0,
        [
          ["floor1", 1, [["desk", 2, []]]],
          ["floor2", 1, []],
        ],
      ],
      ["lab", 0, []],
    ]);
  });

  test("siblings with the same position sort by name, ignoring case", () => {
    const tree = buildTree([folder("b", null, "beta"), folder("a", null, "Alpha")]);
    expect(tree.map((n) => n.folder.name)).toEqual(["Alpha", "beta"]);
  });

  test("orphans and loops are shown at the top level instead of disappearing", () => {
    const bad = [folder("x", "y"), folder("y", "x"), folder("lost", "gone")];
    expect(
      buildTree(bad)
        .map((n) => n.folder.id)
        .sort(),
    ).toEqual(["lost", "x", "y"]);
  });
});

describe("flattenTree", () => {
  test("skips the children of collapsed folders", () => {
    const flat = flattenTree(buildTree(folders), new Set([fid("floor1")]));
    expect(flat.map((n) => n.folder.id)).toEqual(["office", "floor1", "floor2", "lab"]);
  });
});

describe("descendants", () => {
  test("includes the folder and everything below it", () => {
    expect([...descendants(folders, fid("office"))].sort()).toEqual([
      "desk",
      "floor1",
      "floor2",
      "office",
    ]);
  });
});

describe("folderPath", () => {
  test.each([
    ["desk", ["Office", "Floor 1", "Desk"]],
    ["lab", ["Lab"]],
    ["missing", []],
  ])("%s → %j", (id, want) => {
    expect(folderPath(folders, fid(id))).toEqual(want);
  });
});

describe("canMoveFolder", () => {
  test.each([
    ["office", "desk", false],
    ["office", "office", false],
    ["floor1", "lab", true],
    ["desk", null, true],
  ])("%s under %s → %s", (id, parent, ok) => {
    expect(canMoveFolder(folders, fid(id), parent ? fid(parent) : null)).toBe(ok);
  });
});

describe("deviceCounts", () => {
  test("a folder counts the devices of its subfolders", () => {
    const devices = [{ folder_id: fid("desk") }, { folder_id: fid("floor2") }, { folder_id: null }];
    expect(Object.fromEntries(deviceCounts(folders, devices))).toEqual({
      office: 2,
      floor1: 1,
      desk: 1,
      floor2: 1,
      lab: 0,
    });
  });
});
