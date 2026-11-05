// How terminals look: theme, text size, font, line spacing, cursor, and
// instant typing. A bottom sheet on phones, a popover on larger screens;
// every change applies at once to every terminal.

import { useEffect, useMemo, useRef } from "react";
import type { ReactNode } from "react";
import { Icon } from "../../components/icons";
import { MAX_FONT, MIN_FONT } from "../../lib/termGestures";
import {
  DEFAULT_APPEARANCE,
  LINE_HEIGHTS,
  setTermAppearance,
  useTermAppearance,
} from "./appearance";
import type { CursorShape } from "./appearance";
import { TERMINAL_FONTS, TERMINAL_THEMES } from "./theme";
import type { TermThemeId } from "./theme";

type Props = {
  sheet: boolean;
  fontSize: number;
  onFontSize: (n: number) => void;
  onClose: () => void;
};

const GENERIC = new Set(["monospace", "serif", "sans-serif", "ui-monospace"]);

/** Whether any named font of a family list is installed: text in it
 * measures differently from the generic fallbacks. */
function installed(family: string): boolean {
  const ctx = document.createElement("canvas").getContext("2d");
  if (!ctx) return true;
  const sample = "mmmmmmmmmmlli10OQ@#";
  const names = family
    .split(",")
    .map((n) => n.trim())
    .filter((n) => n && !GENERIC.has(n));
  return names.some((name) =>
    ["monospace", "serif", "sans-serif"].some((base) => {
      ctx.font = `32px ${base}`;
      const plain = ctx.measureText(sample).width;
      ctx.font = `32px ${name}, ${base}`;
      return ctx.measureText(sample).width !== plain;
    }),
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="px-4 py-2">
      <h3 className="pb-1.5 text-xs font-medium tracking-wide text-white/50 uppercase">{title}</h3>
      {children}
    </section>
  );
}

/** A row of choices, one picked. */
function Segmented<T extends string | number>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: readonly { value: T; name: string }[];
  onChange: (v: T) => void;
}) {
  return (
    <div role="radiogroup" aria-label={label} className="flex gap-1 rounded-xl bg-white/[0.06] p-1">
      {options.map((o) => (
        <button
          key={String(o.value)}
          role="radio"
          aria-checked={o.value === value}
          className={`min-h-9 flex-1 rounded-lg px-2 text-sm ${
            o.value === value
              ? "bg-white/15 font-medium text-white"
              : "text-white/70 hover:bg-white/10"
          }`}
          onClick={() => onChange(o.value)}
        >
          {o.name}
        </button>
      ))}
    </div>
  );
}

function Toggle({
  label,
  hint,
  checked,
  onChange,
}: {
  label: string;
  hint?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex min-h-11 cursor-pointer items-center gap-3">
      <span className="min-w-0 flex-1">
        <span className="block text-sm">{label}</span>
        {hint && <span className="block text-xs text-white/50">{hint}</span>}
      </span>
      <input
        type="checkbox"
        role="switch"
        className="peer sr-only"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span
        aria-hidden
        className="relative h-6 w-10 shrink-0 rounded-full bg-white/20 transition-colors peer-checked:bg-[#8ab4f8] peer-focus-visible:ring-2 peer-focus-visible:ring-white/60 after:absolute after:top-0.5 after:left-0.5 after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-transform peer-checked:after:translate-x-4"
      />
    </label>
  );
}

const CURSORS: { value: CursorShape; name: string }[] = [
  { value: "block", name: "Block" },
  { value: "bar", name: "Bar" },
  { value: "underline", name: "Underline" },
];

