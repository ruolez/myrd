import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type {
  DragEvent as ReactDragEvent,
  PointerEvent as ReactPointerEvent,
  ReactNode,
  RefObject,
} from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { api } from "../api/client";
import { useUser } from "../app/auth";
import { Icon } from "../components/icons";
import { Badge, Spinner } from "../components/ui";
import { keyFingerprint } from "../lib/devicePins";
import { displayRect } from "../lib/geometry";
import { encodeText } from "../lib/input";
import { streamView } from "../lib/stream";
import { CursorLayer } from "../session/CursorLayer";
import { DropOverlay } from "../session/files/DropOverlay";
import { FilesPanel } from "../session/files/FilesPanel";
import { useTransfers } from "../session/files/TransferList";
import { TransferPill } from "../session/files/TransferPill";
import { isFinal } from "../session/FileChannel";
import { Hud } from "../session/Hud";
import { END_REASONS, RemoteSession } from "../session/RemoteSession";
import type { Snapshot } from "../session/RemoteSession";
import { StreamButton, StreamMenu } from "../session/StreamMenu";
import { useRemoteInput } from "../session/useRemoteInput";

type KeyboardLock = { lock?: (codes?: string[]) => Promise<void>; unlock?: () => void };
const keyboardApi = () => (navigator as Navigator & { keyboard?: KeyboardLock }).keyboard;

const HUD_KEY = "remotectl-hud";

function readHudPref() {
  try {
    return localStorage.getItem(HUD_KEY) === "1";
  } catch {
    return false;
  }
}

const FOLDER_HINT = "Folders can't be uploaded. Drop files, or zip the folder first.";

/** Splits dropped items into files and folders (folders are not supported). */
function droppedFiles(dt: DataTransfer): { files: File[]; folders: number } {
  const files: File[] = [];
  let folders = 0;
  for (const item of Array.from(dt.items)) {
    if (item.kind !== "file") continue;
    if (item.webkitGetAsEntry?.()?.isDirectory) {
      folders++;
      continue;
    }
    const f = item.getAsFile();
    if (f) files.push(f);
  }
  return { files, folders };
}

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

const phaseText: Partial<Record<Snapshot["phase"], string>> = {
  preparing: "Preparing this browser's signing key…",
  creating: "Requesting a session…",
  awaiting_approval: "Waiting for approval on the device…",
  connecting: "Connecting to the device…",
};

