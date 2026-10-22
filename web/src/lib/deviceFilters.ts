// Dashboard filtering: smart views, query parsing, sorting.

import type { Device, Folder, FolderId, Tag, TagId } from "../api/types";
import { descendants, folderPath } from "./folders";
import { osName } from "./format";
import { diskSeverity } from "./health";

export type SmartView =
  "all" | "favorites" | "recent" | "online" | "in_session" | "attention" | "unfiled";

export type DashView = SmartView | { folder: FolderId } | { tag: TagId };

export type SortKey = "status" | "name" | "last_connected" | "last_seen";

export type Chip =
  "online" | "offline" | "in_session" | "darwin" | "windows" | "linux" | "attention";

export const OFFLINE_ATTENTION_DAYS = 7;
const DAY_MS = 86_400_000;

export type AttentionReason = "offline_long" | "update_failed" | "permissions" | "low_disk";

export function attentionReasons(d: Device, now: number): AttentionReason[] {
  const out: AttentionReason[] = [];
  if (d.status !== "disabled" && !d.online && d.last_seen_at) {
    if (now - Date.parse(d.last_seen_at) > OFFLINE_ATTENTION_DAYS * DAY_MS)
      out.push("offline_long");
  }
  if (d.update_state === "failed" || d.update_state === "rolled_back") out.push("update_failed");
  if (d.online && d.permissions && (!d.permissions.screen || !d.permissions.input))
    out.push("permissions");
  if (d.info?.disks.some((k) => diskSeverity(k) !== "ok")) out.push("low_disk");
  return out;
}

export const attentionText: Record<AttentionReason, string> = {
  offline_long: `Offline for more than ${OFFLINE_ATTENTION_DAYS} days`,
  update_failed: "The last update failed",
  permissions: "Screen or input permission is missing on the device",
  low_disk: "A disk is almost full (less than 10% free)",
};

export type Query = {
  text: string[];
  tags: string[];
  folders: string[];
  os: Device["os"][];
  is: ("online" | "offline" | "fav" | "busy")[];
};

const OS_ALIASES: Record<string, Device["os"]> = {
  mac: "darwin",
  macos: "darwin",
  darwin: "darwin",
  win: "windows",
  windows: "windows",
  linux: "linux",
};
const IS_VALUES = ["online", "offline", "fav", "busy"] as const;

/** Free words plus `tag:`, `folder:`, `os:` and `is:` tokens; quotes keep spaces together. */
export function parseQuery(q: string): Query {
  const out: Query = { text: [], tags: [], folders: [], os: [], is: [] };
  for (const m of q.matchAll(/(\w+:)?(?:"([^"]*)"|(\S+))/g)) {
    const key = m[1]?.slice(0, -1).toLowerCase();
    const value = (m[2] ?? m[3] ?? "").toLowerCase();
    if (!value) continue;
    if (key === "tag") out.tags.push(value);
    else if (key === "folder") out.folders.push(value);
    else if (key === "os" && OS_ALIASES[value]) out.os.push(OS_ALIASES[value]);
    else if (key === "is" && (IS_VALUES as readonly string[]).includes(value))
      out.is.push(value as Query["is"][number]);
    else out.text.push((m[1] ?? "").toLowerCase() + value);
  }
  return out;
}

export type FilterCtx = {
  folders: Folder[];
  tags: Tag[];
  now: number;
  /** Devices open in this tab's workspace (not "busy elsewhere"). */
  openHere?: ReadonlySet<string>;
};

function matchesQuery(d: Device, q: Query, ctx: FilterCtx, tagNames: Map<TagId, string>): boolean {
  const names = d.tag_ids.map((t) => tagNames.get(t) ?? "");
  const path = folderPath(ctx.folders, d.folder_id).map((p) => p.toLowerCase());
  if (q.tags.some((t) => !names.some((n) => n === t))) return false;
  if (q.folders.some((f) => !path.some((p) => p.includes(f)))) return false;
  if (q.os.length && !q.os.includes(d.os)) return false;
  for (const v of q.is) {
    if (v === "online" && !d.online) return false;
    if (v === "offline" && d.online) return false;
    if (v === "fav" && !d.favorite) return false;
    if (v === "busy" && !d.busy) return false;
  }
  if (!q.text.length) return true;
  const hay = [d.name, d.hostname, osName(d.os), d.notes, ...names, ...path]
    .join("\n")
    .toLowerCase();
  return q.text.every((w) => hay.includes(w));
}

