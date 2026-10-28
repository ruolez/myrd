import { describe, expect, test } from "vitest";
import type { FileEntry } from "./files";
import { FileError } from "./files";
import {
  batchView,
  decideConflicts,
  findConflicts,
  folded,
  placeFiles,
  walkLocal,
  walkRemote,
} from "./transferPlan";
import type { Choice, LocalNode, PlanFile, RemoteLister } from "./transferPlan";
import type { Batch, BatchId, Transfer } from "../session/FileChannel";
import type { TransferId } from "./files";

const file = (name: string, size = 1, extra: Partial<FileEntry> = {}): FileEntry => ({
  name,
  kind: "file",
  link: false,
  size,
  mtime: 0,
  hidden: false,
  ...extra,
});
const dir = (name: string, extra: Partial<FileEntry> = {}): FileEntry => ({
  ...file(name, 0),
  kind: "dir",
  ...extra,
});

/** A device file system as a map of folder path → entries, listed two per page. */
function lister(tree: Record<string, FileEntry[]>, fail: Record<string, string> = {}) {
  const calls: string[] = [];
  const list: RemoteLister = async (path, offset) => {
    calls.push(`${path}@${offset}`);
    if (fail[path]) throw new FileError(fail[path]);
    const all = tree[path] ?? [];
    const next = offset + 2 < all.length ? offset + 2 : null;
    return { entries: all.slice(offset, offset + 2), next_offset: next, truncated: false };
  };
  return { list, calls };
}

const never = () => false;

describe("walkRemote", () => {
  test("lists picked folders page by page and keeps folders before their files", async () => {
    const { list } = lister({
      "/h/docs": [file("a.txt", 3), dir("sub"), file("b.txt", 4)],
      "/h/docs/sub": [file("c.txt", 5), dir("empty")],
      "/h/docs/sub/empty": [],
    });
    const out = await walkRemote(list, "/h", "/", [dir("docs"), file("top.bin", 7)], never);
    expect(out).toEqual({
      dirs: [["docs"], ["docs", "sub"], ["docs", "sub", "empty"]],
      files: [
        { src: "/h/docs/a.txt", dir: ["docs"], name: "a.txt", size: 3 },
        { src: "/h/docs/sub/c.txt", dir: ["docs", "sub"], name: "c.txt", size: 5 },
        { src: "/h/docs/b.txt", dir: ["docs"], name: "b.txt", size: 4 },
        { src: "/h/top.bin", dir: [], name: "top.bin", size: 7 },
      ],
      problems: [],
    });
  });

  test("follows a picked link to a folder but not links inside it", async () => {
    const { list, calls } = lister({
      "/h/ln": [dir("loop", { link: true }), file("x", 1, { link: true })],
    });
    const out = await walkRemote(list, "/h", "/", [dir("ln", { link: true })], never);
    expect({ out, calls }).toEqual({
      out: {
        dirs: [["ln"]],
        files: [{ src: "/h/ln/x", dir: ["ln"], name: "x", size: 1 }],
        problems: [{ name: "ln/loop", code: "link_skipped" }],
      },
      calls: ["/h/ln@0"],
    });
  });

  test("reports folders it cannot list and entries it cannot copy, and goes on", async () => {
    const { list } = lister(
      { "/h/a": [dir("locked"), file("sock", 0, { kind: "other" }), file("ok")] },
      { "/h/a/locked": "denied" },
    );
    const out = await walkRemote(list, "/h", "/", [dir("a")], never);
    expect(out).toEqual({
      dirs: [["a"], ["a", "locked"]],
      files: [{ src: "/h/a/ok", dir: ["a"], name: "ok", size: 1 }],
      problems: [
        { name: "a/locked", code: "denied" },
        { name: "a/sock", code: "not_file" },
      ],
    });
  });

  test("stops listing once cancelled", async () => {
    const { list } = lister({ "/h/a": [dir("b")], "/h/a/b": [file("c")] });
    await expect(walkRemote(list, "/h", "/", [dir("a")], () => true)).rejects.toEqual(
      new FileError("cancelled"),
    );
  });

  test("joins Windows paths with backslashes", async () => {
    const { list } = lister({ "C:\\Users\\a": [file("f.txt")] });
    const out = await walkRemote(list, "C:\\Users", "\\", [dir("a")], never);
    expect(out.files).toEqual([{ src: "C:\\Users\\a\\f.txt", dir: ["a"], name: "f.txt", size: 1 }]);
  });
});

const localFile = (name: string, size = 1): LocalNode => ({
  kind: "file",
  name,
  file: new File([new Uint8Array(size)], name),
});
const localDir = (name: string, children: LocalNode[] | Error): LocalNode => ({
  kind: "dir",
  name,
  children: async () => {
    if (children instanceof Error) throw children;
    return children;
  },
});

describe("walkLocal", () => {
  test("collects folders and files with their relative folder", async () => {
    const out = await walkLocal(
      [
        localDir("pics", [localFile("1.jpg", 2), localDir("raw", [localFile("1.raw", 9)])]),
        localFile("a.txt"),
      ],
      never,
    );
    expect({
      dirs: out.dirs,
      files: out.files.map((f) => [f.dir.join("/"), f.name, f.size, f.src.name]),
      problems: out.problems,
    }).toEqual({
      dirs: [["pics"], ["pics", "raw"]],
      files: [
        ["pics", "1.jpg", 2, "1.jpg"],
        ["pics/raw", "1.raw", 9, "1.raw"],
        ["", "a.txt", 1, "a.txt"],
      ],
      problems: [],
    });
  });

  test("reports a folder it cannot read", async () => {
    const out = await walkLocal([localDir("x", new Error("NotAllowedError"))], never);
    expect(out).toEqual({
      dirs: [["x"]],
      files: [],
      problems: [{ name: "x", code: "local_denied" }],
    });
  });
});

