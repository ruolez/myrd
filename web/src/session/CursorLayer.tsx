// Draws the remote cursor locally on a canvas so it never lags the video
// (the agent never composites it into the stream).

import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import { contentBox, denormalizePoint } from "../lib/geometry";
import type { RemoteSession } from "./RemoteSession";

type Shape = { bmp: ImageBitmap; hotX: number; hotY: number; w: number; h: number };

export function CursorLayer({
  session,
  video,
  nativeWidth,
  onHasShape,
}: {
  session: RemoteSession | null;
  video: RefObject<HTMLVideoElement | null>;
  nativeWidth: number;
  onHasShape: (has: boolean) => void;
}) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const widthRef = useRef(nativeWidth);
  widthRef.current = nativeWidth;

  useEffect(() => {
    const c = canvas.current;
    if (!session || !c) return;
    const ctx = c.getContext("2d");
    if (!ctx) return;
    const cache = new Map<number, Shape>();
    let current: Shape | null = null;
    let pos = { x: 0, y: 0, visible: false };
    let raf = 0;
    let gen = 0;

    const draw = () => {
      raf = 0;
      const r = c.getBoundingClientRect();
      const dpr = window.devicePixelRatio || 1;
      const w = Math.round(r.width * dpr);
      const h = Math.round(r.height * dpr);
      if (c.width !== w || c.height !== h) {
        c.width = w;
        c.height = h;
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, r.width, r.height);
      if (!current || !pos.visible) return;
      const v = video.current;
      const box = contentBox(r.width, r.height, v?.videoWidth ?? 0, v?.videoHeight ?? 0);
      // Shapes are in native monitor pixels; the video may be encoded smaller.
      const remoteW = widthRef.current || v?.videoWidth || box.width;
      const scale = remoteW > 0 ? box.width / remoteW : 1;
      const p = denormalizePoint(pos.x, pos.y, box);
      ctx.drawImage(
        current.bmp,
        p.x - current.hotX * scale,
        p.y - current.hotY * scale,
        current.w * scale,
        current.h * scale,
      );
    };
    const schedule = () => {
      if (!raf) raf = requestAnimationFrame(draw);
    };

    const off = session.onCursor((m) => {
      if (m.type === "pos") {
        pos = { x: m.x, y: m.y, visible: m.visible };
        schedule();
        return;
      }
      if (m.png.length === 0) {
        const cached = cache.get(m.id);
        if (cached) {
          current = cached;
          schedule();
        }
        return;
      }
      const my = ++gen;
      createImageBitmap(new Blob([m.png as Uint8Array<ArrayBuffer>], { type: "image/png" }))
        .then((bmp) => {
          const shape = {
            bmp,
            hotX: m.hotX,
            hotY: m.hotY,
            w: m.w || bmp.width,
            h: m.h || bmp.height,
          };
          cache.set(m.id, shape);
          if (my === gen) {
            current = shape;
            onHasShape(true);
            schedule();
          }
        })
        .catch(() => undefined);
    });
    const ro = new ResizeObserver(schedule);
    ro.observe(c);
    return () => {
      off();
      ro.disconnect();
      if (raf) cancelAnimationFrame(raf);
      for (const s of cache.values()) s.bmp.close();
    };
  }, [session, video, onHasShape]);

  return (
    <canvas
      ref={canvas}
      className="pointer-events-none absolute inset-0 h-full w-full"
      aria-hidden="true"
    />
  );
}
