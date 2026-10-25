import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { Icon } from "../components/icons";
import type { IconName } from "../components/icons";

export type MoreItem = {
  label: string;
  icon: IconName;
  onSelect: () => void;
  disabled?: boolean;
  /** A toggle: shows a check when on. */
  checked?: boolean;
  hint?: string;
};

/**
 * The session toolbar's overflow menu, dark like the stage whatever the app
 * theme. A click outside closes it and does not reach the remote screen.
 */
export function MoreMenu({ items, disabled }: { items: MoreItem[]; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    list.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    const away = (e: PointerEvent) => {
      if (wrap.current?.contains(e.target as Node)) return;
      e.stopPropagation();
      setOpen(false);
    };
    document.addEventListener("pointerdown", away, true);
    return () => document.removeEventListener("pointerdown", away, true);
  }, [open]);

  const onKeyDown = (e: ReactKeyboardEvent) => {
    const all = [...(list.current?.querySelectorAll<HTMLButtonElement>("button") ?? [])];
    const enabled = all.filter((b) => !b.disabled);
    const at = enabled.indexOf(document.activeElement as HTMLButtonElement);
    const step = e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
    if (step) {
      e.preventDefault();
      enabled[(at + step + enabled.length) % enabled.length]?.focus();
    } else if (e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
    }
  };

  return (
    <div ref={wrap} className="relative">
      <button
        className={`btn btn-sm px-2 text-white/90 hover:bg-white/10 ${open ? "bg-white/15" : ""}`}
        aria-label="More"
        aria-haspopup="menu"
        aria-expanded={open}
        title="More"
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name="moreVert" size={18} />
      </button>
      {open && (
        <div
          ref={list}
          role="menu"
          aria-label="More"
          onKeyDown={onKeyDown}
          className="absolute top-full right-0 z-50 mt-1 w-64 rounded-xl border border-white/10 bg-[#1e1f20] p-1 text-sm text-white shadow-xl"
        >
          {items.map((it) => (
            <button
              key={it.label}
              role={it.checked === undefined ? "menuitem" : "menuitemcheckbox"}
              aria-checked={it.checked}
              disabled={it.disabled}
              className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-white/10 focus-visible:bg-white/10 disabled:opacity-40 disabled:hover:bg-transparent"
              onClick={() => {
                setOpen(false);
                it.onSelect();
              }}
            >
              <Icon name={it.icon} size={18} className="text-white/60" />
              <span className="flex-1 truncate">{it.label}</span>
              {it.hint && <span className="text-xs text-white/50">{it.hint}</span>}
              {it.checked !== undefined && (
                <Icon
                  name="check"
                  size={16}
                  className={it.checked ? "text-[#8ab4f8]" : "invisible"}
                />
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
