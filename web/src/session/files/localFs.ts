// "This computer": a folder the user picked through the File System Access
// API (Chrome, Edge), plus the sources every browser has (drops, file and
// folder inputs) and the browser's Downloads folder as a fallback target.

import { idbGet, idbPut } from "../../lib/idb";
import { FileError } from "../../lib/files";
import type { FileEntry } from "../../lib/files";
import { folded } from "../../lib/transferPlan";
import type { LocalNode } from "../../lib/transferPlan";
import { createDownloadSink } from "../downloadSinks";
import type { DownloadSink } from "../FileChannel";

type Permission = { mode: "readwrite" };
type PermissionHandle = FileSystemHandle & {
  queryPermission?: (d: Permission) => Promise<PermissionState>;
  requestPermission?: (d: Permission) => Promise<PermissionState>;
};
type IterableDir = FileSystemDirectoryHandle & {
  values(): AsyncIterable<FileSystemFileHandle | FileSystemDirectoryHandle>;
  keys(): AsyncIterable<string>;
};
type DirectoryPicker = (opts: {
  id?: string;
  mode?: "read" | "readwrite";
  startIn?: string;
}) => Promise<FileSystemDirectoryHandle>;

const FOLDER_KEY = "files";

const picker = () =>
  (window as Window & { showDirectoryPicker?: DirectoryPicker }).showDirectoryPicker;

/** Whether this browser can open a local folder for reading and writing. */
export const canPickFolders = () => typeof picker() === "function";

/** Opens the folder picker; must run in a click handler. Rejects when dismissed. */
export async function pickFolder(): Promise<FileSystemDirectoryHandle> {
  const show = picker();
  if (!show) throw new FileError("unsupported");
  const dir = await show.call(window, {
    id: "remotectl-files",
    mode: "readwrite",
    startIn: "downloads",
  });
  await idbPut("local_folders", FOLDER_KEY, dir).catch(() => undefined);
  return dir;
}

/** The folder picked last time, if the browser still has it. */
export async function savedFolder(): Promise<FileSystemDirectoryHandle | null> {
  if (!canPickFolders()) return null;
  return (
    (await idbGet<FileSystemDirectoryHandle>("local_folders", FOLDER_KEY).catch(() => null)) ?? null
  );
}

