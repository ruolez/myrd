// Captures pointer, wheel and keyboard input on the session stage and
// serializes it straight into the `input` data channel (PROTOCOL §5).

import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import { contentBox, normalizePoint } from "../lib/geometry";
import { hidUsage, KeyState } from "../lib/hid";
import {
  Button,
  encodeKey,
  encodeMouseButton,
  encodeMouseMove,
  encodeMouseMoveRel,
  encodeMouseWheel,
  encodeText,
} from "../lib/input";
import type { RemoteSession } from "./RemoteSession";

// DOM MouseEvent.button → protocol button.
const domButton: Record<number, number> = {
  0: Button.Left,
  1: Button.Middle,
  2: Button.Right,
  3: Button.Back,
  4: Button.Forward,
};

const WHEEL_LINE_PX = 16;
const WHEEL_PAGE_PX = 800;

type Refs = {
  session: RefObject<RemoteSession | null>;
  surface: RefObject<HTMLDivElement | null>;
  video: RefObject<HTMLVideoElement | null>;
  keyboard: RefObject<HTMLTextAreaElement | null>;
};

export function useRemoteInput(
  { session, surface, video, keyboard }: Refs,
  enabled: boolean,
  monitor: number,
) {
  const keys = useRef(new KeyState());
  const monitorRef = useRef(monitor);
  monitorRef.current = monitor;

  useEffect(() => {
    const el = surface.current;
    const kb = keyboard.current;
    if (!enabled || !el || !kb) return;
    const send = (b: Uint8Array) => session.current?.sendInput(b);
    const ks = keys.current;

    const releaseAll = () => {
      for (const usage of ks.releaseAll()) send(encodeKey(usage, false, 0));
      session.current?.releaseAllKeys();
    };

    const moveAbs = (e: PointerEvent | MouseEvent) => {
      const v = video.current;
      const r = el.getBoundingClientRect();
      const box = contentBox(r.width, r.height, v?.videoWidth ?? 0, v?.videoHeight ?? 0);
      const { x, y } = normalizePoint(e.clientX - r.left, e.clientY - r.top, box);
      send(encodeMouseMove(x, y, monitorRef.current));
    };

    const onMove = (e: PointerEvent) => {
      if (document.pointerLockElement === el) {
        if (e.movementX || e.movementY) send(encodeMouseMoveRel(e.movementX, e.movementY));
        return;
      }
      const events = typeof e.getCoalescedEvents === "function" ? e.getCoalescedEvents() : [];
      moveAbs(events.length ? (events[events.length - 1] as PointerEvent) : e);
    };

    const onDown = (e: PointerEvent) => {
      e.preventDefault();
      kb.focus({ preventScroll: true });
      if (document.pointerLockElement !== el) moveAbs(e);
      const b = domButton[e.button];
      if (b === undefined) return;
      try {
        el.setPointerCapture(e.pointerId);
      } catch {
        // pointer already gone
      }
      send(encodeMouseButton(b, true));
    };

    const onUp = (e: PointerEvent) => {
      e.preventDefault();
      const b = domButton[e.button];
      if (b !== undefined) send(encodeMouseButton(b, false));
    };

    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const k = e.deltaMode === 1 ? WHEEL_LINE_PX : e.deltaMode === 2 ? WHEEL_PAGE_PX : 1;
      send(encodeMouseWheel(e.deltaX * k, e.deltaY * k));
    };

    const onKey = (e: KeyboardEvent) => {
      const down = e.type === "keydown";
      if (e.isComposing || e.key === "Process" || e.keyCode === 229) return; // IME owns it
      const usage = hidUsage(e.code);
      e.preventDefault();
      e.stopPropagation();
      if (usage === undefined) {
        if (down && e.key.length > 0 && [...e.key].length === 1)
          for (const m of encodeText(e.key)) send(m);
        return;
      }
      if (!down && !ks.isDown(usage)) return;
      const mods = down ? ks.down(usage) : ks.up(usage);
      send(encodeKey(usage, down, mods));
    };

    const onCompositionEnd = (e: CompositionEvent) => {
      if (e.data) for (const m of encodeText(e.data)) send(m);
      kb.value = "";
    };
    const onInputEvt = (e: Event) => {
      // Non-IME text that slipped past keydown (e.g. dictation): forward and clear.
      if (!(e as InputEvent).isComposing && kb.value) {
        for (const m of encodeText(kb.value)) send(m);
        kb.value = "";
      }
    };

    const prevent = (e: Event) => e.preventDefault();
    const onHidden = () => {
      if (document.hidden) releaseAll();
    };

    el.addEventListener("pointermove", onMove);
    el.addEventListener("pointerdown", onDown);
    el.addEventListener("pointerup", onUp);
    el.addEventListener("pointercancel", onUp);
    el.addEventListener("wheel", onWheel, { passive: false });
    el.addEventListener("contextmenu", prevent);
    el.addEventListener("dragstart", prevent);
    kb.addEventListener("keydown", onKey);
    kb.addEventListener("keyup", onKey);
    kb.addEventListener("compositionend", onCompositionEnd);
    kb.addEventListener("input", onInputEvt);
    kb.addEventListener("blur", releaseAll);
    window.addEventListener("blur", releaseAll);
    document.addEventListener("visibilitychange", onHidden);
    kb.focus({ preventScroll: true });

    return () => {
      releaseAll();
      el.removeEventListener("pointermove", onMove);
      el.removeEventListener("pointerdown", onDown);
      el.removeEventListener("pointerup", onUp);
      el.removeEventListener("pointercancel", onUp);
      el.removeEventListener("wheel", onWheel);
      el.removeEventListener("contextmenu", prevent);
      el.removeEventListener("dragstart", prevent);
      kb.removeEventListener("keydown", onKey);
      kb.removeEventListener("keyup", onKey);
      kb.removeEventListener("compositionend", onCompositionEnd);
      kb.removeEventListener("input", onInputEvt);
      kb.removeEventListener("blur", releaseAll);
      window.removeEventListener("blur", releaseAll);
      document.removeEventListener("visibilitychange", onHidden);
    };
  }, [enabled, session, surface, video, keyboard]);
}
