// One controller session: signaling → RTCPeerConnection → data channels.
// Media flows P2P; the server only forwards signed SDP blobs (PROTOCOL §3–5).

import { api } from "../api/client";
import type { ICEServer, Monitor, UserId } from "../api/types";
import { ensureClientKey, signFingerprint } from "../lib/clientKey";
import { checkDevicePin, pinDevice, verifyDeviceSignature } from "../lib/devicePins";
import { codecName, preferredCodecs } from "../lib/codecs";
import { sdpFingerprint } from "../lib/fingerprint";
import { filesAvailability } from "../lib/files";
import type { FilesAvailability } from "../lib/files";
import {
  ClipboardAssembler,
  ControlOp,
  decodeCursor,
  encodeClipboard,
  encodeControl,
  encodeMouseMove,
  encodeMouseMoveSeq,
  Mime,
} from "../lib/input";
import type { CursorMessage } from "../lib/input";
import type { SentMove } from "../lib/cursor";
import { signaling } from "../lib/signaling";
import { sealCredential, verifyUnlockKey } from "../lib/unlockCrypto";
import type { SealedCredential, UnlockKey } from "../lib/unlockCrypto";
import type { SignalMessage } from "../lib/signaling";
import {
  encodeStreamRequest,
  legacyTier,
  parseStreamPrefs,
  parseStreamState,
  serializeStreamPrefs,
  streamPrefsKey,
} from "../lib/stream";
import type { StreamPrefs, StreamRequestId, StreamState, StreamView } from "../lib/stream";
import { FileChannel } from "./FileChannel";

export type Phase =
  | "preparing"
  | "creating"
  | "awaiting_approval"
  | "connecting"
  | "connected"
  | "reconnecting"
  | "pin_mismatch"
  | "ended";

export type HudStats = {
  rttMs: number | null;
  fps: number | null;
  bitrateKbps: number | null;
  packetLossPct: number | null;
  jitterMs: number | null;
  decodeMs: number | null;
  frameWidth: number | null;
  frameHeight: number | null;
  path: string | null;
  localCandidate: string | null;
  remoteCandidate: string | null;
  agentFps: number | null;
  agentBitrateKbps: number | null;
  keyframes: number | null;
  targetFps: number | null;
  targetKbps: number | null;
  bweKbps: number | null;
  agentLossPct: number | null;
  encodeWidth: number | null;
  encodeHeight: number | null;
  /** Quantizer of the latest static-screen refinement pass; null while the screen changes. */
  refineQp: number | null;
  /** Refinement passes since new content (repeat passes where the encoder has no quantizer cap). */
  refinePass: number | null;
  /** Repair overhead the agent adds (FlexFEC), percent; 0 = off. */
  fecPct: number | null;
  /** Repair packets this browser received so far. */
  fecReceived: number | null;
  /** What the browser decodes, e.g. "H.264" or "VP9 4:4:4". */
  codec: string | null;
};

export type Snapshot = {
  phase: Phase;
  sessionId: string | null;
  deviceName: string;
  endReason: string | null;
  endDetail: string | null;
  canReconnect: boolean;
  stream: MediaStream | null;
  monitors: Monitor[];
  activeMonitor: number;
  encoder: string | null;
  agentOs: string | null;
  agentVersion: string | null;
  permissions: { screen: boolean; input: boolean } | null;
  /** The device shows its unlock screen (hello `locked`, then `screen`). */
  locked: boolean;
  /** The device's unlock key, signed by its pinned key: it can type a saved password (feature `unlock`). */
  unlockKey: UnlockKey | null;
  /** Not the session in view: streams at background quality (setBackground). */
  background: boolean;
  streamPrefs: StreamPrefs;
  streamState: StreamState | null;
  /** null until `hello`; false for agents without the `stream` feature. */
  streamFeature: boolean | null;
  maxFps: number | null;
  features: string[] | null;
  files: FileChannel | null;
  filesState: FilesAvailability;
  forceRelay: boolean;
  pinMismatch: { pinned: string; offered: string } | null;
  stats: HudStats;
};

