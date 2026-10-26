import { describe, expect, test } from "vitest";
import { HOLD_MS, holdDeadline, initialTouch, touchStep } from "./touch";
import type { TouchAction, TouchInput, TouchMode, TouchState } from "./touch";

/** Runs events through the recognizer; returns every action and the final state. */
function run(mode: TouchMode, events: TouchInput[], from: TouchState = initialTouch) {
  let s = from;
  const out: TouchAction[] = [];
  for (const e of events) {
    const r = touchStep(s, e, mode);
    s = r.s;
    out.push(...r.out);
  }
  return { s, out };
}

const down = (id: number, x: number, y: number, at: number): TouchInput => ({
  t: "down",
  id,
  p: { x, y },
  at,
});
const move = (id: number, x: number, y: number, at: number): TouchInput => ({
  t: "move",
  id,
  p: { x, y },
  at,
});
const up = (id: number, x: number, y: number, at: number): TouchInput => ({
  t: "up",
  id,
  p: { x, y },
  at,
});

const LEFT_CLICK: TouchAction[] = [
  { t: "button", button: "left", down: true },
  { t: "button", button: "left", down: false },
];
const RIGHT_CLICK: TouchAction[] = [
  { t: "button", button: "right", down: true },
  { t: "button", button: "right", down: false },
];

const sum = (out: TouchAction[], t: "scroll" | "nudge") =>
  out.reduce((acc, a) => (a.t === t ? { dx: acc.dx + a.dx, dy: acc.dy + a.dy } : acc), {
    dx: 0,
    dy: 0,
  });

