// Shows the remote cursor shape with no network delay (the agent never
// composites it into the stream). While the controller moves the mouse it is
// the browser's own cursor over the stage; the canvas draws it where the agent
// reports it once the remote cursor moves on its own, under Pointer Lock, and
// while the pointer is outside the stage (lib/cursor.ts).

import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import {
  cursorSyntax,
  cursorValue,
  echoTolerance,
  ECHO_GRACE_MS,
  followsRemote,
  MAX_CSS_CURSOR,
  SHAPE_CACHE,
} from "../lib/cursor";
import { denormalizePoint } from "../lib/geometry";
import type { RemoteSession } from "./RemoteSession";
import { videoBox } from "./videoBox";

type Shape = { bmp: ImageBitmap; hotX: number; hotY: number; w: number; h: number };

export function CursorLayer({
  session,
  video,
  surface,
  nativeWidth,
  active,
}: {
  session: RemoteSession | null;
  video: RefObject<HTMLVideoElement | null>;
  surface: RefObject<HTMLDivElement | null>;
  nativeWidth: number;
  active: boolean;
}) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const widthRef = useRef(nativeWidth);
  widthRef.current = nativeWidth;
  const activeRef = useRef(active);
  activeRef.current = active;
  const redraw = useRef<() => void>(() => undefined);

  useEffect(() => {
    const c = canvas.current;
    const el = surface.current;
    if (!session || !c || !el) return;
    const ctx = c.getContext("2d");
    if (!ctx) return;
    const syntax = cursorSyntax((p, v) => CSS.supports(p, v));
    // Shape ids are content hashes: an id seen before is the same image.
    const cache = new Map<number, Shape>();
    const decoding = new Set<number>();
    let want: number | null = null;
    let current: Shape | null = null;
    let pos = { x: 0, y: 0, visible: false };
    let pointer: { x: number; y: number } | null = null; // over the stage, element coords
    let css: { shape: Shape; size: string; value: string | null } | null = null;
    let painted: { shape: Shape; size: string } | null = null;
    let shown = false;
    let applied = "";
    let raf = 0;
    let recheck: ReturnType<typeof setTimeout> | null = null;
    let closed = false;

    const setCursor = (v: string) => {
      if (v !== applied) el.style.cursor = applied = v;
    };

    // The shape as a CSS cursor at its on-screen size; null when too large.
    const cssCursor = (shape: Shape, scale: number, dpr: number): string | null => {
      const w = shape.w * scale;
      const h = shape.h * scale;
      const size = `${w}x${h}@${dpr}`;
      if (css?.shape === shape && css.size === size) return css.value;
      let value: string | null = null;
      if (w <= MAX_CSS_CURSOR && h <= MAX_CSS_CURSOR && w >= 1 && h >= 1) {
        const density = syntax === "url" ? 1 : dpr;
        const off = document.createElement("canvas");
        off.width = Math.round(w * density);
        off.height = Math.round(h * density);
        const octx = off.getContext("2d");
        if (octx) {
          octx.drawImage(shape.bmp, 0, 0, off.width, off.height);
          value = cursorValue(
            off.toDataURL(),
            syntax,
            density,
            shape.hotX * scale,
            shape.hotY * scale,
          );
        }
      }
      css = { shape, size, value };
      return value;
    };

    const hide = () => {
      if (!shown) return;
      shown = false;
      c.style.visibility = "";
    };

    // The canvas is the size of the cursor and moves with a transform, so a
    // cursor report repaints a few pixels, not the whole stage.
    const show = (shape: Shape, scale: number, dpr: number, x: number, y: number) => {
      const size = `${scale}@${dpr}`;
      if (painted?.shape !== shape || painted.size !== size) {
        const w = Math.max(1, Math.round(shape.w * scale * dpr));
        const h = Math.max(1, Math.round(shape.h * scale * dpr));
        if (c.width !== w || c.height !== h) {
          c.width = w;
          c.height = h;
        }
        c.style.width = `${shape.w * scale}px`;
        c.style.height = `${shape.h * scale}px`;
        ctx.clearRect(0, 0, w, h);
        ctx.drawImage(shape.bmp, 0, 0, w, h);
        painted = { shape, size };
      }
      c.style.transform = `translate3d(${x - shape.hotX * scale}px, ${y - shape.hotY * scale}px, 0)`;
      if (!shown) {
        shown = true;
        c.style.visibility = "visible";
      }
    };

    const draw = () => {
      raf = 0;
      if (!current || !activeRef.current) {
        hide();
        setCursor("");
        return;
      }
      const r = el.getBoundingClientRect();
      const dpr = window.devicePixelRatio || 1;
      const v = video.current;
      const box = videoBox(r, v);
      // Shapes are in native monitor pixels; the video may be encoded smaller.
      const remoteW = widthRef.current || v?.videoWidth || box.width;
      const scale = remoteW > 0 ? box.width / remoteW : 1;
      const remote =
        document.pointerLockElement === el ||
        !pointer ||
        followsRemote(session.lastMove, pos, performance.now(), echoTolerance(remoteW));
      const hardware = remote || !pos.visible ? null : cssCursor(current, scale, dpr);
      setCursor(hardware ?? "none");
      if (hardware || !pos.visible) {
        hide();
        return;
      }
      const p = !remote && pointer ? pointer : denormalizePoint(pos.x, pos.y, box);
      show(current, scale, dpr, p.x, p.y);
    };
    const schedule = () => {
      if (!raf) raf = requestAnimationFrame(draw);
    };
    redraw.current = schedule;

    const use = (id: number): boolean => {
      const shape = cache.get(id);
      if (!shape) return false;
      cache.delete(id);
      cache.set(id, shape);
      current = shape;
      schedule();
      return true;
    };
    const evict = () => {
      for (const [id, shape] of cache) {
        if (cache.size <= SHAPE_CACHE) break;
        if (shape === current) continue;
        cache.delete(id);
        if (css?.shape === shape) css = null;
        if (painted?.shape === shape) painted = null;
        shape.bmp.close();
      }
    };

    const onPointer = (e: PointerEvent) => {
      const r = el.getBoundingClientRect();
      pointer = e.type === "pointerleave" ? null : { x: e.clientX - r.left, y: e.clientY - r.top };
      schedule();
    };

    const off = session.onCursor((m) => {
      if (m.type === "pos") {
        pos = { x: m.x, y: m.y, visible: m.visible };
        // A report that differs from our move is judged once the echo window
        // closes, even if no further report arrives.
        const sent = session.lastMove;
        if (sent && !recheck) {
          const wait = sent.at + ECHO_GRACE_MS - performance.now();
          if (wait > 0)
            recheck = setTimeout(() => {
              recheck = null;
              schedule();
            }, wait + 1);
        }
        schedule();
        return;
      }
      want = m.id;
      // The agent re-sends the current shape now and then: decode each id once.
      if (use(m.id) || decoding.has(m.id) || m.png.length === 0) return;
      const id = m.id;
      decoding.add(id);
      createImageBitmap(new Blob([m.png as Uint8Array<ArrayBuffer>], { type: "image/png" }))
        .then((bmp) => {
          decoding.delete(id);
          if (closed) {
            bmp.close();
            return;
          }
          cache.set(id, {
            bmp,
            hotX: m.hotX,
            hotY: m.hotY,
            w: m.w || bmp.width,
            h: m.h || bmp.height,
          });
          if (want === id) use(id);
          evict();
        })
        .catch(() => decoding.delete(id));
    });
    el.addEventListener("pointermove", onPointer);
    el.addEventListener("pointerdown", onPointer);
    el.addEventListener("pointerleave", onPointer);
    document.addEventListener("pointerlockchange", schedule);
    const ro = new ResizeObserver(schedule);
    ro.observe(el);
    return () => {
      closed = true;
      off();
      el.removeEventListener("pointermove", onPointer);
      el.removeEventListener("pointerdown", onPointer);
      el.removeEventListener("pointerleave", onPointer);
      document.removeEventListener("pointerlockchange", schedule);
      ro.disconnect();
      if (raf) cancelAnimationFrame(raf);
      if (recheck) clearTimeout(recheck);
      el.style.cursor = "";
      redraw.current = () => undefined;
      for (const s of cache.values()) s.bmp.close();
      cache.clear();
    };
  }, [session, video, surface]);

  useEffect(() => redraw.current(), [active, nativeWidth]);

  return (
    <canvas
      ref={canvas}
      className="pointer-events-none invisible absolute top-0 left-0 will-change-transform"
      aria-hidden="true"
    />
  );
}