export function AppearancePanel({ sheet, fontSize, onFontSize, onClose }: Props) {
  const a = useTermAppearance();
  const box = useRef<HTMLDivElement>(null);
  const fonts = useMemo(
    () => TERMINAL_FONTS.filter((f) => f.id === "system" || f.id === a.font || installed(f.family)),
    // Measured once per opening; the chosen font always stays listed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  useEffect(() => {
    box.current?.querySelector<HTMLElement>("[aria-checked=true]")?.focus({ preventScroll: true });
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const body = (
    <div ref={box} className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 px-4 pt-3 pb-1">
        <h2 className="flex-1 text-base font-medium">Appearance</h2>
        <button
          className="btn btn-sm text-white/80 hover:bg-white/10"
          aria-label="Close"
          onClick={onClose}
        >
          <Icon name="close" size={18} />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain pb-[max(0.75rem,env(safe-area-inset-bottom))]">
        <Section title="Theme">
          <div
            role="radiogroup"
            aria-label="Theme"
            className="grid grid-cols-2 gap-2 min-[400px]:grid-cols-3"
          >
            {(Object.keys(TERMINAL_THEMES) as TermThemeId[]).map((id) => {
              const t = TERMINAL_THEMES[id].theme;
              const on = a.theme === id;
              return (
                <button
                  key={id}
                  role="radio"
                  aria-checked={on}
                  aria-label={TERMINAL_THEMES[id].name}
                  className={`overflow-hidden rounded-xl text-left ring-offset-2 ring-offset-[#232425] ${
                    on ? "ring-2 ring-[#8ab4f8]" : "ring-1 ring-white/15 hover:ring-white/35"
                  }`}
                  onClick={() => setTermAppearance({ theme: id })}
                >
                  <div
                    className="px-2.5 py-2 font-mono text-[11px] leading-snug"
                    style={{ background: t.background, color: t.foreground }}
                  >
                    <div>
                      <span style={{ color: t.green }}>~</span>{" "}
                      <span style={{ color: t.blue }}>$</span> ls
                    </div>
                    <div>
                      <span style={{ color: t.red }}>red</span>{" "}
                      <span style={{ color: t.yellow }}>yel</span>{" "}
                      <span style={{ color: t.magenta }}>mag</span>
                    </div>
                  </div>
                  <div className="truncate bg-black/30 px-2.5 py-1 text-xs text-white/85">
                    {TERMINAL_THEMES[id].name}
                  </div>
                </button>
              );
            })}
          </div>
        </Section>

        <Section title="Text size">
          <div className="flex items-center gap-2">
            <button
              className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-white/10 hover:bg-white/15 disabled:opacity-40"
              aria-label="Smaller text"
              disabled={fontSize <= MIN_FONT}
              onClick={() => onFontSize(fontSize - 1)}
            >
              <Icon name="zoomOut" size={18} />
            </button>
            <input
              type="range"
              aria-label="Text size"
              min={MIN_FONT}
              max={MAX_FONT}
              step={1}
              value={fontSize}
              onChange={(e) => onFontSize(Number(e.target.value))}
              className="min-w-0 flex-1 accent-[#8ab4f8]"
            />
            <button
              className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-white/10 hover:bg-white/15 disabled:opacity-40"
              aria-label="Larger text"
              disabled={fontSize >= MAX_FONT}
              onClick={() => onFontSize(fontSize + 1)}
            >
              <Icon name="zoomIn" size={18} />
            </button>
            <span className="w-11 shrink-0 text-right text-sm text-white/70 tabular-nums">
              {fontSize} px
            </span>
          </div>
        </Section>

        <Section title="Font">
          <div role="radiogroup" aria-label="Font" className="flex flex-wrap gap-1.5">
            {fonts.map((f) => (
              <button
                key={f.id}
                role="radio"
                aria-checked={a.font === f.id}
                className={`min-h-9 rounded-full px-3 text-sm ${
                  a.font === f.id
                    ? "bg-[#8ab4f8] font-medium text-[#131314]"
                    : "bg-white/10 text-white/85 hover:bg-white/15"
                }`}
                style={{ fontFamily: f.family }}
                onClick={() => setTermAppearance({ font: f.id })}
              >
                {f.name}
              </button>
            ))}
          </div>
        </Section>

        <Section title="Line spacing">
          <Segmented
            label="Line spacing"
            value={a.lineHeight}
            options={LINE_HEIGHTS}
            onChange={(v) => setTermAppearance({ lineHeight: v })}
          />
        </Section>

        <Section title="Cursor">
          <Segmented
            label="Cursor"
            value={a.cursor}
            options={CURSORS}
            onChange={(v) => setTermAppearance({ cursor: v })}
          />
          <Toggle
            label="Blink"
            checked={a.blink}
            onChange={(v) => setTermAppearance({ blink: v })}
          />
        </Section>

        <Section title="Typing">
          <Toggle
            label="Show typing instantly"
            hint="Draws what you type before the device echoes it back, on slow connections. Never at password prompts or in full-screen programs."
            checked={a.instantTyping}
            onChange={(v) => setTermAppearance({ instantTyping: v })}
          />
        </Section>

        <div className="px-4 pt-1 pb-2">
          <button
            className="btn btn-sm text-white/70 hover:bg-white/10"
            onClick={() => setTermAppearance(DEFAULT_APPEARANCE)}
          >
            Reset to defaults
          </button>
        </div>
      </div>
    </div>
  );

  if (sheet)
    return (
      <div
        className="absolute inset-0 z-40 flex flex-col justify-end bg-black/50"
        onClick={onClose}
      >
        <div
          role="dialog"
          aria-label="Appearance"
          className="flex max-h-[85%] flex-col rounded-t-2xl bg-[#232425] text-white shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <div className="mx-auto mt-2 h-1 w-9 shrink-0 rounded-full bg-white/25" aria-hidden />
          {body}
        </div>
      </div>
    );
  return (
    <>
      <div className="absolute inset-0 z-30" onClick={onClose} />
      <div
        role="dialog"
        aria-label="Appearance"
        className="absolute top-11 right-2 z-40 flex max-h-[min(40rem,calc(100%-3.5rem))] w-[min(26rem,calc(100%-1rem))] flex-col rounded-2xl bg-[#232425] text-white shadow-2xl ring-1 ring-white/10"
      >
        {body}
      </div>
    </>
  );
}
