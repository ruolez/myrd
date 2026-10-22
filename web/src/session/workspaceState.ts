// Which sessions are open in this tab, in what order, and which one has focus.
// Pure: the SessionWorkspace store applies the side effects.

import type { DeviceId } from "../api/types";

export const MAX_LIVE_SESSIONS = 6;

export type Entry = {
  id: DeviceId;
  forceRelay: boolean;
  trustKey?: string;
  /** Bumped by every reconnect: the pane and RemoteSession are new. */
  gen: number;
  openedAt: number;
  lastFocusedAt: number;
};

export type WsState = {
  order: DeviceId[];
  entries: Partial<Record<DeviceId, Entry>>;
  focused: DeviceId | null;
};

export const emptyWorkspace: WsState = { order: [], entries: {}, focused: null };

export type WsAction =
  | { t: "open"; id: DeviceId; forceRelay: boolean; now: number }
  | { t: "focus"; id: DeviceId | null; now: number }
  | { t: "close"; id: DeviceId }
  | { t: "replace"; id: DeviceId; forceRelay?: boolean; trustKey?: string }
  | { t: "reorder"; id: DeviceId; to: number };

export function workspaceReducer(s: WsState, a: WsAction): WsState {
  switch (a.t) {
    case "open": {
      if (s.entries[a.id]) return s;
      const e: Entry = {
        id: a.id,
        forceRelay: a.forceRelay,
        gen: 0,
        openedAt: a.now,
        lastFocusedAt: a.now,
      };
      return { ...s, order: [...s.order, a.id], entries: { ...s.entries, [a.id]: e } };
    }
    case "focus": {
      if (a.id === null) return s.focused === null ? s : { ...s, focused: null };
      const e = s.entries[a.id];
      if (!e) return s;
      return {
        ...s,
        focused: a.id,
        entries: { ...s.entries, [a.id]: { ...e, lastFocusedAt: a.now } },
      };
    }
    case "close": {
      if (!s.entries[a.id]) return s;
      const entries = { ...s.entries };
      delete entries[a.id];
      return {
        order: s.order.filter((x) => x !== a.id),
        entries,
        focused: s.focused === a.id ? null : s.focused,
      };
    }
    case "replace": {
      const e = s.entries[a.id];
      if (!e) return s;
      const next: Entry = {
        ...e,
        gen: e.gen + 1,
        forceRelay: a.forceRelay ?? e.forceRelay,
        trustKey: a.trustKey ?? e.trustKey,
      };
      return { ...s, entries: { ...s.entries, [a.id]: next } };
    }
    case "reorder": {
      const from = s.order.indexOf(a.id);
      if (from < 0) return s;
      const order = s.order.filter((x) => x !== a.id);
      order.splice(Math.max(0, Math.min(a.to, order.length)), 0, a.id);
      return { ...s, order };
    }
  }
}

/** Sessions that hold a device (not ended). */
export function liveIds(s: WsState, isLive: (id: DeviceId) => boolean): DeviceId[] {
  return s.order.filter(isLive);
}

/** The live session focused longest ago, other than the focused one. */
export function lruVictim(s: WsState, isLive: (id: DeviceId) => boolean): DeviceId | null {
  let best: Entry | null = null;
  for (const id of liveIds(s, isLive)) {
    const e = s.entries[id];
    if (!e || id === s.focused) continue;
    if (!best || e.lastFocusedAt < best.lastFocusedAt) best = e;
  }
  return best?.id ?? null;
}

/** The session before or after id in the strip, wrapping around. */
export function neighbor(s: WsState, id: DeviceId | null, dir: 1 | -1): DeviceId | null {
  if (!s.order.length) return null;
  const at = id ? s.order.indexOf(id) : -1;
  if (at < 0) return dir === 1 ? (s.order[0] ?? null) : (s.order[s.order.length - 1] ?? null);
  return s.order[(at + dir + s.order.length) % s.order.length] ?? null;
}

/** The n-th session (1-based), as numbered in the strip. */
export const byIndex = (s: WsState, n: number): DeviceId | null => s.order[n - 1] ?? null;

/** Where to go when id closes: its right neighbor, else its left, else nowhere. */
export function afterClose(s: WsState, id: DeviceId): DeviceId | null {
  const at = s.order.indexOf(id);
  if (at < 0 || s.order.length < 2) return null;
  return s.order[at + 1] ?? s.order[at - 1] ?? null;
}
