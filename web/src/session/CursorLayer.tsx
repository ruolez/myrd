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
} from "../lib/cursor";
import { contentBox, denormalizePoint } from "../lib/geometry";
import type { RemoteSession } from "./RemoteSession";

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
    const cache = new Map<number, Shape>();
    let current: Shape | null = null;
    let pos = { x: 0, y: 0, visible: false };
    let pointer: { x: number; y: number } | null = null; // over the stage, element coords
    let css: { shape: Shape; size: string; value: string | null } | null = null;
    let applied = "";
    let raf = 0;
    let gen = 0;
    let recheck: ReturnType<typeof setTimeout> | null = null;

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
      if (!current || !activeRef.current) {
        setCursor("");
        return;
      }
      const v = video.current;
      const box = contentBox(r.width, r.height, v?.videoWidth ?? 0, v?.videoHeight ?? 0);
      // Shapes are in native monitor pixels; the video may be encoded smaller.
      const remoteW = widthRef.current || v?.videoWidth || box.width;
      const scale = remoteW > 0 ? box.width / remoteW : 1;
      const remote =
        document.pointerLockElement === el ||
        !pointer ||
        followsRemote(session.lastMove, pos, performance.now(), echoTolerance(remoteW));
      const hardware = remote || !pos.visible ? null : cssCursor(current, scale, dpr);
      setCursor(hardware ?? "none");
      if (hardware || !pos.visible) return;
      const p = !remote && pointer ? pointer : denormalizePoint(pos.x, pos.y, box);
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
    redraw.current = schedule;

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
            schedule();
          }
        })
        .catch(() => undefined);
    });
    el.addEventListener("pointermove", onPointer);
    el.addEventListener("pointerdown", onPointer);
    el.addEventListener("pointerleave", onPointer);
    document.addEventListener("pointerlockchange", schedule);
    const ro = new ResizeObserver(schedule);
    ro.observe(c);
    return () => {
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
    };
  }, [session, video, surface]);

  useEffect(() => redraw.current(), [active, nativeWidth]);

  return (
    <canvas
      ref={canvas}
      className="pointer-events-none absolute inset-0 h-full w-full"
      aria-hidden="true"
    />
  );
}
