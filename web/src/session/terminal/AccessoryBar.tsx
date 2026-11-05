// The phone key bar above the keyboard: keys a phone keyboard lacks (Esc,
// Tab, sticky Ctrl/Alt, arrows, symbols), a drag-to-move arrow nub, the
// compose line and the keyboard toggle. Buttons never take focus, so the
// keyboard stays up while they are tapped.

import { useEffect, useRef } from "react";
import type {
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
  ReactNode,
} from "react";
import { Icon } from "../../components/icons";
import type { ArrowKey, ModState, NamedKey } from "../../lib/termKeys";

type Props = {
  ctrl: ModState;
  alt: ModState;
  onMod: (which: "ctrl" | "alt") => void;
  onKey: (k: NamedKey) => void;
  onText: (s: string) => void;
  keyboardOpen: boolean;
  onToggleKeyboard: () => void;
  composeOpen: boolean;
  onToggleCompose: () => void;
};

const SYMBOLS = [
  "|",
  "/",
  "-",
  "~",
  "_",
  "*",
  "&",
  ";",
  ":",
  ">",
  "<",
  "$",
  "=",
  '"',
  "'",
  "`",
  "\\",
  "[",
  "]",
  "{",
  "}",
];
const REPEAT_AFTER_MS = 380;
const REPEAT_EVERY_MS = 55;

/**
 * Keeps focus where it is (the hidden field): the keyboard must not close.
 * A touch moves focus on the compatibility mousedown after touchend, so that
 * one is cancelled too.
 */
const keepFocus = (e: ReactPointerEvent | ReactMouseEvent) => e.preventDefault();

function Key({
  label,
  children,
  wide,
  active,
  locked,
  onPress,
  repeat,
}: {
  label: string;
  children: ReactNode;
  wide?: boolean;
  active?: boolean;
  locked?: boolean;
  onPress: () => void;
  /** Auto-repeat while held (arrows). */
  repeat?: boolean;
}) {
  const t = useRef<{
    delay: ReturnType<typeof setTimeout> | null;
    every: ReturnType<typeof setInterval> | null;
  }>({
    delay: null,
    every: null,
  });
  const fired = useRef(false);
  const stop = () => {
    if (t.current.delay) clearTimeout(t.current.delay);
    if (t.current.every) clearInterval(t.current.every);
    t.current = { delay: null, every: null };
  };
  useEffect(() => stop, []);
  return (
    <button
      type="button"
      aria-label={label}
      aria-pressed={active}
      className={`flex h-9 shrink-0 snap-start items-center justify-center rounded-lg font-mono text-[15px] leading-none transition-colors select-none active:bg-white/30 ${
        wide ? "min-w-12 px-2 text-xs font-sans font-medium" : "min-w-[38px] px-1.5"
      } ${active ? (locked ? "bg-[#8ab4f8] text-[#131314]" : "bg-[#8ab4f8]/40 text-white") : "bg-white/[0.09] text-white"}`}
      onMouseDown={keepFocus}
      onPointerDown={(e) => {
        keepFocus(e);
        fired.current = false;
        if (!repeat) return;
        t.current.delay = setTimeout(() => {
          fired.current = true;
          onPress();
          t.current.every = setInterval(onPress, REPEAT_EVERY_MS);
        }, REPEAT_AFTER_MS);
      }}
      onPointerUp={() => {
        stop();
        if (!fired.current) onPress();
      }}
      onPointerCancel={stop}
      onPointerLeave={stop}
      onContextMenu={(e) => e.preventDefault()}
    >
      {children}
    </button>
  );
}

