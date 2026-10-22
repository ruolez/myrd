import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
} from "react";
import type { ReactNode } from "react";
import { SessionWorkspace } from "../session/workspace";
import type { RemoteSession, Snapshot } from "../session/RemoteSession";
import type { WsState } from "../session/workspaceState";
import { useUser } from "./auth";

const Ctx = createContext<SessionWorkspace | null>(null);

const hasFiles = (e: DragEvent) =>
  !!e.dataTransfer && Array.from(e.dataTransfer.types).includes("Files");

/** Holds this tab's remote sessions for the signed-in user. */
export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const user = useUser();
  const [ws] = useState(() => new SessionWorkspace(user.id));

  useEffect(() => {
    const onHide = () => ws.endAll();
    // Closing the tab during a transfer would cut it off.
    const guard = (e: BeforeUnloadEvent) => {
      if (!ws.transfersActive()) return;
      e.preventDefault();
      e.returnValue = "";
    };
    // A file dropped beside a stage must not make the browser open it
    // (which would leave the page and end every session).
    const prevent = (e: DragEvent) => {
      if (ws.getState().order.length && hasFiles(e)) e.preventDefault();
    };
    window.addEventListener("pagehide", onHide);
    window.addEventListener("beforeunload", guard);
    window.addEventListener("dragover", prevent);
    window.addEventListener("drop", prevent);
    return () => {
      window.removeEventListener("pagehide", onHide);
      window.removeEventListener("beforeunload", guard);
      window.removeEventListener("dragover", prevent);
      window.removeEventListener("drop", prevent);
      ws.endAll();
    };
  }, [ws]);

  return <Ctx.Provider value={ws}>{children}</Ctx.Provider>;
}

export function useWorkspace(): SessionWorkspace {
  const v = useContext(Ctx);
  if (!v) throw new Error("useWorkspace outside WorkspaceProvider");
  return v;
}

export function useWorkspaceState(): WsState {
  const ws = useWorkspace();
  return useSyncExternalStore(ws.subscribe, ws.getState);
}

const noop = () => () => undefined;

export function useSessionSnap(session: RemoteSession | null): Snapshot | null {
  const subscribe = useCallback(
    (fn: () => void) => session?.subscribe(fn) ?? (() => undefined),
    [session],
  );
  return useSyncExternalStore(session ? subscribe : noop, () => session?.snapshot ?? null);
}
