// Local cursor rendering: while the controller moves the mouse, the remote
// cursor shape is the browser's own (hardware) cursor, so it tracks the hand
// with no network delay. The canvas draws it at the remote position only when
// the remote cursor moved on its own (someone at the device, an app warping
// it), under Pointer Lock, or while the pointer is outside the stage.

/** The last absolute move sent to the agent (normalized 0..65535, performance.now()). */
export type SentMove = { x: number; y: number; at: number };

/** Time for our last move to reach the agent and its cursor report to come back. */
export const ECHO_GRACE_MS = 1000;

/** Decoded cursor shapes kept per session (the agent sends each once, then its id). */
export const SHAPE_CACHE = 32;

/** Browsers ignore cursor images larger than this (CSS px). */
export const MAX_CSS_CURSOR = 128;

/** How far (normalized units) a reported position may sit from what we sent: ~4 native pixels. */
export function echoTolerance(nativeWidth: number): number {
  return Math.max(64, Math.ceil((4 * 65535) / Math.max(1, nativeWidth)));
}

/**
 * Whether to show the cursor where the agent reports it rather than under
 * the local pointer: nothing sent yet, or once our last move has had time to
 * echo back, the reported position is not where we put it.
 */
export function followsRemote(
  sent: SentMove | null,
  reported: { x: number; y: number },
  now: number,
  tolerance: number,
): boolean {
  if (!sent) return true;
  if (now - sent.at < ECHO_GRACE_MS) return false;
  return Math.abs(reported.x - sent.x) > tolerance || Math.abs(reported.y - sent.y) > tolerance;
}

export type CursorSyntax = "image-set" | "-webkit-image-set" | "url";

/** The best cursor image syntax this browser accepts. */
export function cursorSyntax(supports: (property: string, value: string) => boolean): CursorSyntax {
  for (const s of ["image-set", "-webkit-image-set"] as const) {
    if (supports("cursor", `${s}(url("a.png") 2x) 1 1, auto`)) return s;
  }
  return "url";
}

/**
 * The CSS `cursor` value for an image drawn at `scale` (image px per CSS px)
 * with its hotspot at (hotX, hotY) CSS px; `url` syntax cannot say the image
 * is high-density, so it expects a 1x image.
 */
export function cursorValue(
  dataUrl: string,
  syntax: CursorSyntax,
  scale: number,
  hotX: number,
  hotY: number,
): string {
  const hot = `${Math.max(0, Math.round(hotX))} ${Math.max(0, Math.round(hotY))}`;
  const img = syntax === "url" ? `url("${dataUrl}")` : `${syntax}(url("${dataUrl}") ${scale}x)`;
  return `${img} ${hot}, auto`;
}
