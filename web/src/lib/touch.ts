// Touch gestures on the remote screen, as a reducer: pointer events of touch
// fingers in, mouse/scroll/view actions out. Points are stage CSS pixels.
//
// direct:   tap = click there, swipe = scroll, hold = right-click there,
//           hold then move = drag, two fingers = zoom/pan the view,
//           two-finger tap = right-click.
// trackpad: finger moves the pointer, tap = click at the pointer, hold =
//           right-click, hold then move = drag, two fingers together =
//           scroll, two fingers apart = zoom/pan, two-finger tap = right-click.

import type { Pt } from "./zoom";

export type TouchMode = "direct" | "trackpad";

export type TouchInput =
  | { t: "down"; id: number; p: Pt; at: number }
  | { t: "move"; id: number; p: Pt; at: number }
  | { t: "up"; id: number; p: Pt; at: number }
  | { t: "cancel"; id: number; at: number }
  /** The long-press timer fired (see holdDeadline). */
  | { t: "hold"; at: number };

export type TouchAction =
  /** Move the remote pointer to this stage point. */
  | { t: "point"; p: Pt }
  /** Move the remote pointer by this many stage pixels, dt ms after the last nudge. */
  | { t: "nudge"; dx: number; dy: number; dt: number }
  | { t: "button"; button: "left" | "right"; down: boolean }
  /** Fingers travelled this far: the remote content should follow them. */
  | { t: "scroll"; dx: number; dy: number }
  /** A scroll released at this speed (px/ms), to coast on. */
  | { t: "fling"; vx: number; vy: number }
  /** Two fingers moved from a0/b0 to a/b: zoom and pan the local view. */
  | { t: "view"; a0: Pt; b0: Pt; a: Pt; b: Pt }
  | { t: "viewEnd" }
  /** A long press was recognized (feedback). */
  | { t: "hold" };

type Finger = { id: number; start: Pt; p: Pt; at: number };
type Sample = { p: Pt; at: number };
type Tap = { p: Pt; at: number };

export type TouchState =
  | { k: "idle"; lastTap: Tap | null }
  | { k: "press"; f: Finger; lastTap: Tap | null }
  | { k: "held"; f: Finger }
  | { k: "drag"; f: Finger }
  | { k: "swipe"; f: Finger; samples: Sample[] }
  | { k: "track"; f: Finger }
  | { k: "two"; a: Finger; b: Finger; at: number; kind: "pending" | "view" | "scroll" }
  | { k: "lifting"; ids: number[] };

export const HOLD_MS = 500;
const SLOP = 8;
const TWO_SLOP = 12;
const DOUBLE_TAP_MS = 350;
const DOUBLE_TAP_SLOP = 24;
const TWO_TAP_MS = 300;
const FLING_WINDOW_MS = 80;
const MIN_FLING = 0.25;

export const initialTouch: TouchState = { k: "idle", lastTap: null };
const IDLE: TouchState = initialTouch;

const dist = (a: Pt, b: Pt) => Math.hypot(a.x - b.x, a.y - b.y);
const mid = (a: Pt, b: Pt) => ({ x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 });
const LEFT_DOWN: TouchAction = { t: "button", button: "left", down: true };
const LEFT_UP: TouchAction = { t: "button", button: "left", down: false };
const click = (button: "left" | "right"): TouchAction[] => [
  { t: "button", button, down: true },
  { t: "button", button, down: false },
];

/** Speed of the last FLING_WINDOW_MS of samples, or null when too slow to coast. */
function flingOf(samples: Sample[]): { vx: number; vy: number } | null {
  const last = samples.at(-1);
  const first = last && samples.find((s) => s.at >= last.at - FLING_WINDOW_MS);
  if (!last || !first) return null;
  const dt = last.at - first.at;
  if (dt <= 0) return null;
  const vx = (last.p.x - first.p.x) / dt;
  const vy = (last.p.y - first.p.y) / dt;
  return Math.hypot(vx, vy) >= MIN_FLING ? { vx, vy } : null;
}

const r = (s: TouchState, out: TouchAction[] = []) => ({ s, out });

