import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import type { ReactNode, RefObject } from "react";
import type { Device } from "../api/types";
import { PreviewManager } from "../lib/previews";
import type { PreviewState } from "../lib/previews";
import { useAuth, useUser } from "./auth";
import { useDirectory } from "./directory";
import { useWorkspaceState } from "./workspace";

const Ctx = createContext<PreviewManager | null>(null);

/** The device may send previews to this browser now (as far as the dashboard knows). */
export function previewsPossible(d: Device, adminOn: boolean): boolean {
  return adminOn && d.previews && d.features.includes("preview") && d.status !== "disabled";
}

export function PreviewProvider({ children }: { children: ReactNode }) {
  const user = useUser();
  const { config } = useAuth();
  const { devices } = useDirectory();
  const ws = useWorkspaceState();
  const adminOn =
    (config?.features?.includes("previews") ?? false) && config?.screen_previews !== false;
  // The manager reads the latest list and workspace through refs.
  const devicesRef = useRef(devices);
  devicesRef.current = devices;
  const openRef = useRef(ws.entries);
  openRef.current = ws.entries;
  const adminRef = useRef(adminOn);
  adminRef.current = adminOn;
  const [mgr, setMgr] = useState<PreviewManager | null>(null);

  useEffect(() => {
    const m = new PreviewManager(
      user.id,
      (id) => devicesRef.current.find((d) => d.id === id),
      // Sessions open here show live video instead.
      (d) => previewsPossible(d, adminRef.current) && !(d.id in openRef.current),
    );
    setMgr(m);
    return () => m.dispose();
  }, [user.id]);

  useEffect(() => mgr?.refresh(), [mgr, devices, ws.entries, adminOn]);

  return <Ctx.Provider value={mgr}>{children}</Ctx.Provider>;
}

const EMPTY: PreviewState = { url: null, capturedAt: null, width: 0, height: 0, reason: null };

/** The latest preview of a device, re-rendering when it changes. */
export function usePreview(id: string): PreviewState {
  const mgr = useContext(Ctx);
  return useSyncExternalStore(
    (fn) => mgr?.subscribe(id, fn) ?? (() => undefined),
    () => mgr?.get(id) ?? EMPTY,
  );
}

/** Asks for previews of the device while ref's element is on screen. */
export function useWatchPreview(id: string, ref: RefObject<Element | null>, enabled: boolean) {
  const mgr = useContext(Ctx);
  useEffect(() => {
    const el = ref.current;
    if (!mgr || !el || !enabled) return;
    return mgr.observe(el, id);
  }, [mgr, id, ref, enabled]);
}
