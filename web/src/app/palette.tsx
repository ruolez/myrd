// Command palette: Cmd/Ctrl+K anywhere outside a remote session.

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import { CommandPalette } from "../components/CommandPalette";
import type { PaletteItem } from "../components/CommandPalette";
import type { IconName } from "../components/icons";
import { Badge } from "../components/ui";
import { viewKey } from "../lib/dashboardState";
import { folderPath } from "../lib/folders";
import { osName } from "../lib/format";
import { canConnect } from "../pages/devices/actions";
import { useUser } from "./auth";
import { useDirectory } from "./directory";
import { useWorkspace, useWorkspaceState } from "./workspace";

type PaletteApi = { open: () => void; extra: (items: PaletteItem[] | null) => void };

const Ctx = createContext<PaletteApi>({ open: () => undefined, extra: () => undefined });

export const usePalette = () => useContext(Ctx);

export function PaletteProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const [extra, setExtra] = useState<PaletteItem[] | null>(null);
  const { devices, folders, tags } = useDirectory();
  const user = useUser();
  const navigate = useNavigate();
  const ws = useWorkspace();
  const wsState = useWorkspaceState();
  const loc = useLocation();
  const inSession = loc.pathname.startsWith("/session/");

  useEffect(() => {
    if (inSession) return; // the session captures the keyboard; it has its own chord
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey) {
        e.preventDefault();
        setOpen((o) => !o);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [inSession]);

  const items = useMemo<PaletteItem[]>(() => {
    if (!open) return [];
    const toDevices = (view: string) => navigate(`/devices?view=${encodeURIComponent(view)}`);
    const devs: PaletteItem[] = devices.map((d) => {
      const path = folderPath(folders, d.folder_id).join(" / ");
      const details = () => navigate(`/devices/${d.id}`);
      const here = d.id in wsState.entries;
      return {
        id: d.id,
        group: "Devices",
        label: d.name,
        detail: [
          here ? "Open here" : d.online ? (d.busy ? "In session" : "Online") : "Offline",
          osName(d.os),
          d.hostname,
          path,
        ]
          .filter(Boolean)
          .join(" · "),
        keywords: [d.notes, ...d.tag_ids.map((t) => tags.find((x) => x.id === t)?.name ?? "")],
        os: d.os,
        badge: d.favorite ? <Badge tone="warning">★</Badge> : undefined,
        run: canConnect(d) || here ? () => navigate(`/session/${d.id}`) : details,
        alt: { label: "details", run: details },
        mod:
          canConnect(d) && !here
            ? { label: "open in background", run: () => void ws.open(d.id) }
            : undefined,
      };
    });
    const pageList: [string, string, IconName][] = [
      ["/devices", "Devices", "devices"],
      ["/devices/add", "Add device", "add"],
      ["/activity", "Activity", "activity"],
      ["/settings", "Settings", "settings"],
      ...(user.is_admin
        ? ([
            ["/admin", "Admin overview", "dashboard"],
            ["/admin/users", "Users", "users"],
            ["/admin/devices", "All devices (admin)", "devices"],
            ["/admin/sessions", "Sessions (admin)", "sessions"],
            ["/admin/audit", "Audit log", "audit"],
            ["/admin/releases", "Releases", "release"],
            ["/admin/settings", "System settings", "settings"],
          ] as [string, string, IconName][])
        : []),
    ];
    const pages: PaletteItem[] = pageList.map(([to, label, icon]) => ({
      id: to,
      group: "Pages",
      label,
      icon,
      run: () => void navigate(to),
    }));
    const openSessions: PaletteItem[] = wsState.order.map((id, i) => {
      const d = devices.find((x) => x.id === id);
      const snapName = ws.session(id)?.snapshot.deviceName;
      return {
        id: `open:${id}`,
        group: "Open sessions",
        label: snapName || d?.name || "Device",
        detail: i < 9 ? `Ctrl+Alt+Shift+${i + 1}` : undefined,
        icon: "sessions" as const,
        run: () => navigate(`/session/${id}`),
        alt: { label: "end session", run: () => ws.close(id) },
      };
    });
    return [
      ...openSessions,
      ...(extra ?? []),
      ...devs,
      ...folders.map((f) => ({
        id: f.id,
        group: "Folders",
        label: folderPath(folders, f.id).join(" / "),
        icon: "folder" as const,
        run: () => toDevices(viewKey({ folder: f.id })),
      })),
      ...tags.map((t) => ({
        id: t.id,
        group: "Tags",
        label: t.name,
        icon: "tag" as const,
        run: () => toDevices(viewKey({ tag: t.id })),
      })),
      ...pages,
    ];
  }, [open, devices, folders, tags, extra, navigate, user.is_admin, ws, wsState]);

  const close = useCallback(() => setOpen(false), []);
  const api = useMemo<PaletteApi>(() => ({ open: () => setOpen(true), extra: setExtra }), []);
  return (
    <Ctx.Provider value={api}>
      {children}
      {open && <CommandPalette items={items} onClose={close} />}
    </Ctx.Provider>
  );
}