export function touchStep(
  s: TouchState,
  e: TouchInput,
  mode: TouchMode,
): { s: TouchState; out: TouchAction[] } {
  const direct = mode === "direct";
  switch (s.k) {
    case "idle":
      if (e.t === "down")
        return r({ k: "press", f: { id: e.id, start: e.p, p: e.p, at: e.at }, lastTap: s.lastTap });
      return r(s);

    case "press": {
      const f = s.f;
      if (e.t === "hold")
        return e.at >= f.at + HOLD_MS ? r({ k: "held", f }, [{ t: "hold" }]) : r(s);
      if (e.t === "down") {
        const a = { ...f, start: f.p };
        const b = { id: e.id, start: e.p, p: e.p, at: e.at };
        return r({ k: "two", a, b, at: e.at, kind: "pending" });
      }
      if (e.id !== f.id) return r(s);
      if (e.t === "cancel") return r(IDLE);
      if (e.t === "up") {
        if (!direct) return r(IDLE, click("left"));
        const last = s.lastTap;
        const p =
          last && e.at - last.at <= DOUBLE_TAP_MS && dist(last.p, f.start) <= DOUBLE_TAP_SLOP
            ? last.p
            : f.start;
        return r({ k: "idle", lastTap: { p, at: e.at } }, [{ t: "point", p }, ...click("left")]);
      }
      if (dist(f.start, e.p) <= SLOP) return r({ ...s, f: { ...f, p: e.p } });
      const moved = { ...f, p: e.p, at: e.at };
      const dx = e.p.x - f.start.x;
      const dy = e.p.y - f.start.y;
      if (direct)
        return r(
          {
            k: "swipe",
            f: moved,
            samples: [
              { p: f.start, at: f.at },
              { p: e.p, at: e.at },
            ],
          },
          [{ t: "scroll", dx, dy }],
        );
      return r({ k: "track", f: moved }, [{ t: "nudge", dx, dy, dt: e.at - f.at }]);
    }

    case "held": {
      const f = s.f;
      if (e.t === "hold") return r(s);
      if (e.t === "down") return r({ k: "lifting", ids: [f.id, e.id] });
      if (e.id !== f.id) return r(s);
      if (e.t === "cancel") return r(IDLE);
      if (e.t === "up")
        return r(IDLE, direct ? [{ t: "point", p: f.start }, ...click("right")] : click("right"));
      if (dist(f.start, e.p) <= SLOP) return r(s);
      const moved = { ...f, p: e.p, at: e.at };
      if (direct)
        return r({ k: "drag", f: moved }, [
          { t: "point", p: f.start },
          LEFT_DOWN,
          { t: "point", p: e.p },
        ]);
      return r({ k: "drag", f: moved }, [
        LEFT_DOWN,
        { t: "nudge", dx: e.p.x - f.start.x, dy: e.p.y - f.start.y, dt: e.at - f.at },
      ]);
    }

    case "drag": {
      const f = s.f;
      if (e.t === "hold" || e.t === "down" || e.id !== f.id) return r(s);
      if (e.t === "cancel") return r(IDLE, [LEFT_UP]);
      if (e.t === "up") return r(IDLE, direct ? [{ t: "point", p: e.p }, LEFT_UP] : [LEFT_UP]);
      const out: TouchAction[] = direct
        ? [{ t: "point", p: e.p }]
        : [{ t: "nudge", dx: e.p.x - f.p.x, dy: e.p.y - f.p.y, dt: e.at - f.at }];
      return r({ k: "drag", f: { ...f, p: e.p, at: e.at } }, out);
    }

    case "swipe":
    case "track": {
      const f = s.f;
      if (e.t === "hold") return r(s);
      if (e.t === "down") {
        const a = { ...f, start: f.p };
        const b = { id: e.id, start: e.p, p: e.p, at: e.at };
        return r({ k: "two", a, b, at: e.at, kind: "pending" });
      }
      if (e.id !== f.id) return r(s);
      if (e.t === "cancel") return r(IDLE);
      if (e.t === "up") {
        if (s.k === "track") return r(IDLE);
        const fling = flingOf([...s.samples, { p: e.p, at: e.at }]);
        return r(IDLE, fling ? [{ t: "fling", ...fling }] : []);
      }
      const dx = e.p.x - f.p.x;
      const dy = e.p.y - f.p.y;
      const moved = { ...f, p: e.p, at: e.at };
      if (s.k === "track")
        return r({ k: "track", f: moved }, [{ t: "nudge", dx, dy, dt: e.at - f.at }]);
      const samples = [
        ...s.samples.filter((x) => x.at >= e.at - FLING_WINDOW_MS),
        { p: e.p, at: e.at },
      ];
      return r({ k: "swipe", f: moved, samples }, [{ t: "scroll", dx, dy }]);
    }

    case "two": {
      if (e.t === "hold" || e.t === "down") return r(s);
      const which = e.id === s.a.id ? "a" : e.id === s.b.id ? "b" : null;
      if (!which) return r(s);
      if (e.t === "up" || e.t === "cancel") {
        const other = which === "a" ? s.b : s.a;
        const lifting: TouchState = { k: "lifting", ids: [other.id] };
        if (s.kind === "view") return r(lifting, [{ t: "viewEnd" }]);
        if (s.kind === "pending" && e.t === "up" && e.at - s.at <= TWO_TAP_MS)
          return r(
            lifting,
            direct ? [{ t: "point", p: s.a.start }, ...click("right")] : click("right"),
          );
        return r(lifting);
      }
      const next = { ...s, [which]: { ...s[which], p: e.p } };
      const { a, b } = next;
      if (next.kind === "pending") {
        const dd = Math.abs(dist(a.p, b.p) - dist(a.start, b.start));
        const dm = dist(mid(a.p, b.p), mid(a.start, b.start));
        if (Math.max(dd, dm) <= TWO_SLOP) return r(next);
        next.kind = direct || dd > dm ? "view" : "scroll";
        if (next.kind === "scroll") {
          const m0 = mid(a.start, b.start);
          const m = mid(a.p, b.p);
          return r(next, [{ t: "scroll", dx: m.x - m0.x, dy: m.y - m0.y }]);
        }
      } else if (next.kind === "scroll") {
        const m0 = mid(s.a.p, s.b.p);
        const m = mid(a.p, b.p);
        return r(next, [{ t: "scroll", dx: m.x - m0.x, dy: m.y - m0.y }]);
      }
      return r(next, [{ t: "view", a0: a.start, b0: b.start, a: a.p, b: b.p }]);
    }

    case "lifting": {
      if (e.t === "down") return r({ k: "lifting", ids: [...s.ids, e.id] });
      if (e.t !== "up" && e.t !== "cancel") return r(s);
      const ids = s.ids.filter((id) => id !== e.id);
      return r(ids.length ? { k: "lifting", ids } : IDLE);
    }
  }
}

/** When the long-press timer should fire a "hold" input, or null when none is due. */
export function holdDeadline(s: TouchState): number | null {
  return s.k === "press" ? s.f.at + HOLD_MS : null;
}
