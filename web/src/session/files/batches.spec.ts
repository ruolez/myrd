import { describe, expect, test, vi } from "vitest";
import { FileError } from "../../lib/files";
import type {
  FileEntry,
  FileRequestId,
  Listing,
  OkReply,
  RootsReply,
  TransferId,
} from "../../lib/files";
import type { ConflictAnswer, LocalNode } from "../../lib/transferPlan";
import type { Batch, BatchId, DownloadSink, FileSource } from "../FileChannel";
import { startDownload, startUpload } from "./batches";
import type { Ask, BatchChannel, ConflictQuestion } from "./batches";
import type { LocalTarget } from "./localFs";

const entry = (name: string, kind: FileEntry["kind"] = "file", size = 1): FileEntry => ({
  name,
  kind,
  link: false,
  size,
  mtime: 0,
  hidden: false,
});

/** A device with folders as path → entries; records what the batch asked of it. */
class FakeDevice implements BatchChannel {
  batches: Batch[] = [];
  uploads: { name: string; dir: string | null; replace: boolean }[] = [];
  downloads: { path: string; name: string; sink: DownloadSink }[] = [];
  mkdirs: string[] = [];
  constructor(private tree: Record<string, FileEntry[]>) {}

  createBatch(init: Pick<Batch, "direction" | "name" | "dest">): BatchId {
    const id = (this.batches.length + 1) as BatchId;
    this.batches.push({
      ...init,
      id,
      phase: "scanning",
      files: 0,
      bytes: 0,
      skipped: 0,
      problems: [],
    });
    return id;
  }
  updateBatch(id: BatchId, patch: Partial<Omit<Batch, "id">>) {
    this.batches = this.batches.map((b) => (b.id === id ? { ...b, ...patch } : b));
  }
  batch(id: BatchId) {
    return this.batches.find((b) => b.id === id);
  }
  async list(path: string, offset = 0): Promise<Listing> {
    const all = this.tree[path];
    if (!all) throw new FileError("not_found");
    return {
      t: "listing",
      id: 1 as FileRequestId,
      path,
      parent: "",
      entries: all.slice(offset),
      offset,
      next_offset: null,
      total: all.length,
      truncated: false,
    };
  }
  async mkdir(dir: string, name: string): Promise<OkReply> {
    const path = `${dir}/${name}`;
    this.mkdirs.push(path);
    if (name === "blocked") throw new FileError("exists");
    const existed = path in this.tree;
    this.tree[path] ??= [];
    return { t: "ok", id: 1 as FileRequestId, path, existed };
  }
  async roots(): Promise<RootsReply> {
    return { t: "roots", id: 1 as FileRequestId, roots: [], default_dir: "/h/Downloads", sep: "/" };
  }
  upload(file: FileSource, dir: string | null, opts: { replace?: boolean } = {}): TransferId {
    this.uploads.push({ name: file.name, dir, replace: opts.replace ?? false });
    return this.uploads.length as TransferId;
  }
  download(e: { path: string; name: string }, sink: DownloadSink): TransferId {
    this.downloads.push({ path: e.path, name: e.name, sink });
    return this.downloads.length as TransferId;
  }
}

/** A local folder as relative folder → names. */
function fakeTarget(existing: Record<string, string[]>) {
  const made: string[] = [];
  const target: LocalTarget = {
    folders: true,
    names: async (rel) => new Set((existing[rel.join("/")] ?? []).map((n) => n.toLowerCase())),
    ensureDir: async (rel) => {
      if (rel.at(-1) === "blocked") throw new FileError("local_exists");
      made.push(rel.join("/"));
    },
    sink: (rel, name) =>
      ({
        kind: "fs",
        write: async () => {},
        close: async () => {},
        abort: () => {},
        rel,
        name,
      }) as DownloadSink,
  };
  return { target, made };
}

function asker(answers: ConflictAnswer[]) {
  const asked: ConflictQuestion[] = [];
  const ask: Ask = async (q) => {
    asked.push(q);
    return answers.shift() ?? { choice: "skip", all: false };
  };
  return { ask, asked };
}

const settled = (dev: FakeDevice) =>
  vi.waitFor(() => {
    const p = dev.batches[0]?.phase;
    if (p === "scanning" || p === "asking") throw new Error(`still ${p}`);
  });

