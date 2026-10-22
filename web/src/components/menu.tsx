import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, ReactNode, RefObject } from "react";
import { createPortal } from "react-dom";
import { placePopover } from "../lib/geometry";
import { Icon } from "./icons";
import type { IconName } from "./icons";

/**
 * Floating panel next to an anchor, rendered in a portal so scrolling
 * containers and cards do not clip it. Closes on Escape, on a click outside
 * (which then does not reach whatever is under it), on scroll and resize.
 */
export function Popover({
  anchor,
  onClose,
  align = "start",
  className = "",
  children,
  ...rest
}: {
  anchor: RefObject<HTMLElement | null>;
  onClose: () => void;
  align?: "start" | "end";
  className?: string;
  children: ReactNode;
  role?: string;
  "aria-label"?: string;
  id?: string;
  onKeyDown?: (e: ReactKeyboardEvent<HTMLDivElement>) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const close = useRef(onClose);
  close.current = onClose;

  useLayoutEffect(() => {
    const a = anchor.current?.getBoundingClientRect();
    const el = ref.current;
    if (!a || !el) return;
    setPos(
      placePopover(
        { left: a.left, top: a.top, width: a.width, height: a.height },
        { width: el.offsetWidth, height: el.offsetHeight },
        { width: window.innerWidth, height: window.innerHeight },
        align,
      ),
    );
  }, [anchor, align]);

  useEffect(() => {
    const away = (e: PointerEvent) => {
      const t = e.target as Node;
      if (ref.current?.contains(t) || anchor.current?.contains(t)) return;
      e.stopPropagation();
      close.current();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        close.current();
      }
    };
    const moved = (e: Event) => {
      if (e.target instanceof Node && ref.current?.contains(e.target)) return;
      close.current();
    };
    document.addEventListener("pointerdown", away, true);
    document.addEventListener("keydown", key, true);
    window.addEventListener("scroll", moved, true);
    window.addEventListener("resize", moved);
    return () => {
      document.removeEventListener("pointerdown", away, true);
      document.removeEventListener("keydown", key, true);
      window.removeEventListener("scroll", moved, true);
      window.removeEventListener("resize", moved);
    };
  }, [anchor]);

  return createPortal(
    <div
      ref={ref}
      {...rest}
      className={`fixed z-[70] rounded-xl border border-outline bg-surface text-sm text-text shadow-xl ${className}`}
      style={pos ?? { left: -9999, top: -9999 }}
    >
      {children}
    </div>,
    document.body,
  );
}

export type MenuEntry =
  | {
      label: string;
      icon?: IconName;
      onSelect: () => void;
      disabled?: boolean;
      danger?: boolean;
      checked?: boolean;
      hint?: string;
    }
  | "separator";

/** Arrow keys, Home/End and first-letter typeahead over the items of a menu. */
export function menuKeys(e: ReactKeyboardEvent<HTMLElement>, container: HTMLElement | null) {
  const items = [
    ...(container?.querySelectorAll<HTMLButtonElement>("[role^=menuitem]:not(:disabled)") ?? []),
  ];
  if (!items.length) return;
  const at = items.indexOf(document.activeElement as HTMLButtonElement);
  let next: number | null = null;
  if (e.key === "ArrowDown") next = (at + 1) % items.length;
  else if (e.key === "ArrowUp") next = (at - 1 + items.length) % items.length;
  else if (e.key === "Home") next = 0;
  else if (e.key === "End") next = items.length - 1;
  else if (e.key.length === 1 && /\S/.test(e.key)) {
    const k = e.key.toLowerCase();
    const order = [...items.slice(at + 1), ...items.slice(0, at + 1)];
    const hit = order.find((b) => b.textContent?.trim().toLowerCase().startsWith(k));
    if (hit) next = items.indexOf(hit);
  }
  if (next === null) return;
  e.preventDefault();
  items[next]?.focus();
}

export function MenuItems({ items, onDone }: { items: MenuEntry[]; onDone: () => void }) {
  return (
    <>
      {items.map((it, i) =>
        it === "separator" ? (
          <div key={`sep${i}`} role="separator" className="my-1 border-t border-outline" />
        ) : (
          <button
            key={it.label}
            type="button"
            role={it.checked === undefined ? "menuitem" : "menuitemcheckbox"}
            aria-checked={it.checked}
            disabled={it.disabled}
            className={`flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left outline-none hover:bg-surface-2 focus-visible:bg-surface-2 disabled:opacity-40 ${
              it.danger ? "text-error" : ""
            }`}
            onClick={() => {
              onDone();
              it.onSelect();
            }}
          >
            {it.checked !== undefined ? (
              <Icon name={it.checked ? "check" : "blank"} size={18} className="text-primary" />
            ) : it.icon ? (
              <Icon name={it.icon} size={18} className={it.danger ? "" : "text-muted"} />
            ) : null}
            <span className="flex-1 truncate">{it.label}</span>
            {it.hint && <span className="text-xs text-muted">{it.hint}</span>}
          </button>
        ),
      )}
    </>
  );
}

/** A button with a dropdown menu. */
export function Menu({
  label,
  items,
  children,
  className = "btn-text btn-sm",
  align = "end",
  title,
}: {
  label: string;
  items: MenuEntry[];
  children: ReactNode;
  className?: string;
  align?: "start" | "end";
  title?: string;
}) {
  const [open, setOpen] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const id = useId();
  const close = useCallback(() => setOpen(false), []);
  useEffect(() => {
    if (open)
      list.current?.querySelector<HTMLButtonElement>("[role^=menuitem]:not(:disabled)")?.focus();
  }, [open]);
  return (
    <>
      <button
        ref={btn}
        type="button"
        className={className}
        aria-label={label}
        title={title ?? label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
      >
        {children}
      </button>
      {open && (
        <Popover
          anchor={btn}
          onClose={close}
          align={align}
          role="menu"
          aria-label={label}
          id={id}
          className="w-60 p-1"
          onKeyDown={(e) => {
            if (e.key === "Tab") close();
            menuKeys(e, list.current);
          }}
        >
          <div ref={list}>
            <MenuItems
              items={items}
              onDone={() => {
                close();
                btn.current?.focus({ preventScroll: true });
              }}
            />
          </div>
        </Popover>
      )}
    </>
  );
}
