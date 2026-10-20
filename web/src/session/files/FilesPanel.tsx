// In-stage side panel for browsing the device and moving files. It is a sibling
// of the input surface, so nothing typed or clicked here reaches the remote.

import { useCallback, useEffect, useRef, useState } from "react";
import { Icon } from "../../components/icons";
import { Spinner } from "../../components/ui";
import { breadcrumbs, describeFileError, FileError, joinPath } from "../../lib/files";
import type { FileEntry, Listing, RootsReply } from "../../lib/files";
import { bytes } from "../../lib/format";
import { createDownloadSink, warmDownloadWorker } from "../downloadSinks";
import type { FileChannel } from "../FileChannel";
import { TransferList, useTransfers } from "./TransferList";

type Dir = {
  path: string;
  parent: string;
  entries: FileEntry[];
  next: number | null;
  total: number;
  truncated: boolean;
};

const errorCode = (e: unknown) => (e instanceof FileError ? e.code : "io");

export function FilesPanel({
  files,
  onClose,
  onFolder,
}: {
  files: FileChannel;
  onClose: () => void;
  /** The folder on screen, used as the drop target (null until known). */
  onFolder: (path: string | null) => void;
}) {
  const [roots, setRoots] = useState<RootsReply | null>(null);
  const [dir, setDir] = useState<Dir | null>(null);
  const [hidden, setHidden] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const transfers = useTransfers(files);
  const input = useRef<HTMLInputElement>(null);
  const root = useRef<HTMLElement>(null);
  const gen = useRef(0);

  useEffect(() => {
    root.current?.focus({ preventScroll: true });
    warmDownloadWorker();
  }, []);

  const load = useCallback(
    async (path: string, offset: number, showHidden: boolean) => {
      const my = ++gen.current;
      setLoading(true);
      setError(null);
      try {
        const l: Listing = await files.list(path, offset, showHidden);
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
        if (offset === 0) onFolder(l.path);
      } catch (e) {
        if (my === gen.current) setError(errorCode(e));
      } finally {
        if (my === gen.current) setLoading(false);
      }
    },
    [files, onFolder],
  );

  useEffect(() => {
    let cancelled = false;
    files
      .roots()
      .then((r) => {
        if (cancelled) return;
        setRoots(r);
        const start = r.default_dir || r.roots[0]?.path;
        if (start) void load(start, 0, false);
      })
      .catch((e: unknown) => !cancelled && setError(errorCode(e)));
    return () => {
      cancelled = true;
    };
  }, [files, load]);

  // Show an upload in the folder it landed in.
  const doneUploads = transfers.filter((t) => t.direction === "upload" && t.status === "done");
  const lastDone = doneUploads.at(-1);
  const seenDone = useRef<number | null>(null);
  useEffect(() => {
    if (!lastDone || seenDone.current === lastDone.xfer || !dir || !roots) return;
    seenDone.current = lastDone.xfer;
    const sep = roots.sep;
    const parent = lastDone.path?.slice(0, lastDone.path.lastIndexOf(sep)) ?? "";
    if (parent === dir.path || `${parent}${sep}` === dir.path) void load(dir.path, 0, hidden);
  }, [lastDone, dir, roots, hidden, load]);

  const sep = roots?.sep ?? "/";
  const crumbs = dir && roots ? breadcrumbs(dir.path, sep, roots.roots) : [];
  const currentRoot = roots?.roots.find((r) => r.path === crumbs[0]?.path)?.path ?? "";

  const open = (path: string) => void load(path, 0, hidden);
  const download = (e: FileEntry) => {
    if (!dir) return;
    // Synchronous on purpose: the save picker needs this click's user activation.
    const sink = createDownloadSink(e.name);
    files.download({ path: joinPath(dir.path, e.name, sep), name: e.name, size: e.size }, sink);
  };
  const upload = (list: FileList | null) => {
    for (const f of list ?? []) files.upload(f, dir?.path ?? null);
    if (input.current) input.current.value = "";
  };

  return (
    <aside
      ref={root}
      tabIndex={-1}
      aria-label="Files on the device"
      className="absolute top-0 right-0 bottom-0 z-30 flex w-full flex-col border-l border-white/10 bg-[#1e1f20] text-sm text-white shadow-xl outline-none sm:w-96"
    >
      <div className="flex items-center gap-2 border-b border-white/10 px-4 py-3">
        <Icon name="folder" size={18} className="text-white/70" />
        <h2 className="font-medium">Files</h2>
        <button
          className="btn btn-sm ml-auto px-2 text-white/80 hover:bg-white/10"
          aria-label="Close files"
          onClick={onClose}
        >
          <Icon name="close" size={18} />
        </button>
      </div>

      <div className="space-y-2 border-b border-white/10 px-4 py-3">
        <div className="flex gap-2">
          <select
            className="min-w-0 flex-1 rounded-lg bg-white/10 px-2 py-1.5 text-xs"
            aria-label="Location"
            value={currentRoot}
            onChange={(e) => e.target.value && open(e.target.value)}
            disabled={!roots}
          >
            {!currentRoot && (
              <option value="" className="text-black">
                Other location
              </option>
            )}
            {roots?.roots.map((r) => (
              <option key={r.path} value={r.path} className="text-black">
                {r.name}
              </option>
            ))}
          </select>
          <button
            className="btn btn-sm bg-[#8ab4f8] text-[#0b1d36] hover:brightness-110"
            onClick={() => input.current?.click()}
            disabled={!dir}
            title={dir ? `Upload to ${dir.path}` : undefined}
          >
            <Icon name="upload" size={16} />
            Upload
          </button>
          <input ref={input} type="file" multiple hidden onChange={(e) => upload(e.target.files)} />
        </div>
        <nav aria-label="Path" className="flex flex-wrap items-center gap-0.5 text-xs">
          {dir?.parent && (
            <button
              className="mr-1 rounded p-0.5 text-white/70 hover:bg-white/10"
              aria-label="Parent folder"
              onClick={() => open(dir.parent)}
            >
              <Icon name="back" size={14} />
            </button>
          )}
          {crumbs.map((c, i) => (
            <span key={c.path} className="flex items-center">
              {i > 0 && <Icon name="chevronRight" size={14} className="text-white/40" />}
              <button
                className="max-w-40 truncate rounded px-1 py-0.5 text-white/80 hover:bg-white/10"
                onClick={() => open(c.path)}
                aria-current={i === crumbs.length - 1 ? "page" : undefined}
              >
                {c.name}
              </button>
            </span>
          ))}
        </nav>
        <label className="flex items-center gap-2 text-xs text-white/60">
          <input
            type="checkbox"
            checked={hidden}
            onChange={(e) => {
              setHidden(e.target.checked);
              if (dir) void load(dir.path, 0, e.target.checked);
            }}
          />
          Show hidden files
        </label>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        {error && (
          <div className="m-2 rounded-lg bg-[#3c1f1d] px-3 py-2 text-xs text-[#f28b82]">
            {describeFileError(error)}
          </div>
        )}
        {dir && dir.entries.length === 0 && !loading && !error && (
          <p className="p-4 text-center text-xs text-white/50">This folder is empty.</p>
        )}
        <ul>
          {dir?.entries.map((e) => (
            <li
              key={e.name}
              className={`group flex items-center gap-2 rounded-lg px-2 py-1.5 hover:bg-white/5 ${e.hidden ? "opacity-60" : ""}`}
            >
              <Icon
                name={e.kind === "dir" ? "folder" : "file"}
                size={18}
                className={e.kind === "dir" ? "text-[#8ab4f8]" : "text-white/50"}
              />
              {e.kind === "dir" ? (
                <button
                  className="min-w-0 flex-1 truncate text-left hover:underline"
                  onClick={() => open(joinPath(dir.path, e.name, sep))}
                >
                  {e.name}
                </button>
              ) : (
                <span className="min-w-0 flex-1 truncate" title={e.name}>
                  {e.name}
                  {e.link && <span className="ml-1 text-xs text-white/40">(link)</span>}
                </span>
              )}
              {e.kind !== "dir" && (
                <span className="text-xs text-white/40 tabular-nums">{bytes(e.size)}</span>
              )}
              {e.kind === "file" && (
                <button
                  className="rounded-full p-1 text-white/60 hover:bg-white/10 hover:text-white"
                  aria-label={`Download ${e.name}`}
                  title="Download"
                  onClick={() => download(e)}
                >
                  <Icon name="download" size={16} />
                </button>
              )}
            </li>
          ))}
        </ul>
        {loading && (
          <div className="flex justify-center p-3">
            <Spinner size={20} />
          </div>
        )}
        {dir && dir.next !== null && !loading && (
          <button
            className="btn btn-sm mx-auto mt-2 flex text-[#8ab4f8] hover:bg-white/10"
            onClick={() => dir.next !== null && void load(dir.path, dir.next, hidden)}
          >
            Load more ({dir.entries.length} of {dir.total})
          </button>
        )}
        {dir?.truncated && dir.next === null && (
          <p className="p-2 text-center text-xs text-white/50">
            This folder has more entries than can be listed.
          </p>
        )}
      </div>

      <div className="max-h-[45%] overflow-y-auto border-t border-white/10 px-4 py-3 empty:hidden">
        <TransferList
          transfers={transfers}
          onCancel={(x) => files.cancel(x)}
          onClear={() => files.clearFinished()}
        />
      </div>
    </aside>
  );
}