/** Drag from the nub to send arrows that way, faster the farther the finger goes. */
function ArrowNub({ onArrow }: { onArrow: (k: ArrowKey) => void }) {
  const st = useRef<{
    x: number;
    y: number;
    dir: ArrowKey | null;
    timer: ReturnType<typeof setTimeout> | null;
    dist: number;
  }>({
    x: 0,
    y: 0,
    dir: null,
    timer: null,
    dist: 0,
  });
  const stop = () => {
    if (st.current.timer) clearTimeout(st.current.timer);
    st.current.timer = null;
    st.current.dir = null;
  };
  const tick = () => {
    const s = st.current;
    if (!s.dir) return;
    onArrow(s.dir);
    const every = Math.max(35, 220 - s.dist * 3);
    s.timer = setTimeout(tick, every);
  };
  useEffect(() => stop, []);
  return (
    <button
      type="button"
      aria-label="Arrow keys: drag in a direction"
      title="Drag to move the cursor"
      className="flex h-9 w-11 shrink-0 touch-none items-center justify-center rounded-lg bg-white/[0.14] text-white select-none"
      onMouseDown={keepFocus}
      onPointerDown={(e) => {
        keepFocus(e);
        e.currentTarget.setPointerCapture(e.pointerId);
        st.current = { x: e.clientX, y: e.clientY, dir: null, timer: null, dist: 0 };
      }}
      onPointerMove={(e) => {
        const s = st.current;
        const dx = e.clientX - s.x;
        const dy = e.clientY - s.y;
        const d = Math.hypot(dx, dy);
        s.dist = d;
        if (d < 14) {
          if (s.dir) stop();
          return;
        }
        const dir: ArrowKey =
          Math.abs(dx) > Math.abs(dy) ? (dx > 0 ? "right" : "left") : dy > 0 ? "down" : "up";
        if (dir !== s.dir) {
          if (s.timer) clearTimeout(s.timer);
          s.dir = dir;
          tick();
        }
      }}
      onPointerUp={stop}
      onPointerCancel={stop}
      onContextMenu={(e) => e.preventDefault()}
    >
      <svg width="22" height="22" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
        <path d="M12 2l3 3h-2v4h-2V5H9l3-3zm0 20l-3-3h2v-4h2v4h2l-3 3zM2 12l3-3v2h4v2H5v2l-3-3zm20 0l-3 3v-2h-4v-2h4V9l3 3z" />
      </svg>
    </button>
  );
}

export function AccessoryBar({
  ctrl,
  alt,
  onMod,
  onKey,
  onText,
  keyboardOpen,
  onToggleKeyboard,
  composeOpen,
  onToggleCompose,
}: Props) {
  return (
    <div
      className="flex h-11 shrink-0 items-center gap-1 border-t border-white/10 bg-[#1b1c1d] px-1"
      role="toolbar"
      aria-label="Terminal keys"
    >
      <button
        type="button"
        aria-label={
          composeOpen ? "Close the command line" : "Type a command with autocorrect and dictation"
        }
        aria-pressed={composeOpen}
        className={`flex h-9 w-10 shrink-0 items-center justify-center rounded-lg ${composeOpen ? "bg-[#8ab4f8] text-[#131314]" : "bg-white/[0.09] text-white"}`}
        onPointerDown={keepFocus}
        onMouseDown={keepFocus}
        onClick={onToggleCompose}
      >
        <Icon name="edit" size={18} />
      </button>
      <div className="flex min-w-0 flex-1 snap-x items-center gap-1 overflow-x-auto overscroll-x-contain [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        <Key label="Escape" wide onPress={() => onKey("esc")}>
          esc
        </Key>
        <Key label="Tab" wide onPress={() => onKey("tab")}>
          tab
        </Key>
        <Key
          label={`Control (${ctrl === "locked" ? "locked" : ctrl === "armed" ? "on for the next key" : "off"})`}
          wide
          active={ctrl !== "off"}
          locked={ctrl === "locked"}
          onPress={() => onMod("ctrl")}
        >
          ctrl
        </Key>
        <Key
          label={`Alt (${alt === "locked" ? "locked" : alt === "armed" ? "on for the next key" : "off"})`}
          wide
          active={alt !== "off"}
          locked={alt === "locked"}
          onPress={() => onMod("alt")}
        >
          alt
        </Key>
        <Key label="Up" repeat onPress={() => onKey("up")}>
          ↑
        </Key>
        <Key label="Down" repeat onPress={() => onKey("down")}>
          ↓
        </Key>
        <Key label="Left" repeat onPress={() => onKey("left")}>
          ←
        </Key>
        <Key label="Right" repeat onPress={() => onKey("right")}>
          →
        </Key>
        {SYMBOLS.map((s) => (
          <Key key={s} label={s} onPress={() => onText(s)}>
            {s}
          </Key>
        ))}
        <Key label="Home" wide onPress={() => onKey("home")}>
          home
        </Key>
        <Key label="End" wide onPress={() => onKey("end")}>
          end
        </Key>
        <Key label="Page up" wide repeat onPress={() => onKey("pageUp")}>
          pg↑
        </Key>
        <Key label="Page down" wide repeat onPress={() => onKey("pageDown")}>
          pg↓
        </Key>
        {(["f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12"] as const).map(
          (f) => (
            <Key key={f} label={f.toUpperCase()} wide onPress={() => onKey(f)}>
              {f.toUpperCase()}
            </Key>
          ),
        )}
      </div>
      <ArrowNub onArrow={onKey} />
      <button
        type="button"
        aria-label={keyboardOpen ? "Hide keyboard" : "Show keyboard"}
        className="flex h-9 w-10 shrink-0 items-center justify-center rounded-lg bg-white/[0.09] text-white"
        onPointerDown={keepFocus}
        onMouseDown={keepFocus}
        onClick={onToggleKeyboard}
      >
        <Icon name={keyboardOpen ? "keyboardHide" : "keyboard"} size={20} />
      </button>
    </div>
  );
}