export function SessionView() {
  const { deviceId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const user = useUser();
  const navigate = useNavigate();
  const forceRelay = params.get("relay") === "1";

  const [session, setSession] = useState<RemoteSession | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [trustKey, setTrustKey] = useState<string | undefined>(undefined);
  const [hud, setHud] = useState(readHudPref);
  const [fullscreen, setFullscreen] = useState(false);
  const [relative, setRelative] = useState(false);
  const [deviceName, setDeviceName] = useState("");
  const [streamOpen, setStreamOpen] = useState(false);
  const [filesOpen, setFilesOpen] = useState(false);
  const [filesFolder, setFilesFolder] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);
  const [hint, setHint] = useState<string | null>(null);
  const [pill, setPill] = useState(false);
  const dragDepth = useRef(0);
  const pillTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const relockOnClose = useRef(false);

  const stageRef = useRef<HTMLDivElement>(null);
  const surfaceRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const kbRef = useRef<HTMLTextAreaElement>(null);
  const sessionRef = useRef<RemoteSession | null>(null);

  useEffect(() => {
    let cancelled = false;
    api.devices
      .get(deviceId)
      .then((r) => !cancelled && setDeviceName(r.device.name))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [deviceId]);

  useEffect(() => {
    const s = new RemoteSession({
      deviceId,
      deviceName: "",
      userId: user.id,
      forceRelay,
      trustNewDeviceKey: trustKey,
    });
    sessionRef.current = s;
    setSession(s);
    void s.start();
    const onUnload = () => s.end("user_hangup");
    window.addEventListener("pagehide", onUnload);
    return () => {
      window.removeEventListener("pagehide", onUnload);
      s.end("user_hangup");
      if (sessionRef.current === s) sessionRef.current = null;
    };
  }, [deviceId, user.id, forceRelay, trustKey, attempt]);

  const snap = useSyncExternalStore(
    useCallback((fn: () => void) => session?.subscribe(fn) ?? (() => undefined), [session]),
    () => session?.snapshot ?? null,
  );

  // Attach the stream without re-rendering frames through a canvas.
  useEffect(() => {
    const v = videoRef.current;
    if (!v) return;
    if (v.srcObject !== (snap?.stream ?? null)) v.srcObject = snap?.stream ?? null;
    if (snap?.stream) void v.play().catch(() => undefined);
  }, [snap?.stream]);

  const live = snap?.phase === "connected" || snap?.phase === "reconnecting";
  const files = snap?.filesState === "ready" ? snap.files : null;

  // The stream is sized to the stage in device pixels and drawn 1:1 (fit),
  // or kept native and scrolled (actual).
  const stage = useStageSize(stageRef);
  const videoSize = useVideoSize(videoRef);
  const scale = snap?.streamPrefs.scale ?? "fit";
  const rect = displayRect(stage.w, stage.h, videoSize.w, videoSize.h, stage.dpr, scale);
  useEffect(() => {
    session?.setView(scale === "fit" ? streamView(stage.w, stage.h, stage.dpr) : null);
  }, [session, scale, stage.w, stage.h, stage.dpr]);
  const transfers = useTransfers(snap?.files ?? null);
  const transfersActive = transfers.some((t) => !isFinal(t));

  // In-stage panels release pointer lock; it comes back on close if it was on.
  const openPanel = useCallback((which: "stream" | "files") => {
    relockOnClose.current ||= document.pointerLockElement !== null;
    setRelative(false);
    if (which === "stream") setStreamOpen(true);
    else setFilesOpen(true);
  }, []);
  const closePanel = (which: "stream" | "files") => {
    const other = which === "stream" ? filesOpen : streamOpen;
    if (which === "stream") setStreamOpen(false);
    else setFilesOpen(false);
    if (!other) {
      if (relockOnClose.current) setRelative(true);
      relockOnClose.current = false;
      kbRef.current?.focus({ preventScroll: true });
    }
  };
  const onHotkey = useCallback(() => openPanel("stream"), [openPanel]);

  useRemoteInput(
    { session: sessionRef, surface: surfaceRef, video: videoRef, keyboard: kbRef },
    live,
    snap?.activeMonitor ?? 0,
    onHotkey,
  );

  useEffect(() => {
    if (!live) {
      setStreamOpen(false);
      setFilesOpen(false);
    }
  }, [live]);

  // A drop that misses the stage must not navigate away (and end the session).
  useEffect(() => {
    const prevent = (e: DragEvent) => {
      if (hasFiles(e)) e.preventDefault();
    };
    window.addEventListener("dragover", prevent);
    window.addEventListener("drop", prevent);
    return () => {
      window.removeEventListener("dragover", prevent);
      window.removeEventListener("drop", prevent);
    };
  }, []);

  useEffect(() => {
    if (!transfersActive) return;
    const guard = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", guard);
    return () => window.removeEventListener("beforeunload", guard);
  }, [transfersActive]);

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
    if (fullscreen) revealPill(3000);
    else setPill(false);
  }, [fullscreen, revealPill]);
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
    const dropped = droppedFiles(e.dataTransfer);
    for (const f of dropped.files) files.upload(f, filesOpen ? filesFolder : null);
    if (dropped.folders > 0) setHint(FOLDER_HINT);
  };

  // Clipboard: push local clipboard to the remote when the session regains focus.
  const lastClip = useRef<string | null>(null);
  useEffect(() => {
    if (!live) return;
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
  }, [live]);

  // Fullscreen ⇒ Keyboard Lock (so Cmd+W, Alt+Tab, Esc reach the remote). Pointer Lock
  // stays opt-in: under it the cursor can only be drawn where the device reports
  // it, a network round trip behind the hand, while without it the browser's own
  // cursor follows the mouse with no delay.
  useEffect(() => {
    const onFs = () => {
      const fs = document.fullscreenElement === stageRef.current && !!stageRef.current;
      setFullscreen(fs);
      if (fs) {
        void keyboardApi()
          ?.lock?.()
          .catch(() => undefined);
      } else {
        keyboardApi()?.unlock?.();
        setRelative(false);
      }
    };
    const onLock = () => setRelative(document.pointerLockElement === surfaceRef.current);
    document.addEventListener("fullscreenchange", onFs);
    document.addEventListener("pointerlockchange", onLock);
    return () => {
      document.removeEventListener("fullscreenchange", onFs);
      document.removeEventListener("pointerlockchange", onLock);
    };
  }, []);

  useEffect(() => {
    const el = surfaceRef.current;
    if (!el || !live) return;
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
  }, [relative, live]);

  const toggleFullscreen = () => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void stageRef.current?.requestFullscreen({ navigationUI: "hide" }).catch(() => undefined);
    kbRef.current?.focus();
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
    kbRef.current?.focus();
  };

  const reconnect = (relay = forceRelay) => {
    if (relay !== forceRelay) setParams(relay ? { relay: "1" } : {}, { replace: true });
    else setAttempt((a) => a + 1);
  };

  const phase = snap?.phase ?? "preparing";
  const name = snap?.deviceName || deviceName || "Device";
  const monitors = snap?.monitors ?? [];
  const monitor = monitors[snap?.activeMonitor ?? 0];
  // Cursor shapes are in native pixels: prefer the agent's native size for this stream.
  const nativeWidth = snap?.streamState?.native_width || monitor?.width || 0;
  // The agent's relay address can reach the browser as a peer-reflexive
  // candidate, so trust the agent when it reports a relayed path.
  const path = snap?.streamState?.relay ? "relay" : snap?.stats.path;
  const filesLabel =
    snap?.filesState === "unsupported" ? "Files (not supported by this device's agent)" : "Files";

  return (
    <div className="flex h-screen flex-col bg-black text-white">
      <header className="flex flex-wrap items-center gap-2 border-b border-white/10 bg-[#1e1f20] px-3 py-2 text-sm">
        <Link
          to="/devices"
          className="btn btn-sm text-white/80 hover:bg-white/10"
          aria-label="Back to devices"
        >
          <Icon name="back" size={18} />
        </Link>
        <span className="truncate font-medium">{name}</span>
        {phase === "connected" && (
          <Badge tone={path === "relay" ? "relay" : "success"} dot>
            {path ? `Live · ${path}` : "Live"}
          </Badge>
        )}
        {phase === "reconnecting" && (
          <Badge tone="warning" dot>
            Reconnecting
          </Badge>
        )}
        {forceRelay && <Badge tone="relay">relay forced</Badge>}
        <div className="ml-auto flex flex-wrap items-center gap-1">
          {monitors.length > 1 && (
            <label className="flex items-center gap-1">
              <Icon name="monitor" size={16} className="text-white/70" />
              <span className="sr-only">Monitor</span>
              <select
                className="rounded bg-white/10 px-2 py-1 text-xs"
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
          <ToolButton
            label={filesLabel}
            icon="folder"
            active={filesOpen}
            onClick={() => (filesOpen ? closePanel("files") : openPanel("files"))}
            disabled={!live || !files}
            text="Files"
          />
          <ToolButton
            label="Type clipboard as keystrokes"
            icon="paste"
            onClick={() => void typeClipboard()}
            disabled={!live}
          />
          <ToolButton
            label={relative ? "Absolute mouse" : "Relative mouse (pointer lock)"}
            icon="lock"
            active={relative}
            onClick={() => setRelative((r) => !r)}
            disabled={!live}
          />
          <ToolButton
            label="Request keyframe"
            icon="refresh"
            onClick={() => session?.requestKeyframe()}
            disabled={!live}
          />
          <ToolButton
            label="Latency HUD"
            icon="speed"
            active={hud}
            onClick={toggleHud}
            text="HUD"
          />
          <ToolButton
            label={fullscreen ? "Exit fullscreen" : "Fullscreen"}
            icon={fullscreen ? "fullscreenExit" : "fullscreen"}
            onClick={toggleFullscreen}
          />
          <button
            className="btn btn-sm ml-1 bg-[#d93025] text-white hover:brightness-110"
            onClick={() => {
              session?.end("user_hangup");
            }}
            disabled={phase === "ended" || phase === "pin_mismatch"}
          >
            <Icon name="callEnd" size={16} />
            End
          </button>
        </div>
      </header>

      <div
        ref={stageRef}
        className="session-stage relative min-h-0 flex-1 overflow-hidden"
        onPointerMove={onStagePointerMove}
        onDragEnter={onDragEnter}
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
      >
        <div
          className={`absolute inset-0 ${scale === "actual" ? "overflow-auto" : "overflow-hidden"}`}
        >
          <div
            className="relative"
            style={{
              width: Math.max(stage.w, rect.left + rect.width),
              height: Math.max(stage.h, rect.top + rect.height),
            }}
          >
            <video
              ref={videoRef}
              className="absolute"
              style={{ left: rect.left, top: rect.top, width: rect.width, height: rect.height }}
              playsInline
              autoPlay
              muted
              disablePictureInPicture
            />
            <CursorLayer
              session={session}
              video={videoRef}
              surface={surfaceRef}
              nativeWidth={nativeWidth}
              active={live}
            />
            <div
              ref={surfaceRef}
              className="absolute inset-0 cursor-default touch-none select-none"
              onClick={() => kbRef.current?.focus({ preventScroll: true })}
            >
              <textarea
                ref={kbRef}
                aria-label="Remote keyboard input"
                className="absolute top-0 left-0 h-px w-px resize-none overflow-hidden border-0 p-0 opacity-0"
                autoCapitalize="off"
                autoCorrect="off"
                autoComplete="off"
                spellCheck={false}
                tabIndex={0}
              />
            </div>
          </div>
        </div>
        {hud && live && snap && (
          <Hud
            stats={snap.stats}
            encoder={snap.encoder}
            video={videoRef}
            stream={snap.streamState}
          />
        )}

        {fullscreen && pill && live && (
          <div
            className="absolute top-3 left-1/2 z-30 flex -translate-x-1/2 items-center gap-1 rounded-full bg-black/75 px-2 py-1 text-sm shadow-lg"
            onPointerEnter={() => pillTimer.current && clearTimeout(pillTimer.current)}
            onPointerLeave={() => revealPill(1500)}
          >
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
            <ToolButton
              label="Exit fullscreen"
              icon="fullscreenExit"
              text="Exit fullscreen"
              onClick={toggleFullscreen}
            />
          </div>
        )}

        {filesOpen && files && (
          <FilesPanel files={files} onClose={() => closePanel("files")} onFolder={setFilesFolder} />
        )}
        {streamOpen && snap && live && (
          <StreamMenu
            className={`top-3 ${filesOpen ? "right-3 sm:right-[25rem]" : "right-3"}`}
            prefs={snap.streamPrefs}
            state={snap.streamState}
            feature={snap.streamFeature}
            canHideWallpaper={snap.features?.includes("wallpaper") ?? false}
            maxFps={snap.maxFps}
            monitor={monitor}
            onChange={(p) => session?.setStreamPrefs(p)}
            onClose={() => closePanel("stream")}
          />
        )}
        {!filesOpen && live && (
          <TransferPill transfers={transfers} onOpen={() => openPanel("files")} />
        )}
        {dragging && <DropOverlay target={filesOpen ? filesFolder : null} />}
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
        {phase === "reconnecting" && (
          <div className="absolute top-4 left-1/2 z-20 flex -translate-x-1/2 items-center gap-2 rounded-full bg-black/70 px-4 py-2 text-sm">
            <Spinner size={16} />
            Connection interrupted — restarting ICE…
          </div>
        )}

        {phaseText[phase] && (
          <Overlay>
            <Spinner size={36} />
            <div className="text-lg">{phaseText[phase]}</div>
            {phase === "awaiting_approval" && (
              <p className="max-w-sm text-center text-sm text-white/70">
                Someone at <b>{name}</b> must allow this browser on the device's own screen. New
                browsers always need approval there.
              </p>
            )}
            <button
              className="btn-outline mt-2 border-white/30 text-white hover:bg-white/10"
              onClick={() => session?.end("user_hangup")}
            >
              Cancel
            </button>
          </Overlay>
        )}

        {phase === "ended" && snap && (
          <Overlay>
            <Icon
              name={snap.endReason === "user_hangup" ? "check" : "warning"}
              size={40}
              className="text-white/80"
            />
            <div className="text-lg">
              {snap.endReason === "user_hangup" ? "Session ended" : "Session closed"}
            </div>
            <p className="max-w-md text-center text-sm text-white/70">
              {END_REASONS[snap.endReason ?? ""] ?? snap.endReason}
              {snap.endDetail && snap.endDetail !== END_REASONS[snap.endReason ?? ""] && (
                <>
                  <br />
                  <span className="text-white/50">{snap.endDetail}</span>
                </>
              )}
            </p>
            <div className="mt-2 flex flex-wrap justify-center gap-2">
              {snap.canReconnect && (
                <button className="btn-primary" onClick={() => reconnect()}>
                  <Icon name="refresh" size={18} />
                  Reconnect
                </button>
              )}
              {snap.canReconnect && !forceRelay && (
                <button
                  className="btn-outline border-white/30 text-white hover:bg-white/10"
                  onClick={() => reconnect(true)}
                >
                  Reconnect via relay
                </button>
              )}
              {forceRelay && (
                <button
                  className="btn-outline border-white/30 text-white hover:bg-white/10"
                  onClick={() => reconnect(false)}
                >
                  Reconnect direct
                </button>
              )}
              <button
                className="btn-outline border-white/30 text-white hover:bg-white/10"
                onClick={() => navigate("/devices")}
              >
                Back to devices
              </button>
            </div>
          </Overlay>
        )}

        {phase === "pin_mismatch" && snap?.pinMismatch && (
          <PinMismatch
            name={name}
            pinned={snap.pinMismatch.pinned}
            offered={snap.pinMismatch.offered}
            onCancel={() => navigate("/devices")}
            onTrust={() => setTrustKey(snap.pinMismatch?.offered)}
          />
        )}
      </div>
    </div>
  );
}

function ToolButton({
  label,
  icon,
  onClick,
  active,
  disabled,
  text,
}: {
  label: string;
  icon: Parameters<typeof Icon>[0]["name"];
  onClick: () => void;
  active?: boolean;
  disabled?: boolean;
  text?: string;
}) {
  return (
    <button
      className={`btn btn-sm text-white/90 hover:bg-white/10 ${active ? "bg-white/15" : ""}`}
      aria-label={label}
      aria-pressed={active}
      title={label}
      onClick={onClick}
      disabled={disabled}
    >
      <Icon name={icon} size={18} />
      {text && <span className="hidden sm:inline">{text}</span>}
    </button>
  );
}

function Overlay({ children }: { children: ReactNode }) {
  return (
    <div className="absolute inset-0 z-30 flex flex-col items-center justify-center gap-3 bg-black/80 p-6">
      {children}
    </div>
  );
}

function PinMismatch({
  name,
  pinned,
  offered,
  onCancel,
  onTrust,
}: {
  name: string;
  pinned: string;
  offered: string;
  onCancel: () => void;
  onTrust: () => void;
}) {
  const [fps, setFps] = useState<[string, string]>(["…", "…"]);
  const [ack, setAck] = useState(false);
  useEffect(() => {
    void Promise.all([keyFingerprint(pinned), keyFingerprint(offered)]).then(([a, b]) =>
      setFps([a, b]),
    );
  }, [pinned, offered]);
  return (
    <div className="absolute inset-0 z-40 flex items-center justify-center bg-[#3c1f1d]/95 p-6">
      <div className="max-w-lg space-y-4 rounded-2xl border border-[#f28b82]/40 bg-black/60 p-6">
        <div className="flex items-center gap-3 text-[#f28b82]">
          <Icon name="warning" size={32} />
          <h2 className="text-xl font-medium">Device identity changed</h2>
        </div>
        <p className="text-sm text-white/80">
          <b>{name}</b> presented a different identity key than the one this browser pinned on your
          first session. This happens if the agent was reinstalled — or if someone is impersonating
          the device. The session was refused.
        </p>
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 font-mono text-xs">
          <dt className="text-white/50">Pinned</dt>
          <dd>{fps[0]}</dd>
          <dt className="text-white/50">Offered</dt>
          <dd className="text-[#f28b82]">{fps[1]}</dd>
        </dl>
        <label className="flex items-start gap-2 text-sm text-white/80">
          <input
            type="checkbox"
            className="mt-1"
            checked={ack}
            onChange={(e) => setAck(e.target.checked)}
          />
          I reinstalled or re-enrolled this device myself and want to trust the new key.
        </label>
        <div className="flex justify-end gap-2">
          <button className="btn-primary" onClick={onCancel}>
            Keep me safe
          </button>
          <button
            className="btn-outline border-[#f28b82]/50 text-[#f28b82] hover:bg-white/5"
            disabled={!ack}
            onClick={onTrust}
          >
            Trust new key
          </button>
        </div>
      </div>
    </div>
  );
}
