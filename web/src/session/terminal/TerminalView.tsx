// One terminal on screen: xterm.js fed by a Term, acked as it renders. On
// touch screens a gesture layer covers it: swipe scrolls (or, in full-screen
// programs, sends wheel / arrow keys), pinch sets the font size, tap raises
// the keyboard (or clicks, in programs that use the mouse), a long press
// selects a word and dragging extends the selection.

import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";
import { Terminal } from "@xterm/xterm";
import type { ITheme } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useEffect, useImperativeHandle, useRef, useState } from "react";
import type { PointerEvent as ReactPointerEvent, Ref } from "react";
import { Icon } from "../../components/icons";
import type { Hotkey } from "../../lib/shortcuts";
import { workspaceHotkey } from "../../lib/shortcuts";
import {
  cellAt,
  clickBytes,
  coast,
  pinchFont,
  scrollBytes,
  scrollLines,
  wordAt,
} from "../../lib/termGestures";
import type { MouseModes } from "../../lib/termGestures";
import { closedLine } from "../../lib/terminal";
import type { TermSize } from "../../lib/terminal";
import { pasteBytes } from "../../lib/termKeys";
import { holdDeadline, initialTouch, touchStep } from "../../lib/touch";
import type { TouchAction, TouchState } from "../../lib/touch";
import type { Pt } from "../../lib/zoom";
import type { Term } from "../TerminalChannel";
import { expire, NO_PREDICT, output, shown, typed } from "../../lib/termPredict";
import type { Predict } from "../../lib/termPredict";
import type { TermAppearance } from "./appearance";
import { fontFamily } from "./theme";
import { adoptXtermStyles } from "./xtermStyles";

export type TerminalViewHandle = {
  /** How the program wants input encoded right now. */
  modes(): MouseModes & { bracketed: boolean };
  /** Types text as a paste (bracketed when the program asked for it). */
  paste(text: string): void;
  /** The selected text, or "". */
  selection(): string;
  /** The last lines of the buffer as plain text (for native selection). */
  text(maxLines?: number): string;
  scrollToBottom(): void;
  focus(): void;
  /** The size that fits the view now. */
  fit(): TermSize | null;
  /** Finds the next (or previous) match of query; false when there is none. */
  find(query: string, back?: boolean): boolean;
  clearFind(): void;
};

export type FindResults = { index: number; count: number };

const FIND_DECORATIONS = {
  matchBackground: "#fdd66340",
  matchOverviewRuler: "#fdd663",
  activeMatchBackground: "#f9ab00",
  activeMatchColorOverviewRuler: "#f9ab00",
};

type Props = {
  term: Term;
  /** In view: measured, fitted and given input. Hidden views keep their buffer. */
  visible: boolean;
  touch: boolean;
  fontSize: number;
  onFontSize: (n: number) => void;
  onSize: (s: TermSize) => void;
  /** A tap that should raise the phone's keyboard. */
  onTap: () => void;
  onHotkey?: (h: Hotkey) => void;
  theme: ITheme;
  appearance: TermAppearance;
  /** Cmd+F / Ctrl+Shift+F. */
  onFind?: () => void;
  onFindResults?: (r: FindResults) => void;
  handleRef?: Ref<TerminalViewHandle>;
};

const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
const HAPTIC_MS = 8;

