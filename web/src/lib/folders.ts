// Folder tree helpers (PROTOCOL §1 Folder). The server rejects cycles, but the
// tree is built defensively: a folder whose parent is missing, or that sits in
// a loop, is shown at the top level so bad data can never hang the page.

import type { Folder, FolderId } from "../api/types";

export type FolderNode = { folder: Folder; children: FolderNode[]; depth: number };

const bySiblingOrder = (a: Folder, b: Folder) =>
  a.position - b.position || a.name.localeCompare(b.name, undefined, { sensitivity: "base" });

/** Ancestors of id from its parent up to the root, stopping at a loop. */
function chain(id: FolderId, byId: Map<FolderId, Folder>): FolderId[] {
  const out: FolderId[] = [];
  const seen = new Set<FolderId>([id]);
  let cur = byId.get(id)?.parent_id ?? null;
  while (cur && byId.has(cur) && !seen.has(cur)) {
    out.push(cur);
    seen.add(cur);
    cur = byId.get(cur)?.parent_id ?? null;
  }
  return out;
}

/** The parent the tree places a folder under: null for orphans and loop members. */
function effectiveParent(f: Folder, byId: Map<FolderId, Folder>): FolderId | null {
  if (!f.parent_id || !byId.has(f.parent_id)) return null;
  let cur: FolderId | null = f.parent_id;
  const seen = new Set<FolderId>();
  while (cur) {
    if (cur === f.id || seen.has(cur)) return null;
    seen.add(cur);
    cur = byId.get(cur)?.parent_id ?? null;
    if (cur && !byId.has(cur)) break;
  }
  return f.parent_id;
}

export function buildTree(folders: Folder[]): FolderNode[] {
  const byId = new Map(folders.map((f) => [f.id, f]));
  const kids = new Map<FolderId | null, Folder[]>();
  for (const f of folders) {
    const p = effectiveParent(f, byId);
    kids.set(p, [...(kids.get(p) ?? []), f]);
  }
  const make = (parent: FolderId | null, depth: number): FolderNode[] =>
    (kids.get(parent) ?? [])
      .sort(bySiblingOrder)
      .map((folder) => ({ folder, depth, children: make(folder.id, depth + 1) }));
  return make(null, 0);
}

/** Depth-first order, skipping the children of collapsed folders. */
export function flattenTree(tree: FolderNode[], collapsed: ReadonlySet<FolderId>): FolderNode[] {
  const out: FolderNode[] = [];
  const walk = (nodes: FolderNode[]) => {
    for (const n of nodes) {
      out.push(n);
      if (!collapsed.has(n.folder.id)) walk(n.children);
    }
  };
  walk(tree);
  return out;
}

/** id and every folder below it. */
export function descendants(folders: Folder[], id: FolderId): Set<FolderId> {
  const out = new Set<FolderId>([id]);
  const byId = new Map(folders.map((f) => [f.id, f]));
  for (const f of folders) if (chain(f.id, byId).includes(id)) out.add(f.id);
  return out;
}

/** Names from the top-level folder down to id, e.g. ["Office", "Floor 2"]. */
export function folderPath(folders: Folder[], id: FolderId | null): string[] {
  if (!id) return [];
  const byId = new Map(folders.map((f) => [f.id, f]));
  const self = byId.get(id);
  if (!self) return [];
  return [
    ...chain(id, byId)
      .reverse()
      .map((a) => byId.get(a)?.name ?? ""),
    self.name,
  ];
}

/** Whether folder id may move under newParent (null = top level). */
export function canMoveFolder(folders: Folder[], id: FolderId, newParent: FolderId | null) {
  if (newParent === null) return true;
  return !descendants(folders, id).has(newParent);
}

/** Devices per folder, each folder counting those in its subfolders too. */
export function deviceCounts(
  folders: Folder[],
  devices: { folder_id: FolderId | null }[],
): Map<FolderId, number> {
  const byId = new Map(folders.map((f) => [f.id, f]));
  const out = new Map<FolderId, number>(folders.map((f) => [f.id, 0]));
  for (const d of devices) {
    if (!d.folder_id || !byId.has(d.folder_id)) continue;
    for (const id of [d.folder_id, ...chain(d.folder_id, byId)])
      out.set(id, (out.get(id) ?? 0) + 1);
  }
  return out;
}