function matchesChips(d: Device, chips: ReadonlySet<Chip>, now: number): boolean {
  const status = (["online", "offline", "in_session"] as const).filter((c) => chips.has(c));
  if (status.length) {
    const ok = status.some((c) =>
      c === "online" ? d.online && !d.busy : c === "offline" ? !d.online : d.online && d.busy,
    );
    if (!ok) return false;
  }
  const os = (["darwin", "windows", "linux"] as const).filter((c) => chips.has(c));
  if (os.length && !os.includes(d.os)) return false;
  if (chips.has("attention") && attentionReasons(d, now).length === 0) return false;
  return true;
}

export const RECENT_LIMIT = 8;

/** The most recently connected devices, newest first. */
export function recent(devices: Device[], n = RECENT_LIMIT): Device[] {
  return devices
    .filter((d) => d.last_connected_at)
    .sort((a, b) => Date.parse(b.last_connected_at ?? "") - Date.parse(a.last_connected_at ?? ""))
    .slice(0, n);
}

export function inView(devices: Device[], view: DashView, ctx: FilterCtx): Device[] {
  if (typeof view === "object") {
    if ("folder" in view) {
      const ids = descendants(ctx.folders, view.folder);
      return devices.filter((d) => d.folder_id && ids.has(d.folder_id));
    }
    return devices.filter((d) => d.tag_ids.includes(view.tag));
  }
  switch (view) {
    case "all":
      return devices;
    case "favorites":
      return devices.filter((d) => d.favorite);
    case "recent":
      return recent(devices);
    case "online":
      return devices.filter((d) => d.online);
    case "in_session":
      return devices.filter((d) => d.online && (d.busy || ctx.openHere?.has(d.id)));
    case "attention":
      return devices.filter((d) => attentionReasons(d, ctx.now).length > 0);
    case "unfiled":
      return devices.filter((d) => !d.folder_id);
  }
}

const time = (ts: string | null) => (ts ? Date.parse(ts) : 0);
const statusRank = (d: Device) => (d.status === "disabled" ? 2 : d.online ? 0 : 1);

export function sortDevices(devices: Device[], key: SortKey): Device[] {
  const byName = (a: Device, b: Device) =>
    a.name.localeCompare(b.name, undefined, { sensitivity: "base", numeric: true });
  const cmp: Record<SortKey, (a: Device, b: Device) => number> = {
    status: (a, b) => statusRank(a) - statusRank(b),
    name: () => 0,
    last_connected: (a, b) => time(b.last_connected_at) - time(a.last_connected_at),
    last_seen: (a, b) =>
      Number(b.online) - Number(a.online) || time(b.last_seen_at) - time(a.last_seen_at),
  };
  return [...devices].sort(
    (a, b) => Number(b.favorite) - Number(a.favorite) || cmp[key](a, b) || byName(a, b),
  );
}

export type DashFilter = { view: DashView; query: string; chips: ReadonlySet<Chip>; sort: SortKey };

/** The devices a dashboard state shows, in order. Recent keeps its own order. */
export function applyFilter(devices: Device[], f: DashFilter, ctx: FilterCtx): Device[] {
  const tagNames = new Map(ctx.tags.map((t) => [t.id, t.name.toLowerCase()]));
  const q = parseQuery(f.query);
  const list = inView(devices, f.view, ctx).filter(
    (d) => matchesQuery(d, q, ctx, tagNames) && matchesChips(d, f.chips, ctx.now),
  );
  return f.view === "recent" ? list : sortDevices(list, f.sort);
}

/** Counts shown next to the smart views. */
export function viewCounts(devices: Device[], ctx: FilterCtx): Record<SmartView, number> {
  const views: SmartView[] = [
    "all",
    "favorites",
    "recent",
    "online",
    "in_session",
    "attention",
    "unfiled",
  ];
  return Object.fromEntries(views.map((v) => [v, inView(devices, v, ctx).length])) as Record<
    SmartView,
    number
  >;
}
