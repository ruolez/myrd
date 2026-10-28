// One side of the file manager: a file list with checkboxes, click /
// Ctrl-⌘-click / Shift-click selection, keyboard movement, and dragging
// rows to the other side.

import { useEffect, useRef, useState } from "react";
import type { DragEvent, KeyboardEvent, ReactNode } from "react";
import { Icon } from "../../components/icons";
import { Spinner } from "../../components/ui";
import type { FileEntry } from "../../lib/files";
import { clickSelect, keySelect, selectAll, EMPTY_SELECTION } from "../../lib/fileSelection";
import type { Selection } from "../../lib/fileSelection";
import { bytes } from "../../lib/format";

export type Side = "local" | "remote";

/** The MIME type of rows dragged between the panes; its value is the source side. */
export const DRAG_TYPE = "application/x-remotectl-files";

const modified = (ms: number) =>
  ms > 0
    ? new Date(ms).toLocaleString(undefined, {
        year: "numeric",
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "";

export function FilePane({
  side,
  label,
  location,
  toolbar,
  entries,
  loading,
  error,
  notice,
  footer,
  sel,
  onSel,
  onOpen,
  onParent,
  onTransfer,
  onDelete,
  onRename,
  onDropFrom,
  onEscape,
  className = "",
}: {
  side: Side;
  /** Accessible name of the list. */
  label: string;
  location: ReactNode;
  toolbar: ReactNode;
  entries: readonly FileEntry[] | null;
  loading: boolean;
  error: string | null;
  /** Shown instead of the list (e.g. "choose a folder"). */
  notice?: ReactNode;
  footer?: ReactNode;
  sel: Selection;
  onSel: (s: Selection) => void;
  onOpen: (e: FileEntry) => void;
  onParent?: () => void;
  /** Sends the selection to the other side. */
  onTransfer: () => void;
  onDelete?: () => void;
  onRename?: () => void;
  /** Rows from the other side dropped here, into `folder` (a row) or the folder on screen. */
  onDropFrom: (folder: string | null) => void;
  onEscape: () => void;
  className?: string;
}) {
  const list = useRef<HTMLDivElement>(null);
  const touch = useRef(false);
  const [dropTarget, setDropTarget] = useState<string | "" | null>(null);
  const order = entries?.map((e) => e.name) ?? [];
  const count = sel.selected.size;
  const all = order.length > 0 && count === order.length;

  const head = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (head.current) head.current.indeterminate = count > 0 && !all;
  }, [count, all]);

  useEffect(() => {
    if (sel.focus === null) return;
    list.current
      ?.querySelector(`[data-name="${CSS.escape(sel.focus)}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [sel.focus]);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const mod = e.metaKey || e.ctrlKey;
    const moved = keySelect(sel, order, e.key, e.shiftKey);
    if (moved && !mod) {
      e.preventDefault();
      onSel(moved);
      return;
    }
    const focused = entries?.find((x) => x.name === sel.focus);
    if (mod && e.key.toLowerCase() === "a") {
      onSel(selectAll(sel, order));
    } else if (e.key === " " && sel.focus !== null) {
      onSel(clickSelect(sel, order, sel.focus, { toggle: true }));
    } else if (e.key === "Enter") {
      if (focused?.kind === "dir" && count <= 1) onOpen(focused);
      else if (count > 0) onTransfer();
      else return;
    } else if (e.key === "Backspace" || (mod && e.key === "ArrowUp")) {
      if (!onParent) return;
      onParent();
    } else if (e.key === "Delete" && onDelete && count > 0) {
      onDelete();
    } else if (e.key === "F2" && onRename && count === 1) {
      onRename();
    } else if (e.key === "Escape") {
      if (count > 0) onSel(EMPTY_SELECTION);
      else onEscape();
    } else {
      return;
    }
    e.preventDefault();
    e.stopPropagation();
  };

  const fromOtherSide = (e: DragEvent) =>
    e.dataTransfer.types.includes(DRAG_TYPE) && document.body.dataset.dragSide !== side;

  const dragOver = (e: DragEvent, folder: string | "") => {
    if (!fromOtherSide(e)) return;
    e.preventDefault();
    e.stopPropagation();
    e.dataTransfer.dropEffect = "copy";
    if (dropTarget !== folder) setDropTarget(folder);
  };
  const drop = (e: DragEvent, folder: string | null) => {
    if (!fromOtherSide(e)) return;
    e.preventDefault();
    e.stopPropagation();
    setDropTarget(null);
    onDropFrom(folder);
  };

  const dragStart = (e: DragEvent, entry: FileEntry) => {
    if (!sel.selected.has(entry.name)) onSel(clickSelect(sel, order, entry.name, {}));
    e.dataTransfer.setData(DRAG_TYPE, side);
    e.dataTransfer.effectAllowed = "copy";
    document.body.dataset.dragSide = side;
  };
  const dragEnd = () => delete document.body.dataset.dragSide;

  return (
    <section
      aria-label={label}
      className={`flex min-h-0 min-w-0 flex-1 flex-col bg-[#1e1f20] ${className}`}
    >
      <div className="space-y-2 border-b border-white/10 px-3 py-2">
        {location}
        <div className="flex flex-wrap items-center gap-1">{toolbar}</div>
      </div>
      {notice ?? (
        <div
          ref={list}
          role="grid"
          aria-label={label}
          aria-multiselectable="true"
          aria-rowcount={order.length}
          tabIndex={0}
          onKeyDown={onKeyDown}
          onDragOver={(e) => dragOver(e, "")}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDropTarget(null);
          }}
          onDrop={(e) => drop(e, null)}
          className={`relative min-h-0 flex-1 overflow-y-auto outline-none select-none focus-visible:ring-2 focus-visible:ring-[#8ab4f8]/60 focus-visible:ring-inset ${dropTarget === "" ? "bg-[#8ab4f8]/10" : ""}`}
        >
          <div
            role="row"
            className="sticky top-0 z-10 grid grid-cols-[2rem_1fr_5rem] items-center gap-2 border-b border-white/10 bg-[#1e1f20] px-2 py-1.5 text-xs text-white/50 sm:grid-cols-[2rem_1fr_5rem_9.5rem]"
          >
            <span role="columnheader" className="flex justify-center">
              <input
                ref={head}
                type="checkbox"
                aria-label={all ? "Select none" : "Select all"}
                checked={all}
                disabled={order.length === 0}
                onChange={() => onSel(all ? EMPTY_SELECTION : selectAll(sel, order))}
              />
            </span>
            <span role="columnheader">{count > 0 ? `${count} selected` : "Name"}</span>
            <span role="columnheader" className="text-right">
              Size
            </span>
            <span role="columnheader" className="hidden sm:block">
              Modified
            </span>
          </div>
          {error && (
            <div
              role="alert"
              className="m-2 rounded-lg bg-[#3c1f1d] px-3 py-2 text-xs text-[#f28b82]"
            >
              {error}
            </div>
          )}
          {entries?.length === 0 && !loading && !error && (
            <p className="p-6 text-center text-xs text-white/50">This folder is empty.</p>
          )}
          {entries?.map((e, i) => {
            const selected = sel.selected.has(e.name);
            const focus = sel.focus === e.name;
            const isDir = e.kind === "dir";
            return (
              <div
                key={e.name}
                role="row"
                aria-rowindex={i + 1}
                aria-selected={selected}
                data-name={e.name}
                draggable
                onDragStart={(ev) => dragStart(ev, e)}
                onDragEnd={dragEnd}
                onDragOver={isDir ? (ev) => dragOver(ev, e.name) : undefined}
                onDrop={isDir ? (ev) => drop(ev, e.name) : undefined}
                onPointerDown={(ev) => (touch.current = ev.pointerType === "touch")}
                onClick={(ev) => {
                  // Touch has no double-click: a tap opens a folder and toggles a file.
                  if (touch.current) {
                    if (isDir) onOpen(e);
                    else onSel(clickSelect(sel, order, e.name, { toggle: true }));
                    return;
                  }
                  onSel(
                    clickSelect(sel, order, e.name, {
                      shift: ev.shiftKey,
                      toggle: ev.metaKey || ev.ctrlKey,
                    }),
                  );
                }}
                onDoubleClick={() => onOpen(e)}
                className={`grid cursor-default grid-cols-[2rem_1fr_5rem] items-center gap-2 px-2 py-1 text-sm sm:grid-cols-[2rem_1fr_5rem_9.5rem] ${
                  selected ? "bg-[#8ab4f8]/20" : "hover:bg-white/5"
                } ${focus ? "outline outline-1 -outline-offset-1 outline-[#8ab4f8]/60" : ""} ${
                  dropTarget === e.name ? "bg-[#8ab4f8]/30" : ""
                } ${e.hidden ? "opacity-60" : ""}`}
              >
                <span role="gridcell" className="flex justify-center">
                  <input
                    type="checkbox"
                    tabIndex={-1}
                    aria-label={`Select ${e.name}`}
                    checked={selected}
                    onPointerDown={(ev) => ev.stopPropagation()}
                    onClick={(ev) => {
                      ev.stopPropagation();
                      onSel(clickSelect(sel, order, e.name, { shift: ev.shiftKey, toggle: true }));
                    }}
                    onChange={() => undefined}
                  />
                </span>
                <span role="gridcell" className="flex min-w-0 items-center gap-2">
                  <Icon
                    name={isDir ? "folder" : "file"}
                    size={18}
                    className={`shrink-0 ${isDir ? "text-[#8ab4f8]" : "text-white/50"}`}
                  />
                  <span className="min-w-0 truncate" title={e.name}>
                    {e.name}
                  </span>
                  {e.link && <span className="shrink-0 text-xs text-white/40">link</span>}
                </span>
                <span role="gridcell" className="text-right text-xs text-white/50 tabular-nums">
                  {isDir ? "" : bytes(e.size)}
                </span>
                <span role="gridcell" className="hidden truncate text-xs text-white/50 sm:block">
                  {modified(e.mtime)}
                </span>
              </div>
            );
          })}
          {loading && (
            <div className="flex justify-center p-3">
              <Spinner size={20} />
            </div>
          )}
          {footer}
        </div>
      )}
    </section>
  );
}

/** A compact toolbar button for the panes. */
export function PaneButton({
  icon,
  label,
  text,
  onClick,
  disabled,
  title,
  primary,
}: {
  icon: Parameters<typeof Icon>[0]["name"];
  label: string;
  text?: string;
  onClick: () => void;
  disabled?: boolean;
  title?: string;
  primary?: boolean;
}) {
  return (
    <button
      className={`btn btn-sm px-2 ${
        primary
          ? "bg-[#8ab4f8] text-[#0b1d36] hover:brightness-110"
          : "text-white/80 hover:bg-white/10"
      }`}
      aria-label={label}
      title={title ?? label}
      onClick={onClick}
      disabled={disabled}
    >
      <Icon name={icon} size={16} />
      {text && <span>{text}</span>}
    </button>
  );
}
