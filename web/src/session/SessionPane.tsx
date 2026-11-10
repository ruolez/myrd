import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type {
  DragEvent as ReactDragEvent,
  PointerEvent as ReactPointerEvent,
  ReactNode,
  RefObject,
} from "react";
import { useSessionSnap } from "../app/workspace";
import { Icon } from "../components/icons";
import { PowerDialog } from "../components/PowerDialog";
import { Spinner } from "../components/ui";
import { displayRect } from "../lib/geometry";
import { comboInput } from "../lib/combos";
import type { Combo } from "../lib/combos";
import { fileOpsAvailable } from "../lib/files";
import { encodeText } from "../lib/input";
import { powerFailure } from "../lib/power";
import type { Hotkey } from "../lib/shortcuts";
import { streamView } from "../lib/stream";
import type { TouchMode } from "../lib/touch";
import { viewZoom } from "../lib/zoom";
import { CursorLayer } from "./CursorLayer";
import { startUpload } from "./files/batches";
import { useConflictPrompt } from "./files/ConflictDialog";
import { DropOverlay } from "./files/DropOverlay";
import { FileManager } from "./files/FileManager";
import { nodesFromDrop } from "./files/localFs";
import { useTransfers } from "./files/TransferQueue";
import { TransferPill } from "./files/TransferPill";
import { Hud } from "./Hud";
import { KeysButton } from "./KeysMenu";
import { MoreMenu } from "./MoreMenu";
import type { RemoteSession } from "./RemoteSession";
import { SessionOverlays } from "./SessionOverlays";
import type { DeckHandle } from "./terminal/TerminalDeck";

// xterm.js is large: terminals load with the first one opened.
const TerminalPane = lazy(() =>
  import("./terminal/TerminalPane").then((m) => ({ default: m.TerminalPane })),
);
const TerminalDeck = lazy(() =>
  import("./terminal/TerminalDeck").then((m) => ({ default: m.TerminalDeck })),
);
import type { SessionMode } from "./workspaceState";
import { StreamButton, StreamMenu } from "./StreamMenu";
import { LockedBanner, SignInDialog, UnlockingOverlay, useSavedSignIn } from "./SavedSignIn";
import { useRemoteInput } from "./useRemoteInput";
import { touchFirst, useTouchInput } from "./useTouchInput";

const HUD_KEY = "remotectl-hud";
const TOUCH_KEY = "remotectl-touch";
const TOUCH_HINT_KEY = "remotectl-touch-hint";

function readHudPref() {
  try {
    return localStorage.getItem(HUD_KEY) === "1";
  } catch {
    return false;
  }
}

function readTouchMode(): TouchMode {
  try {
    return localStorage.getItem(TOUCH_KEY) === "trackpad" ? "trackpad" : "direct";
  } catch {
    return "direct";
  }
}

const TOUCH_HINTS: Record<TouchMode, string> = {
  direct: "Tap to click · Hold to right-click · Swipe to scroll · Pinch to zoom",
  trackpad: "Drag to move the pointer · Tap to click · Two fingers to scroll · Pinch to zoom",
};

type Panel = "stream" | "files" | "keys";

const FOLDER_HINT =
  "This device's agent can't receive folders yet. Drop files, or zip the folder first.";

const hasFiles = (e: { dataTransfer: DataTransfer | null }) =>
  !!e.dataTransfer && Array.from(e.dataTransfer.types).includes("Files");

type StageSize = { w: number; h: number; dpr: number };

/** Tracks an element's CSS size and the device pixel ratio (which changes across monitors). */
function useStageSize(ref: RefObject<HTMLDivElement | null>): StageSize {
  const [size, setSize] = useState<StageSize>({ w: 0, h: 0, dpr: window.devicePixelRatio || 1 });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    let mq: MediaQueryList | null = null;
    const measure = () => {
      const r = el.getBoundingClientRect();
      const dpr = window.devicePixelRatio || 1;
      setSize((s) =>
        s.w === r.width && s.h === r.height && s.dpr === dpr ? s : { w: r.width, h: r.height, dpr },
      );
    };
    const watchDpr = () => {
      mq?.removeEventListener("change", onDpr);
      mq = window.matchMedia(`(resolution: ${window.devicePixelRatio || 1}dppx)`);
      mq.addEventListener("change", onDpr);
    };
    const onDpr = () => {
      watchDpr();
      measure();
    };
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    watchDpr();
    measure();
    return () => {
      ro.disconnect();
      mq?.removeEventListener("change", onDpr);
    };
  }, [ref]);
  return size;
}