describe("touchStep direct", () => {
  test("a tap clicks where the finger landed", () => {
    const { out } = run("direct", [
      down(1, 100, 200, 0),
      move(1, 103, 202, 40),
      up(1, 103, 202, 90),
    ]);
    expect(out).toEqual([{ t: "point", p: { x: 100, y: 200 } }, ...LEFT_CLICK]);
  });

  test("a quick second tap nearby clicks on the first tap's point (double-click)", () => {
    const first = 0;
    const second = 250;
    const { out } = run("direct", [
      down(1, 100, 200, first),
      up(1, 100, 200, first + 60),
      down(2, 114, 210, second),
      up(2, 114, 210, second + 60),
    ]);
    expect(out).toEqual([
      { t: "point", p: { x: 100, y: 200 } },
      ...LEFT_CLICK,
      { t: "point", p: { x: 100, y: 200 } },
      ...LEFT_CLICK,
    ]);
  });

  test("a tap long after the previous one clicks where it lands", () => {
    const { out } = run("direct", [
      down(1, 100, 200, 0),
      up(1, 100, 200, 60),
      down(2, 110, 205, 2000),
      up(2, 110, 205, 2060),
    ]);
    expect(out.filter((a) => a.t === "point")).toEqual([
      { t: "point", p: { x: 100, y: 200 } },
      { t: "point", p: { x: 110, y: 205 } },
    ]);
  });

  test("a swipe scrolls by the finger's travel and presses no button", () => {
    const { out } = run("direct", [
      down(1, 200, 600, 0),
      move(1, 200, 500, 200),
      move(1, 200, 300, 400),
      up(1, 200, 300, 600),
    ]);
    expect([sum(out, "scroll"), out.some((a) => a.t === "button")]).toEqual([
      { dx: 0, dy: -300 },
      false,
    ]);
  });

  test("a fast swipe released while moving flings at the finger's speed", () => {
    const { out } = run("direct", [
      down(1, 200, 600, 0),
      move(1, 200, 560, 16),
      move(1, 200, 520, 32),
      move(1, 200, 480, 48),
      up(1, 200, 440, 64),
    ]);
    expect(out.at(-1)).toEqual({ t: "fling", vx: 0, vy: -2.5 });
  });

  test("a swipe that stops before lifting does not fling", () => {
    const { out } = run("direct", [
      down(1, 200, 600, 0),
      move(1, 200, 400, 50),
      move(1, 200, 400, 300),
      up(1, 200, 400, 400),
    ]);
    expect(out.some((a) => a.t === "fling")).toBe(false);
  });

  test("holding still right-clicks where the finger landed", () => {
    const { out } = run("direct", [
      down(1, 100, 200, 0),
      { t: "hold", at: HOLD_MS },
      up(1, 102, 201, HOLD_MS + 200),
    ]);
    expect(out).toEqual([{ t: "hold" }, { t: "point", p: { x: 100, y: 200 } }, ...RIGHT_CLICK]);
  });

  test("a hold fired early by a late timer does nothing", () => {
    const { out } = run("direct", [down(1, 100, 200, 0), { t: "hold", at: HOLD_MS - 100 }]);
    expect(out).toEqual([]);
  });

  test("holding then moving drags with the left button", () => {
    const { out } = run("direct", [
      down(1, 100, 200, 0),
      { t: "hold", at: HOLD_MS },
      move(1, 150, 260, HOLD_MS + 50),
      move(1, 200, 300, HOLD_MS + 100),
      up(1, 200, 300, HOLD_MS + 150),
    ]);
    expect(out).toEqual([
      { t: "hold" },
      { t: "point", p: { x: 100, y: 200 } },
      { t: "button", button: "left", down: true },
      { t: "point", p: { x: 150, y: 260 } },
      { t: "point", p: { x: 200, y: 300 } },
      { t: "point", p: { x: 200, y: 300 } },
      { t: "button", button: "left", down: false },
    ]);
  });

  test("a second finger during a drag is ignored", () => {
    const { out } = run("direct", [
      down(1, 100, 200, 0),
      { t: "hold", at: HOLD_MS },
      move(1, 150, 200, 600),
      down(2, 300, 300, 650),
      move(2, 320, 300, 660),
      up(2, 320, 300, 700),
      up(1, 150, 200, 800),
    ]);
    expect(out.filter((a) => a.t !== "point")).toEqual([
      { t: "hold" },
      { t: "button", button: "left", down: true },
      { t: "button", button: "left", down: false },
    ]);
  });

  test("a two-finger tap right-clicks at the first finger and no left click follows", () => {
    const { out } = run("direct", [
      down(1, 100, 200, 0),
      down(2, 160, 200, 20),
      up(1, 100, 200, 120),
      up(2, 160, 200, 130),
    ]);
    expect(out).toEqual([{ t: "point", p: { x: 100, y: 200 } }, ...RIGHT_CLICK]);
  });

  test("two moving fingers drive the view from where both were down", () => {
    const { out } = run("direct", [
      down(1, 100, 400, 0),
      down(2, 200, 400, 10),
      move(1, 80, 400, 40),
      move(2, 220, 400, 50),
      up(1, 80, 400, 100),
      move(2, 240, 400, 110),
      up(2, 240, 400, 120),
    ]);
    const a0 = { x: 100, y: 400 };
    const b0 = { x: 200, y: 400 };
    expect(out).toEqual([
      { t: "view", a0, b0, a: { x: 80, y: 400 }, b: b0 },
      { t: "view", a0, b0, a: { x: 80, y: 400 }, b: { x: 220, y: 400 } },
      { t: "viewEnd" },
    ]);
  });
});

