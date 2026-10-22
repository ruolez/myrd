export type Rect = { left: number; top: number; width: number; height: number };

// Within this of 1:1 the video is drawn unscaled: any resampling blurs text.
const SNAP = 0.02;

/**
 * Where a videoW×videoH (device px) stream goes in a stage of stageW×stageH
 * CSS px. fit: as large as fits (object-fit: contain), snapped to exactly 1:1
 * when it would be enlarged by under 2%; actual: always 1:1, centered when
 * smaller, else at the top-left of a stage that scrolls. Offsets land on
 * device pixels. Before the stream size is known, the whole stage.
 */
export function displayRect(
  stageW: number,
  stageH: number,
  videoW: number,
  videoH: number,
  dpr: number,
  mode: "fit" | "actual",
): Rect {
  if (stageW <= 0 || stageH <= 0 || videoW <= 0 || videoH <= 0 || dpr <= 0) {
    return { left: 0, top: 0, width: Math.max(0, stageW), height: Math.max(0, stageH) };
  }
  const sw = stageW * dpr;
  const sh = stageH * dpr;
  let s = mode === "actual" ? 1 : Math.min(sw / videoW, sh / videoH);
  if (s >= 1 && s < 1 + SNAP) s = 1;
  const w = videoW * s;
  const h = videoH * s;
  const left = Math.max(0, Math.round((sw - w) / 2));
  const top = Math.max(0, Math.round((sh - h) / 2));
  return { left: left / dpr, top: top / dpr, width: w / dpr, height: h / dpr };
}

/** inner's box relative to outer's top-left (both from getBoundingClientRect). */
export function relativeRect(outer: Rect, inner: Rect): Rect {
  return {
    left: inner.left - outer.left,
    top: inner.top - outer.top,
    width: inner.width,
    height: inner.height,
  };
}

/**
 * Maps a point (relative to the element's top-left) to 0..65535 of the
 * remote monitor. Points in the letterbox bars clamp to the nearest edge.
 */
export function normalizePoint(px: number, py: number, box: Rect): { x: number; y: number } {
  const fx = box.width > 0 ? (px - box.left) / box.width : 0;
  const fy = box.height > 0 ? (py - box.top) / box.height : 0;
  const clamp = (f: number) => Math.round(Math.max(0, Math.min(1, f)) * 65535);
  return { x: clamp(fx), y: clamp(fy) };
}

/** Inverse of normalizePoint: 0..65535 → element coordinates. */
export function denormalizePoint(nx: number, ny: number, box: Rect): { x: number; y: number } {
  return { x: box.left + (nx / 65535) * box.width, y: box.top + (ny / 65535) * box.height };
}

/**
 * Where a popover of the given size goes next to its anchor (viewport
 * coordinates): below it, or above when it does not fit below and fits
 * better above; aligned to the anchor's start or end edge; kept inside the
 * viewport with a margin.
 */
export function placePopover(
  anchor: Rect,
  size: { width: number; height: number },
  viewport: { width: number; height: number },
  align: "start" | "end",
  gap = 4,
  margin = 8,
): { left: number; top: number } {
  const below = anchor.top + anchor.height + gap;
  const spaceBelow = viewport.height - margin - below;
  const spaceAbove = anchor.top - gap - margin;
  let top = below;
  if (size.height > spaceBelow && spaceAbove > spaceBelow) top = anchor.top - gap - size.height;
  top = Math.max(margin, Math.min(top, viewport.height - margin - size.height));
  let left = align === "start" ? anchor.left : anchor.left + anchor.width - size.width;
  left = Math.max(margin, Math.min(left, viewport.width - margin - size.width));
  return { left, top };
}
