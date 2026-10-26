import { useEffect, useRef } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { Icon } from "../components/icons";
import type { Combo } from "../lib/combos";
import { combosFor } from "../lib/combos";

const LABEL = "Send keys (Ctrl+Alt+Del, Alt+Tab…)";

/**
 * The Keys button with its dropdown right under it: the first combination
 * is under the pointer and has focus (Enter sends it), arrows move, Escape or
 * a click elsewhere closes. That click does not reach the remote screen.
 */
export function KeysButton({
  os,
  open,
  disabled,
  align,
  onToggle,
  onSend,
  onClose,
  icon = "keyboard",
}: {
  os: string | null;
  open: boolean;
  disabled?: boolean;
  /** "end": the menu's right edge under the button's (toolbar); "center": centred (fullscreen bar). */
  align: "end" | "center";
  onToggle: () => void;
  onSend: (c: Combo) => void;
  onClose: () => void;
  /** Where the on-screen keyboard has its own button, this one shows ⌘. */
  icon?: "keyboard" | "keyboardCommand";
}) {
  const wrap = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const close = useRef(onClose);
  close.current = onClose;

  useEffect(() => {
    if (!open) return;
    // Taking focus blurs the remote keyboard, which releases held keys on the device.
    list.current?.querySelector("button")?.focus({ preventScroll: true });
    const away = (e: PointerEvent) => {
      if (wrap.current?.contains(e.target as Node)) return;
      e.stopPropagation();
      close.current();
    };
    document.addEventListener("pointerdown", away, true);
    return () => document.removeEventListener("pointerdown", away, true);
  }, [open]);

  const onKeyDown = (e: ReactKeyboardEvent) => {
    const items = [...(list.current?.querySelectorAll("button") ?? [])];
    const at = items.indexOf(document.activeElement as HTMLButtonElement);
    const step = e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
    if (step) {
      e.preventDefault();
      items[(at + step + items.length) % items.length]?.focus();
    } else if (e.key === "Escape") {
      e.preventDefault();
      close.current();
    }
  };

  return (
    <div ref={wrap} className="relative">
      <button
        className={`btn btn-sm text-white/90 hover:bg-white/10 max-sm:px-2 ${open ? "bg-white/15" : ""}`}
        aria-label={LABEL}
        aria-haspopup="menu"
        aria-expanded={open}
        title={LABEL}
        onClick={onToggle}
        disabled={disabled}
      >
        <Icon name={icon} size={18} />
        <span className="hidden sm:inline">Keys</span>
      </button>
      {open && (
        <ul
          ref={list}
          role="menu"
          aria-label="Send keys"
          onKeyDown={onKeyDown}
          className={`absolute top-full z-50 mt-1 w-72 rounded-xl border border-white/10 bg-[#1e1f20] p-1 text-sm text-white shadow-xl ${
            align === "end" ? "right-0" : "left-1/2 -translate-x-1/2"
          }`}
        >
          {combosFor(os).map((c) => (
            <li key={c.label} role="none">
              <button
                role="menuitem"
                className="flex w-full items-baseline gap-3 rounded-lg px-2.5 py-1.5 text-left outline-none hover:bg-white/10 focus-visible:bg-white/10"
                onClick={() => onSend(c)}
              >
                <span className="min-w-[7rem] font-mono text-xs text-[#8ab4f8]">{c.label}</span>
                <span className="text-white/70">{c.hint}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