/** The stream's encoded size, from the video element. */
function useVideoSize(ref: RefObject<HTMLVideoElement | null>) {
  const [size, setSize] = useState({ w: 0, h: 0 });
  useEffect(() => {
    const v = ref.current;
    if (!v) return;
    const read = () =>
      setSize((s) =>
        s.w === v.videoWidth && s.h === v.videoHeight ? s : { w: v.videoWidth, h: v.videoHeight },
      );
    v.addEventListener("resize", read);
    v.addEventListener("loadedmetadata", read);
    return () => {
      v.removeEventListener("resize", read);
      v.removeEventListener("loadedmetadata", read);
    };
  }, [ref]);
  return size;
}

export type PaneProps = {
  session: RemoteSession;
  /** Name from the device list until the session reports its own. */
  name: string;
  /** The device's signed-in user (device info), to prefill its saved sign-in. */
  consoleUser: string | null;
  /** The session in view; the others stay mounted (state, cursor) but hidden. */
  focused: boolean;
  forceRelay: boolean;
  fullscreen: boolean;
  keyboardLocked: boolean;
  onToggleFullscreen: () => void;
  onHotkey: (h: Hotkey) => void;
  onReconnect: (relay: boolean) => void;
  onTrust: (key: string) => void;
  /** Ends and removes this session from the workspace. */
  onClose: () => void;
  onBack: () => void;
  /** Where the focused session puts its controls (the workspace bar). */
  toolbar: HTMLElement | null;
  /** Extra controls in the fullscreen bar (session switching). */
  fullscreenExtras?: ReactNode;
  /** Reconnects as the other kind of session (terminal ↔ desktop). */
  onSwitchMode?: (mode: SessionMode) => void;
  /** A new request opens the Terminal (panel) of the session; shell: start a new shell in it. */
  terminalRequest?: TerminalRequest;
  /** A terminal panel is open in this (focused) desktop session. */
  onTerminalPanel?: (open: boolean) => void;
};

export type TerminalRequest = { n: number; shell: boolean };

/** A terminal session shows terminals only; a desktop one the screen, with terminals in a panel. */
export function SessionPane(props: PaneProps) {
  return props.session.snapshot.mode === "terminal" ? (
    <Suspense fallback={<LoadingPane focused={props.focused} />}>
      <TerminalPane {...props} />
    </Suspense>
  ) : (
    <DesktopPane {...props} />
  );
}

function LoadingPane({ focused }: { focused: boolean }) {
  return (
    <div
      className={`min-h-0 flex-1 items-center justify-center bg-[#131314] ${focused ? "flex" : "hidden"}`}
    >
      <Spinner size={32} />
    </div>
  );
}

