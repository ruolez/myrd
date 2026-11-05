// A terminal session in the workspace: no video, just the device's
// terminals. Phones drop the connection when the screen locks or the app
// goes to the background; the shells keep running on the device, so the
// pane reconnects by itself when the page is visible again and reattaches.

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useSessionSnap } from "../../app/workspace";
import { Icon } from "../../components/icons";
import { PowerDialog } from "../../components/PowerDialog";
import { powerFailure } from "../../lib/power";
import { MoreMenu } from "../MoreMenu";
import type { MoreItem } from "../MoreMenu";
import type { PaneProps } from "../SessionPane";
import { SessionOverlays } from "../SessionOverlays";
import { touchFirst } from "../useTouchInput";
import { TerminalDeck } from "./TerminalDeck";
import type { DeckHandle } from "./TerminalDeck";

/** End reasons a dropped phone connection produces: worth reconnecting by itself. */
const TRANSIENT = new Set(["ice_failed", "peer_disconnected", "error", "server_restart"]);
const AUTO_TRIES = 3;
const AUTO_WINDOW_MS = 2 * 60_000;
// Per device, across the panes a reconnect replaces.
const autoTries = new Map<string, number[]>();

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

  // Back from the lock screen or another app: reconnect a dropped session.
  const endReason = snap?.endReason ?? null;
  useEffect(() => {
    if (phase !== "ended" || !endReason || !TRANSIENT.has(endReason) || !focused) return;
    const id = session.deviceId;
    const tryNow = () => {
      if (document.hidden) return;
      const now = Date.now();
      const recent = (autoTries.get(id) ?? []).filter((t) => now - t < AUTO_WINDOW_MS);
      if (recent.length >= AUTO_TRIES) return;
      autoTries.set(id, [...recent, now]);
      onReconnect(forceRelay);
    };
    const t = setTimeout(tryNow, 800);
    document.addEventListener("visibilitychange", tryNow);
    return () => {
      clearTimeout(t);
      document.removeEventListener("visibilitychange", tryNow);
    };
  }, [phase, endReason, focused, session, forceRelay, onReconnect]);

  const items: MoreItem[] = [
    { label: "New shell", icon: "add", onSelect: () => deck.current?.newShell(), disabled: !live },
    {
      label: "Terminals on the device…",
      icon: "terminal",
      onSelect: () => deck.current?.openPicker(),
      disabled: !live,
    },
    { label: "Paste", icon: "paste", onSelect: () => void deck.current?.paste(), disabled: !live },
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
        />
        <SessionOverlays
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