const emptyStats: HudStats = {
  rttMs: null,
  fps: null,
  bitrateKbps: null,
  packetLossPct: null,
  jitterMs: null,
  decodeMs: null,
  frameWidth: null,
  frameHeight: null,
  path: null,
  localCandidate: null,
  remoteCandidate: null,
  agentFps: null,
  agentBitrateKbps: null,
  keyframes: null,
  targetFps: null,
  targetKbps: null,
  bweKbps: null,
  agentLossPct: null,
  encodeWidth: null,
  encodeHeight: null,
  refineQp: null,
  refinePass: null,
  fecPct: null,
  fecReceived: null,
  codec: null,
};

const STREAM_DEBOUNCE_MS = 200;
// Typing a password takes the device a few seconds (Windows raises its sign-in box first).
const UNLOCK_TIMEOUT_MS = 30_000;

export type UnlockResult = { ok: boolean; code: string | null };
// Stats in the background feed only thumbnails and the HUD of a hidden pane.
const BACKGROUND_STATS_EVERY = 5;
// Cursor shapes kept to replay to a newly mounted cursor layer (the agent
// sends each shape once, then only refers to it by id).
const SHAPE_CACHE = 32;
// Each new view size restarts capture on the device (a keyframe): let a
// window drag finish first.
const VIEW_DEBOUNCE_MS = 400;

function loadStreamPrefs(deviceId: string): StreamPrefs {
  try {
    return parseStreamPrefs(localStorage.getItem(streamPrefsKey(deviceId)));
  } catch {
    return parseStreamPrefs(null);
  }
}

function saveStreamPrefs(deviceId: string, prefs: StreamPrefs) {
  try {
    localStorage.setItem(streamPrefsKey(deviceId), serializeStreamPrefs(prefs));
  } catch {
    // storage unavailable
  }
}

const num = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) ? v : null);

export const END_REASONS: Record<string, string> = {
  user_hangup: "You ended the session.",
  agent_kill_switch: "The session was ended from the device (kill switch).",
  not_approved: "The request was not approved on the device.",
  timeout: "The session timed out.",
  ice_failed: "The connection to the device was lost.",
  error: "The session ended because of an error.",
  peer_disconnected: "The device disconnected.",
  admin_terminated: "An administrator ended this session.",
  device_offline: "The device is offline.",
  server_restart: "The server restarted while the session was being set up.",
  bad_signature:
    "The connection could not be verified (signature mismatch). The session was refused for your safety.",
  permission_missing: "The device has not granted screen recording or input permission.",
  input_abuse: "The device ended the session after receiving invalid input.",
  device_busy: "The device is already in another session.",
  device_disabled: "This device is disabled.",
  client_key_invalid: "This browser's key was revoked. Reload the page to register a new one.",
  rate_limited: "Too many requests. Wait a moment and try again.",
  not_found: "Device not found.",
  forbidden: "You do not have access to this device.",
  capture_failed: "The device could not capture its screen.",
  encoder_failed: "The device's video encoder failed.",
  agent_update: "The device is installing an update. Reconnect in a minute.",
};

type Options = {
  deviceId: string;
  deviceName: string;
  userId: UserId;
  forceRelay: boolean;
  trustNewDeviceKey?: string; // re-trust explicitly accepted by the user
};

type Listener = (s: Snapshot) => void;

export class RemoteSession {
  private snap: Snapshot;
  private listeners = new Set<Listener>();
  private cursorListeners = new Set<(m: CursorMessage) => void>();
  private pc: RTCPeerConnection | null = null;
  private input: RTCDataChannel | null = null;
  private move: RTCDataChannel | null = null;
  private moveSeq = 0;
  private sentMove: SentMove | null = null;
  private control: RTCDataChannel | null = null;
  private file: RTCDataChannel | null = null;
  private fileChannel: FileChannel | null = null;
  private streamReqId = 0;
  private streamTimer: ReturnType<typeof setTimeout> | null = null;
  private view: StreamView | null = null;
  private offs: (() => void)[] = [];
  private timers: ReturnType<typeof setInterval>[] = [];
  private pendingCandidates: RTCIceCandidateInit[] = [];
  private clientKey: Awaited<ReturnType<typeof ensureClientKey>> | null = null;
  private devicePublicKey = "";
  private pinIsNew = false;
  private disconnectedAt: number | null = null;
  private restartedAt = 0;
  private lastInbound: { ts: number; bytes: number; lost: number; recv: number } | null = null;
  private pingId = 0;
  private pings = new Map<number, number>();
  private unlockReqId = 0;
  private unlocks = new Map<number, (r: UnlockResult) => void>();
  private clipboard = new ClipboardAssembler();
  private clipXfer = 1;
  private ended = false;
  private background = false;
  private pendingClip: string | null = null;
  private ticks = 0;
  private shapes = new Map<number, CursorMessage>();
  private shapeId: number | null = null;
  private lastPos: CursorMessage | null = null;

