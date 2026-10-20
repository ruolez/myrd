// Browser signaling WebSocket (PROTOCOL §3): one shared socket per tab with
// jittered reconnect, typed pub/sub and request/response by `req`.

import type { ICEServer } from "../api/types";

export type SignalMessage = { v: 1; type: string; [k: string]: unknown };

export type SessionCreated = SignalMessage & {
  type: "session.created";
  req: string;
  session_id: string;
  ice_servers: ICEServer[];
  device: { id: string; name: string; public_key: string };
};

export type SignalError = SignalMessage & {
  type: "error";
  req?: string;
  session_id?: string;
  code: string;
  message: string;
};

export type ConnState = "idle" | "connecting" | "open" | "closed";

type Handler = (msg: SignalMessage) => void;

const MAX_BACKOFF_MS = 30_000;

class Signaling {
  private ws: WebSocket | null = null;
  private handlers = new Map<string, Set<Handler>>();
  private stateListeners = new Set<(s: ConnState) => void>();
  private _state: ConnState = "idle";
  private wanted = false;
  private attempt = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private pingTimer: ReturnType<typeof setInterval> | null = null;
  private openWaiters: (() => void)[] = [];

  get state() {
    return this._state;
  }

  start() {
    this.wanted = true;
    if (!this.ws) this.connect();
  }

  stop() {
    this.wanted = false;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    this.ws?.close(1000);
    this.ws = null;
    this.setState("idle");
  }

  private setState(s: ConnState) {
    this._state = s;
    for (const l of this.stateListeners) l(s);
    if (s === "open") {
      const waiters = this.openWaiters;
      this.openWaiters = [];
      for (const w of waiters) w();
    }
  }

  onState(fn: (s: ConnState) => void): () => void {
    this.stateListeners.add(fn);
    return () => this.stateListeners.delete(fn);
  }

  private connect() {
    const url = `${location.origin.replace(/^http/, "ws")}/v1/ws`;
    this.setState("connecting");
    const ws = new WebSocket(url);
    this.ws = ws;
    ws.onopen = () => {
      this.attempt = 0;
      this.setState("open");
      this.pingTimer = setInterval(() => this.send({ type: "ping", ts: Date.now() }), 25_000);
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") return;
      let msg: SignalMessage;
      try {
        msg = JSON.parse(ev.data) as SignalMessage;
      } catch {
        return;
      }
      if (msg.v !== 1 || typeof msg.type !== "string") return;
      for (const h of this.handlers.get(msg.type) ?? []) h(msg);
      for (const h of this.handlers.get("*") ?? []) h(msg);
    };
    ws.onclose = (ev) => {
      if (this.pingTimer) clearInterval(this.pingTimer);
      this.pingTimer = null;
      if (this.ws === ws) this.ws = null;
      this.setState("closed");
      if (!this.wanted || ev.code === 4001) return;
      const ceiling = Math.min(MAX_BACKOFF_MS, 1000 * 2 ** this.attempt++);
      this.reconnectTimer = setTimeout(() => this.connect(), Math.random() * ceiling + 250);
    };
  }

  on(type: string, fn: Handler): () => void {
    let set = this.handlers.get(type);
    if (!set) this.handlers.set(type, (set = new Set()));
    set.add(fn);
    return () => set.delete(fn);
  }

  send(msg: { type: string; [k: string]: unknown }): boolean {
    if (this.ws?.readyState !== WebSocket.OPEN) return false;
    this.ws.send(JSON.stringify({ v: 1, ...msg }));
    return true;
  }

  waitOpen(timeoutMs = 10_000): Promise<void> {
    if (this._state === "open") return Promise.resolve();
    this.start();
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("Signaling server unreachable.")), timeoutMs);
      this.openWaiters.push(() => {
        clearTimeout(t);
        resolve();
      });
    });
  }

  /** Sends session.create and resolves with session.created (or rejects with the server error). */
  async createSession(
    deviceId: string,
    clientKeyId: string,
    forceRelay: boolean,
  ): Promise<SessionCreated> {
    await this.waitOpen();
    const req = crypto.randomUUID();
    return new Promise((resolve, reject) => {
      const cleanup = () => {
        offOk();
        offErr();
        clearTimeout(timer);
      };
      const offOk = this.on("session.created", (m) => {
        if (m.req !== req) return;
        cleanup();
        resolve(m as SessionCreated);
      });
      const offErr = this.on("error", (m) => {
        if (m.req !== req) return;
        cleanup();
        const e = m as SignalError;
        reject(Object.assign(new Error(e.message || e.code), { code: e.code }));
      });
      const timer = setTimeout(() => {
        cleanup();
        reject(Object.assign(new Error("The server did not answer."), { code: "timeout" }));
      }, 15_000);
      this.send({
        type: "session.create",
        req,
        device_id: deviceId,
        client_key_id: clientKeyId,
        force_relay: forceRelay,
      });
    });
  }
}

export const signaling = new Signaling();