describe("touchStep trackpad", () => {
  test("a moving finger nudges the pointer by its travel without clicking", () => {
    const { out } = run("trackpad", [
      down(1, 100, 100, 0),
      move(1, 130, 120, 16),
      move(1, 160, 90, 32),
      up(1, 160, 90, 48),
    ]);
    expect([sum(out, "nudge"), out.some((a) => a.t === "button")]).toEqual([
      { dx: 60, dy: -10 },
      false,
    ]);
  });

  test("a tap clicks wherever the pointer is", () => {
    const { out } = run("trackpad", [down(1, 100, 100, 0), up(1, 100, 100, 80)]);
    expect(out).toEqual(LEFT_CLICK);
  });

  test("a two-finger tap right-clicks wherever the pointer is", () => {
    const { out } = run("trackpad", [
      down(1, 100, 100, 0),
      down(2, 150, 100, 10),
      up(2, 150, 100, 90),
      up(1, 100, 100, 95),
    ]);
    expect(out).toEqual(RIGHT_CLICK);
  });

  test("two fingers moving together scroll by their midpoint's travel", () => {
    const { out } = run("trackpad", [
      down(1, 100, 500, 0),
      down(2, 160, 500, 5),
      move(1, 100, 450, 30),
      move(2, 160, 450, 35),
      move(1, 100, 380, 60),
      move(2, 160, 380, 65),
      up(1, 100, 380, 90),
      up(2, 160, 380, 95),
    ]);
    expect([sum(out, "scroll"), out.some((a) => a.t === "view")]).toEqual([
      { dx: 0, dy: -120 },
      false,
    ]);
  });

  test("two fingers spreading apart drive the view", () => {
    const { out } = run("trackpad", [
      down(1, 150, 400, 0),
      down(2, 250, 400, 5),
      move(1, 120, 400, 30),
      move(2, 280, 400, 35),
      up(1, 120, 400, 90),
      up(2, 280, 400, 95),
    ]);
    expect([out.some((a) => a.t === "scroll"), out.at(-1)]).toEqual([false, { t: "viewEnd" }]);
  });
});

describe("holdDeadline", () => {
  test("is due HOLD_MS after a lone finger lands, and not otherwise", () => {
    const pressed = run("direct", [down(1, 10, 10, 1000)]).s;
    const swiping = run("direct", [down(1, 10, 10, 1000), move(1, 10, 80, 1050)]).s;
    expect([holdDeadline(initialTouch), holdDeadline(pressed), holdDeadline(swiping)]).toEqual([
      null,
      1000 + HOLD_MS,
      null,
    ]);
  });
});

describe("touchStep properties", () => {
  function rng(seed: number) {
    return () => {
      seed = (seed + 0x6d2b79f5) | 0;
      let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  test.each<TouchMode>(["direct", "trackpad"])(
    "%s: once every finger is up, every pressed button was released",
    (mode) => {
      const r = rng(mode === "direct" ? 1 : 2);
      for (let round = 0; round < 300; round++) {
        let at = 0;
        const fingers = new Map<number, { x: number; y: number }>();
        const events: TouchInput[] = [];
        let nextId = 1;
        for (let i = 0; i < 30; i++) {
          at += Math.floor(r() * 300);
          const roll = r();
          const ids = [...fingers.keys()];
          if (roll < 0.25 && fingers.size < 3) {
            const p = { x: r() * 400, y: r() * 800 };
            fingers.set(nextId, p);
            events.push(down(nextId++, p.x, p.y, at));
          } else if (roll < 0.35) {
            events.push({ t: "hold", at });
          } else if (roll < 0.8 && ids.length) {
            const id = ids[Math.floor(r() * ids.length)] ?? 0;
            const p = { x: r() * 400, y: r() * 800 };
            fingers.set(id, p);
            events.push(move(id, p.x, p.y, at));
          } else if (ids.length) {
            const id = ids[Math.floor(r() * ids.length)] ?? 0;
            const p = fingers.get(id) ?? { x: 0, y: 0 };
            fingers.delete(id);
            events.push(r() < 0.15 ? { t: "cancel", id, at } : up(id, p.x, p.y, at));
          }
        }
        for (const [id, p] of fingers) events.push(up(id, p.x, p.y, (at += 10)));
        const { s, out } = run(mode, events);
        const held = { left: 0, right: 0 };
        let views = 0;
        for (const a of out) {
          if (a.t === "button") held[a.button] += a.down ? 1 : -1;
          if (a.t === "view") views = 1;
          if (a.t === "viewEnd") views = 0;
        }
        expect({ held, views, state: s.k }).toEqual({
          held: { left: 0, right: 0 },
          views: 0,
          state: "idle",
        });
      }
    },
  );
});