function DesktopPane({
  session,
  name: listName,
  consoleUser,
  focused,
  forceRelay,
  fullscreen,
  keyboardLocked,
  onToggleFullscreen,
  onHotkey: onWorkspaceHotkey,
  onReconnect,
  onTrust,
  onClose,
  onBack,
  toolbar,
  fullscreenExtras,
  terminalRequest,
  onTerminalPanel,
}: PaneProps) {
  const [hud, setHud] = useState(readHudPref);
  const [touchMode, setTouchMode] = useState(readTouchMode);
  const [coarse] = useState(touchFirst);
  const [softKeyboard, setSoftKeyboard] = useState(false);
  const [touchHint, setTouchHint] = useState<string | null>(null);
  const [relative, setRelative] = useState(false);
  const [streamOpen, setStreamOpen] = useState(false);
  const [filesOpen, setFilesOpen] = useState(false);
  const [keysOpen, setKeysOpen] = useState(false);
  const [powerOpen, setPowerOpen] = useState(false);
  // The Terminal panel stays mounted once opened, so its terminals keep their scrollback.
  const [termOpen, setTermOpen] = useState(false);
  const [termMounted, setTermMounted] = useState(false);
  const [filesFolder, setFilesFolder] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);
  const [hint, setHint] = useState<string | null>(null);
  const [pill, setPill] = useState(false);
  const dragDepth = useRef(0);
  const pillTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const relockOnClose = useRef(false);

  const stageRef = useRef<HTMLDivElement>(null);
  const scrollerRef = useRef<HTMLDivElement>(null);
  const zoomRef = useRef<HTMLDivElement>(null);
  const surfaceRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const kbRef = useRef<HTMLTextAreaElement>(null);
  const sessionRef = useRef<RemoteSession | null>(session);
  sessionRef.current = session;

  const snap = useSessionSnap(session);
  const signIn = useSavedSignIn(session, snap, focused);

  // Attach the stream without re-rendering frames through a canvas.
  useEffect(() => {
    const v = videoRef.current;
    if (!v) return;
    if (v.srcObject !== (snap?.stream ?? null)) v.srcObject = snap?.stream ?? null;
    if (snap?.stream) void v.play().catch(() => undefined);
    return () => {
      v.srcObject = null;
    };
  }, [snap?.stream]);

  const live = snap?.phase === "connected" || snap?.phase === "reconnecting";
  const active = live && focused && !termOpen;
  const openTerminal = useCallback(() => {
    setTermMounted(true);
    setTermOpen(true);
  }, []);
  const termDeck = useRef<DeckHandle | null>(null);
  const setTermDeck = useCallback((h: DeckHandle | null) => (termDeck.current = h), []);
  useEffect(() => {
    if (!terminalRequest) return;
    if (terminalRequest.shell && termOpenRef.current) termDeck.current?.newShell();
    openTerminal();
    // Only new requests count.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [terminalRequest]);
  const termOpenRef = useRef(termOpen);
  termOpenRef.current = termOpen;
  useEffect(() => {
    onTerminalPanel?.(focused && termOpen);
  }, [focused, termOpen, onTerminalPanel]);
  const terminalLabel =
    snap?.terminalState === "unsupported" ? "Terminal (not offered by this device)" : "Terminal";
  const files = snap?.filesState === "ready" ? snap.files : null;
  const fileOps = fileOpsAvailable(snap?.features ?? null);
  const conflicts = useConflictPrompt();

  // The stream is sized to the stage in device pixels and drawn 1:1 (fit),
  // or kept native and scrolled (actual).
  const stage = useStageSize(stageRef);
  const videoSize = useVideoSize(videoRef);
  const scale = snap?.streamPrefs.scale ?? "fit";
  const rect = displayRect(stage.w, stage.h, videoSize.w, videoSize.h, stage.dpr, scale);
  const monitors = snap?.monitors ?? [];
  const monitor = monitors[snap?.activeMonitor ?? 0];
  // Cursor shapes are in native pixels: prefer the agent's native size for this stream.
  const nativeWidth = snap?.streamState?.native_width || monitor?.width || 0;
  const touch = useTouchInput(
    {
      session: sessionRef,
      surface: surfaceRef,
      video: videoRef,
      zoom: zoomRef,
      scroller: scrollerRef,
    },
    active,
    snap?.activeMonitor ?? 0,
    touchMode,
    { content: rect, stage, nativeWidth, zoomable: scale === "fit" },
  );
  // A zoomed-in picture gets a larger stream, in steps, so it stays sharp.
  const streamZoom = viewZoom(touch.zoom);
  // Only the pane in view measures a real stage; a hidden one keeps its size.
  useEffect(() => {
    if (!focused || stage.w <= 0) return;
    session.setView(
      scale === "fit" ? streamView(stage.w * streamZoom, stage.h * streamZoom, stage.dpr) : null,
    );
  }, [session, focused, scale, stage.w, stage.h, stage.dpr, streamZoom]);
  const transfers = useTransfers(snap?.files ?? null);

  // In-stage panels release pointer lock; it comes back on close if it was on.
  const openPanel = useCallback((which: Panel) => {
    relockOnClose.current ||= document.pointerLockElement !== null;
    setRelative(false);
    ({ stream: setStreamOpen, files: setFilesOpen, keys: setKeysOpen })[which](true);
  }, []);
  const closePanel = (which: Panel) => {
    const open = { stream: streamOpen, files: filesOpen, keys: keysOpen };
    ({ stream: setStreamOpen, files: setFilesOpen, keys: setKeysOpen })[which](false);
    const other = (Object.keys(open) as Panel[]).some((p) => p !== which && open[p]);
    if (!other) {
      if (relockOnClose.current) setRelative(true);
      relockOnClose.current = false;
      refocus();
    }
  };
  const onHotkey = useCallback(
    (h: Hotkey) => (h.t === "streamMenu" ? openPanel("stream") : onWorkspaceHotkey(h)),
    [openPanel, onWorkspaceHotkey],
  );
  // Back to typing on the remote, unless that would raise a phone's keyboard.
  const refocus = () => {
    if (!coarse) kbRef.current?.focus({ preventScroll: true });
  };
  const toggleSoftKeyboard = () => {
    const kb = kbRef.current;
    if (!kb) return;
    if (document.activeElement === kb) kb.blur();
    else kb.focus({ preventScroll: true });
  };
  useEffect(() => {
    const kb = kbRef.current;
    if (!kb || !coarse) return;
    const onFocus = () => setSoftKeyboard(true);
    const onBlur = () => {
      setSoftKeyboard(false);
      // iOS can leave the page scrolled after its keyboard closes.
      requestAnimationFrame(() => window.scrollTo(0, 0));
    };
    kb.addEventListener("focus", onFocus);
    kb.addEventListener("blur", onBlur);
    return () => {
      kb.removeEventListener("focus", onFocus);
      kb.removeEventListener("blur", onBlur);
    };
  }, [coarse]);

  const chooseTouchMode = (m: TouchMode) => {
    setTouchMode(m);
    setTouchHint(TOUCH_HINTS[m]);
    try {
      localStorage.setItem(TOUCH_KEY, m);
    } catch {
      // storage unavailable
    }
  };
  // The gestures are shown once, the first time a touch session goes live.
  useEffect(() => {
    if (!active || !coarse) return;
    try {
      if (localStorage.getItem(TOUCH_HINT_KEY)) return;
      localStorage.setItem(TOUCH_HINT_KEY, "1");
    } catch {
      return;
    }
    setTouchHint(TOUCH_HINTS[touchMode]);
  }, [active, coarse, touchMode]);
  useEffect(() => {
    if (!touchHint) return;
    const t = setTimeout(() => setTouchHint(null), 5000);
    return () => clearTimeout(t);
  }, [touchHint]);

  const sendCombo = (c: Combo) => {
    for (const m of comboInput(c.keys)) session?.sendInput(m);
    closePanel("keys");
  };

  useRemoteInput(
    { session: sessionRef, surface: surfaceRef, video: videoRef, keyboard: kbRef },
    active,
    snap?.activeMonitor ?? 0,
    onHotkey,
    keyboardLocked,
  );

  useEffect(() => {
    if (!live) {
      setStreamOpen(false);
      setFilesOpen(false);
      setKeysOpen(false);
    }
  }, [live]);

  useEffect(() => {
    if (!hint) return;
    const t = setTimeout(() => setHint(null), 6000);
    return () => clearTimeout(t);
  }, [hint]);

  const revealPill = useCallback((ms: number) => {
    setPill(true);
    if (pillTimer.current) clearTimeout(pillTimer.current);
    pillTimer.current = setTimeout(() => setPill(false), ms);
  }, []);
  useEffect(() => {
    if (fullscreen && focused) revealPill(3000);
    else setPill(false);
  }, [fullscreen, focused, revealPill]);
  useEffect(
    () => () => {
      if (pillTimer.current) clearTimeout(pillTimer.current);
    },
    [],
  );

  const onStagePointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (!fullscreen || document.pointerLockElement) return;
    if (e.clientY - e.currentTarget.getBoundingClientRect().top < 40) revealPill(2500);
  };
  const lastPointer = useRef("");

  const onDragEnter = (e: ReactDragEvent<HTMLDivElement>) => {
    if (!hasFiles(e) || !files) return;
    e.preventDefault();
    dragDepth.current++;
    setDragging(true);
  };
  const onDragOver = (e: ReactDragEvent<HTMLDivElement>) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = files ? "copy" : "none";
  };
  const onDragLeave = () => {
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  };
  const onDrop = (e: ReactDragEvent<HTMLDivElement>) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    if (!files) return;
    const to = filesOpen ? filesFolder : null;
    // Read synchronously: the dropped items are gone once this handler returns.
    void nodesFromDrop(e.dataTransfer)
      .then((nodes) => {
        if (nodes.length === 0) return;
        if (!fileOps && nodes.some((n) => n.kind === "dir")) setHint(FOLDER_HINT);
        startUpload(files, { nodes, to, ops: fileOps, ask: conflicts.ask });
      })
      .catch(() => undefined);
  };

  // Clipboard: push local clipboard to the remote when the session regains focus.
  const lastClip = useRef<string | null>(null);
  useEffect(() => {
    if (!active) return;
    const sync = () => {
      if (!document.hasFocus()) return;
      navigator.clipboard
        ?.readText()
        .then((t) => {
          if (t && t !== lastClip.current) {
            lastClip.current = t;
            sessionRef.current?.sendClipboardText(t);
          }
        })
        .catch(() => undefined);
    };
    sync();
    window.addEventListener("focus", sync);
    return () => window.removeEventListener("focus", sync);
  }, [active]);

  // Pointer Lock stays opt-in: under it the cursor can only be drawn where
  // the device reports it, a network round trip behind the hand, while
  // without it the browser's own cursor follows the mouse with no delay.
  useEffect(() => {
    const onLock = () => setRelative(document.pointerLockElement === surfaceRef.current);
    document.addEventListener("pointerlockchange", onLock);
    return () => document.removeEventListener("pointerlockchange", onLock);
  }, []);
  useEffect(() => {
    if (!fullscreen) setRelative(false);
  }, [fullscreen]);

  useEffect(() => {
    const el = surfaceRef.current;
    if (!el || !active) return;
    if (relative && document.pointerLockElement !== el) {
      const req = el.requestPointerLock as unknown as (o?: {
        unadjustedMovement?: boolean;
      }) => Promise<void> | void;
      try {
        const r = req.call(el, { unadjustedMovement: true });
        if (r instanceof Promise) r.catch(() => setRelative(false));
      } catch {
        setRelative(false);
      }
    } else if (!relative && document.pointerLockElement === el) {
      document.exitPointerLock();
    }
  }, [relative, active]);

  const toggleFullscreen = () => {
    onToggleFullscreen();
    refocus();
  };

  const toggleHud = () =>
    setHud((h) => {
      try {
        localStorage.setItem(HUD_KEY, h ? "0" : "1");
      } catch {
        // storage unavailable
      }
      return !h;
    });

  const typeClipboard = async () => {
    const text = await navigator.clipboard?.readText().catch(() => "");
    if (text) for (const m of encodeText(text)) sessionRef.current?.sendInput(m);
    refocus();
  };

  const reconnect = (relay = forceRelay) => onReconnect(relay);

  const phase = snap?.phase ?? "preparing";
  const name = snap?.deviceName || listName || "Device";
  // iPhone Safari has no element fullscreen.
  const canFullscreen = document.fullscreenEnabled;
  const keyboardButton = coarse && (
    <ToolButton
      label={softKeyboard ? "Hide keyboard" : "Show keyboard"}
      icon="keyboard"
      active={softKeyboard}
      disabled={!live}
      // Keeps focus where it is, so the keyboard does not close and reopen.
      onPointerDown={(e) => e.preventDefault()}
      onClick={toggleSoftKeyboard}
    />
  );
  const filesLabel =
    snap?.filesState === "unsupported" ? "Files (not supported by this device's agent)" : "Files";

  return (
    <div
      className={`min-h-0 flex-1 flex-col bg-black text-white ${focused ? "flex" : "hidden"}`}
      aria-hidden={!focused}
    >
      {toolbar &&
        focused &&
        !fullscreen &&
        createPortal(
          <div className="flex items-center gap-0.5">
            {monitors.length > 1 && (
              <label className="mr-1 flex items-center gap-1">
                <Icon name="monitor" size={16} className="text-white/60" />
                <span className="sr-only">Monitor</span>
                <select
                  className="rounded-md bg-white/10 px-2 py-1 text-xs"
                  value={snap?.activeMonitor ?? 0}
                  onChange={(e) => session?.selectMonitor(Number(e.target.value))}
                >
                  {monitors.map((m, i) => (
                    <option key={m.id} value={i} className="text-black">
                      {m.name || `Display ${i + 1}`} ({m.width}×{m.height})
                    </option>
                  ))}
                </select>
              </label>
            )}
            {snap && (
              <StreamButton
                prefs={snap.streamPrefs}
                state={snap.streamState}
                open={streamOpen}
                disabled={!live}
                onClick={() => (streamOpen ? closePanel("stream") : openPanel("stream"))}
              />
            )}
            {!coarse && (
              <ToolButton
                label={filesLabel}
                icon="folder"
                active={filesOpen}
                onClick={() => (filesOpen ? closePanel("files") : openPanel("files"))}
                disabled={!live || !files}
                text="Files"
              />
            )}
            {!coarse && (
              <ToolButton
                label={terminalLabel}
                icon="terminal"
                active={termOpen}
                onClick={() => (termOpen ? setTermOpen(false) : openTerminal())}
                disabled={!live || snap?.terminalState === "unsupported"}
                text="Terminal"
              />
            )}
            {keyboardButton}
            <KeysButton
              icon={coarse ? "keyboardCommand" : "keyboard"}
              os={snap?.agentOs ?? null}
              open={keysOpen && !fullscreen}
              disabled={!live}
              align="end"
              onToggle={() => (keysOpen ? closePanel("keys") : openPanel("keys"))}
              onSend={sendCombo}
              onClose={() => closePanel("keys")}
            />
            <MoreMenu
              items={[
                // A phone's toolbar has no room for Files or Terminal.
                ...(coarse
                  ? [
                      {
                        label: "Files",
                        icon: "folder" as const,
                        onSelect: () => openPanel("files"),
                        disabled: !live || !files,
                      },
                      {
                        label: terminalLabel,
                        icon: "terminal" as const,
                        onSelect: openTerminal,
                        disabled: !live || snap?.terminalState === "unsupported",
                      },
                    ]
                  : []),
                {
                  label: "Paste as keystrokes",
                  icon: "paste",
                  onSelect: () => void typeClipboard(),
                  disabled: !live,
                },
                ...(signIn.canUnlock
                  ? [
                      {
                        label: signIn.saved ? "Saved sign-in…" : "Save sign-in for unlocking…",
                        icon: "key" as const,
                        onSelect: signIn.openDialog,
                        disabled: !live || signIn.saved === undefined,
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
                ...(coarse
                  ? [
                      {
                        label: "Trackpad mode",
                        icon: "touch" as const,
                        checked: touchMode === "trackpad",
                        onSelect: () =>
                          chooseTouchMode(touchMode === "trackpad" ? "direct" : "trackpad"),
                      },
                    ]
                  : [
                      {
                        label: "Lock pointer (games, 3D)",
                        icon: "lock" as const,
                        checked: relative,
                        onSelect: () => setRelative((r) => !r),
                        disabled: !live,
                      },
                    ]),
                {
                  label: "Latency HUD",
                  icon: "speed",
                  checked: hud,
                  onSelect: toggleHud,
                },
                {
                  label: "Refresh picture",
                  icon: "refresh",
                  onSelect: () => session?.requestKeyframe(),
                  disabled: !live,
                },
              ]}
            />
            {canFullscreen && (
              <>
                <span className="mx-1.5 h-5 w-px bg-white/15" />
                <ToolButton label="Fullscreen" icon="fullscreen" onClick={toggleFullscreen} />
              </>
            )}
            <button
              className="btn btn-sm ml-1 bg-[#d93025] px-3 text-white hover:brightness-110 max-sm:px-2.5"
              onClick={() => session.end("user_hangup")}
              disabled={phase === "ended" || phase === "pin_mismatch"}
              aria-label="End session"
              title="End this session (the device is released)"
            >
              <Icon name="callEnd" size={16} />
              <span className="hidden md:inline">End</span>
            </button>
          </div>,
          toolbar,
        )}

      <div
        ref={stageRef}
        className="session-stage relative min-h-0 flex-1 overflow-hidden"
        onPointerMove={onStagePointerMove}
        onPointerDown={onStagePointerMove}
        onDragEnter={onDragEnter}
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
      >
        <div
          ref={scrollerRef}
          className={`absolute inset-0 ${scale === "actual" ? "overflow-auto" : "overflow-hidden"}`}
        >
          <div
            className="relative"
            style={{
              width: Math.max(stage.w, rect.left + rect.width),
              height: Math.max(stage.h, rect.top + rect.height),
            }}
          >
            <div ref={zoomRef} className="absolute inset-0 origin-top-left">
              <video
                ref={videoRef}
                className="absolute"
                style={{ left: rect.left, top: rect.top, width: rect.width, height: rect.height }}
                playsInline
                autoPlay
                muted
                disablePictureInPicture
              />
            </div>
            <CursorLayer
              session={session}
              video={videoRef}
              surface={surfaceRef}
              nativeWidth={nativeWidth}
              active={active}
            />
            <div
              ref={surfaceRef}
              className="absolute inset-0 cursor-default touch-none select-none [-webkit-touch-callout:none]"
              onPointerDownCapture={(e) => (lastPointer.current = e.pointerType)}
              onClick={() => lastPointer.current !== "touch" && refocus()}
            />
          </div>
        </div>
        {/* Outside the zoomed and scrolled content, so focusing it never scrolls
            the page; 16px text, or iOS zooms in on focus. */}
        <textarea
          ref={kbRef}
          aria-label="Remote keyboard input"
          className="absolute top-0 left-0 h-px w-px resize-none overflow-hidden border-0 p-0 text-base opacity-0"
          autoCapitalize="off"
          autoCorrect="off"
          autoComplete="off"
          spellCheck={false}
          tabIndex={0}
        />
        {touch.zoom > 1.01 && live && (
          <button
            className="absolute right-3 bottom-3 z-20 flex items-center gap-1.5 rounded-full bg-black/70 px-3 py-1.5 text-xs text-white/90 shadow-lg tabular-nums"
            aria-label="Zoom out to the whole screen"
            onClick={touch.resetZoom}
          >
            {touch.zoom.toFixed(1)}×
            <Icon name="close" size={14} />
          </button>
        )}
        {touchHint && live && (
          <div
            role="status"
            className="pointer-events-none absolute bottom-14 left-1/2 z-30 w-max max-w-[calc(100%-2rem)] -translate-x-1/2 rounded-lg bg-black/80 px-4 py-2 text-center text-sm text-white shadow-lg"
          >
            {touchHint}
          </div>
        )}
        {live && snap && <UnlockingOverlay deviceName={snap.deviceName} signIn={signIn} />}
        {live && snap?.locked && !signIn.covering && (
          <LockedBanner deviceName={snap.deviceName} signIn={signIn} />
        )}
        {signIn.dialog && snap && (
          <SignInDialog
            deviceName={snap.deviceName}
            consoleUser={consoleUser}
            signIn={signIn}
            locked={snap.locked}
          />
        )}
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
        {hud && live && snap && (
          <Hud
            session={session}
            encoder={snap.encoder}
            video={videoRef}
            stream={snap.streamState}
          />
        )}

        {fullscreen && (pill || keysOpen) && live && (
          <div
            className="absolute top-3 left-1/2 z-30 flex -translate-x-1/2 items-center gap-1 rounded-full bg-black/75 px-2 py-1 text-sm shadow-lg"
            onPointerEnter={() => pillTimer.current && clearTimeout(pillTimer.current)}
            onPointerLeave={() => revealPill(1500)}
          >
            {fullscreenExtras}
            {monitors.length > 1 && (
              <>
                <div role="group" aria-label="Remote screen" className="flex items-center gap-0.5">
                  <Icon name="monitor" size={16} className="mx-1 text-white/60" />
                  {monitors.map((m, i) => {
                    const current = i === (snap?.activeMonitor ?? 0);
                    const label = `${m.name || `Display ${i + 1}`} (${m.width}×${m.height})`;
                    return (
                      <button
                        key={m.id}
                        className={`btn btn-sm min-w-8 justify-center px-2 tabular-nums ${
                          current ? "bg-white/20 text-white" : "text-white/70 hover:bg-white/10"
                        }`}
                        aria-label={label}
                        aria-pressed={current}
                        title={label}
                        onClick={() => {
                          if (!current) session?.selectMonitor(i);
                          refocus();
                        }}
                      >
                        {i + 1}
                      </button>
                    );
                  })}
                </div>
                <span className="mx-1 h-5 w-px bg-white/20" />
              </>
            )}
            <ToolButton
              label="Stream settings (Ctrl+Alt+Shift+S)"
              icon="speed"
              text="Stream"
              active={streamOpen}
              onClick={() => openPanel("stream")}
            />
            <ToolButton
              label={filesLabel}
              icon="folder"
              text="Files"
              active={filesOpen}
              onClick={() => openPanel("files")}
              disabled={!files}
            />
            {keyboardButton}
            <KeysButton
              icon={coarse ? "keyboardCommand" : "keyboard"}
              os={snap?.agentOs ?? null}
              open={keysOpen && fullscreen}
              align="center"
              onToggle={() => (keysOpen ? closePanel("keys") : openPanel("keys"))}
              onSend={sendCombo}
              onClose={() => closePanel("keys")}
            />
            {!coarse && (
              <ToolButton
                label={
                  relative
                    ? "Absolute mouse"
                    : "Relative mouse (pointer lock) for games and 3D apps; the cursor follows a round trip behind"
                }
                icon="lock"
                active={relative}
                onClick={() => setRelative((r) => !r)}
              />
            )}
            <ToolButton
              label="Exit fullscreen"
              icon="fullscreenExit"
              text="Exit fullscreen"
              onClick={toggleFullscreen}
            />
          </div>
        )}

        {filesOpen && files && (
          <FileManager
            files={files}
            ops={fileOps}
            deviceName={name}
            ask={conflicts.ask}
            onClose={() => closePanel("files")}
            onFolder={setFilesFolder}
          />
        )}
        {termMounted && (
          <aside
            aria-label="Terminal"
            className={`absolute inset-0 z-30 flex flex-col overflow-hidden bg-[#131314] sm:inset-3 sm:rounded-xl sm:border sm:border-white/10 sm:shadow-2xl ${termOpen ? "" : "hidden"}`}
          >
            <Suspense fallback={<LoadingPane focused />}>
              <TerminalDeck
                deviceId={session.deviceId}
                channel={snap?.terminal ?? null}
                state={snap?.terminalState ?? "pending"}
                visible={focused && termOpen}
                touch={coarse}
                autoStart
                connected={live}
                onHotkey={onHotkey}
                deckRef={setTermDeck}
                files={snap?.files ?? null}
                headerEnd={
                  <button
                    className="btn btn-sm shrink-0 px-2 text-white/80 hover:bg-white/10"
                    aria-label="Back to the screen (terminals keep running)"
                    title="Back to the screen"
                    onClick={() => {
                      setTermOpen(false);
                      refocus();
                    }}
                  >
                    <Icon name="close" size={18} />
                  </button>
                }
              />
            </Suspense>
          </aside>
        )}
        {streamOpen && snap && live && (
          <StreamMenu
            className="top-3 right-3"
            prefs={snap.streamPrefs}
            state={snap.streamState}
            feature={snap.streamFeature}
            canHideWallpaper={snap.features?.includes("wallpaper") ?? false}
            maxFps={snap.maxFps}
            monitor={monitor}
            onChange={(p) => session?.setStreamPrefs(p)}
            onDisplay={(c) => void session?.setDisplay(c)}
            displayBusy={snap.displayBusy}
            displayError={snap.displayError}
            onClose={() => closePanel("stream")}
          />
        )}
        {!filesOpen && live && (
          <TransferPill transfers={transfers} onOpen={() => openPanel("files")} />
        )}
        {dragging && <DropOverlay target={filesOpen ? filesFolder : null} folders={fileOps} />}
        {conflicts.dialog}
        {hint && (
          <div
            role="status"
            className="absolute bottom-16 left-1/2 z-30 -translate-x-1/2 rounded-lg bg-[#f9ab00] px-4 py-2 text-sm text-black shadow"
          >
            {hint}
          </div>
        )}

        {snap?.endDetail && live && (
          <div className="absolute bottom-4 left-1/2 z-20 -translate-x-1/2 rounded-lg bg-[#f9ab00] px-4 py-2 text-sm text-black shadow">
            {snap.endDetail}
          </div>
        )}
        <SessionOverlays
          snap={snap}
          deviceId={session.deviceId}
          name={name}
          forceRelay={forceRelay}
          onReconnect={reconnect}
          onClose={onClose}
          onBack={onBack}
          onTrust={onTrust}
        />
      </div>
    </div>
  );
}

function ToolButton({
  label,
  icon,
  onClick,
  onPointerDown,
  active,
  disabled,
  text,
}: {
  label: string;
  icon: Parameters<typeof Icon>[0]["name"];
  onClick: () => void;
  onPointerDown?: (e: ReactPointerEvent<HTMLButtonElement>) => void;
  active?: boolean;
  disabled?: boolean;
  text?: string;
}) {
  return (
    <button
      className={`btn btn-sm text-white/90 hover:bg-white/10 max-sm:px-2 ${active ? "bg-white/15" : ""}`}
      aria-label={label}
      aria-pressed={active}
      title={label}
      onClick={onClick}
      onPointerDown={onPointerDown}
      disabled={disabled}
    >
      <Icon name={icon} size={18} />
      {text && <span className="hidden sm:inline">{text}</span>}
    </button>
  );
}