describe("startDownload", () => {
  test("walks the folder, asks about conflicts, makes folders and queues the files", async () => {
    const dev = new FakeDevice({
      "/h/docs": [entry("a.txt"), entry("b.txt"), entry("sub", "dir")],
      "/h/docs/sub": [entry("c.txt", "file", 5)],
    });
    const { target, made } = fakeTarget({ docs: ["A.TXT", "b.txt"] });
    const { ask, asked } = asker([
      { choice: "keep", all: false },
      { choice: "skip", all: false },
    ]);
    startDownload(dev, {
      from: "/h",
      sep: "/",
      entries: [entry("docs", "dir")],
      hidden: false,
      to: target,
      toLabel: "work",
      ask,
    });
    await settled(dev);
    expect({
      asked: asked.map((q) => [q.name, q.where, q.remaining]),
      made,
      queued: dev.downloads.map((d) => [d.path, d.name]),
      batch: dev.batches[0],
    }).toEqual({
      asked: [
        ["a.txt", "work/docs", 2],
        ["b.txt", "work/docs", 1],
      ],
      made: ["docs", "docs/sub"],
      queued: [
        ["/h/docs/a.txt", "a (1).txt"],
        ["/h/docs/sub/c.txt", "c.txt"],
      ],
      batch: expect.objectContaining({
        phase: "running",
        files: 2,
        bytes: 6,
        skipped: 1,
        problems: [],
      }),
    });
  });

  test("a local folder that cannot be made drops its files and is reported", async () => {
    const dev = new FakeDevice({ "/h/blocked": [entry("x")] });
    const { target } = fakeTarget({});
    startDownload(dev, {
      from: "/h",
      sep: "/",
      entries: [entry("blocked", "dir"), entry("y")],
      hidden: false,
      to: target,
      toLabel: "work",
      ask: asker([]).ask,
    });
    await settled(dev);
    expect({ queued: dev.downloads.map((d) => d.name), batch: dev.batches[0] }).toEqual({
      queued: ["y"],
      batch: expect.objectContaining({
        files: 1,
        problems: [{ name: "blocked", code: "local_exists" }],
      }),
    });
  });

  test("a batch cancelled while asking queues nothing", async () => {
    const dev = new FakeDevice({ "/h": [] });
    const { target } = fakeTarget({ "": ["a"] });
    let answer!: (a: ConflictAnswer) => void;
    startDownload(dev, {
      from: "/h",
      sep: "/",
      entries: [entry("a")],
      hidden: false,
      to: target,
      toLabel: "work",
      ask: () => new Promise((r) => (answer = r)),
    });
    await vi.waitFor(() => expect(dev.batches[0]?.phase).toEqual("asking"));
    dev.updateBatch(1 as BatchId, { phase: "cancelled" });
    answer({ choice: "overwrite", all: true });
    await new Promise((r) => setTimeout(r, 0));
    expect({ queued: dev.downloads, phase: dev.batches[0]?.phase }).toEqual({
      queued: [],
      phase: "cancelled",
    });
  });
});

const local = (name: string, children?: LocalNode[]): LocalNode =>
  children
    ? { kind: "dir", name, children: async () => children }
    : { kind: "file", name, file: new File(["x"], name) };

describe("startUpload", () => {
  test("creates folders, overwrites or skips per answer, and queues into the created paths", async () => {
    const dev = new FakeDevice({
      "/h/d": [entry("a.txt"), entry("pics", "dir")],
      "/h/d/pics": [entry("1.jpg"), entry("2.jpg")],
    });
    const { ask, asked } = asker([
      { choice: "overwrite", all: false },
      { choice: "skip", all: true },
    ]);
    startUpload(dev, {
      nodes: [
        local("pics", [local("1.jpg"), local("2.jpg"), local("new", [local("n.txt")])]),
        local("a.txt"),
      ],
      to: "/h/d",
      ops: true,
      ask,
    });
    await settled(dev);
    expect({
      mkdirs: dev.mkdirs,
      asked: asked.map((q) => [q.name, q.where]),
      uploads: dev.uploads,
    }).toEqual({
      mkdirs: ["/h/d/pics", "/h/d/pics/new"],
      asked: [
        ["1.jpg", "/h/d/pics"],
        ["2.jpg", "/h/d/pics"],
      ],
      uploads: [
        { name: "1.jpg", dir: "/h/d/pics", replace: true },
        { name: "n.txt", dir: "/h/d/pics/new", replace: false },
      ],
    });
  });

  test("an old agent gets top-level files only and no questions", async () => {
    const dev = new FakeDevice({ "/h/Downloads": [entry("a.txt")] });
    startUpload(dev, {
      nodes: [local("a.txt"), local("dir", [local("x")])],
      to: null,
      ops: false,
      ask: () => Promise.reject(new Error("asked")),
    });
    await settled(dev);
    expect({ mkdirs: dev.mkdirs, uploads: dev.uploads, batch: dev.batches[0] }).toEqual({
      mkdirs: [],
      uploads: [{ name: "a.txt", dir: "/h/Downloads", replace: false }],
      batch: expect.objectContaining({
        dest: "/h/Downloads",
        problems: [{ name: "dir", code: "folders_unsupported" }],
      }),
    });
  });

  test("a folder the device refuses drops its files and is reported", async () => {
    const dev = new FakeDevice({ "/h/d": [] });
    startUpload(dev, {
      nodes: [local("blocked", [local("x"), local("inner", [local("y")])])],
      to: "/h/d",
      ops: true,
      ask: asker([]).ask,
    });
    await settled(dev);
    expect({
      mkdirs: dev.mkdirs,
      uploads: dev.uploads,
      problems: dev.batches[0]?.problems,
    }).toEqual({
      mkdirs: ["/h/d/blocked"],
      uploads: [],
      problems: [{ name: "blocked", code: "exists" }],
    });
  });
});
