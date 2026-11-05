// A terminal session in the workspace: no video, just the device's
// terminals. Phones drop the connection when the screen locks or the app
// goes to the background; the shells keep running on the device, so the
// pane reconnects by itself when the page is visible again and reattaches.

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useSessionSnap } from "../../app/workspace";
import { Icon } from "../../components/icons";
import { Spinner } from "../../components/ui";
import { retryDelay, shouldRetry } from "../../lib/termRetry";
import { PowerDialog } from "../../components/PowerDialog";
import { powerFailure } from "../../lib/power";
import { MoreMenu } from "../MoreMenu";
import type { MoreItem } from "../MoreMenu";
import type { PaneProps } from "../SessionPane";
import { SessionOverlays } from "../SessionOverlays";
import { touchFirst } from "../useTouchInput";
import { TerminalDeck } from "./TerminalDeck";
import type { DeckHandle } from "./TerminalDeck";

// Per device, across the panes a reconnect replaces.
const retries = new Map<string, { attempts: number; since: number }>();

export function TerminalPane({
  session,
  name: listName,
  focused,
  forceRelay,
  fullscreen,
  onHotkey,
  onReconnect,
  onTrust,
  onClose,
  onBack,
  onSwitchMode,
  terminalRequest,
  toolbar,
}: PaneProps) {
  const snap = useSessionSnap(session);
  const [touch] = useState(touchFirst);
  const [powerOpen, setPowerOpen] = useState(false);
  const deck = useRef<DeckHandle | null>(null);
  const setDeck = useCallback((h: DeckHandle | null) => (deck.current = h), []);
  const phase = snap?.phase ?? "preparing";
  const live = phase === "connected" || phase === "reconnecting";
  const name = snap?.deviceName || listName || "Device";
  const reconnect = (relay = forceRelay) => onReconnect(relay);
  useEffect(() => {
    if (terminalRequest?.shell) deck.current?.newShell();
  }, [terminalRequest]);

  // A dropped connection reconnects by itself, sooner and sooner after a
  // retry fails, at once when the network or the page comes back; the
  // terminals stay readable meanwhile and are reattached.
  const endReason = snap?.endReason ?? null;
  const id = session.deviceId;
  const [retryAt, setRetryAt] = useState<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [online, setOnline] = useState(() => navigator.onLine);
  const auto =
    phase === "ended" && !!endReason && shouldRetry(endReason, retries.get(id), Date.now());

  useEffect(() => {
    if (phase !== "connected") return;
    // Connected for a while: the next drop starts over from a short wait.
    const t = setTimeout(() => retries.delete(id), 10_000);
    return () => clearTimeout(t);
  }, [phase, id]);

  useEffect(() => {
    const on = () => setOnline(true);
    const off = () => setOnline(false);
    window.addEventListener("online", on);
    window.addEventListener("offline", off);
    return () => {
      window.removeEventListener("online", on);
      window.removeEventListener("offline", off);
    };
  }, []);

  const retryNow = useCallback(() => {
    const r = retries.get(id) ?? { attempts: 0, since: Date.now() };
    retries.set(id, { ...r, attempts: r.attempts + 1 });
    setRetryAt(null);
    onReconnect(forceRelay);
  }, [id, onReconnect, forceRelay]);

  useEffect(() => {
    if (!auto || !focused) {
      setRetryAt(null);
      return;
    }
    const wait = retryDelay(retries.get(id)?.attempts ?? 0);
    let timer: ReturnType<typeof setTimeout> | null = null;
    const arm = () => {
      if (timer) clearTimeout(timer);
      timer = null;
      if (document.hidden || !navigator.onLine) {
        setRetryAt(null); // waits for the page or the network to come back
        return;
      }
      setNow(Date.now());
      setRetryAt(Date.now() + wait);
      timer = setTimeout(retryNow, wait);
    };
    const back = () => {
      if (!document.hidden && navigator.onLine) retryNow();
    };
    arm();
    document.addEventListener("visibilitychange", back);
    window.addEventListener("online", back);
    return () => {
      if (timer) clearTimeout(timer);
      document.removeEventListener("visibilitychange", back);
      window.removeEventListener("online", back);
    };
  }, [auto, focused, id, retryNow]);

  useEffect(() => {
    if (retryAt === null) return;
    const t = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(t);
  }, [retryAt]);

  const items: MoreItem[] = [
    { label: "New shell", icon: "add", onSelect: () => deck.current?.newShell(), disabled: !live },
    {
      label: "Terminals on the device…",
      icon: "terminal",
      onSelect: () => deck.current?.openPicker(),
      disabled: !live,
    },
    { label: "Paste", icon: "paste", onSelect: () => void deck.current?.paste(), disabled: !live },
    { label: "Find…", icon: "search", onSelect: () => deck.current?.find(), disabled: !live },
    {
      label: "Upload a file here…",
      icon: "upload",
      onSelect: () => deck.current?.upload(),
      disabled: !live || !snap?.files,
    },
    ...(touch
      ? [
          {
            label: "Select text…",
            icon: "textSelect" as const,
            onSelect: () => deck.current?.selectText(),
            disabled: !live,
          },
        ]
      : []),
    { label: "Larger text", icon: "zoomIn", onSelect: () => deck.current?.zoom(1) },
    { label: "Smaller text", icon: "zoomOut", onSelect: () => deck.current?.zoom(-1) },
    { label: "Appearance…", icon: "palette", onSelect: () => deck.current?.appearance() },
    ...(onSwitchMode
      ? [
          {
            label: "Show the screen",
            icon: "monitor" as const,
            onSelect: () => onSwitchMode("desktop"),
          },
        ]
      : []),
    ...(snap?.powerActions.length
      ? [
          {
            label: "Restart…",
            icon: "power" as const,
            onSelect: () => setPowerOpen(true),
            disabled: !live,
          },
        ]
      : []),
  ];

  return (
    <div
      className={`min-h-0 flex-1 flex-col bg-[#131314] text-white ${focused ? "flex" : "hidden"}`}
      aria-hidden={!focused}
    >
      {toolbar &&
        focused &&
        !fullscreen &&
        createPortal(
          <div className="flex items-center gap-0.5">
            <MoreMenu items={items} />
            <button
              className="btn btn-sm ml-1 bg-[#d93025] px-3 text-white hover:brightness-110 max-sm:px-2.5"
              onClick={() => session.end("user_hangup")}
              disabled={phase === "ended" || phase === "pin_mismatch"}
              aria-label="End session"
              title="End this session (shells keep running on the device)"
            >
              <Icon name="callEnd" size={16} />
              <span className="hidden md:inline">End</span>
            </button>
          </div>,
          toolbar,
        )}
      <div className="relative flex min-h-0 flex-1 flex-col">
        <TerminalDeck
          deviceId={session.deviceId}
          channel={snap?.terminal ?? null}
          state={snap?.terminalState ?? "pending"}
          visible={focused}
          touch={touch}
          autoStart
          connected={live}
          onHotkey={onHotkey}
          deckRef={setDeck}
          files={snap?.files ?? null}
        />
        {auto && focused && (
          <div
            role="status"
            className="absolute top-12 left-1/2 z-30 flex max-w-[calc(100%-1rem)] -translate-x-1/2 items-center gap-2 rounded-full bg-[#2d2e30] py-1.5 pr-1.5 pl-3.5 text-sm text-white shadow-lg ring-1 ring-white/10"
          >
            <Spinner size={14} label="Reconnecting" />
            <span className="min-w-0 truncate">
              {!online
                ? "Offline. Reconnecting when the network is back…"
                : retryAt === null
                  ? "Connection lost. Reconnecting…"
                  : `Connection lost. Reconnecting in ${Math.max(1, Math.ceil((retryAt - now) / 1000))} s…`}
            </span>
            <button
              className="shrink-0 rounded-full bg-white/10 px-3 py-1 text-xs font-medium hover:bg-white/15"
              onClick={retryNow}
            >
              Now
            </button>
          </div>
        )}
        <SessionOverlays
          hideEnded={auto}
          snap={snap}
          name={name}
          forceRelay={forceRelay}
          onReconnect={reconnect}
          onClose={onClose}
          onBack={onBack}
          onTrust={onTrust}
        />
        {powerOpen && snap && (
          <PowerDialog
            deviceName={snap.deviceName}
            os={snap.agentOs}
            actions={snap.powerActions}
            onRun={async (action) => {
              const r = await session.power(action);
              if (!r.ok) throw new Error(powerFailure(r.code));
              setPowerOpen(false);
            }}
            onClose={() => setPowerOpen(false)}
          />
        )}
      </div>
    </div>
  );
}
