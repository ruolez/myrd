// Runs a user's transfer request as one batch: plan the files (walking
// folders), create the destination folders, settle name conflicts with the
// user, then queue the transfers on the FileChannel.

import { FileError } from "../../lib/files";
import type { FileEntry, Sep } from "../../lib/files";
import {
  decideConflicts,
  findConflicts,
  folded,
  placeFiles,
  walkLocal,
  walkRemote,
} from "../../lib/transferPlan";
import type { ConflictAnswer, LocalNode, Plan, PlanFile } from "../../lib/transferPlan";
import type { BatchId, BatchProblem, FileChannel, FileSource } from "../FileChannel";
import type { LocalTarget } from "./localFs";

export type ConflictQuestion = {
  direction: "upload" | "download";
  name: string;
  /** The folder it goes to, for display. */
  where: string;
  /** Conflicts left including this one. */
  remaining: number;
};
export type Ask = (q: ConflictQuestion) => Promise<ConflictAnswer>;

export type BatchChannel = Pick<
  FileChannel,
  "createBatch" | "updateBatch" | "batch" | "list" | "mkdir" | "roots" | "upload" | "download"
>;

const keyOf = (rel: readonly string[]) => rel.join("/");
const parentKey = (rel: readonly string[]) => keyOf(rel.slice(0, -1));
const labelOf = (names: readonly string[]) =>
  names.length === 1 ? (names[0] as string) : `${names.length} items`;
const codeOf = (e: unknown) => (e instanceof FileError ? e.code : "io");

/** Every name in a device folder, folded, across pages and including hidden ones. */
async function remoteNames(fc: BatchChannel, path: string): Promise<Set<string>> {
  const out = new Set<string>();
  let offset: number | null = 0;
  while (offset !== null) {
    const l = await fc.list(path, offset, true);
    for (const e of l.entries) out.add(folded(e.name));
    offset = l.next_offset;
  }
  return out;
}

/** Files under a folder that could not be created are dropped (its problem is listed). */
const underFailed = (f: PlanFile<unknown>, failed: ReadonlySet<string>) =>
  [...failed].some((k) => keyOf(f.dir) === k || keyOf(f.dir).startsWith(`${k}/`));

function run(
  fc: BatchChannel,
  id: BatchId,
  label: string,
  body: (cancelled: () => boolean) => Promise<void>,
) {
  const cancelled = () => fc.batch(id)?.phase === "cancelled";
  void body(cancelled).catch((e: unknown) => {
    if (cancelled()) return;
    const code = codeOf(e);
    const b = fc.batch(id);
    fc.updateBatch(id, {
      phase: code === "cancelled" ? "cancelled" : "failed",
      problems:
        code === "cancelled"
          ? (b?.problems ?? [])
          : [...(b?.problems ?? []), { name: label, code }],
    });
  });
}

async function settle<T>(
  plan: Plan<T>,
  existing: (dir: readonly string[]) => ReadonlySet<string>,
  ask: ((f: PlanFile<T>, remaining: number) => Promise<ConflictAnswer>) | null,
  onAsking: () => void,
) {
  const conflicts = ask ? findConflicts(plan.files, existing) : [];
  if (conflicts.length > 0) onAsking();
  const choices = await decideConflicts(
    conflicts,
    ask ?? (() => Promise.reject(new FileError("io"))),
  );
  return placeFiles(plan.files, existing, choices);
}

