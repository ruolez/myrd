export type Rect = { left: number; top: number; width: number; height: number };

/**
 * The rectangle the video's pixels actually occupy inside an element of
 * elementW×elementH using object-fit: contain (letterboxed / pillarboxed).
 */
export function contentBox(
  elementW: number,
  elementH: number,
  videoW: number,
  videoH: number,
): Rect {
  if (elementW <= 0 || elementH <= 0 || videoW <= 0 || videoH <= 0) {
    return { left: 0, top: 0, width: Math.max(0, elementW), height: Math.max(0, elementH) };
  }
  const scale = Math.min(elementW / videoW, elementH / videoH);
  const width = videoW * scale;
  const height = videoH * scale;
  return { left: (elementW - width) / 2, top: (elementH - height) / 2, width, height };
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
