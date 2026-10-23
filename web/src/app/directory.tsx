// The signed-in user's devices, folders and tags, loaded once and kept live
// by pushes, shared by the dashboard, the command palette and session chrome.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import type { ReactNode } from "react";
import { api, errorMessage } from "../api/client";
import type { Device, Folder, Tag } from "../api/types";
import { useLiveDeviceStatus, useLiveUpdates } from "../components/devices";
import { useToast } from "../components/ui";
import { applyInfo, deviceLabels, patchDevices, sameLabels } from "../lib/directory";
import type { DeviceLabel } from "../lib/directory";
import { signaling } from "../lib/signaling";
import { useAuth } from "./auth";

type Directory = {
  devices: Device[];
  folders: Folder[];
  tags: Tag[];
  loading: boolean;
  error: string | null;
  /** The server supports folders, tags, favorites and notes. */
  organization: boolean;
  reload: (quiet?: boolean) => Promise<void>;
  setDevices: (fn: (d: Device[]) => Device[]) => void;
  /** Starts update polling for a minute (see useLiveUpdates). */
  kickUpdates: () => void;
  /**
   * Applies `local` to the devices at once, then sends the change; on failure
   * the list is reloaded and the error shown. Resolves to whether it worked.
   */
  editDevices: (
    ids: readonly string[],
    local: (d: Device) => Device,
    send: () => Promise<unknown>,
  ) => Promise<boolean>;
  reloadOrg: () => Promise<void>;
};

const Ctx = createContext<Directory | null>(null);

// The device list outside React state, for components that need a sliver of
// it (session chrome) and must not re-render on every health report.
type DeviceStore = { get: () => Device[]; subscribe: (fn: () => void) => () => void };
const StoreCtx = createContext<DeviceStore | null>(null);

function createDeviceStore(): DeviceStore & { set: (d: Device[]) => void } {
  let devices: Device[] = [];
  const listeners = new Set<() => void>();
  return {
    get: () => devices,
    set: (d) => {
      devices = d;
      for (const l of listeners) l();
    },
    subscribe: (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
  };
}

const ORG_DEBOUNCE_MS = 250;

export function DirectoryProvider({ children }: { children: ReactNode }) {
  const { config } = useAuth();
  const toast = useToast();
  const organization = config?.features?.includes("organization") ?? false;
  const [devices, setDevicesState] = useState<Device[]>([]);
  const [folders, setFolders] = useState<Folder[]>([]);
  const [tags, setTags] = useState<Tag[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const seq = useRef(0);
  const [store] = useState(createDeviceStore);
  useLayoutEffect(() => store.set(devices), [store, devices]);

  const setDevices = useCallback((fn: (d: Device[]) => Device[]) => setDevicesState(fn), []);

  const reloadDevices = useCallback(async () => {
    const my = ++seq.current;
    const r = await api.devices.list();
    if (my === seq.current) setDevicesState(r.items);
  }, []);

  const reloadOrg = useCallback(async () => {
    if (!organization) return;
    const [f, t] = await Promise.all([api.folders.list(), api.tags.list()]);
    setFolders(f.items);
    setTags(t.items);
  }, [organization]);

  const reload = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true);
      try {
        await Promise.all([reloadDevices(), reloadOrg()]);
        setError(null);
      } catch (e) {
        setError(errorMessage(e));
      } finally {
        setLoading(false);
      }
    },
    [reloadDevices, reloadOrg],
  );

  useEffect(() => {
    void reload();
  }, [reload]);

  useLiveDeviceStatus(setDevices);
  useEffect(
    () => signaling.on("device.info", (m) => setDevices((list) => applyInfo(list, m))),
    [setDevices],
  );
  const kickUpdates = useLiveUpdates(
    devices,
    () => void reloadDevices().catch(() => undefined),
    setDevices,
  );

  // Other tabs' (and this tab's) organization edits; a reconnect may have
  // missed pushes, so it refetches everything.
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    const kinds = new Set<string>();
    const flush = () => {
      timer = null;
      const org = kinds.has("folders") || kinds.has("tags");
      kinds.clear();
      void Promise.all([reloadDevices(), org ? reloadOrg() : null]).catch(() => undefined);
    };
    const offOrg = signaling.on("org.changed", (m) => {
      kinds.add(String(m.kind));
      if (!timer) timer = setTimeout(flush, ORG_DEBOUNCE_MS);
    });
    let wasOpen = signaling.state === "open";
    const offState = signaling.onState((s) => {
      if (s === "open" && !wasOpen) void reload(true);
      wasOpen = s === "open";
    });
    return () => {
      offOrg();
      offState();
      if (timer) clearTimeout(timer);
    };
  }, [reload, reloadDevices, reloadOrg]);

  const editDevices = useCallback<Directory["editDevices"]>(
    async (ids, local, send) => {
      setDevicesState((list) => patchDevices(list, new Set(ids), local));
      try {
        await send();
        return true;
      } catch (e) {
        toast(errorMessage(e), "error");
        void reloadDevices().catch(() => undefined);
        return false;
      }
    },
    [toast, reloadDevices],
  );

  const value = useMemo<Directory>(
    () => ({
      devices,
      folders,
      tags,
      loading,
      error,
      organization,
      reload,
      setDevices,
      kickUpdates,
      editDevices,
      reloadOrg,
    }),
    [
      devices,
      folders,
      tags,
      loading,
      error,
      organization,
      reload,
      setDevices,
      kickUpdates,
      editDevices,
      reloadOrg,
    ],
  );
  return (
    <StoreCtx.Provider value={store}>
      <Ctx.Provider value={value}>{children}</Ctx.Provider>
    </StoreCtx.Provider>
  );
}

/** Name, OS and signed-in user by device id; re-renders only when one of those changes. */
export function useDeviceLabels(): Map<string, DeviceLabel> {
  const store = useContext(StoreCtx);
  if (!store) throw new Error("useDeviceLabels outside DirectoryProvider");
  const last = useRef<Map<string, DeviceLabel> | null>(null);
  const get = () => {
    const next = deviceLabels(store.get());
    if (last.current && sameLabels(last.current, next)) return last.current;
    return (last.current = next);
  };
  return useSyncExternalStore(store.subscribe, get);
}

export function useDirectory(): Directory {
  const v = useContext(Ctx);
  if (!v) throw new Error("useDirectory outside DirectoryProvider");
  return v;
}
