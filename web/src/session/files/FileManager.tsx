// Two-pane file manager over the session: this computer on the left, the
// device on the right, the transfer queue below. It is a sibling of the input
// surface, so nothing typed or clicked here reaches the remote.

import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "../../components/icons";
import { SegmentedControl, useConfirm, usePrompt, useToast } from "../../components/ui";
import { baseName, breadcrumbs, describeFileError, FileError, joinPath } from "../../lib/files";
import type { FileEntry } from "../../lib/files";
import { EMPTY_SELECTION, pruneSelection } from "../../lib/fileSelection";
import type { Selection } from "../../lib/fileSelection";
import { warmDownloadWorker } from "../downloadSinks";
import type { FileChannel } from "../FileChannel";
import { startDownload, startUpload } from "./batches";
import type { Ask } from "./batches";
import { useLocalBrowser, useRemoteBrowser } from "./browsers";
import { FilePane, PaneButton } from "./FilePane";
import type { Side } from "./FilePane";
import { browserDownloads, folderTarget, nodesFromInput, nodesIn } from "./localFs";
import { TransferQueue, useBatches, useTransfers } from "./TransferQueue";

const codeOf = (e: unknown) => (e instanceof FileError ? e.code : "io");

/** Selection state that forgets names no longer listed. */
function useSelection(entries: readonly FileEntry[] | null) {
  const [sel, setSel] = useState<Selection>(EMPTY_SELECTION);
  const order = useMemo(() => entries?.map((e) => e.name) ?? [], [entries]);
  const pruned = pruneSelection(sel, order);
  return [pruned, setSel] as const;
}

/** Calls `fn` (debounced) whenever transfers of `direction` finish. */
function useOnFinished(files: FileChannel, direction: "upload" | "download", fn: () => void) {
  const transfers = useTransfers(files);
  const done = transfers.filter((t) => t.direction === direction && t.status === "done").length;
  const batches = useBatches(files);
  const settled = batches.filter((b) => b.direction === direction && b.phase === "running").length;
  const ref = useRef(fn);
  useEffect(() => {
    ref.current = fn;
  });
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    const t = setTimeout(() => ref.current(), 400);
    return () => clearTimeout(t);
  }, [done, settled]);
}