const pf = (dirPath: string[], name: string): PlanFile<string> => ({
  src: [...dirPath, name].join("/"),
  dir: dirPath,
  name,
  size: 1,
});
const existingOf = (m: Record<string, string[]>) => (d: readonly string[]) =>
  new Set((m[d.join("/")] ?? []).map(folded));

describe("findConflicts", () => {
  test("matches names case-insensitively in each destination folder", () => {
    const files = [pf([], "A.txt"), pf([], "b.txt"), pf(["sub"], "a.txt"), pf(["sub"], "c.txt")];
    const out = findConflicts(files, existingOf({ "": ["a.txt"], sub: ["C.TXT"] }));
    expect(out.map((f) => f.src)).toEqual(["A.txt", "sub/c.txt"]);
  });
});

describe("decideConflicts", () => {
  const files = [pf([], "a"), pf([], "b"), pf([], "c")];

  test("asks for each conflict until one answer applies to all", async () => {
    const asked: string[] = [];
    const answers: { choice: Choice; all: boolean }[] = [
      { choice: "skip", all: false },
      { choice: "overwrite", all: true },
    ];
    const out = await decideConflicts(files, async (f, left) => {
      asked.push(`${f.name}:${left}`);
      return answers.shift() ?? { choice: "keep", all: false };
    });
    expect({ asked, out: [...out.entries()].map(([f, c]) => [f.name, c]) }).toEqual({
      asked: ["a:3", "b:2"],
      out: [
        ["a", "skip"],
        ["b", "overwrite"],
        ["c", "overwrite"],
      ],
    });
  });

  test("asks nothing without conflicts", async () => {
    const out = await decideConflicts([], () => Promise.reject(new Error("asked")));
    expect(out.size).toEqual(0);
  });
});

describe("placeFiles", () => {
  test("overwrite keeps the name, keep both takes a free one, skip drops the file", () => {
    const files = [pf([], "a.txt"), pf([], "b.txt"), pf([], "c.txt"), pf([], "new.txt")];
    const choices = new Map<PlanFile<string>, Choice>([
      [files[0] as PlanFile<string>, "overwrite"],
      [files[1] as PlanFile<string>, "keep"],
      [files[2] as PlanFile<string>, "skip"],
    ]);
    const out = placeFiles(
      files,
      existingOf({ "": ["a.txt", "b.txt", "b (1).txt", "c.txt"] }),
      choices,
    );
    expect(out.map((p) => [p.file.src, p.name, p.replace])).toEqual([
      ["a.txt", "a.txt", true],
      ["b.txt", "b (2).txt", false],
      ["new.txt", "new.txt", false],
    ]);
  });

  test("two sources whose names differ only in case both arrive", () => {
    const files = [pf([], "Read.me"), pf([], "read.me")];
    const out = placeFiles(files, existingOf({}), new Map());
    expect(out.map((p) => p.name)).toEqual(["Read.me", "read (1).me"]);
  });
});

describe("batchView", () => {
  const B = 1 as BatchId;
  const batch = (over: Partial<Batch> = {}): Batch => ({
    id: B,
    direction: "download",
    name: "docs",
    dest: "~/x",
    phase: "running",
    files: 3,
    bytes: 300,
    skipped: 1,
    problems: [],
    ...over,
  });
  let xfer = 0;
  const tr = (
    status: Transfer["status"],
    bytes: number,
    over: Partial<Transfer> = {},
  ): Transfer => ({
    xfer: ++xfer as TransferId,
    batch: B,
    direction: "download",
    name: `f${xfer}`,
    path: `/d/f${xfer}`,
    size: 100,
    bytes,
    status,
    error: status === "failed" ? { code: "io", detail: "" } : null,
    speed: status === "active" ? 50 : null,
    eta: null,
    sink: "fs",
    ...over,
  });

  test("sums progress while running", () => {
    const v = batchView(batch(), [
      tr("done", 100),
      tr("active", 40),
      tr("queued", 0),
      tr("done", 7, { batch: null }),
    ]);
    expect(v).toEqual({
      status: "running",
      files: 3,
      filesDone: 1,
      filesFailed: 0,
      skipped: 1,
      bytes: 300,
      bytesDone: 140,
      speed: 50,
      eta: 160 / 50,
      current: "f2",
      failures: [],
    });
  });

  test.each([
    ["done when every transfer succeeded", ["done", "done"], [], "done"],
    ["partial when some failed", ["done", "failed"], [], "partial"],
    [
      "partial when a folder could not be read",
      ["done", "done"],
      [{ name: "x", code: "denied" }],
      "partial",
    ],
    ["failed when nothing arrived", ["failed", "cancelled"], [], "failed"],
  ] as const)("is %s", (_name, statuses, problems, want) => {
    const v = batchView(
      batch({ files: statuses.length, problems }),
      statuses.map((s) => tr(s, s === "done" ? 100 : 0)),
    );
    expect(v.status).toEqual(want);
  });

  test("lists failed transfers and problems as failures", () => {
    const v = batchView(batch({ problems: [{ name: "a/locked", code: "denied" }] }), [
      tr("failed", 0, { name: "z" }),
    ]);
    expect(v.failures).toEqual([
      { name: "a/locked", code: "denied" },
      { name: "z", code: "io" },
    ]);
  });
});
