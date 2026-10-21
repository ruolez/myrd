import { relativeRect } from "../lib/geometry";
import type { Rect } from "../lib/geometry";

/**
 * Where the video's pixels are inside el (whose rect is r): the video element
 * is laid out exactly over them (lib/geometry displayRect). The whole element
 * before the stream size is known.
 */
export function videoBox(r: Rect, video: HTMLVideoElement | null): Rect {
  if (!video?.videoWidth || !video.videoHeight) {
    return { left: 0, top: 0, width: r.width, height: r.height };
  }
  return relativeRect(r, video.getBoundingClientRect());
}