export function FileManager({
  files,
  ops,
  deviceName,
  ask,
  onClose,
  onFolder,
}: {
  files: FileChannel;
  /** The agent takes mkdir, rename, delete and replacing uploads. */
  ops: boolean;
  deviceName: string;
  ask: Ask;
  onClose: () => void;
  /** The device folder on screen, used as the target of drops on the stage. */
  onFolder: (path: string | null) => void;
}) {
  const [hidden, setHidden] = useState(false);
  const [side, setSide] = useState<Side>("remote");
  const remote = useRemoteBrowser(files, hidden);
  const local = useLocalBrowser(hidden);
  const [rsel, setRsel] = useSelection(remote.dir?.entries ?? null);
  const [lsel, setLsel] = useSelection(local.entries);
  const confirm = useConfirm();
  const prompt = usePrompt();
  const toast = useToast();
  const folderInput = useRef<HTMLInputElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const root = useRef<HTMLElement>(null);

  useEffect(() => {
    root.current?.querySelector<HTMLElement>("[role=grid]")?.focus({ preventScroll: true });
    warmDownloadWorker();
  }, []);

  const rdir = remote.dir;
  useEffect(() => onFolder(rdir?.path ?? null), [rdir?.path, onFolder]);
  useEffect(() => () => onFolder(null), [onFolder]);

  useOnFinished(files, "upload", () => void remote.refresh());
  useOnFinished(files, "download", () => void local.refresh());

  const sep = remote.roots?.sep ?? "/";
  const fail = (e: unknown) => {
    const code = codeOf(e);
    if (code !== "cancelled") toast(describeFileError(code), "error");
  };

  // ---------- this computer ----------

  const localPath =
    local.state.status === "ready" ? local.state.stack.map((d) => d.name).join("/") : "Downloads";

  const download = async (entries: FileEntry[], into?: string | null) => {
    if (!rdir || entries.length === 0) return;
    let dir = local.current;
    let base = localPath;
    // Choosing or re-allowing the folder needs this click's user activation, so it goes first.
    if (local.state.status === "none" || local.state.status === "locked") {
      try {
        dir = local.state.status === "none" ? await local.pick() : await local.unlock();
        base = dir.name;
      } catch (e) {
        return fail(e);
      }
    }
    let label = base;
    if (dir && into) {
      try {
        dir = await dir.getDirectoryHandle(into);
        label = `${base}/${into}`;
      } catch (e) {
        return fail(e);
      }
    }
    startDownload(files, {
      from: rdir.path,
      sep,
      entries,
      hidden,
      to: dir ? folderTarget(dir) : browserDownloads,
      toLabel: dir ? label : "Downloads",
      ask,
    });
  };

  const upload = async (names: string[], into?: string | null) => {
    if (!rdir || !local.current || names.length === 0) return;
    try {
      const nodes = await nodesIn(local.current, names);
      startUpload(files, {
        nodes,
        to: into ? joinPath(rdir.path, into, sep) : rdir.path,
        ops,
        ask,
      });
    } catch (e) {
      fail(e);
    }
  };

  const fromInput = (list: FileList | null) => {
    if (list && list.length > 0 && rdir) {
      startUpload(files, { nodes: nodesFromInput(list), to: rdir.path, ops, ask });
    }
    if (folderInput.current) folderInput.current.value = "";
    if (fileInput.current) fileInput.current.value = "";
  };

  const newLocalFolder = async () => {
    const name = await prompt({
      title: "New folder",
      label: "Name",
      initial: "New folder",
      confirm: "Create",
    });
    if (name) await local.mkdir(name).catch(fail);
  };

  const remoteSelected = rdir?.entries.filter((e) => rsel.selected.has(e.name)) ?? [];
  const localSelected = local.entries?.filter((e) => lsel.selected.has(e.name)) ?? [];
  const canFolders = local.state.status !== "unsupported";
  const downloadBlocked =
    !canFolders && remoteSelected.some((e) => e.kind === "dir")
      ? "Downloading folders needs Chrome or Edge"
      : null;
  const uploadBlocked =
    !ops && localSelected.some((e) => e.kind === "dir")
      ? "Update the agent on this device to upload folders"
      : null;

  const localNotice = ((): ReactNode => {
    const box = (children: ReactNode) => (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center text-sm text-white/70">
        {children}
      </div>
    );
    switch (local.state.status) {
      case "loading":
        return box(null);
      case "none":
        return box(
          <>
            <Icon name="folderOpen" size={36} className="text-[#8ab4f8]" />
            <p>Choose a folder on this computer to download into and upload from.</p>
            <button className="btn-primary" onClick={() => void local.pick().catch(fail)}>
              Choose folder…
            </button>
          </>,
        );
      case "locked":
        return box(
          <>
            <Icon name="lock" size={32} className="text-[#8ab4f8]" />
            <p>
              Allow this page to use <span className="font-medium">{local.state.root.name}</span>{" "}
              again.
            </p>
            <div className="flex gap-2">
              <button className="btn-primary" onClick={() => void local.unlock().catch(fail)}>
                Allow
              </button>
              <button className="btn-text" onClick={() => void local.pick().catch(fail)}>
                Choose another
              </button>
            </div>
          </>,
        );
      case "unsupported":
        return box(
          <>
            <Icon name="download" size={32} className="text-[#8ab4f8]" />
            <p>
              This browser can't open folders on this computer, so downloads go to its Downloads
              folder. Chrome and Edge can show a folder here.
            </p>
            <div className="flex flex-wrap justify-center gap-2">
              <button
                className="btn-primary"
                onClick={() => fileInput.current?.click()}
                disabled={!rdir}
              >
                <Icon name="upload" size={16} />
                Upload files…
              </button>
              <button
                className="btn-tonal"
                onClick={() => folderInput.current?.click()}
                disabled={!rdir || !ops}
              >
                Upload a folder…
              </button>
            </div>
          </>,
        );
    }
    return undefined;
  })();

  const localCrumbs =
    local.state.status === "ready" ? (
      <nav aria-label="Local path" className="flex min-w-0 flex-wrap items-center gap-0.5 text-xs">
        {local.state.stack.map((d, i, all) => (
          <span key={i} className="flex min-w-0 items-center">
            {i > 0 && <Icon name="chevronRight" size={14} className="text-white/40" />}
            <button
              className="max-w-40 truncate rounded px-1 py-0.5 text-white/80 hover:bg-white/10"
              onClick={() => local.up(i)}
              aria-current={i === all.length - 1 ? "page" : undefined}
            >
              {d.name}
            </button>
          </span>
        ))}
      </nav>
    ) : (
      <p className="text-xs text-white/50">
        {local.state.status === "unsupported"
          ? "The browser's Downloads folder"
          : "No folder chosen"}
      </p>
    );

  const localPane = (
    <FilePane
      side="local"
      label="Files on this computer"
      className={side === "local" ? "" : "hidden md:flex"}
      location={
        <div className="flex items-center gap-2">
          <Icon name="monitor" size={16} className="shrink-0 text-white/60" />
          <span className="text-xs font-medium text-white/80">This computer</span>
          <div className="min-w-0 flex-1">{localCrumbs}</div>
        </div>
      }
      toolbar={
        local.state.status === "ready" ? (
          <>
            <PaneButton
              icon="upload"
              label="Upload to the device"
              text={localSelected.length > 1 ? `Upload ${localSelected.length}` : "Upload"}
              primary
              onClick={() => void upload(localSelected.map((e) => e.name))}
              disabled={localSelected.length === 0 || !rdir || !!uploadBlocked}
              title={uploadBlocked ?? (rdir ? `Upload to ${rdir.path}` : undefined)}
            />
            <PaneButton
              icon="back"
              label="Parent folder"
              onClick={() =>
                local.up(local.state.status === "ready" ? local.state.stack.length - 2 : 0)
              }
              disabled={local.state.stack.length < 2}
            />
            <PaneButton icon="newFolder" label="New folder" onClick={() => void newLocalFolder()} />
            <PaneButton icon="refresh" label="Refresh" onClick={() => void local.refresh()} />
            <PaneButton
              icon="folderOpen"
              label="Choose another folder"
              text="Change"
              onClick={() => void local.pick().catch(fail)}
            />
          </>
        ) : null
      }
      entries={local.state.status === "ready" ? local.entries : null}
      loading={local.loading}
      error={local.error}
      notice={localNotice}
      sel={lsel}
      onSel={setLsel}
      onOpen={(e) => (e.kind === "dir" ? void local.open(e.name) : void upload([e.name]))}
      onParent={() => local.state.status === "ready" && local.up(local.state.stack.length - 2)}
      onTransfer={() => !uploadBlocked && void upload(localSelected.map((e) => e.name))}
      onDropFrom={(folder) => {
        if (downloadBlocked) return toast(downloadBlocked, "error");
        void download(remoteSelected, folder);
      }}
      onEscape={onClose}
    />
  );

  // ---------- the device ----------

  const crumbs = rdir && remote.roots ? breadcrumbs(rdir.path, sep, remote.roots.roots) : [];
  const currentRoot = remote.roots?.roots.find((r) => r.path === crumbs[0]?.path)?.path ?? "";

  const newRemoteFolder = async () => {
    if (!rdir) return;
    const name = await prompt({
      title: "New folder",
      label: "Name",
      initial: "New folder",
      confirm: "Create",
    });
    if (!name) return;
    try {
      const r = await files.mkdir(rdir.path, name);
      await remote.load(rdir.path);
      setRsel({
        selected: new Set([baseName(r.path, sep)]),
        anchor: baseName(r.path, sep),
        focus: baseName(r.path, sep),
      });
    } catch (e) {
      fail(e);
    }
  };

  const renameRemote = async () => {
    const e = remoteSelected[0];
    if (!rdir || !e || remoteSelected.length !== 1) return;
    const name = await prompt({
      title: `Rename ${e.name}`,
      label: "New name",
      initial: e.name,
      confirm: "Rename",
    });
    if (!name || name === e.name) return;
    try {
      const r = await files.rename(joinPath(rdir.path, e.name, sep), name);
      await remote.load(rdir.path);
      const n = baseName(r.path, sep);
      setRsel({ selected: new Set([n]), anchor: n, focus: n });
    } catch (err) {
      fail(err);
    }
  };

  const deleteRemote = async () => {
    if (!rdir || remoteSelected.length === 0) return;
    const items = remoteSelected;
    const folders = items.filter((e) => e.kind === "dir").length;
    const what = items.length === 1 ? `“${items[0]?.name}”` : `${items.length} items`;
    const ok = await confirm({
      title: `Delete ${what}?`,
      message: (
        <>
          <p>
            {items.length === 1 ? "It is" : "They are"} deleted from {deviceName} permanently, not
            moved to the Trash or Recycle Bin.
            {folders > 0 && " Folders are deleted with everything in them."}
          </p>
          {items.length > 1 && (
            <ul className="mt-3 max-h-40 list-disc overflow-y-auto pl-5 text-muted">
              {items.slice(0, 50).map((e) => (
                <li key={e.name} className="break-all">
                  {e.name}
                  {e.kind === "dir" ? "/" : ""}
                </li>
              ))}
              {items.length > 50 && <li>and {items.length - 50} more</li>}
            </ul>
          )}
        </>
      ),
      confirm: "Delete",
      danger: true,
    });
    if (!ok) return;
    const failed: string[] = [];
    for (const e of items) {
      try {
        await files.remove(joinPath(rdir.path, e.name, sep));
      } catch (err) {
        failed.push(`${e.name}: ${describeFileError(codeOf(err))}`);
      }
    }
    setRsel(EMPTY_SELECTION);
    await remote.load(rdir.path);
    if (failed.length > 0) toast(`Not deleted — ${failed.join("; ")}`, "error");
  };

  const remotePane = (
    <FilePane
      side="remote"
      label={`Files on ${deviceName}`}
      className={side === "remote" ? "" : "hidden md:flex"}
      location={
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <Icon name="devices" size={16} className="shrink-0 text-white/60" />
          <select
            className="max-w-40 min-w-0 rounded-lg bg-white/10 px-2 py-1 text-xs"
            aria-label="Location on the device"
            value={currentRoot}
            onChange={(e) => e.target.value && void remote.load(e.target.value)}
            disabled={!remote.roots}
          >
            {!currentRoot && (
              <option value="" className="text-black">
                Other location
              </option>
            )}
            {remote.roots?.roots.map((r) => (
              <option key={r.path} value={r.path} className="text-black">
                {r.name}
              </option>
            ))}
          </select>
          <nav
            aria-label="Device path"
            className="flex min-w-0 flex-1 flex-wrap items-center gap-0.5 text-xs"
          >
            {crumbs.map((c, i) => (
              <span key={c.path} className="flex min-w-0 items-center">
                {i > 0 && <Icon name="chevronRight" size={14} className="text-white/40" />}
                <button
                  className="max-w-40 truncate rounded px-1 py-0.5 text-white/80 hover:bg-white/10"
                  onClick={() => void remote.load(c.path)}
                  aria-current={i === crumbs.length - 1 ? "page" : undefined}
                >
                  {c.name}
                </button>
              </span>
            ))}
          </nav>
        </div>
      }
      toolbar={
        <>
          <PaneButton
            icon="download"
            label="Download to this computer"
            text={remoteSelected.length > 1 ? `Download ${remoteSelected.length}` : "Download"}
            primary
            onClick={() => void download(remoteSelected)}
            disabled={remoteSelected.length === 0 || !!downloadBlocked}
            title={downloadBlocked ?? `Download to ${localPath}`}
          />
          <PaneButton
            icon="back"
            label="Parent folder"
            onClick={() => rdir?.parent && void remote.load(rdir.parent)}
            disabled={!rdir?.parent}
          />
          {ops && (
            <>
              <PaneButton
                icon="newFolder"
                label="New folder"
                onClick={() => void newRemoteFolder()}
                disabled={!rdir}
              />
              <PaneButton
                icon="edit"
                label="Rename"
                onClick={() => void renameRemote()}
                disabled={remoteSelected.length !== 1}
              />
              <PaneButton
                icon="trash"
                label="Delete"
                onClick={() => void deleteRemote()}
                disabled={remoteSelected.length === 0}
              />
            </>
          )}
          <PaneButton
            icon="refresh"
            label="Refresh"
            onClick={() => void remote.refresh()}
            disabled={!rdir}
          />
        </>
      }
      entries={rdir?.entries ?? null}
      loading={remote.loading}
      error={remote.error}
      sel={rsel}
      onSel={setRsel}
      onOpen={(e) =>
        e.kind === "dir" && rdir
          ? void remote.load(joinPath(rdir.path, e.name, sep))
          : void download([e])
      }
      onParent={() => rdir?.parent && void remote.load(rdir.parent)}
      onTransfer={() => !downloadBlocked && void download(remoteSelected)}
      onDelete={ops ? () => void deleteRemote() : undefined}
      onRename={ops ? () => void renameRemote() : undefined}
      onDropFrom={(folder) => {
        if (uploadBlocked) return toast(uploadBlocked, "error");
        void upload(
          localSelected.map((e) => e.name),
          folder,
        );
      }}
      onEscape={onClose}
      footer={
        <>
          {rdir && rdir.next !== null && !remote.loading && (
            <button
              className="btn btn-sm mx-auto my-2 flex text-[#8ab4f8] hover:bg-white/10"
              onClick={() => rdir.next !== null && void remote.load(rdir.path, rdir.next)}
            >
              Load more ({rdir.entries.length} of {rdir.total})
            </button>
          )}
          {rdir?.truncated && rdir.next === null && (
            <p className="p-2 text-center text-xs text-white/50">
              This folder has more entries than can be listed.
            </p>
          )}
        </>
      }
    />
  );

  return (
    <aside
      ref={root}
      aria-label="Files"
      className="absolute inset-0 z-30 flex flex-col overflow-hidden border-white/10 bg-[#131314] text-sm text-white shadow-2xl sm:inset-3 sm:rounded-xl sm:border"
      onKeyDown={(e) => {
        if (e.key === "Escape" && e.target === e.currentTarget) onClose();
      }}
    >
      <header className="flex items-center gap-x-3 border-b border-white/10 px-4 py-2.5">
        <Icon name="folder" size={18} className="text-white/70" />
        <h2 className="font-medium">Files</h2>
        <div className="md:hidden">
          <SegmentedControl
            label="Show"
            value={side}
            onChange={setSide}
            options={[
              { value: "local", label: "This computer" },
              { value: "remote", label: "Device" },
            ]}
          />
        </div>
        <label
          className="ml-auto flex items-center gap-2 text-xs text-white/60"
          title="Show hidden files"
        >
          <input type="checkbox" checked={hidden} onChange={(e) => setHidden(e.target.checked)} />
          <span className="hidden sm:inline">Show hidden files</span>
          <span className="sm:hidden">Hidden</span>
        </label>
        <button
          className="btn btn-sm px-2 text-white/80 hover:bg-white/10"
          aria-label="Close files"
          onClick={onClose}
        >
          <Icon name="close" size={18} />
        </button>
      </header>
      <div className="flex min-h-0 flex-1 gap-px bg-white/10">
        {localPane}
        {remotePane}
      </div>
      <div className="flex max-h-[38%] min-h-0 flex-col border-t border-white/10 bg-[#1e1f20]">
        <TransferQueue files={files} />
      </div>
      <input
        ref={fileInput}
        type="file"
        multiple
        hidden
        onChange={(e) => fromInput(e.target.files)}
      />
      <input
        ref={folderInput}
        type="file"
        hidden
        // @ts-expect-error webkitdirectory is supported by every current browser but not typed
        webkitdirectory=""
        onChange={(e) => fromInput(e.target.files)}
      />
    </aside>
  );
}