export function TerminalView({
  term,
  visible,
  touch,
  fontSize,
  onFontSize,
  onSize,
  onTap,
  onHotkey,
  theme,
  appearance,
  onFind,
  onFindResults,
  handleRef,
}: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  const xRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const searchRef = useRef<SearchAddon | null>(null);
  const sizeRef = useRef<TermSize | null>(null);
  const cb = useRef({
    onSize,
    onTap,
    onHotkey,
    onFontSize,
    visible,
    onFind,
    onFindResults,
    theme,
    appearance,
  });
  cb.current = {
    onSize,
    onTap,
    onHotkey,
    onFontSize,
    visible,
    onFind,
    onFindResults,
    theme,
    appearance,
  };
  const [bubble, setBubble] = useState<{ x: number; y: number; copy: boolean } | null>(null);
  // Local echo: typing drawn at the cursor until the device echoes it.
  const predict = useRef<Predict>(NO_PREDICT);
  const [guess, setGuess] = useState<Guess | null>(null);
  // Scrolled back from the latest output; fresh: more arrived meanwhile.
  const [away, setAway] = useState<{ fresh: boolean } | null>(null);

  // ---------- xterm lifetime ----------
  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const look = cb.current.appearance;
    const x = new Terminal({
      fontFamily: fontFamily(look.font),
      fontSize,
      lineHeight: look.lineHeight,
      theme: cb.current.theme,
      scrollback: 5000,
      cursorStyle: look.cursor,
      cursorBlink: look.blink,
      cursorInactiveStyle: touch ? "block" : "outline",
      macOptionIsMeta: true,
      allowProposedApi: true,
      smoothScrollDuration: 0,
      // Our gesture layer handles touch; xterm's own textarea stays out of
      // the way (focusing it would raise the keyboard).
      disableStdin: false,
    });
    const fit = new FitAddon();
    x.loadAddon(fit);
    const search = new SearchAddon();
    x.loadAddon(search);
    const offResults = search.onDidChangeResults((r) =>
      cb.current.onFindResults?.({ index: r.resultIndex, count: r.resultCount }),
    );
    searchRef.current = search;
    const unicode = new Unicode11Addon();
    x.loadAddon(unicode);
    x.unicode.activeVersion = "11";
    if (!touch)
      x.loadAddon(
        new WebLinksAddon((_e, uri) => {
          window.open(uri, "_blank", "noopener,noreferrer");
        }),
      );
    const unstyle = adoptXtermStyles(host);
    x.open(host);
    // WebGL draws every cell itself (no inline styles for CSP to block);
    // the DOM renderer is the fallback.
    let gl: WebglAddon | null = null;
    try {
      gl = new WebglAddon();
      gl.onContextLoss(() => gl?.dispose());
      x.loadAddon(gl);
    } catch {
      gl = null; // no WebGL2: DOM renderer
    }
    // WebGL sizes its canvas from devicePixelContentBoxSize; where a browser
    // reports that inconsistently with devicePixelRatio (emulated scale
    // factors) it draws nothing. Fall back to the DOM renderer then.
    const glCheck = setTimeout(() => {
      const c = [...host.querySelectorAll<HTMLCanvasElement>(".xterm-screen canvas")].find(
        (el) => !el.classList.contains("xterm-link-layer") && el.getContext("webgl2"),
      );
      if (gl && c && c.clientWidth > 0) {
        const want = Math.round(c.clientWidth * window.devicePixelRatio);
        if (Math.abs(c.width - want) > 2) {
          gl.dispose();
          gl = null;
        }
      }
    }, 400);
    if (touch && x.textarea) {
      // Phones type into the deck's own field (TermKeyboard). xterm focuses
      // its textarea on mousedown and keyup; there it would swallow the keys
      // (bypassing the key bar's Ctrl/Alt) and, read-only, raise no keyboard.
      const ta = x.textarea;
      ta.readOnly = true;
      ta.tabIndex = -1;
      ta.inputMode = "none";
      ta.setAttribute("aria-hidden", "true");
      ta.focus = () => undefined;
    }
    xRef.current = x;
    fitRef.current = fit;

    x.attachCustomKeyEventHandler((e) => {
      const h = workspaceHotkey(e, false);
      if (h) {
        if (e.type === "keydown") cb.current.onHotkey?.(h);
        return false;
      }
      const findKey = e.key.toLowerCase() === "f" && (isMac ? e.metaKey : e.ctrlKey && e.shiftKey);
      if (findKey && cb.current.onFind) {
        if (e.type === "keydown") cb.current.onFind();
        return false;
      }
      const copyKey = e.key.toLowerCase() === "c" && (isMac ? e.metaKey : e.ctrlKey && e.shiftKey);
      if (copyKey && x.hasSelection()) {
        if (e.type === "keydown") void navigator.clipboard?.writeText(x.getSelection());
        return false;
      }
      return true;
    });
    // Responses xterm generates (device attributes, mouse reports, keys)
    // go to the program like typing.
    const offs = [
      x.onData((d) => term.write(d)),
      x.onBinary((d) => term.write(Uint8Array.from(d, (c) => c.charCodeAt(0)))),
    ];
    const decoder = new TextDecoder();
    const showGuess = () => setGuess(guessAt(x, host, predict.current));
    const offData = term.onData((b) => {
      const text = decoder.decode(b, { stream: true });
      x.write(b, () => {
        term.consumed(b.length);
        if (predict.current === NO_PREDICT) return;
        predict.current = output(predict.current, text, performance.now());
        showGuess();
      });
    });
    const inputDecoder = new TextDecoder();
    const offInput = term.onInput((b) => {
      if (!cb.current.appearance.instantTyping) return;
      const text = inputDecoder.decode(b, { stream: true });
      const alt = x.buffer.active.type === "alternate";
      predict.current = typed(predict.current, text, performance.now(), alt);
      showGuess();
    });
    const expiry = setInterval(() => {
      if (predict.current === NO_PREDICT) return;
      const next = expire(predict.current, performance.now());
      if (next !== predict.current) {
        predict.current = next;
        showGuess();
      }
    }, 250);
    let awayRaf = 0;
    const checkAway = (wrote: boolean) => {
      if (awayRaf) return;
      awayRaf = requestAnimationFrame(() => {
        awayRaf = 0;
        const b = x.buffer.active;
        const back = b.viewportY < b.baseY;
        setAway((a) => (!back ? null : a && (a.fresh || !wrote) ? a : { fresh: wrote && !!a }));
      });
    };
    const offScroll = [
      x.onScroll(() => checkAway(false)),
      x.onWriteParsed(() => checkAway(true)),
      x.buffer.onBufferChange(() => checkAway(false)),
    ];
    const offEnd = term.onEnd((e) => {
      x.write(`\r\n\x1b[2m${closedLine(e.reason, e.exitCode)}\x1b[0m\r\n`);
    });
    return () => {
      clearTimeout(glCheck);
      offResults.dispose();
      searchRef.current = null;
      offData();
      offInput();
      clearInterval(expiry);
      predict.current = NO_PREDICT;
      offEnd();
      cancelAnimationFrame(awayRaf);
      for (const o of [...offs, ...offScroll]) o.dispose();
      x.dispose();
      unstyle();
      xRef.current = null;
      fitRef.current = null;
    };
    // The view is per Term; font size changes are applied below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [term, touch]);

  // ---------- size ----------
  const refit = () => {
    const x = xRef.current;
    const dims = fitRef.current?.proposeDimensions();
    if (!x || !dims || !Number.isFinite(dims.cols) || !Number.isFinite(dims.rows)) return null;
    const size = { cols: Math.max(2, dims.cols), rows: Math.max(1, dims.rows) };
    if (size.cols !== x.cols || size.rows !== x.rows) x.resize(size.cols, size.rows);
    const prev = sizeRef.current;
    if (!prev || prev.cols !== size.cols || prev.rows !== size.rows) {
      sizeRef.current = size;
      term.resize(size);
      cb.current.onSize(size);
    }
    return size;
  };

  useEffect(() => {
    const x = xRef.current;
    if (!x) return;
    x.options.fontSize = fontSize;
    if (visible) refit();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fontSize]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host || !visible) return;
    let t: ReturnType<typeof setTimeout> | null = null;
    const ro = new ResizeObserver(() => {
      if (t) clearTimeout(t);
      t = setTimeout(refit, 60);
    });
    ro.observe(host);
    refit();
    if (!touch) xRef.current?.focus();
    return () => {
      ro.disconnect();
      if (t) clearTimeout(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, term]);

  // ---------- handle ----------
  const modes = (): MouseModes & { bracketed: boolean } => {
    const x = xRef.current;
    return {
      tracking: x?.modes.mouseTrackingMode ?? "none",
      alt: x?.buffer.active.type === "alternate",
      appCursor: x?.modes.applicationCursorKeysMode ?? false,
      bracketed: x?.modes.bracketedPasteMode ?? false,
    };
  };
  useImperativeHandle(handleRef, () => ({
    modes,
    paste: (text) => {
      term.write(pasteBytes(text, modes().bracketed));
      toBottom();
    },
    selection: () => xRef.current?.getSelection() ?? "",
    text: (maxLines = 2000) => {
      const b = xRef.current?.buffer.active;
      if (!b) return "";
      const lines: string[] = [];
      for (let y = Math.max(0, b.length - maxLines); y < b.length; y++)
        lines.push(b.getLine(y)?.translateToString(true) ?? "");
      while (lines.length && !lines.at(-1)) lines.pop();
      return lines.join("\n");
    },
    scrollToBottom: () => toBottom(),
    focus: () => xRef.current?.focus(),
    fit: refit,
    find: (query, back = false) => {
      const sa = searchRef.current;
      if (!sa || !query) return false;
      const opts = { decorations: FIND_DECORATIONS };
      return back ? sa.findPrevious(query, opts) : sa.findNext(query, opts);
    },
    clearFind: () => {
      searchRef.current?.clearDecorations();
      xRef.current?.clearSelection();
    },
  }));

  useEffect(() => {
    const x = xRef.current;
    if (x) x.options.theme = theme;
  }, [theme]);

  useEffect(() => {
    const x = xRef.current;
    if (!x) return;
    x.options.fontFamily = fontFamily(appearance.font);
    x.options.lineHeight = appearance.lineHeight;
    x.options.cursorStyle = appearance.cursor;
    x.options.cursorBlink = appearance.blink;
    if (!appearance.instantTyping) {
      predict.current = NO_PREDICT;
      setGuess(null);
    }
    if (visible) refit();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appearance]);

  // ---------- touch ----------
  const g = useRef({
    s: initialTouch as TouchState,
    hold: null as ReturnType<typeof setTimeout> | null,
    last: null as Pt | null,
    down: false,
    dragged: false,
    carry: 0,
    pinchFrom: 0,
    fling: 0,
    anchor: null as { col: number; row: number } | null,
  });

  const screenRect = () => {
    const el = hostRef.current?.querySelector(".xterm-screen") ?? hostRef.current;
    return el?.getBoundingClientRect() ?? null;
  };
  const cellUnder = (p: Pt) => {
    const x = xRef.current;
    const r = screenRect();
    if (!x || !r) return null;
    return cellAt(p, r, x.cols, x.rows);
  };
  const cellHeight = () => {
    const x = xRef.current;
    const r = screenRect();
    return x && r ? r.height / x.rows : 16;
  };

  const scrollBy = (dy: number, at: Pt | null) => {
    const x = xRef.current;
    if (!x) return;
    const st = g.current;
    const r = scrollLines(st.carry, dy, cellHeight());
    st.carry = r.carry;
    if (!r.lines) return;
    const cell = (at && cellUnder(at)) ?? { col: 1, row: 1 };
    const bytes = scrollBytes(r.lines, cell, modes());
    if (bytes === null) x.scrollLines(r.lines);
    else if (bytes) term.write(bytes);
  };

  const stopFling = () => {
    if (g.current.fling) cancelAnimationFrame(g.current.fling);
    g.current.fling = 0;
  };
  /** To the latest output; a fling still coasting would scroll right back. */
  const toBottom = () => {
    stopFling();
    xRef.current?.scrollToBottom();
  };
  const startFling = (vy: number, at: Pt | null) => {
    stopFling();
    let v = vy;
    let prev = performance.now();
    const step = (now: number) => {
      const c = coast(v, Math.min(50, now - prev));
      prev = now;
      v = c.v;
      scrollBy(c.d, at);
      g.current.fling = Math.abs(v) > 0.02 ? requestAnimationFrame(step) : 0;
    };
    g.current.fling = requestAnimationFrame(step);
  };

  /** Selects from the anchor cell to the cell at p (buffer rows). */
  const selectTo = (p: Pt) => {
    const x = xRef.current;
    const a = g.current.anchor;
    const c = cellUnder(p);
    if (!x || !a || !c) return;
    const top = x.buffer.active.viewportY;
    const pa = a.row * x.cols + a.col;
    const pb = (top + c.row - 1) * x.cols + (c.col - 1);
    const start = Math.min(pa, pb);
    x.select(start % x.cols, Math.floor(start / x.cols), Math.abs(pb - pa) + 1);
  };

  const selectWord = (p: Pt) => {
    const x = xRef.current;
    const c = cellUnder(p);
    if (!x || !c) return;
    const row = x.buffer.active.viewportY + c.row - 1;
    const line = x.buffer.active.getLine(row)?.translateToString(false) ?? "";
    const [s, e] = wordAt(line, c.col - 1);
    x.select(s, row, e - s);
    g.current.anchor = { col: s, row };
    navigator.vibrate?.(HAPTIC_MS);
  };

  const showBubble = (p: Pt) => {
    const host = hostRef.current?.getBoundingClientRect();
    if (!host) return;
    setBubble({
      x: Math.min(Math.max(p.x - host.left, 90), host.width - 90),
      y: Math.max(p.y - host.top - 56, 4),
      copy: !!xRef.current?.hasSelection(),
    });
  };

  const apply = (out: TouchAction[]) => {
    const st = g.current;
    const x = xRef.current;
    if (!x) return;
    for (const a of out) {
      switch (a.t) {
        case "point":
          if (st.down && st.anchor) {
            st.dragged = true;
            selectTo(a.p);
          }
          st.last = a.p;
          break;
        case "button":
          if (a.button === "right" && !a.down && st.last) {
            // A long press released in place, or a two-finger tap.
            if (!x.hasSelection()) selectWord(st.last);
            showBubble(st.last);
          } else if (a.button === "left" && a.down) {
            st.down = true;
            st.dragged = false;
          } else if (a.button === "left" && !a.down) {
            st.down = false;
            if (st.dragged) {
              if (st.last) showBubble(st.last);
            } else if (st.last) {
              // A tap: clears a selection, clicks in mouse programs, raises the keyboard.
              setBubble(null);
              if (x.hasSelection()) x.clearSelection();
              const cell = cellUnder(st.last);
              const click = cell && clickBytes(cell, modes());
              if (click) term.write(click);
              cb.current.onTap();
            }
            st.anchor = null;
          }
          break;
        case "hold":
          if (st.last) {
            setBubble(null);
            selectWord(st.last);
          }
          break;
        case "scroll":
          setBubble(null);
          scrollBy(a.dy, st.last);
          break;
        case "fling":
          startFling(a.vy, st.last);
          break;
        case "view":
          if (!st.pinchFrom) st.pinchFrom = x.options.fontSize ?? fontSize;
          cb.current.onFontSize(pinchFont(st.pinchFrom, a.a0, a.b0, a.a, a.b));
          break;
        case "viewEnd":
          st.pinchFrom = 0;
          break;
        case "nudge":
          break;
      }
    }
  };

  const feed = (e: Parameters<typeof touchStep>[1]) => {
    const st = g.current;
    const { s, out } = touchStep(st.s, e, "direct");
    st.s = s;
    if (st.hold) clearTimeout(st.hold);
    st.hold = null;
    const due = holdDeadline(s);
    if (due !== null)
      st.hold = setTimeout(
        () => feed({ t: "hold", at: performance.now() }),
        Math.max(0, due - performance.now()),
      );
    apply(out);
  };

  const pt = (e: ReactPointerEvent) => ({ x: e.clientX, y: e.clientY });
  const onDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    stopFling();
    e.currentTarget.setPointerCapture(e.pointerId);
    if (g.current.s.k === "idle") g.current.last = pt(e);
    feed({ t: "down", id: e.pointerId, p: pt(e), at: performance.now() });
  };
  const onMove = (e: ReactPointerEvent<HTMLDivElement>) =>
    feed({ t: "move", id: e.pointerId, p: pt(e), at: performance.now() });
  const onUp = (e: ReactPointerEvent<HTMLDivElement>) =>
    feed({ t: "up", id: e.pointerId, p: pt(e), at: performance.now() });
  const onCancel = (e: ReactPointerEvent<HTMLDivElement>) =>
    feed({ t: "cancel", id: e.pointerId, at: performance.now() });

  useEffect(
    () => () => {
      stopFling();
      if (g.current.hold) clearTimeout(g.current.hold);
    },
    [],
  );
  useEffect(() => {
    if (!visible) setBubble(null);
  }, [visible]);

  const copy = () => {
    const text = xRef.current?.getSelection() ?? "";
    if (text) void navigator.clipboard?.writeText(text);
    xRef.current?.clearSelection();
    setBubble(null);
  };
  const paste = async () => {
    setBubble(null);
    const text = await navigator.clipboard?.readText().catch(() => "");
    if (text) term.write(pasteBytes(text, modes().bracketed));
  };

  return (
    <div
      className={`absolute inset-0 ${visible ? "" : "invisible"}`}
      style={{ background: theme.background }}
      aria-hidden={!visible}
    >
      {/* isolate: xterm's own z-indexes stay under the gesture layer. */}
      <div ref={hostRef} className="terminal-host absolute inset-0 isolate pt-1 pl-1.5" />
      {guess && (
        <div
          aria-hidden
          data-predicted={guess.text}
          className="pointer-events-none absolute z-[5] flex"
          style={{
            left: guess.left,
            top: guess.top,
            height: guess.cellH,
            fontFamily: fontFamily(appearance.font),
            fontSize,
            lineHeight: `${guess.cellH}px`,
            color: theme.foreground,
            background: theme.background,
          }}
        >
          {[...guess.text].map((c, i) => (
            <span key={i} className="inline-block text-center" style={{ width: guess.cellW }}>
              {c}
            </span>
          ))}
          <span
            className="inline-block opacity-70"
            style={{ width: guess.cellW, background: theme.cursor }}
          />
        </div>
      )}
      {touch && (
        <div
          className="absolute inset-0 z-10 touch-none select-none [-webkit-touch-callout:none]"
          onPointerDown={onDown}
          onPointerMove={onMove}
          onPointerUp={onUp}
          onPointerCancel={onCancel}
          // The compatibility mousedown after a tap would blur the keyboard field.
          onMouseDown={(e) => e.preventDefault()}
          onContextMenu={(e) => e.preventDefault()}
        />
      )}
      {away && visible && (
        <button
          type="button"
          aria-label="Jump to the latest output"
          title="Jump to the latest output"
          className="absolute right-3 bottom-3 z-20 flex h-11 w-11 items-center justify-center rounded-full bg-[#2d2e30]/95 text-white shadow-lg ring-1 ring-white/15 active:bg-[#3c3d3f]"
          // Keeps the keyboard field focused on phones.
          onMouseDown={(e) => e.preventDefault()}
          onPointerDown={(e) => e.preventDefault()}
          onClick={toBottom}
        >
          <Icon name="chevronDown" size={24} />
          {away.fresh && (
            <span className="absolute top-1.5 right-1.5 h-2 w-2 rounded-full bg-[#8ab4f8]" />
          )}
        </button>
      )}
      {bubble && (
        <div
          role="menu"
          onMouseDown={(e) => e.preventDefault()}
          className="absolute z-20 flex -translate-x-1/2 overflow-hidden rounded-xl bg-[#2d2e30] text-sm text-white shadow-lg ring-1 ring-white/10"
          style={{ left: bubble.x, top: bubble.y }}
        >
          {bubble.copy && (
            <button
              role="menuitem"
              className="flex min-h-11 items-center gap-1.5 px-4"
              onClick={copy}
            >
              <Icon name="copy" size={16} />
              Copy
            </button>
          )}
          <button
            role="menuitem"
            className="flex min-h-11 items-center gap-1.5 border-l border-white/10 px-4 first:border-0"
            onClick={() => void paste()}
          >
            <Icon name="paste" size={16} />
            Paste
          </button>
        </div>
      )}
    </div>
  );
}

/** Where the guessed typing goes: at the cursor, in cells, inside the host. */
type Guess = { text: string; left: number; top: number; cellW: number; cellH: number };

function guessAt(x: Terminal, host: HTMLElement, p: Predict): Guess | null {
  const text = shown(p);
  const buf = x.buffer.active;
  const screen = host.querySelector<HTMLElement>(".xterm-screen");
  if (!text || !screen || buf.type !== "normal" || buf.viewportY !== buf.baseY) return null;
  const cellW = screen.clientWidth / x.cols;
  const cellH = screen.clientHeight / x.rows;
  if (!cellW || !cellH) return null;
  const room = x.cols - buf.cursorX - 1; // keep the line's last cell for the cursor
  if (room <= 0) return null;
  const hostBox = host.getBoundingClientRect();
  const box = screen.getBoundingClientRect();
  return {
    text: text.slice(0, room),
    left: box.left - hostBox.left + buf.cursorX * cellW,
    top: box.top - hostBox.top + buf.cursorY * cellH,
    cellW,
    cellH,
  };
}