  constructor(private readonly opts: Options) {
    this.snap = {
      phase: "preparing",
      sessionId: null,
      deviceName: opts.deviceName,
      endReason: null,
      endDetail: null,
      canReconnect: false,
      stream: null,
      monitors: [],
      activeMonitor: 0,
      encoder: null,
      agentOs: null,
      agentVersion: null,
      permissions: null,
      locked: false,
      unlockKey: null,
      background: false,
      streamPrefs: loadStreamPrefs(opts.deviceId),
      streamState: null,
      streamFeature: null,
      maxFps: null,
      features: null,
      files: null,
      filesState: "pending",
      forceRelay: opts.forceRelay,
      pinMismatch: null,
      stats: emptyStats,
    };
  }

  get snapshot() {
    return this.snap;
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  get deviceId() {
    return this.opts.deviceId;
  }

  /** Replays the current cursor shape and position, then every new message. */
  onCursor(fn: (m: CursorMessage) => void): () => void {
    this.cursorListeners.add(fn);
    for (const m of this.shapes.values()) fn(m);
    const current = this.shapeId === null ? undefined : this.shapes.get(this.shapeId);
    if (current) fn(current);
    if (this.lastPos) fn(this.lastPos);
    return () => this.cursorListeners.delete(fn);
  }

  /**
   * A session that is open but not in view streams at background quality,
   * as when its tab is hidden, and holds remote clipboard text until it is
   * in view again (so a background session cannot overwrite the clipboard).
   */
  setBackground(background: boolean) {
    if (background === this.background) return;
    this.background = background;
    this.update({ background });
    this.applyStream();
    this.flushClipboard();
  }

  private flushClipboard() {
    if (this.background || document.hidden || this.pendingClip === null) return;
    const text = this.pendingClip;
    this.pendingClip = null;
    void navigator.clipboard?.writeText(text).catch(() => undefined);
  }

  private remember(msg: CursorMessage) {
    if (msg.type === "pos") {
      this.lastPos = msg;
      return;
    }
    this.shapeId = msg.id;
    if (msg.png.length === 0) return;
    this.shapes.delete(msg.id);
    this.shapes.set(msg.id, msg);
    if (this.shapes.size > SHAPE_CACHE) {
      const oldest = this.shapes.keys().next().value;
      if (oldest !== undefined) this.shapes.delete(oldest);
    }
  }

  private update(patch: Partial<Snapshot>) {
    this.snap = { ...this.snap, ...patch };
    for (const l of this.listeners) l(this.snap);
  }

  async start(): Promise<void> {
    try {
      this.clientKey = await ensureClientKey(this.opts.userId);
      this.update({ phase: "creating" });
      const created = await signaling.createSession(
        this.opts.deviceId,
        this.clientKey.id,
        this.opts.forceRelay,
      );
      if (this.ended) {
        signaling.send({
          type: "session.end",
          session_id: created.session_id,
          reason: "user_hangup",
        });
        return;
      }
      const sid = created.session_id;
      this.update({ sessionId: sid, deviceName: created.device.name || this.opts.deviceName });
      this.listen(sid);

      this.devicePublicKey = created.device.public_key;
      const pin = await checkDevicePin(this.opts.deviceId, this.devicePublicKey);
      if (pin.status === "mismatch" && this.opts.trustNewDeviceKey !== this.devicePublicKey) {
        signaling.send({ type: "session.end", session_id: sid, reason: "bad_signature" });
        this.cleanup();
        this.update({
          phase: "pin_mismatch",
          pinMismatch: { pinned: pin.pinned, offered: this.devicePublicKey },
          endReason: "bad_signature",
          endDetail:
            "This device presented a different identity key than the one pinned in this browser.",
        });
        this.ended = true;
        return;
      }
      this.pinIsNew = pin.status !== "match";

      await this.setupPeer(sid, created.ice_servers);
      await this.sendOffer(false);
      this.update({ phase: "connecting" });
    } catch (e) {
      const code = (e as { code?: string }).code ?? "error";
      this.finish(code, e instanceof Error ? e.message : String(e), code !== "client_key_invalid");
    }
  }

  private listen(sid: string) {
    const mine = (m: SignalMessage) => m.session_id === sid;
    this.offs.push(
      signaling.on("session.status", (m) => {
        if (!mine(m)) return;
        if (m.status === "awaiting_approval") this.update({ phase: "awaiting_approval" });
        else if (m.status === "approved") this.update({ phase: "connecting" });
      }),
      signaling.on("session.answer", (m) => {
        if (mine(m)) void this.onAnswer(m.sdp as string, m.fp_sig as string);
      }),
      signaling.on("ice.candidate", (m) => {
        if (mine(m)) void this.onRemoteCandidate(m.candidate as RTCIceCandidateInit | null);
      }),
      signaling.on("session.state", (m) => {
        if (mine(m) && typeof m.path === "string") {
          this.update({ stats: { ...this.snap.stats, path: m.path } });
        }
      }),
      signaling.on("session.end", (m) => {
        if (mine(m)) this.finish(String(m.reason ?? "error"), null, true, false);
      }),
      signaling.on("error", (m) => {
        if (mine(m)) this.finish(String(m.code), String(m.message ?? ""), true);
      }),
    );
  }

  private async setupPeer(sid: string, iceServers: ICEServer[]) {
    const pc = new RTCPeerConnection({
      iceServers,
      iceTransportPolicy: this.opts.forceRelay ? "relay" : "all",
      bundlePolicy: "max-bundle",
    });
    this.pc = pc;

    const tr = pc.addTransceiver("video", { direction: "recvonly" });
    preferCodecs(tr);

    this.input = pc.createDataChannel("input", { ordered: true });
    // Absolute moves, latest wins: a lost one is superseded, never waited for.
    this.move = pc.createDataChannel("move", { ordered: false, maxRetransmits: 0 });
    const cursor = pc.createDataChannel("cursor", { ordered: false, maxRetransmits: 0 });
    this.control = pc.createDataChannel("control", { ordered: true });
    this.file = pc.createDataChannel("file", { ordered: true });
    for (const ch of [this.input, this.move, cursor, this.control, this.file])
      ch.binaryType = "arraybuffer";
    // Agents without file transfer close this channel; that means "unsupported".
    this.file.addEventListener("open", () => this.refreshFiles());
    this.file.addEventListener("close", () => this.refreshFiles());

    cursor.onmessage = (ev) => {
      if (!(ev.data instanceof ArrayBuffer)) return;
      const msg = decodeCursor(new Uint8Array(ev.data));
      if (!msg) return;
      this.remember(msg);
      for (const l of this.cursorListeners) l(msg);
    };
    this.control.onmessage = (ev) => this.onControl(ev.data);

    pc.ontrack = (ev) => {
      const stream = ev.streams[0] ?? new MediaStream([ev.track]);
      // Keep latency minimal: no jitter-buffer padding where supported.
      const recv = ev.receiver as unknown as {
        playoutDelayHint?: number;
        jitterBufferTarget?: number | null;
      };
      try {
        if ("jitterBufferTarget" in recv) recv.jitterBufferTarget = 0;
        recv.playoutDelayHint = 0;
      } catch {
        // unsupported
      }
      this.update({ stream });
    };
    pc.onicecandidate = (ev) => {
      signaling.send({
        type: "ice.candidate",
        session_id: sid,
        candidate: ev.candidate ? ev.candidate.toJSON() : null,
      });
    };
    pc.onconnectionstatechange = () => this.onConnState();
    pc.oniceconnectionstatechange = () => this.onConnState();

    this.timers.push(setInterval(() => void this.tick(), 1000));
    const onVis = () => {
      this.applyStream();
      this.flushClipboard();
    };
    document.addEventListener("visibilitychange", onVis);
    this.offs.push(() => document.removeEventListener("visibilitychange", onVis));
  }

  private async sendOffer(iceRestart: boolean) {
    const pc = this.pc;
    const sid = this.snap.sessionId;
    if (!pc || !sid || !this.clientKey) return;
    const offer = await pc.createOffer({ iceRestart });
    await pc.setLocalDescription(offer);
    const sdp = pc.localDescription?.sdp ?? offer.sdp ?? "";
    const fp = sdpFingerprint(sdp);
    if (!fp) throw new Error("The browser produced an SDP without a DTLS fingerprint.");
    const fpSig = await signFingerprint(this.clientKey, sid, fp);
    signaling.send({ type: "session.offer", session_id: sid, sdp, fp_sig: fpSig });
  }

  private async onAnswer(sdp: string, fpSig: string) {
    const pc = this.pc;
    const sid = this.snap.sessionId;
    if (!pc || !sid) return;
    const fp = sdpFingerprint(sdp);
    const ok = fp ? await verifyDeviceSignature(this.devicePublicKey, fpSig, sid, fp) : false;
    if (!ok) {
      signaling.send({ type: "session.end", session_id: sid, reason: "bad_signature" });
      this.finish(
        "bad_signature",
        "The device's answer was not signed by its pinned key. Someone may be tampering with the connection.",
        false,
        false,
      );
      return;
    }
    if (this.pinIsNew) {
      await pinDevice(this.opts.deviceId, this.devicePublicKey);
      this.pinIsNew = false;
    }
    try {
      await pc.setRemoteDescription({ type: "answer", sdp });
    } catch (e) {
      this.finish("error", `Invalid answer from device: ${(e as Error).message}`, true);
      return;
    }
    const queued = this.pendingCandidates;
    this.pendingCandidates = [];
    for (const c of queued) await pc.addIceCandidate(c).catch(() => undefined);
    if (this.snap.phase === "awaiting_approval" || this.snap.phase === "creating") {
      this.update({ phase: "connecting" });
    }
  }

  private async onRemoteCandidate(c: RTCIceCandidateInit | null) {
    const pc = this.pc;
    if (!pc) return;
    if (!pc.remoteDescription) {
      if (c) this.pendingCandidates.push(c);
      return;
    }
    await pc.addIceCandidate(c ?? undefined).catch(() => undefined);
  }

  private onConnState() {
    const pc = this.pc;
    if (!pc || this.ended) return;
    const s = pc.connectionState;
    if (s === "connected") {
      this.disconnectedAt = null;
      if (this.snap.phase !== "connected") this.update({ phase: "connected" });
    } else if (s === "disconnected" || s === "failed") {
      this.disconnectedAt ??= Date.now();
      if (this.snap.phase === "connected") this.update({ phase: "reconnecting" });
    }
  }

  private async tick() {
    const pc = this.pc;
    if (!pc || this.ended) return;

    // Resilience: ICE restart after 3 s disconnected, give up after 15 s.
    if (this.disconnectedAt !== null) {
      const down = Date.now() - this.disconnectedAt;
      if (down >= 15_000) {
        this.finish("ice_failed", null, true);
        return;
      }
      if (down >= 3_000 && Date.now() - this.restartedAt > 5_000) {
        this.restartedAt = Date.now();
        void this.sendOffer(true).catch(() => undefined);
      }
    }

    if (this.control?.readyState === "open") {
      const id = ++this.pingId;
      this.pings.set(id, performance.now());
      this.control.send(JSON.stringify({ t: "ping", id, ts: Date.now() }));
      if (this.pings.size > 10) this.pings.delete(Math.min(...this.pings.keys()));
    }

    if (this.background && this.ticks++ % BACKGROUND_STATS_EVERY !== 0) return;
    const stats = await pc.getStats().catch(() => null);
    if (!stats) return;
    const next: HudStats = { ...this.snap.stats };
    let selectedPair: string | undefined;
    const byId = new Map<string, Record<string, unknown>>();
    stats.forEach((r: Record<string, unknown>) => {
      byId.set(r.id as string, r);
      if (r.type === "transport" && r.selectedCandidatePairId) {
        selectedPair = r.selectedCandidatePairId as string;
      }
    });
    stats.forEach((r: Record<string, unknown>) => {
      if (r.type === "inbound-rtp" && r.kind === "video") {
        const now = r.timestamp as number;
        const bytes = (r.bytesReceived as number) ?? 0;
        const lost = (r.packetsLost as number) ?? 0;
        const recv = (r.packetsReceived as number) ?? 0;
        if (this.lastInbound) {
          const dt = (now - this.lastInbound.ts) / 1000;
          if (dt > 0)
            next.bitrateKbps = Math.round(((bytes - this.lastInbound.bytes) * 8) / dt / 1000);
          const dLost = lost - this.lastInbound.lost;
          const dTotal = dLost + (recv - this.lastInbound.recv);
          next.packetLossPct = dTotal > 0 ? Math.max(0, (dLost / dTotal) * 100) : 0;
        }
        this.lastInbound = { ts: now, bytes, lost, recv };
        next.fps = (r.framesPerSecond as number | undefined) ?? next.fps;
        next.jitterMs =
          r.jitter !== undefined ? Math.round((r.jitter as number) * 1000) : next.jitterMs;
        next.frameWidth = (r.frameWidth as number | undefined) ?? next.frameWidth;
        next.frameHeight = (r.frameHeight as number | undefined) ?? next.frameHeight;
        next.fecReceived = (r.fecPacketsReceived as number | undefined) ?? next.fecReceived;
        const codec = byId.get(r.codecId as string);
        if (codec) next.codec = codecName(codec.mimeType as string, codec.sdpFmtpLine as string);
        const decoded = r.framesDecoded as number | undefined;
        const decodeTime = r.totalDecodeTime as number | undefined;
        if (decoded && decodeTime !== undefined) next.decodeMs = (decodeTime / decoded) * 1000;
      }
      if (!selectedPair && r.type === "candidate-pair" && r.nominated && r.state === "succeeded") {
        selectedPair = r.id as string;
      }
    });
    if (selectedPair) {
      const pair = byId.get(selectedPair);
      const local = pair ? byId.get(pair.localCandidateId as string) : undefined;
      const remote = pair ? byId.get(pair.remoteCandidateId as string) : undefined;
      const lt = local?.candidateType as string | undefined;
      const rt = remote?.candidateType as string | undefined;
      if (lt || rt) {
        next.path =
          lt === "relay" || rt === "relay"
            ? "relay"
            : lt === "srflx" || rt === "srflx" || lt === "prflx" || rt === "prflx"
              ? "srflx"
              : "host";
        next.localCandidate = local
          ? `${lt} ${local.address ?? local.ip ?? ""}:${local.port ?? ""}`
          : null;
        next.remoteCandidate = remote
          ? `${rt} ${remote.address ?? remote.ip ?? ""}:${remote.port ?? ""}`
          : null;
      }
    }
    this.update({ stats: next });
  }

  private onControl(data: unknown) {
    if (data instanceof ArrayBuffer) {
      const done = this.clipboard.push(new Uint8Array(data));
      if (done && done.mime === Mime.Text) {
        const text = new TextDecoder().decode(done.bytes);
        if (this.background || document.hidden) this.pendingClip = text;
        else void navigator.clipboard?.writeText(text).catch(() => undefined);
      }
      return;
    }
    if (typeof data !== "string") return;
    let m: Record<string, unknown>;
    try {
      m = JSON.parse(data) as Record<string, unknown>;
    } catch {
      return;
    }
    switch (m.t) {
      case "hello": {
        const features = Array.isArray(m.features)
          ? m.features.filter((f): f is string => typeof f === "string")
          : null;
        this.update({
          monitors: (m.monitors as Monitor[]) ?? [],
          activeMonitor: (m.active as number) ?? 0,
          encoder: (m.encoder as string) ?? null,
          agentOs: (m.os as string) ?? null,
          agentVersion: (m.agent_version as string) ?? null,
          permissions: (m.permissions as Snapshot["permissions"]) ?? null,
          locked: m.locked === true,
          features: features ?? [],
          streamFeature: features?.includes("stream") ?? false,
          maxFps: num(m.max_fps),
        });
        this.applyStream();
        this.refreshFiles();
        if (features?.includes("unlock") && typeof m.unlock_key === "string")
          void this.checkUnlockKey(m.unlock_key, String(m.unlock_key_sig));
        break;
      }
      case "monitor": {
        const active = (m.active as number) ?? 0;
        const w = num(m.width);
        const h = num(m.height);
        this.update({
          activeMonitor: active,
          monitors:
            w && h
              ? this.snap.monitors.map((mon, i) =>
                  i === active ? { ...mon, width: w, height: h } : mon,
                )
              : this.snap.monitors,
        });
        break;
      }
      case "screen":
        this.update({ locked: m.locked === true });
        break;
      case "unlock": {
        const done = this.unlocks.get(m.id as number);
        this.unlocks.delete(m.id as number);
        done?.({ ok: m.ok === true, code: typeof m.code === "string" ? m.code : null });
        break;
      }
      case "stream": {
        const state = parseStreamState(m);
        if (state) this.update({ streamState: state, maxFps: state.max_fps });
        break;
      }
      case "pong": {
        const sent = this.pings.get(m.id as number);
        if (sent !== undefined) {
          this.pings.delete(m.id as number);
          this.update({ stats: { ...this.snap.stats, rttMs: performance.now() - sent } });
        }
        break;
      }
      case "stats":
        this.update({
          encoder: (m.encoder as string) ?? this.snap.encoder,
          stats: {
            ...this.snap.stats,
            agentFps: num(m.fps),
            agentBitrateKbps: num(m.bitrate_kbps),
            keyframes: num(m.keyframes),
            targetFps: num(m.target_fps),
            targetKbps: num(m.target_kbps),
            bweKbps: num(m.bwe_kbps),
            agentLossPct: num(m.loss) === null ? null : (m.loss as number) * 100,
            encodeWidth: num(m.width),
            encodeHeight: num(m.height),
            refineQp: num(m.refine_qp) || null,
            refinePass: num(m.refine_pass) || null,
            fecPct: num(m.fec_pct),
          },
        });
        break;
      case "error":
        this.update({
          endDetail: `${END_REASONS[m.code as string] ?? m.code}${m.detail ? ` (${String(m.detail)})` : ""}`,
        });
        break;
      case "bye":
        this.finish(String(m.reason ?? "peer_disconnected"), null, true, false);
        break;
    }
  }

  // ---------- controller actions ----------

  sendInput(bytes: Uint8Array) {
    if (this.input?.readyState === "open") this.input.send(bytes as Uint8Array<ArrayBuffer>);
  }

  /**
   * Moves the remote pointer. Lossy by default (the `move` channel when the
   * agent has it); reliable moves go in order with clicks and keys, so send
   * one before a button and when the pointer comes to rest.
   */
  sendMove(x: number, y: number, monitor: number, reliable = false) {
    this.sentMove = { x, y, at: performance.now() };
    if (!reliable && this.move?.readyState === "open" && this.snap.features?.includes("move")) {
      this.moveSeq = (this.moveSeq + 1) & 0xffff;
      this.move.send(encodeMouseMoveSeq(this.moveSeq, x, y, monitor) as Uint8Array<ArrayBuffer>);
      return;
    }
    this.sendInput(encodeMouseMove(x, y, monitor));
  }

  /** The last absolute move sent, for telling our cursor echo from a remote move. */
  get lastMove(): SentMove | null {
    return this.sentMove;
  }

  selectMonitor(index: number) {
    this.sendInput(encodeControl(ControlOp.SelectMonitor, index));
    this.update({ activeMonitor: index });
  }

  requestKeyframe() {
    this.sendInput(encodeControl(ControlOp.RequestKeyframe));
  }

  releaseAllKeys() {
    this.sendInput(encodeControl(ControlOp.ReleaseAll));
  }

  /** Saves the per-device preference and tells the agent after a short debounce. */
  setStreamPrefs(prefs: StreamPrefs) {
    this.update({ streamPrefs: prefs });
    saveStreamPrefs(this.opts.deviceId, prefs);
    if (this.streamTimer) clearTimeout(this.streamTimer);
    this.streamTimer = setTimeout(() => {
      this.streamTimer = null;
      this.applyStream();
    }, STREAM_DEBOUNCE_MS);
  }

  /**
   * The stage's size in device pixels (null at actual size): the agent sizes
   * the stream to it. A new size restarts capture, so it waits for a resize
   * to settle.
   */
  setView(view: StreamView | null) {
    if (view?.w === this.view?.w && view?.h === this.view?.h) return;
    this.view = view;
    if (this.streamTimer) clearTimeout(this.streamTimer);
    this.streamTimer = setTimeout(() => {
      this.streamTimer = null;
      this.applyStream();
    }, VIEW_DEBOUNCE_MS);
  }

  /** `stream` on control for agents with the feature, legacy `0x20 op 3` otherwise. */
  private applyStream() {
    const hidden = document.hidden || this.background;
    const prefs = this.snap.streamPrefs;
    if (this.snap.streamFeature === true) {
      if (this.control?.readyState !== "open") return;
      this.streamReqId = (this.streamReqId + 1) >>> 0;
      const id = this.streamReqId as StreamRequestId;
      this.control.send(encodeStreamRequest(prefs, hidden, id, this.view));
    } else if (this.snap.streamFeature === false) {
      this.sendInput(encodeControl(ControlOp.SetQuality, legacyTier(prefs, hidden)));
    }
  }

  private refreshFiles() {
    const dc = this.file;
    const state = filesAvailability(this.snap.features, dc?.readyState ?? "closed");
    if (state === "ready" && dc && !this.fileChannel) this.fileChannel = new FileChannel(dc);
    if (state !== this.snap.filesState || this.fileChannel !== this.snap.files) {
      this.update({ filesState: state, files: this.fileChannel });
    }
  }

  /** Pushes local clipboard text to the remote clipboard. */
  sendClipboardText(text: string) {
    const bytes = new TextEncoder().encode(text);
    if (bytes.length === 0 || bytes.length > 1024 * 1024) return;
    const xfer = this.clipXfer++ & 0xffff;
    for (const f of encodeClipboard(xfer, Mime.Text, bytes)) this.sendInput(f);
  }

  end(reason = "user_hangup") {
    if (this.snap.sessionId && !this.ended) {
      this.releaseAllKeys();
      signaling.send({ type: "session.end", session_id: this.snap.sessionId, reason });
    }
    this.finish(reason, null, true, false);
  }

  private finish(reason: string, detail: string | null, canReconnect: boolean, notify = true) {
    if (this.ended && this.snap.phase === "ended") return;
    const sid = this.snap.sessionId;
    if (notify && sid && !this.ended) {
      signaling.send({ type: "session.end", session_id: sid, reason });
    }
    this.ended = true;
    this.cleanup();
    this.update({
      phase: this.snap.phase === "pin_mismatch" ? "pin_mismatch" : "ended",
      endReason: reason,
      endDetail: detail ?? this.snap.endDetail,
      canReconnect,
      stream: null,
    });
  }

  private async checkUnlockKey(spki: string, sig: string) {
    const unlockKey = await verifyUnlockKey(this.devicePublicKey, spki, sig);
    if (this.ended) return;
    this.update({ unlockKey });
    // So sign-ins can be saved from the dashboard too (the server checks the signature).
    if (unlockKey)
      void api.devices.setUnlockKey(this.opts.deviceId, spki, sig).catch(() => undefined);
  }

  /** Seals a username and password for this device, signed by this browser. */
  async sealCredential(username: string, password: string): Promise<SealedCredential> {
    const unlockKey = this.snap.unlockKey;
    if (!unlockKey || !this.clientKey) throw new Error("This device cannot unlock itself.");
    return sealCredential({
      unlockKey,
      clientSpki: this.clientKey.spki,
      clientKey: this.clientKey.keyPair.privateKey,
      userId: this.opts.userId,
      username,
      password,
    });
  }

  /** Asks the device to type a saved password on its lock screen. */
  unlock(credential: SealedCredential): Promise<UnlockResult> {
    const control = this.control;
    if (control?.readyState !== "open") return Promise.resolve({ ok: false, code: "ended" });
    const id = ++this.unlockReqId;
    return new Promise((resolve) => {
      const timer = setTimeout(() => {
        this.unlocks.delete(id);
        resolve({ ok: false, code: "timeout" });
      }, UNLOCK_TIMEOUT_MS);
      this.unlocks.set(id, (r) => {
        clearTimeout(timer);
        resolve(r);
      });
      control.send(JSON.stringify({ t: "unlock", id, credential }));
    });
  }

  private cleanup() {
    for (const done of this.unlocks.values()) done({ ok: false, code: "ended" });
    this.unlocks.clear();
    if (this.streamTimer) clearTimeout(this.streamTimer);
    this.streamTimer = null;
    this.fileChannel?.dispose("session_ended");
    for (const off of this.offs) off();
    this.offs = [];
    for (const t of this.timers) clearInterval(t);
    this.timers = [];
    try {
      this.pc?.close();
    } catch {
      // already closed
    }
    this.pc = null;
    this.input = null;
    this.move = null;
    this.control = null;
    this.file = null;
  }
}

function preferCodecs(tr: RTCRtpTransceiver) {
  const caps = RTCRtpReceiver.getCapabilities?.("video");
  if (!caps || typeof tr.setCodecPreferences !== "function") return;
  const codecs = preferredCodecs(caps.codecs);
  if (!codecs) return;
  try {
    tr.setCodecPreferences(codecs);
  } catch {
    // leave browser defaults
  }
}
