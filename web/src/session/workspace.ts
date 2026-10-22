// The remote sessions open in this tab. Sessions live here, not in a page,
// so switching between devices or visiting the dashboard keeps them
// connected; the session in view streams normally, the others at
// background quality (RemoteSession.setBackground).

import type { DeviceId, UserId } from "../api/types";
import { RemoteSession } from "./RemoteSession";
import { emptyWorkspace, lruVictim, MAX_LIVE_SESSIONS, workspaceReducer } from "./workspaceState";
import type { Entry, WsAction, WsState } from "./workspaceState";

export type OpenResult = { ok: true } | { ok: false; victim: DeviceId | null };

export class SessionWorkspace {
  private state: WsState = emptyWorkspace;
  private sessions = new Map<DeviceId, RemoteSession>();
  private listeners = new Set<() => void>();
  private backgroundQueued = false;

  constructor(private readonly userId: UserId) {}

  getState = (): WsState => this.state;

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  session(id: DeviceId): RemoteSession | null {
    return this.sessions.get(id) ?? null;
  }

  /** Still holding the device: not ended or refused. */
  isLive = (id: DeviceId): boolean => {
    const p = this.sessions.get(id)?.snapshot.phase;
    return p !== undefined && p !== "ended" && p !== "pin_mismatch";
  };

  has(id: DeviceId): boolean {
    return id in this.state.entries;
  }

  /** Any file transfer still running (the tab should not close silently). */
  transfersActive(): boolean {
    for (const s of this.sessions.values())
      if ((s.snapshot.files?.activeCount ?? 0) > 0) return true;
    return false;
  }

  /**
   * Opens a session to id unless one is open already. At the limit it opens
   * nothing and names the session to end first (the least recently used).
   */
  open(id: DeviceId, forceRelay = false): OpenResult {
    if (this.has(id)) return { ok: true };
    const live = this.state.order.filter(this.isLive).length;
    if (live >= MAX_LIVE_SESSIONS) return { ok: false, victim: lruVictim(this.state, this.isLive) };
    this.dispatch({ t: "open", id, forceRelay, now: Date.now() });
    const e = this.state.entries[id];
    if (e) this.create(e);
    return { ok: true };
  }

  focus(id: DeviceId | null) {
    if (id === this.state.focused) return;
    this.dispatch({ t: "focus", id, now: Date.now() });
  }

  /** Ends the session (if still running) and removes it from the strip. */
  close(id: DeviceId) {
    this.sessions.get(id)?.end("user_hangup");
    this.sessions.delete(id);
    this.dispatch({ t: "close", id });
  }

  /** Reconnect: ends the current session and starts a new one for the same device. */
  replace(id: DeviceId, opts: { forceRelay?: boolean; trustKey?: string } = {}) {
    if (!this.has(id)) return;
    this.sessions.get(id)?.end("user_hangup");
    this.dispatch({ t: "replace", id, ...opts });
    const e = this.state.entries[id];
    if (e) this.create(e);
  }

  reorder(id: DeviceId, to: number) {
    this.dispatch({ t: "reorder", id, to });
  }

  endAll(reason = "user_hangup") {
    for (const s of this.sessions.values()) s.end(reason);
    this.sessions.clear();
    this.state = emptyWorkspace;
    this.emit();
  }

  private create(e: Entry) {
    const s = new RemoteSession({
      deviceId: e.id,
      deviceName: "",
      userId: this.userId,
      forceRelay: e.forceRelay,
      trustNewDeviceKey: e.trustKey,
    });
    this.sessions.set(e.id, s);
    s.setBackground(e.id !== this.state.focused);
    void s.start();
  }

  private dispatch(a: WsAction) {
    const next = workspaceReducer(this.state, a);
    if (next === this.state) return;
    this.state = next;
    this.emit();
    this.queueBackground();
  }

  private emit() {
    for (const l of this.listeners) l();
  }

  // Route changes blur then focus within one tick (and StrictMode runs
  // effects twice): apply the final focus once, after the dust settles.
  private queueBackground() {
    if (this.backgroundQueued) return;
    this.backgroundQueued = true;
    queueMicrotask(() => {
      this.backgroundQueued = false;
      for (const [id, s] of this.sessions) s.setBackground(id !== this.state.focused);
    });
  }
}
