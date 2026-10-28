// Planning folder and multi-file transfers: walking the source tree,
// finding name conflicts at the destination, applying the user's choices,
// and summarising a batch's progress. Pure: the file systems are passed in.

import { FileError, joinPath, uniqueName } from "./files";
import type { FileEntry, Sep } from "./files";
import type { Batch, BatchProblem, Transfer } from "../session/FileChannel";

/** A file to copy: `dir` is its folder relative to the destination. */
export type PlanFile<T> = { src: T; dir: readonly string[]; name: string; size: number };
/** Folders (parents first) and files to copy, plus what could not be read. */
export type Plan<T> = { dirs: string[][]; files: PlanFile<T>[]; problems: BatchProblem[] };

export type Choice = "overwrite" | "skip" | "keep";

export type RemoteLister = (
  path: string,
  offset: number,
) => Promise<{ entries: FileEntry[]; next_offset: number | null; truncated: boolean }>;

const codeOf = (e: unknown) => (e instanceof FileError ? e.code : "io");

/**
 * Walks the picked entries of the device folder `base`. Picked links to
 * folders are followed; links met inside are not (they could loop).
 */
export async function walkRemote(
  list: RemoteLister,
  base: string,
  sep: Sep,
  picked: readonly FileEntry[],
  cancelled: () => boolean,
): Promise<Plan<string>> {
  const plan: Plan<string> = { dirs: [], files: [], problems: [] };
  const visit = async (e: FileEntry, parent: string, rel: string[], top: boolean) => {
    if (cancelled()) throw new FileError("cancelled");
    const path = joinPath(parent, e.name, sep);
    if (e.kind === "file") {
      plan.files.push({ src: path, dir: rel, name: e.name, size: e.size });
      return;
    }
    const at = [...rel, e.name];
    if (e.kind !== "dir") {
      plan.problems.push({ name: at.join("/"), code: "not_file" });
      return;
    }
    if (e.link && !top) {
      plan.problems.push({ name: at.join("/"), code: "link_skipped" });
      return;
    }
    plan.dirs.push(at);
    let offset: number | null = 0;
    while (offset !== null) {
      if (cancelled()) throw new FileError("cancelled");
      let page: Awaited<ReturnType<RemoteLister>>;
      try {
        page = await list(path, offset);
      } catch (err) {
        plan.problems.push({ name: at.join("/"), code: codeOf(err) });
        return;
      }
      for (const child of page.entries) await visit(child, path, at, false);
      if (page.truncated && page.next_offset === null) {
        plan.problems.push({ name: at.join("/"), code: "truncated" });
      }
      offset = page.next_offset;
    }
  };
  for (const e of picked) await visit(e, base, [], true);
  return plan;
}

/** A local file or folder, from the File System Access API, a drop or a folder input. */
export type LocalNode =
  | { kind: "file"; name: string; file: File }
  | { kind: "dir"; name: string; children: () => Promise<LocalNode[]> };

export async function walkLocal(
  picked: readonly LocalNode[],
  cancelled: () => boolean,
): Promise<Plan<File>> {
  const plan: Plan<File> = { dirs: [], files: [], problems: [] };
  const visit = async (n: LocalNode, rel: string[]) => {
    if (cancelled()) throw new FileError("cancelled");
    if (n.kind === "file") {
      plan.files.push({ src: n.file, dir: rel, name: n.name, size: n.file.size });
      return;
    }
    const at = [...rel, n.name];
    plan.dirs.push(at);
    let children: LocalNode[];
    try {
      children = await n.children();
    } catch {
      plan.problems.push({ name: at.join("/"), code: "local_denied" });
      return;
    }
    for (const c of children) await visit(c, at);
  };
  for (const n of picked) await visit(n, []);
  return plan;
}

/** Names are compared case-insensitively: a conflict is asked about rather than missed. */
export const folded = (name: string) => name.toLowerCase();

/** Files whose name is taken in their destination folder (`existing` holds folded names). */
export function findConflicts<T>(
  files: readonly PlanFile<T>[],
  existing: (dir: readonly string[]) => ReadonlySet<string>,
): PlanFile<T>[] {
  return files.filter((f) => existing(f.dir).has(folded(f.name)));
}