/** Downloads the picked entries of the device folder `from` into `to`. */
export function startDownload(
  fc: BatchChannel,
  opts: {
    from: string;
    sep: Sep;
    entries: readonly FileEntry[];
    hidden: boolean;
    to: LocalTarget;
    toLabel: string;
    ask: Ask;
  },
): BatchId {
  const label = labelOf(opts.entries.map((e) => e.name));
  const id = fc.createBatch({ direction: "download", name: label, dest: opts.toLabel });
  run(fc, id, label, async (cancelled) => {
    const { to } = opts;
    const plan = await walkRemote(
      (p, o) => fc.list(p, o, opts.hidden),
      opts.from,
      opts.sep,
      opts.entries,
      cancelled,
    );
    if (!to.folders && plan.dirs.length > 0) {
      for (const d of plan.dirs.filter((d) => d.length === 1)) {
        plan.problems.push({ name: keyOf(d), code: "folders_unsupported" });
      }
      plan.files = plan.files.filter((f) => f.dir.length === 0);
      plan.dirs = [];
    }
    const names = new Map<string, Set<string>>();
    for (const f of plan.files) {
      if (!names.has(keyOf(f.dir))) names.set(keyOf(f.dir), await to.names(f.dir));
      if (cancelled()) return;
    }
    const existing = (d: readonly string[]) => names.get(keyOf(d)) ?? new Set<string>();
    const placed = await settle(
      plan,
      existing,
      (f, remaining) => {
        if (cancelled()) throw new FileError("cancelled");
        return opts.ask({
          direction: "download",
          name: f.name,
          where: [opts.toLabel, ...f.dir].join("/"),
          remaining,
        });
      },
      () => fc.updateBatch(id, { phase: "asking" }),
    );
    if (cancelled()) return;
    const problems: BatchProblem[] = [...plan.problems];
    const failed = new Set<string>();
    for (const d of plan.dirs) {
      if (underFailed({ src: null, dir: d.slice(0, -1), name: "", size: 0 }, failed)) continue;
      try {
        await to.ensureDir(d);
      } catch (e) {
        problems.push({ name: keyOf(d), code: codeOf(e) });
        failed.add(keyOf(d));
      }
    }
    const go = placed.filter((p) => !underFailed(p.file, failed));
    fc.updateBatch(id, {
      phase: "running",
      files: go.length,
      bytes: go.reduce((n, p) => n + p.file.size, 0),
      skipped: plan.files.length - placed.length,
      problems,
    });
    for (const p of go) {
      fc.download(
        { path: p.file.src, name: p.name, size: p.file.size },
        to.sink(p.file.dir, p.name),
        {
          batch: id,
        },
      );
    }
  });
  return id;
}

const renamed = (f: File, name: string): FileSource => ({
  name,
  size: f.size,
  slice: (s, e) => f.slice(s, e),
});

/**
 * Uploads local files and folders into the device folder `to` (null = the
 * agent's default). Without `ops` (old agents) folders are refused and
 * conflicts are not asked about: the agent keeps both. `ask` null keeps both too.
 */
export function startUpload(
  fc: BatchChannel,
  opts: {
    nodes: readonly LocalNode[];
    to: string | null;
    ops: boolean;
    ask: Ask | null;
  },
): BatchId {
  const label = labelOf(opts.nodes.map((n) => n.name));
  const id = fc.createBatch({ direction: "upload", name: label, dest: opts.to ?? "Downloads" });
  run(fc, id, label, async (cancelled) => {
    const plan = await walkLocal(opts.nodes, cancelled);
    const dest = opts.to ?? (await fc.roots()).default_dir;
    if (opts.to === null) fc.updateBatch(id, { dest });
    if (!opts.ops && plan.dirs.length > 0) {
      for (const d of plan.dirs.filter((d) => d.length === 1)) {
        plan.problems.push({ name: keyOf(d), code: "folders_unsupported" });
      }
      plan.files = plan.files.filter((f) => f.dir.length === 0);
      plan.dirs = [];
    }
    const problems: BatchProblem[] = [...plan.problems];
    const dirs = new Map<string, { path: string; existed: boolean }>([
      ["", { path: dest, existed: true }],
    ]);
    for (const d of plan.dirs) {
      if (cancelled()) return;
      const parent = dirs.get(parentKey(d));
      if (!parent) continue;
      try {
        const r = await fc.mkdir(parent.path, d[d.length - 1] as string, true);
        dirs.set(keyOf(d), { path: r.path, existed: r.existed });
      } catch (e) {
        problems.push({ name: keyOf(d), code: codeOf(e) });
      }
    }
    const names = new Map<string, Set<string>>();
    const ask = opts.ops ? opts.ask : null;
    if (ask) {
      for (const f of plan.files) {
        const d = dirs.get(keyOf(f.dir));
        if (!d || names.has(keyOf(f.dir))) continue;
        names.set(keyOf(f.dir), d.existed ? await remoteNames(fc, d.path) : new Set());
        if (cancelled()) return;
      }
    }
    const existing = (d: readonly string[]) => names.get(keyOf(d)) ?? new Set<string>();
    const placed = await settle(
      plan,
      existing,
      ask &&
        ((f, remaining) => {
          if (cancelled()) throw new FileError("cancelled");
          return ask({
            direction: "upload",
            name: f.name,
            where: dirs.get(keyOf(f.dir))?.path ?? dest,
            remaining,
          });
        }),
      () => fc.updateBatch(id, { phase: "asking" }),
    );
    if (cancelled()) return;
    const go = placed.filter((p) => dirs.has(keyOf(p.file.dir)));
    fc.updateBatch(id, {
      phase: "running",
      files: go.length,
      bytes: go.reduce((n, p) => n + p.file.size, 0),
      skipped: plan.files.length - placed.length,
      problems,
    });
    for (const p of go) {
      const dir = dirs.get(keyOf(p.file.dir))?.path ?? dest;
      fc.upload(renamed(p.file.src, p.name), dir, { replace: p.replace, batch: id });
    }
  });
  return id;
}
