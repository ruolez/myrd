// Captures pointer, wheel and keyboard input on the session stage and
// serializes it straight into the `input` and `move` data channels (PROTOCOL §5).

import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import { normalizePoint } from "../lib/geometry";
import { hidUsage, KeyState } from "../lib/hid";
import {
  Button,
  encodeKey,
  encodeMouseButton,
  encodeMouseMoveRel,
  encodeMouseWheel,
  encodeText,
} from "../lib/input";
import { isStreamMenuHotkey } from "../lib/stream";
import type { RemoteSession } from "./RemoteSession";
import { videoBox } from "./videoBox";

export type LocalHotkey = "streamMenu";

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
// Moves travel on a lossy channel; once the pointer rests this long, its
// final position is re-sent reliably so a lost last move cannot strand it.
const SETTLE_MS = 80;

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
  onHotkey?: (hotkey: LocalHotkey) => void,
) {
  const keys = useRef(new KeyState());
  const monitorRef = useRef(monitor);
  monitorRef.current = monitor;
  const hotkeyRef = useRef(onHotkey);
  hotkeyRef.current = onHotkey;

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

    let settle: ReturnType<typeof setTimeout> | null = null;
    const moveAbs = (e: PointerEvent | MouseEvent, reliable: boolean) => {
      const r = el.getBoundingClientRect();
      const box = videoBox(r, video.current);
      const { x, y } = normalizePoint(e.clientX - r.left, e.clientY - r.top, box);
      if (settle) clearTimeout(settle);
      settle = reliable
        ? null
        : setTimeout(() => {
            settle = null;
            session.current?.sendMove(x, y, monitorRef.current, true);
          }, SETTLE_MS);
      session.current?.sendMove(x, y, monitorRef.current, reliable);
    };

    const onMove = (e: PointerEvent) => {
      if (document.pointerLockElement === el) {
        if (e.movementX || e.movementY) send(encodeMouseMoveRel(e.movementX, e.movementY));
        return;
      }
      const events = typeof e.getCoalescedEvents === "function" ? e.getCoalescedEvents() : [];
      moveAbs(events.length ? (events[events.length - 1] as PointerEvent) : e, false);
    };

    const onDown = (e: PointerEvent) => {
      e.preventDefault();
      kb.focus({ preventScroll: true });
      // Reliable, so the click lands where the pointer is, not where a lost move left it.
      if (document.pointerLockElement !== el) moveAbs(e, true);
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
      if (e.type === "pointerup" && document.pointerLockElement !== el) moveAbs(e, true);
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
      // Local shortcuts never reach the remote; the S keyup is dropped below (S never went down).
      if (down && hotkeyRef.current && isStreamMenuHotkey(e)) {
        hotkeyRef.current("streamMenu");
        return;
      }
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
      if (settle) clearTimeout(settle);
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