export type ConflictAnswer = { choice: Choice; all: boolean };

/** Asks about each conflict in turn until an answer applies to all the rest. */
export async function decideConflicts<T>(
  conflicts: readonly PlanFile<T>[],
  ask: (f: PlanFile<T>, remaining: number) => Promise<ConflictAnswer>,
): Promise<Map<PlanFile<T>, Choice>> {
  const out = new Map<PlanFile<T>, Choice>();
  let all: Choice | null = null;
  for (const [i, f] of conflicts.entries()) {
    if (all === null) {
      const a = await ask(f, conflicts.length - i);
      out.set(f, a.choice);
      if (a.all) all = a.choice;
    } else {
      out.set(f, all);
    }
  }
  return out;
}

export type Placed<T> = { file: PlanFile<T>; name: string; replace: boolean };

/**
 * Final names: a conflict follows its choice (overwrite keeps the name, keep
 * both takes the next free "name (N)", skip drops it); any other file keeps
 * its name unless an earlier file of this plan took it.
 */
export function placeFiles<T>(
  files: readonly PlanFile<T>[],
  existing: (dir: readonly string[]) => ReadonlySet<string>,
  choices: ReadonlyMap<PlanFile<T>, Choice>,
): Placed<T>[] {
  const taken = new Map<string, Set<string>>();
  const takenIn = (dir: readonly string[]) => {
    const key = dir.join("/");
    let s = taken.get(key);
    if (!s) {
      s = new Set(existing(dir));
      taken.set(key, s);
    }
    return s;
  };
  const out: Placed<T>[] = [];
  const placed = new Set<string>();
  for (const f of files) {
    const choice = choices.get(f);
    if (choice === "skip") continue;
    const names = takenIn(f.dir);
    const key = `${f.dir.join("/")}/${folded(f.name)}`;
    let name = f.name;
    let replace = false;
    if (choice === "overwrite" && !placed.has(key)) replace = true;
    else if (names.has(folded(f.name))) name = uniqueName(f.name, (c) => names.has(folded(c)));
    names.add(folded(name));
    placed.add(`${f.dir.join("/")}/${folded(name)}`);
    out.push({ file: f, name, replace });
  }
  return out;
}

export type BatchStatus =
  "scanning" | "asking" | "running" | "done" | "partial" | "failed" | "cancelled";

export type BatchView = {
  status: BatchStatus;
  files: number;
  filesDone: number;
  filesFailed: number;
  skipped: number;
  bytes: number;
  bytesDone: number;
  /** Bytes per second over the running transfers. */
  speed: number | null;
  /** Seconds left at that speed. */
  eta: number | null;
  /** A file being transferred now. */
  current: string | null;
  failures: BatchProblem[];
};

export function batchView(b: Batch, all: readonly Transfer[]): BatchView {
  const ts = all.filter((t) => t.batch === b.id);
  const done = ts.filter((t) => t.status === "done");
  const failed = ts.filter((t) => t.status === "failed");
  const running = ts.filter((t) => t.status === "active" || t.status === "verifying");
  const bytesDone = ts.reduce((n, t) => n + (t.status === "done" ? t.size : t.bytes), 0);
  const speeds = running.map((t) => t.speed).filter((s): s is number => s !== null);
  const speed = speeds.length > 0 ? speeds.reduce((a, s) => a + s, 0) : null;
  const finished = ts.every(
    (t) => t.status === "done" || t.status === "failed" || t.status === "cancelled",
  );
  let status: BatchStatus = b.phase === "running" ? "running" : b.phase;
  if (b.phase === "running" && finished) {
    if (done.length === 0 && ts.length > 0) status = "failed";
    else if (failed.length > 0 || b.problems.length > 0) status = "partial";
    else status = "done";
  }
  return {
    status,
    files: b.files,
    filesDone: done.length,
    filesFailed: failed.length,
    skipped: b.skipped,
    bytes: b.bytes,
    bytesDone,
    speed,
    eta: speed && speed > 0 ? Math.max(0, b.bytes - bytesDone) / speed : null,
    current: running[0]?.name ?? null,
    failures: [
      ...b.problems,
      ...failed.map((t) => ({ name: t.name, code: t.error?.code ?? "io" })),
    ],
  };
}