/** Whether the page may read and write `dir`; `ask` prompts (needs a click). */
export async function folderAccess(dir: FileSystemDirectoryHandle, ask: boolean): Promise<boolean> {
  const h = dir as PermissionHandle;
  const opts: Permission = { mode: "readwrite" };
  if (!h.queryPermission) return true;
  if ((await h.queryPermission(opts)) === "granted") return true;
  return ask && !!h.requestPermission && (await h.requestPermission(opts)) === "granted";
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

/** Folders first, then files, by natural name order (as the agent lists them). */
export function sortEntries(entries: FileEntry[]): FileEntry[] {
  const rank = (e: FileEntry) => (e.kind === "dir" ? 0 : 1);
  return entries.sort((a, b) => rank(a) - rank(b) || collator.compare(a.name, b.name));
}

export async function listFolder(dir: FileSystemDirectoryHandle): Promise<FileEntry[]> {
  const pending: Promise<FileEntry>[] = [];
  for await (const h of (dir as IterableDir).values()) {
    const hidden = h.name.startsWith(".");
    if (h.kind === "directory") {
      pending.push(
        Promise.resolve({ name: h.name, kind: "dir", link: false, size: 0, mtime: 0, hidden }),
      );
    } else {
      pending.push(
        h.getFile().then(
          (f): FileEntry => ({
            name: h.name,
            kind: "file",
            link: false,
            size: f.size,
            mtime: f.lastModified,
            hidden,
          }),
          (): FileEntry => ({
            name: h.name,
            kind: "other",
            link: false,
            size: 0,
            mtime: 0,
            hidden,
          }),
        ),
      );
    }
  }
  return sortEntries(await Promise.all(pending));
}

/** The sub-folder at `rel` below `root`; null when it does not exist and `create` is false. */
export async function subFolder(
  root: FileSystemDirectoryHandle,
  rel: readonly string[],
  create: boolean,
): Promise<FileSystemDirectoryHandle | null> {
  let dir = root;
  for (const name of rel) {
    try {
      dir = await dir.getDirectoryHandle(name, { create });
    } catch (e) {
      if (!create && e instanceof DOMException && e.name === "NotFoundError") return null;
      throw localError(e);
    }
  }
  return dir;
}

/** Maps a File System Access failure to a file error code. */
export function localError(e: unknown): FileError {
  if (e instanceof FileError) return e;
  const name = e instanceof DOMException ? e.name : "";
  const detail = e instanceof Error ? e.message : String(e);
  if (name === "TypeMismatchError") return new FileError("local_exists", detail);
  if (name === "NotAllowedError" || name === "SecurityError")
    return new FileError("local_denied", detail);
  if (name === "AbortError") return new FileError("cancelled", detail);
  if (name === "QuotaExceededError") return new FileError("no_space", detail);
  return new FileError("io", detail);
}

export function nodeFromHandle(
  h: FileSystemFileHandle | FileSystemDirectoryHandle,
): Promise<LocalNode> {
  if (h.kind === "file") return h.getFile().then((file) => ({ kind: "file", name: h.name, file }));
  return Promise.resolve({
    kind: "dir",
    name: h.name,
    children: async () => {
      const out: Promise<LocalNode>[] = [];
      for await (const c of (h as IterableDir).values()) out.push(nodeFromHandle(c));
      return Promise.all(out);
    },
  });
}

/** The picked entries of a local folder as transfer sources. */
export async function nodesIn(
  dir: FileSystemDirectoryHandle,
  names: readonly string[],
): Promise<LocalNode[]> {
  const want = new Set(names);
  const out: Promise<LocalNode>[] = [];
  for await (const h of (dir as IterableDir).values())
    if (want.has(h.name)) out.push(nodeFromHandle(h));
  return Promise.all(out);
}

function nodeFromEntry(e: FileSystemEntry): LocalNode | Promise<LocalNode> {
  if (e.isFile) {
    return new Promise((resolve, reject) =>
      (e as FileSystemFileEntry).file(
        (file) => resolve({ kind: "file", name: e.name, file }),
        reject,
      ),
    );
  }
  return {
    kind: "dir",
    name: e.name,
    children: async () => {
      const reader = (e as FileSystemDirectoryEntry).createReader();
      const all: FileSystemEntry[] = [];
      // readEntries returns at most 100 entries per call; an empty batch is the end.
      for (;;) {
        const batch = await new Promise<FileSystemEntry[]>((res, rej) =>
          reader.readEntries(res, rej),
        );
        if (batch.length === 0) break;
        all.push(...batch);
      }
      return Promise.all(all.map(nodeFromEntry));
    },
  };
}

/** Files and folders dropped from the operating system (any browser). */
export function nodesFromDrop(dt: DataTransfer): Promise<LocalNode[]> {
  const out: (LocalNode | Promise<LocalNode>)[] = [];
  for (const item of Array.from(dt.items)) {
    if (item.kind !== "file") continue;
    const entry = item.webkitGetAsEntry?.();
    if (entry) {
      out.push(nodeFromEntry(entry));
      continue;
    }
    const f = item.getAsFile();
    if (f) out.push({ kind: "file", name: f.name, file: f });
  }
  return Promise.all(out);
}

/** Files from an `<input type=file>`, rebuilt into folders from `webkitRelativePath`. */
export function nodesFromInput(files: FileList | readonly File[]): LocalNode[] {
  type Dir = { kind: "dir"; name: string; items: Map<string, Dir | LocalNode> };
  const root: Dir = { kind: "dir", name: "", items: new Map() };
  for (const f of Array.from(files)) {
    const parts = (f.webkitRelativePath || f.name).split("/").filter(Boolean);
    let dir = root;
    for (const p of parts.slice(0, -1)) {
      let next = dir.items.get(`d:${p}`) as Dir | undefined;
      if (!next) {
        next = { kind: "dir", name: p, items: new Map() };
        dir.items.set(`d:${p}`, next);
      }
      dir = next;
    }
    dir.items.set(`f:${f.name}`, { kind: "file", name: f.name, file: f });
  }
  const toNode = (n: Dir | LocalNode): LocalNode =>
    "items" in n
      ? { kind: "dir", name: n.name, children: async () => [...n.items.values()].map(toNode) }
      : n;
  return [...root.items.values()].map(toNode);
}

/** Where downloads are written. */
export type LocalTarget = {
  /** Can hold folders (a picked folder, not the browser's Downloads). */
  folders: boolean;
  /** Folded names already in `rel` (empty when the folder does not exist yet). */
  names(rel: readonly string[]): Promise<Set<string>>;
  ensureDir(rel: readonly string[]): Promise<void>;
  /** A sink that opens the file when the transfer starts; overwrites an existing file. */
  sink(rel: readonly string[], name: string): DownloadSink;
};

export function folderTarget(root: FileSystemDirectoryHandle): LocalTarget {
  const dirs = new Map<string, Promise<FileSystemDirectoryHandle>>();
  const dir = (rel: readonly string[]) => {
    const key = rel.join("/");
    let p = dirs.get(key);
    if (!p) {
      p = subFolder(root, rel, true) as Promise<FileSystemDirectoryHandle>;
      p.catch(() => dirs.delete(key));
      dirs.set(key, p);
    }
    return p;
  };
  return {
    folders: true,
    async names(rel) {
      const d = await subFolder(root, rel, false);
      const out = new Set<string>();
      if (!d) return out;
      for await (const n of (d as IterableDir).keys()) out.add(folded(n));
      return out;
    },
    async ensureDir(rel) {
      await dir(rel);
    },
    sink(rel, name) {
      let writable: FileSystemWritableFileStream | null = null;
      let ready: Promise<void> | null = null;
      const w = () => {
        if (!writable) throw new FileError("io", "The file is not open.");
        return writable;
      };
      return {
        kind: "fs",
        get ready() {
          ready ??= (async () => {
            try {
              const fh = await (await dir(rel)).getFileHandle(name, { create: true });
              writable = await fh.createWritable();
            } catch (e) {
              throw localError(e);
            }
          })();
          return ready;
        },
        write: (chunk) => w().write(chunk as Uint8Array<ArrayBuffer>),
        close: () => w().close(),
        abort: (reason) => void writable?.abort(reason).catch(() => undefined),
      };
    },
  };
}

/** Browsers without folder access save each file to their Downloads folder. */
export const browserDownloads: LocalTarget = {
  folders: false,
  names: async () => new Set(),
  ensureDir: async () => undefined,
  sink: (_rel, name) => createDownloadSink(name),
};
