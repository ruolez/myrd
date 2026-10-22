// App sidebar state: which half of the app a path belongs to, and the
// sidebar's remembered layout.

import type { HotkeyEvent } from "./shortcuts";

export type NavMode = "workspace" | "admin";

export function navMode(pathname: string): NavMode {
  return pathname === "/admin" || pathname.startsWith("/admin/") ? "admin" : "workspace";
}

export type SidebarPrefs = {
  /** Desktop sidebar shrunk to an icon rail. */
  collapsed: boolean;
  /** The device views under Devices are shown. */
  devices: boolean;
  /** The open sessions list is shown. */
  sessions: boolean;
};

export const DEFAULT_SIDEBAR: SidebarPrefs = { collapsed: false, devices: true, sessions: true };

export function parseSidebarPrefs(raw: string | null): SidebarPrefs {
  let v: unknown;
  try {
    v = JSON.parse(raw ?? "null");
  } catch {
    return DEFAULT_SIDEBAR;
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return DEFAULT_SIDEBAR;
  const o = v as Record<string, unknown>;
  const flag = (k: keyof SidebarPrefs) =>
    typeof o[k] === "boolean" ? (o[k] as boolean) : DEFAULT_SIDEBAR[k];
  return { collapsed: flag("collapsed"), devices: flag("devices"), sessions: flag("sessions") };
}

/** Cmd+B (Mac) / Ctrl+B elsewhere collapses or expands the sidebar. */
export function isSidebarToggle(e: HotkeyEvent): boolean {
  return e.code === "KeyB" && e.ctrlKey !== e.metaKey && !e.altKey && !e.shiftKey;
}
