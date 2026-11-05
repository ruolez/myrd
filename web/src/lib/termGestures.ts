// Touch on a terminal, on top of the touch reducer (lib/touch.ts, direct
// mode): swipes scroll the scrollback (or, in a full-screen program, send
// wheel or arrow keys), pinch sets the font size, taps and long presses
// place a click or start a selection. Pure.

import type { Pt } from "./zoom";

export const MIN_FONT = 9;
export const MAX_FONT = 22;

/** The default font size for a terminal this wide (CSS px): ~44+ columns on a 320 px phone. */
export function defaultFontSize(width: number): number {
  if (width < 360) return 12;
  if (width < 420) return 13;
  return 14;
}

/** Font size after a pinch from fingers a0/b0 to a/b, in half-pixel steps. */
export function pinchFont(start: number, a0: Pt, b0: Pt, a: Pt, b: Pt): number {
  const d0 = Math.hypot(a0.x - b0.x, a0.y - b0.y);
  const d = Math.hypot(a.x - b.x, a.y - b.y);
  const f = d0 > 0 ? (start * d) / d0 : start;
  return Math.min(MAX_FONT, Math.max(MIN_FONT, Math.round(f * 2) / 2));
}

/**
 * Whole lines to scroll for a finger movement of dy pixels, carrying the
 * remainder. Content follows the finger: moving down shows older lines
 * (negative = up).
 */
export function scrollLines(
  carry: number,
  dy: number,
  cellHeight: number,
): { lines: number; carry: number } {
  if (cellHeight <= 0) return { lines: 0, carry: 0 };
  const total = carry - dy / cellHeight;
  const lines = Math.trunc(total) || 0; // never -0
  return { lines, carry: total - lines };
}

/** The 1-based cell under a point of the terminal's screen area. */
export function cellAt(
  p: Pt,
  screen: { left: number; top: number; width: number; height: number },
  cols: number,
  rows: number,
): { col: number; row: number } {
  const col = Math.floor(((p.x - screen.left) / screen.width) * cols) + 1;
  const row = Math.floor(((p.y - screen.top) / screen.height) * rows) + 1;
  return { col: Math.min(cols, Math.max(1, col)), row: Math.min(rows, Math.max(1, row)) };
}

export type MouseModes = {
  /** xterm.js `modes.mouseTrackingMode`. */
  tracking: "none" | "x10" | "vt200" | "drag" | "any";
  /** The alternate screen is shown (a full-screen program). */
  alt: boolean;
  /** DECCKM. */
  appCursor: boolean;
};

/**
 * What a scroll of `lines` (negative = up) sends to the program, or null when
 * the local scrollback should move instead: wheel events for programs that
 * track the mouse (SGR encoding), arrow keys for other full-screen ones
 * (less, man), nothing at a plain prompt.
 */
export function scrollBytes(
  lines: number,
  at: { col: number; row: number },
  m: MouseModes,
): string | null {
  if (lines === 0) return "";
  const n = Math.min(Math.abs(lines), 20);
  if (m.tracking !== "none") {
    const button = lines < 0 ? 64 : 65;
    return `\x1b[<${button};${at.col};${at.row}M`.repeat(n);
  }
  if (m.alt)
    return (
      lines < 0 ? (m.appCursor ? "\x1bOA" : "\x1b[A") : m.appCursor ? "\x1bOB" : "\x1b[B"
    ).repeat(n);
  return null;
}

/** A left click at a cell for programs that track the mouse; null when none does. */
export function clickBytes(at: { col: number; row: number }, m: MouseModes): string | null {
  if (m.tracking === "none") return null;
  return `\x1b[<0;${at.col};${at.row}M\x1b[<0;${at.col};${at.row}m`;
}

const WORD = /[\p{L}\p{N}_\-./~:@%+=]/u;

/**
 * The word around a 0-based column of a line, as [start, end) columns; a
 * single cell when the column is not on a word character.
 */
export function wordAt(line: string, col: number): [number, number] {
  const chars = [...line];
  if (col < 0 || col >= chars.length || !WORD.test(chars[col]!)) return [col, col + 1];
  let s = col;
  let e = col + 1;
  while (s > 0 && WORD.test(chars[s - 1]!)) s--;
  while (e < chars.length && WORD.test(chars[e]!)) e++;
  return [s, e];
}

/** Coasting after a fling: the speed decays with this time constant (ms). */
export const FLING_TAU = 325;

/** Distance travelled over dt ms at speed v (px/ms), and the speed left. */
export function coast(v: number, dt: number): { d: number; v: number } {
  const k = Math.exp(-dt / FLING_TAU);
  return { d: v * FLING_TAU * (1 - k), v: v * k };
}
