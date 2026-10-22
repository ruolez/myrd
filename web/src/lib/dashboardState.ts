// Dashboard state: what is shown lives in the URL (shareable, back button),
// how it is shown lives in localStorage per browser. Neither ever holds
// device screens.

import type { FolderId, TagId } from "../api/types";
import type { Chip, DashView, SmartView, SortKey } from "./deviceFilters";

const SMART: readonly SmartView[] = [
  "all",
  "favorites",
  "recent",
  "online",
  "in_session",
  "attention",
  "unfiled",
];
const SORTS: readonly SortKey[] = ["status", "name", "last_connected", "last_seen"];
const CHIPS: readonly Chip[] = [
  "online",
  "offline",
  "in_session",
  "darwin",
  "windows",
  "linux",
  "attention",
];

export type DashParams = { view: DashView; query: string; chips: Set<Chip>; sort: SortKey | null };

export function viewKey(v: DashView): string {
  if (typeof v === "string") return v;
  return "folder" in v ? `folder:${v.folder}` : `tag:${v.tag}`;
}

export function parseView(s: string | null): DashView {
  if (!s) return "all";
  if ((SMART as readonly string[]).includes(s)) return s as SmartView;
  const [kind, id] = [s.slice(0, s.indexOf(":")), s.slice(s.indexOf(":") + 1)];
  if (id && kind === "folder") return { folder: id as FolderId };
  if (id && kind === "tag") return { tag: id as TagId };
  return "all";
}

export function parseDashParams(p: URLSearchParams): DashParams {
  const sort = p.get("sort");
  return {
    view: parseView(p.get("view")),
    query: p.get("q") ?? "",
    chips: new Set(
      (p.get("f") ?? "")
        .split(",")
        .filter((c): c is Chip => (CHIPS as readonly string[]).includes(c)),
    ),
    sort: (SORTS as readonly string[]).includes(sort ?? "") ? (sort as SortKey) : null,
  };
}

/** Only non-default values, so a plain dashboard has a clean URL. */
export function toDashParams(s: DashParams): URLSearchParams {
  const p = new URLSearchParams();
  if (viewKey(s.view) !== "all") p.set("view", viewKey(s.view));
  if (s.query) p.set("q", s.query);
  if (s.chips.size) p.set("f", CHIPS.filter((c) => s.chips.has(c)).join(","));
  if (s.sort) p.set("sort", s.sort);
  return p;
}

export type Layout = "grid" | "list";
export type TileSize = "s" | "m" | "l";

export type DashPrefs = {
  layout: Layout;
  size: TileSize;
  sort: SortKey;
  collapsed: FolderId[];
};

export const DEFAULT_PREFS: DashPrefs = {
  layout: "grid",
  size: "m",
  sort: "status",
  collapsed: [],
};

export const PREFS_KEY = "remotectl-dashboard";

export function parseDashPrefs(raw: string | null): DashPrefs {
  let v: Partial<Record<keyof DashPrefs, unknown>> = {};
  try {
    v = raw ? (JSON.parse(raw) as typeof v) : {};
  } catch {
    return DEFAULT_PREFS;
  }
  if (typeof v !== "object" || v === null) return DEFAULT_PREFS;
  return {
    layout: v.layout === "list" ? "list" : "grid",
    size: v.size === "s" || v.size === "l" ? v.size : "m",
    sort: (SORTS as readonly unknown[]).includes(v.sort) ? (v.sort as SortKey) : DEFAULT_PREFS.sort,
    collapsed: Array.isArray(v.collapsed)
      ? v.collapsed.filter((x): x is FolderId => typeof x === "string").slice(0, 500)
      : [],
  };
}

export const serializeDashPrefs = (p: DashPrefs) => JSON.stringify(p);

/** Minimum tile width per size, in CSS px (Guacamole caps previews near 320). */
export const TILE_MIN_WIDTH: Record<TileSize, number> = { s: 200, m: 264, l: 340 };
