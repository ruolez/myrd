// State for the two sides of the file manager: the device's folders (over
// the FileChannel) and the picked local folder (File System Access API).

import { useCallback, useEffect, useRef, useState } from "react";
import { describeFileError, FileError } from "../../lib/files";
import type { FileEntry, Listing, RootsReply } from "../../lib/files";
import type { FileChannel } from "../FileChannel";
import {
  canPickFolders,
  folderAccess,
  listFolder,
  localError,
  pickFolder,
  savedFolder,
  subFolder,
} from "./localFs";

const message = (e: unknown) => describeFileError(e instanceof FileError ? e.code : "io");

export type RemoteDir = {
  path: string;
  parent: string;
  entries: FileEntry[];
  next: number | null;
  total: number;
  truncated: boolean;
};

/** The folder each session's Files view was last in. */
const lastRemote = new WeakMap<FileChannel, string>();

export function useRemoteBrowser(files: FileChannel, hidden: boolean) {
  const [roots, setRoots] = useState<RootsReply | null>(null);
  const [dir, setDir] = useState<RemoteDir | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const gen = useRef(0);

  const load = useCallback(
    async (path: string, offset = 0) => {
      const my = ++gen.current;
      setLoading(true);
      setError(null);
      try {
        const l: Listing = await files.list(path, offset, hidden);
        if (my !== gen.current) return;
        setDir((prev) => ({
          path: l.path,
          parent: l.parent,
          entries:
            offset > 0 && prev?.path === l.path ? [...prev.entries, ...l.entries] : l.entries,
          next: l.next_offset,
          total: l.total,
          truncated: l.truncated,
        }));
        lastRemote.set(files, l.path);
      } catch (e) {
        if (my === gen.current) setError(message(e));
      } finally {
        if (my === gen.current) setLoading(false);
      }
    },
    [files, hidden],
  );

  const path = dir?.path ?? null;
  useEffect(() => {
    let cancelled = false;
    files
      .roots()
      .then((r) => {
        if (cancelled) return;
        setRoots(r);
        const start = lastRemote.get(files) || r.default_dir || r.roots[0]?.path;
        if (start) void load(start);
      })
      .catch((e: unknown) => !cancelled && setError(message(e)));
    return () => {
      cancelled = true;
    };
    // Roots once per channel; `load` changes with `hidden`, handled below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [files]);

  const firstHidden = useRef(hidden);
  useEffect(() => {
    if (firstHidden.current === hidden) return;
    firstHidden.current = hidden;
    if (path) void load(path);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hidden]);

  return { roots, dir, loading, error, load, refresh: () => path && load(path) };
}

export type LocalState =
  | { status: "unsupported" }
  | { status: "loading" }
  | { status: "none" }
  /** A saved folder the browser needs permission for again (a click). */
  | { status: "locked"; root: FileSystemDirectoryHandle }
  | { status: "ready"; root: FileSystemDirectoryHandle; stack: FileSystemDirectoryHandle[] };

/** Sub-folder names below the picked folder that the Files view was last in. */
let lastLocal: string[] = [];

export function useLocalBrowser(hidden: boolean) {
  const [state, setState] = useState<LocalState>(() =>
    canPickFolders() ? { status: "loading" } : { status: "unsupported" },
  );
  const [entries, setEntries] = useState<FileEntry[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const gen = useRef(0);

  const current = state.status === "ready" ? (state.stack.at(-1) ?? null) : null;

  const list = useCallback(async (dir: FileSystemDirectoryHandle) => {
    const my = ++gen.current;
    setLoading(true);
    setError(null);
    try {
      const all = await listFolder(dir);
      if (my === gen.current) setEntries(all);
    } catch (e) {
      if (my === gen.current) {
        setEntries(null);
        setError(message(localError(e)));
      }
    } finally {
      if (my === gen.current) setLoading(false);
    }
  }, []);

  /** Opens `root` at the folder last shown below it, as far as that still exists; returns that folder. */
  const enter = useCallback(async (root: FileSystemDirectoryHandle, below: string[]) => {
    const stack = [root];
    for (const name of below) {
      const next = await subFolder(stack.at(-1) as FileSystemDirectoryHandle, [name], false).catch(
        () => null,
      );
      if (!next) break;
      stack.push(next);
    }
    setState({ status: "ready", root, stack });
    return stack.at(-1) as FileSystemDirectoryHandle;
  }, []);

  useEffect(() => {
    if (!canPickFolders()) return;
    let cancelled = false;
    void (async () => {
      const root = await savedFolder();
      if (cancelled) return;
      if (!root) return setState({ status: "none" });
      if (await folderAccess(root, false)) {
        if (!cancelled) await enter(root, lastLocal);
      } else if (!cancelled) {
        setState({ status: "locked", root });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [enter]);

  useEffect(() => {
    if (state.status !== "ready") return;
    lastLocal = state.stack.slice(1).map((d) => d.name);
    void list(state.stack.at(-1) as FileSystemDirectoryHandle);
  }, [state, list]);

  const shown = entries && !hidden ? entries.filter((e) => !e.hidden) : entries;

  return {
    state,
    current,
    entries: shown,
    loading,
    error,
    /** Opens the folder picker and shows the chosen folder; call from a click. */
    pick: async (): Promise<FileSystemDirectoryHandle> => {
      try {
        return await enter(await pickFolder(), []);
      } catch (e) {
        const err = localError(e);
        if (err.code !== "cancelled") setError(message(err));
        throw err;
      }
    },
    /** Asks again for a saved folder and shows it; call from a click. */
    unlock: async (): Promise<FileSystemDirectoryHandle> => {
      if (state.status !== "locked") throw new FileError("local_denied");
      if (await folderAccess(state.root, true)) return enter(state.root, lastLocal);
      throw new FileError("local_denied");
    },
    open: async (name: string) => {
      if (state.status !== "ready" || !current) return;
      try {
        const next = await subFolder(current, [name], false);
        if (next) setState({ ...state, stack: [...state.stack, next] });
      } catch (e) {
        setError(message(localError(e)));
      }
    },
    /** Goes up to the folder at `depth` in the path (0 = the picked folder). */
    up: (depth: number) => {
      if (state.status !== "ready") return;
      setState({ ...state, stack: state.stack.slice(0, Math.max(1, depth + 1)) });
    },
    refresh: () => current && list(current),
    mkdir: async (name: string) => {
      if (!current) return;
      const taken = await Promise.any([
        current.getDirectoryHandle(name),
        current.getFileHandle(name),
      ]).then(
        () => true,
        () => false,
      );
      if (taken) throw new FileError("exists");
      await current.getDirectoryHandle(name, { create: true }).catch((e: unknown) => {
        throw localError(e);
      });
      await list(current);
    },
  };
}

export type LocalBrowser = ReturnType<typeof useLocalBrowser>;
export type RemoteBrowser = ReturnType<typeof useRemoteBrowser>;
