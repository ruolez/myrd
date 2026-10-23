import { useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import type { DragEvent, ReactNode } from "react";
import { api, errorMessage } from "../../api/client";
import type { FolderId, Tag } from "../../api/types";
import { useDirectory } from "../../app/directory";
import { useWorkspaceState } from "../../app/workspace";
import { Icon } from "../../components/icons";
import type { IconName } from "../../components/icons";
import { Menu } from "../../components/menu";
import { TagDot, useConfirm, usePrompt, useToast } from "../../components/ui";
import { viewCounts } from "../../lib/deviceFilters";
import type { DashView, FilterCtx, SmartView } from "../../lib/deviceFilters";
import { parseDashParams, toDashParams, viewKey } from "../../lib/dashboardState";
import { useNow } from "../../lib/hooks";
import { buildTree, canMoveFolder, deviceCounts, flattenTree } from "../../lib/folders";
import { useCreateFolder, useOrgDialog } from "./dialogs";
import { DEVICE_DRAG } from "./DeviceTile";

export const FOLDER_DRAG = "application/x-remotectl-folder";

const SMART: { view: SmartView; label: string; icon: IconName; org?: boolean }[] = [
  { view: "all", label: "All devices", icon: "devices" },
  { view: "favorites", label: "Favorites", icon: "star", org: true },
  { view: "online", label: "Online", icon: "check" },
  { view: "in_session", label: "In session", icon: "sessions" },
  { view: "attention", label: "Needs attention", icon: "warning" },
  { view: "unfiled", label: "Not in a folder", icon: "inbox", org: true },
];

/** Device ids from a drag, or null when the drag carries none. */
export function draggedDevices(e: DragEvent): string[] | null {
  const raw = e.dataTransfer.getData(DEVICE_DRAG);
  if (!raw) return null;
  try {
    const ids = JSON.parse(raw) as unknown;
    return Array.isArray(ids) ? ids.filter((x): x is string => typeof x === "string") : null;
  } catch {
    return null;
  }
}

function Row({
  active,
  icon,
  label,
  count,
  depth = 0,
  onClick,
  onDrop,
  dropOk,
  draggable,
  onDragStart,
  lead,
  menu,
}: {
  active: boolean;
  icon: ReactNode;
  label: string;
  count?: number;
  depth?: number;
  onClick: () => void;
  onDrop?: (e: DragEvent) => void;
  dropOk?: (e: DragEvent) => boolean;
  draggable?: boolean;
  onDragStart?: (e: DragEvent) => void;
  lead?: ReactNode;
  menu?: ReactNode;
}) {
  const [over, setOver] = useState(false);
  return (
    <li
      className={`group flex items-center rounded-full pr-1 text-[13px] ${
        over
          ? "bg-primary-soft ring-2 ring-primary"
          : active
            ? "bg-primary-soft text-primary"
            : "hover:bg-surface-2"
      }`}
      style={{ paddingLeft: 12 + depth * 14 }}
      draggable={draggable}
      onDragStart={onDragStart}
      onDragOver={(e) => {
        if (!onDrop || !(dropOk?.(e) ?? true)) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = "move";
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        setOver(false);
        if (!onDrop) return;
        e.preventDefault();
        onDrop(e);
      }}
    >
      {lead ?? <span className="w-4 shrink-0" />}
      <button
        type="button"
        className="flex min-w-0 flex-1 items-center gap-2 py-1 pr-1 pl-1 text-left"
        aria-current={active ? "page" : undefined}
        onClick={onClick}
      >
        <span className={active ? "" : "text-muted"}>{icon}</span>
        <span className="min-w-0 flex-1 truncate">{label}</span>
        {count !== undefined && <span className="text-xs text-muted tabular-nums">{count}</span>}
      </button>
      {menu && (
        <span className="opacity-0 group-hover:opacity-100 group-focus-within:opacity-100">
          {menu}
        </span>
      )}
    </li>
  );
}

export function DashboardPane({
  view,
  onView,
  ctx,
  collapsed,
  onToggleFolder,
}: {
  view: DashView;
  onView: (v: DashView) => void;
  ctx: FilterCtx;
  collapsed: ReadonlySet<FolderId>;
  onToggleFolder: (id: FolderId) => void;
}) {
  const { devices, folders, tags, organization, editDevices, reloadOrg } = useDirectory();
  const prompt = usePrompt();
  const confirm = useConfirm();
  const toast = useToast();
  const dialog = useOrgDialog();
  const createFolder = useCreateFolder();
  const counts = useMemo(() => viewCounts(devices, ctx), [devices, ctx]);
  const folderCounts = useMemo(() => deviceCounts(folders, devices), [folders, devices]);
  const tree = useMemo(() => buildTree(folders), [folders]);
  const rows = useMemo(() => flattenTree(tree, collapsed), [tree, collapsed]);
  const tagCounts = useMemo(() => {
    const m = new Map<string, number>();
    for (const d of devices) for (const t of d.tag_ids) m.set(t, (m.get(t) ?? 0) + 1);
    return m;
  }, [devices]);
  const current = viewKey(view);

  const moveDevices = (ids: string[], folder: FolderId | null) =>
    void editDevices(
      ids,
      (d) => ({ ...d, folder_id: folder }),
      () =>
        ids.length === 1
          ? api.devices.update(ids[0] as string, { folder_id: folder })
          : api.devices.bulk(ids, { folder_id: folder }),
    );

  const dropOnFolder = (target: FolderId | null) => (e: DragEvent) => {
    const ids = draggedDevices(e);
    if (ids?.length) return moveDevices(ids, target);
    const folder = e.dataTransfer.getData(FOLDER_DRAG) as FolderId;
    if (!folder || folder === target || !canMoveFolder(folders, folder, target)) return;
    void api.folders
      .update(folder, { parent_id: target })
      .catch((err) => toast(errorMessage(err), "error"))
      .then(() => reloadOrg());
  };
  const accepts = (e: DragEvent) =>
    e.dataTransfer.types.includes(DEVICE_DRAG) || e.dataTransfer.types.includes(FOLDER_DRAG);

  const renameFolder = async (id: FolderId, name: string) => {
    const next = await prompt({
      title: `Rename ${name}`,
      label: "Name",
      initial: name,
      maxLength: 64,
    });
    if (!next || next === name) return;
    try {
      await api.folders.update(id, { name: next });
      await reloadOrg();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  const deleteFolder = async (id: FolderId, name: string) => {
    const ok = await confirm({
      title: `Delete folder ${name}?`,
      message:
        "Its devices and subfolders move up to the folder that contains it. No device is removed.",
      confirm: "Delete folder",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.folders.remove(id);
      if (current === `folder:${id}`) onView("all");
      await reloadOrg();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  const deleteTag = async (t: Tag) => {
    const ok = await confirm({
      title: `Delete tag ${t.name}?`,
      message: `It is removed from ${tagCounts.get(t.id) ?? 0} device(s).`,
      confirm: "Delete tag",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.tags.remove(t.id);
      if (current === `tag:${t.id}`) onView("all");
      await reloadOrg();
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  return (
    <nav aria-label="Device views" className="space-y-3">
      <ul className="space-y-0.5">
        {SMART.filter((s) => organization || !s.org).map((s) => (
          <Row
            key={s.view}
            active={current === s.view}
            icon={<Icon name={s.icon} size={16} />}
            label={s.label}
            count={counts[s.view]}
            onClick={() => onView(s.view)}
            onDrop={s.view === "unfiled" ? dropOnFolder(null) : undefined}
            dropOk={(e) => e.dataTransfer.types.includes(DEVICE_DRAG)}
          />
        ))}
      </ul>

      {organization && (
        <section>
          <div className="mb-0.5 flex items-center justify-between pr-1 pl-5">
            <h2 className="text-[11px] font-medium tracking-wide text-muted uppercase">Folders</h2>
            <button
              className="btn-text btn-sm px-1"
              aria-label="New folder"
              title="New folder"
              onClick={() => void createFolder(null)}
            >
              <Icon name="add" size={16} />
            </button>
          </div>
          {rows.length === 0 ? (
            <p className="pr-2 pl-5 text-xs text-muted">
              None yet. Add one, then drag devices onto it.
            </p>
          ) : (
            <ul className="space-y-0.5">
              {rows.map(({ folder: f, depth, children }) => (
                <Row
                  key={f.id}
                  depth={depth}
                  active={current === `folder:${f.id}`}
                  icon={
                    <Icon
                      name={children.length && !collapsed.has(f.id) ? "folderOpen" : "folder"}
                      size={16}
                    />
                  }
                  label={f.name}
                  count={folderCounts.get(f.id) ?? 0}
                  onClick={() => onView({ folder: f.id })}
                  onDrop={dropOnFolder(f.id)}
                  dropOk={accepts}
                  draggable
                  onDragStart={(e) => {
                    e.dataTransfer.setData(FOLDER_DRAG, f.id);
                    e.dataTransfer.effectAllowed = "move";
                  }}
                  lead={
                    children.length ? (
                      <button
                        type="button"
                        className="flex h-4 w-4 shrink-0 items-center justify-center rounded text-muted hover:text-text"
                        aria-label={collapsed.has(f.id) ? `Expand ${f.name}` : `Collapse ${f.name}`}
                        aria-expanded={!collapsed.has(f.id)}
                        onClick={() => onToggleFolder(f.id)}
                      >
                        <Icon
                          name="chevronDown"
                          size={16}
                          className={`transition-transform motion-reduce:transition-none ${collapsed.has(f.id) ? "-rotate-90" : ""}`}
                        />
                      </button>
                    ) : undefined
                  }
                  menu={
                    <Menu
                      label={`Folder ${f.name}`}
                      className="btn-text btn-sm px-1"
                      items={[
                        {
                          label: "New subfolder…",
                          icon: "newFolder",
                          onSelect: () => void createFolder(f.id),
                        },
                        {
                          label: "Rename…",
                          icon: "edit",
                          onSelect: () => void renameFolder(f.id, f.name),
                        },
                        {
                          label: "Move to…",
                          icon: "folder",
                          onSelect: () => dialog({ kind: "moveFolder", folder: f }),
                        },
                        "separator",
                        {
                          label: "Delete folder",
                          icon: "trash",
                          danger: true,
                          onSelect: () => void deleteFolder(f.id, f.name),
                        },
                      ]}
                    >
                      <Icon name="moreVert" size={16} />
                    </Menu>
                  }
                />
              ))}
            </ul>
          )}
        </section>
      )}

      {organization && (
        <section>
          <div className="mb-0.5 flex items-center justify-between pr-1 pl-5">
            <h2 className="text-[11px] font-medium tracking-wide text-muted uppercase">Tags</h2>
            <button
              className="btn-text btn-sm px-1"
              aria-label="New tag"
              title="New tag"
              onClick={() => dialog({ kind: "tag", tag: null })}
            >
              <Icon name="add" size={16} />
            </button>
          </div>
          {tags.length === 0 ? (
            <p className="pr-2 pl-5 text-xs text-muted">None yet, e.g. “Windows 7”.</p>
          ) : (
            <ul className="space-y-0.5">
              {tags.map((t) => (
                <Row
                  key={t.id}
                  active={current === `tag:${t.id}`}
                  icon={<TagDot color={t.color} />}
                  label={t.name}
                  count={tagCounts.get(t.id) ?? 0}
                  onClick={() => onView({ tag: t.id })}
                  onDrop={(e) => {
                    const ids = draggedDevices(e);
                    if (ids?.length)
                      void editDevices(
                        ids,
                        (d) => ({ ...d, tag_ids: [...new Set([...d.tag_ids, t.id])] }),
                        () => api.devices.bulk(ids, { add_tag_ids: [t.id] }),
                      );
                  }}
                  dropOk={(e) => e.dataTransfer.types.includes(DEVICE_DRAG)}
                  menu={
                    <Menu
                      label={`Tag ${t.name}`}
                      className="btn-text btn-sm px-1"
                      items={[
                        {
                          label: "Edit…",
                          icon: "edit",
                          onSelect: () => dialog({ kind: "tag", tag: t }),
                        },
                        "separator",
                        {
                          label: "Delete tag",
                          icon: "trash",
                          danger: true,
                          onSelect: () => void deleteTag(t),
                        },
                      ]}
                    >
                      <Icon name="moreVert" size={16} />
                    </Menu>
                  }
                />
              ))}
            </ul>
          )}
        </section>
      )}
    </nav>
  );
}

/** Title of the current view for the content header. */
export function viewTitle(
  view: DashView,
  folders: { id: string; name: string }[],
  tags: Tag[],
): string {
  if (typeof view === "object") {
    if ("folder" in view) return folders.find((f) => f.id === view.folder)?.name ?? "Folder";
    return tags.find((t) => t.id === view.tag)?.name ?? "Tag";
  }
  return SMART.find((s) => s.view === view)?.label ?? "All devices";
}

const COLLAPSED_KEY = "remotectl-folders-collapsed";

function loadCollapsed(): Set<FolderId> {
  try {
    const v = JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? "[]") as unknown;
    return new Set(Array.isArray(v) ? v.filter((x): x is FolderId => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

/**
 * The device views as sub-navigation of "Devices" in the app sidebar: reads
 * the view from the dashboard URL and navigates to /devices?view=…, keeping
 * the dashboard's search and filters.
 */
export function DeviceViewsNav({ onNavigate }: { onNavigate?: () => void }) {
  const { folders, tags } = useDirectory();
  const ws = useWorkspaceState();
  const now = useNow(60_000);
  const loc = useLocation();
  const navigate = useNavigate();
  const [collapsed, setCollapsed] = useState(loadCollapsed);
  const onDashboard = loc.pathname === "/devices";
  const params = new URLSearchParams(onDashboard ? loc.search : "");
  const view = onDashboard ? parseDashParams(params).view : ("" as DashView);
  const openHere = useMemo(() => new Set<string>(ws.order), [ws.order]);
  const ctx: FilterCtx = useMemo(
    () => ({ folders, tags, now, openHere }),
    [folders, tags, now, openHere],
  );
  return (
    <DashboardPane
      view={view}
      ctx={ctx}
      collapsed={collapsed}
      onToggleFolder={(id) =>
        setCollapsed((c) => {
          const next = new Set(c);
          if (next.has(id)) next.delete(id);
          else next.add(id);
          try {
            localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...next]));
          } catch {
            // storage unavailable
          }
          return next;
        })
      }
      onView={(v) => {
        const next = toDashParams({ ...parseDashParams(params), view: v }).toString();
        navigate(next ? `/devices?${next}` : "/devices");
        onNavigate?.();
      }}
    />
  );
}
