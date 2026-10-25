// /session/:deviceId: opens that device's session in the workspace (or
// switches to it) and shows every open session, the focused one in view.

import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import type { DeviceId } from "../api/types";
import { useDeviceLabels } from "../app/directory";
import { usePalette } from "../app/palette";
import { useWorkspace, useWorkspaceState } from "../app/workspace";
import { Icon } from "../components/icons";
import type { Hotkey } from "../lib/shortcuts";
import { workspaceHotkey } from "../lib/shortcuts";
import { SessionPane } from "./SessionPane";
import { SessionTabs } from "./SessionTabs";
import { afterClose, byIndex, MAX_LIVE_SESSIONS, neighbor } from "./workspaceState";

type KeyboardLock = { lock?: (codes?: string[]) => Promise<void>; unlock?: () => void };
const keyboardApi = () => (navigator as Navigator & { keyboard?: KeyboardLock }).keyboard;

export function WorkspaceView() {
  const { deviceId = "" } = useParams();
  const id = deviceId as DeviceId;
  const [params] = useSearchParams();
  const relay = params.get("relay") === "1";
  const navigate = useNavigate();
  const ws = useWorkspace();
  const st = useWorkspaceState();
  const labels = useDeviceLabels();
  const palette = usePalette();
  const [blocked, setBlocked] = useState<DeviceId | null | false>(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [keyboardLocked, setKeyboardLocked] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const [toolbar, setToolbar] = useState<HTMLDivElement | null>(null);
  const nameOf = (x: DeviceId | null) => (x && labels.get(x)?.name) || "a device";

  // The route decides what is in view; leaving it puts every session in the background.
  useEffect(() => {
    const r = ws.open(id, relay);
    if (!r.ok) {
      setBlocked(r.victim);
      return;
    }
    setBlocked(false);
    ws.focus(id);
    return () => ws.focus(null);
  }, [ws, id, relay]);

  const go = useCallback(
    (to: DeviceId | null, replace = true) =>
      to ? navigate(`/session/${to}`, { replace }) : navigate("/devices"),
    [navigate],
  );

  const close = useCallback(
    (x: DeviceId) => {
      const next = afterClose(ws.getState(), x);
      ws.close(x);
      if (x === id) go(next);
    },
    [ws, id, go],
  );

  const onHotkey = useCallback(
    (h: Hotkey) => {
      const s = ws.getState();
      if (h.t === "palette") palette.open();
      else if (h.t === "dashboard") navigate("/devices");
      else if (h.t === "prev" || h.t === "next") {
        const to = neighbor(s, s.focused, h.t === "next" ? 1 : -1);
        if (to && to !== s.focused) go(to);
      } else if (h.t === "focus") {
        const to = byIndex(s, h.n);
        if (to && to !== s.focused) go(to);
      }
    },
    [ws, palette, navigate, go],
  );

  // Shortcuts while focus is on the toolbar rather than the remote screen
  // (the remote keyboard handles its own and stops them there).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const h = workspaceHotkey(e, keyboardLocked);
      if (!h || h.t === "streamMenu") return;
      e.preventDefault();
      onHotkey(h);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onHotkey, keyboardLocked]);

  // The workspace, not one stage, is fullscreen, so switching sessions keeps
  // it (and Keyboard Lock, which sends Cmd+W, Alt+Tab and Esc to the remote).
  useEffect(() => {
    const onFs = () => {
      const fs = !!rootRef.current && document.fullscreenElement === rootRef.current;
      setFullscreen(fs);
      if (fs) {
        const lock = keyboardApi()?.lock?.();
        if (lock)
          void lock.then(
            () => setKeyboardLocked(true),
            () => setKeyboardLocked(false),
          );
      } else {
        keyboardApi()?.unlock?.();
        setKeyboardLocked(false);
      }
    };
    document.addEventListener("fullscreenchange", onFs);
    return () => document.removeEventListener("fullscreenchange", onFs);
  }, []);
  const toggleFullscreen = useCallback(() => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void rootRef.current?.requestFullscreen({ navigationUI: "hide" }).catch(() => undefined);
  }, []);

  if (blocked !== false) {
    return (
      <div className="flex h-screen items-center justify-center bg-[#121314] p-6 text-white">
        <div className="max-w-md space-y-4 rounded-2xl border border-white/10 bg-[#1e1f20] p-6">
          <h1 className="text-lg font-medium">{MAX_LIVE_SESSIONS} sessions are open</h1>
          <p className="text-sm text-white/70">
            One tab keeps at most {MAX_LIVE_SESSIONS} remote sessions connected.
            {blocked
              ? ` End the one you used least recently, ${nameOf(blocked)}, to open ${nameOf(id)}?`
              : " Close one to open another."}
          </p>
          <div className="flex flex-wrap justify-end gap-2">
            <button
              className="btn-outline border-white/30 text-white hover:bg-white/10"
              onClick={() => navigate(-1)}
            >
              Cancel
            </button>
            {blocked && (
              <button
                className="btn-primary"
                onClick={() => {
                  ws.close(blocked);
                  setBlocked(false);
                  if (ws.open(id, relay).ok) ws.focus(id);
                }}
              >
                End {nameOf(blocked)} and open
              </button>
            )}
          </div>
        </div>
      </div>
    );
  }

  const many = st.order.length > 1;
  const back = (
    <button
      className="btn btn-sm shrink-0 px-2 text-white/80 hover:bg-white/10"
      aria-label="Devices (sessions keep running)"
      title="Devices (sessions keep running) · Ctrl+Alt+Shift+D"
      onClick={() => navigate("/devices")}
    >
      <Icon name="back" size={18} />
    </button>
  );
  const switcher = many && (
    <>
      <button
        className="btn btn-sm text-white/90 hover:bg-white/10"
        aria-label="Previous session (Ctrl+Alt+Shift+←)"
        title="Previous session (Ctrl+Alt+Shift+←)"
        onClick={() => onHotkey({ t: "prev" })}
      >
        <Icon name="chevronLeft" size={18} />
      </button>
      <span className="text-xs text-white/70 tabular-nums">
        {st.order.indexOf(id) + 1}/{st.order.length}
      </span>
      <button
        className="btn btn-sm text-white/90 hover:bg-white/10"
        aria-label="Next session (Ctrl+Alt+Shift+→)"
        title="Next session (Ctrl+Alt+Shift+→)"
        onClick={() => onHotkey({ t: "next" })}
      >
        <Icon name="chevronRight" size={18} />
      </button>
      <span className="mx-1 h-5 w-px bg-white/20" />
    </>
  );

  return (
    <div ref={rootRef} className="flex h-screen flex-col bg-black text-white">
      {!fullscreen && (
        <div className="flex h-12 shrink-0 items-center gap-1 border-b border-white/10 bg-[#161718] pr-2 pl-1.5">
          {back}
          <SessionTabs onSelect={(x) => go(x)} onClose={close} onAdd={() => palette.open()} />
          <div ref={setToolbar} className="flex shrink-0 items-center" />
        </div>
      )}
      <div className="sr-only" aria-live="polite">
        {st.focused ? `Showing ${nameOf(st.focused)}` : ""}
      </div>
      {st.order.map((x) => {
        const e = st.entries[x];
        const session = ws.session(x);
        if (!e || !session) return null;
        return (
          <SessionPane
            key={`${x}:${e.gen}`}
            session={session}
            name={nameOf(x)}
            consoleUser={labels.get(x)?.user ?? null}
            focused={x === st.focused}
            forceRelay={e.forceRelay}
            fullscreen={fullscreen}
            keyboardLocked={keyboardLocked}
            onToggleFullscreen={toggleFullscreen}
            onHotkey={onHotkey}
            onReconnect={(r) => ws.replace(x, { forceRelay: r })}
            onTrust={(key) => ws.replace(x, { trustKey: key })}
            onClose={() => close(x)}
            onBack={() => navigate("/devices")}
            toolbar={toolbar}
            fullscreenExtras={switcher}
          />
        );
      })}
    </div>
  );
}
