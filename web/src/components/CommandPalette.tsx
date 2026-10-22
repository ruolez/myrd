import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon, OsIcon } from "./icons";
import type { IconName } from "./icons";
import { rankItems } from "../lib/palette";

export type PaletteItem = {
  id: string;
  group: string;
  label: string;
  /** Searched too, shown under the label. */
  detail?: string;
  keywords?: string[];
  icon?: IconName;
  os?: string;
  badge?: ReactNode;
  /** Enter. */
  run: () => void;
  /** Shift+Enter, with its hint. */
  alt?: { label: string; run: () => void };
  /** Ctrl/Cmd+Enter, with its hint. */
  mod?: { label: string; run: () => void };
  disabled?: boolean;
};

const GROUP_LIMIT = 8;

/** A searchable list of things to open or do (combobox + listbox). */
export function CommandPalette({ items, onClose }: { items: PaletteItem[]; onClose: () => void }) {
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const id = useId();

  const shown = useMemo(() => {
    const ranked = rankItems(
      items,
      q,
      (it) => [it.label, it.detail ?? "", ...(it.keywords ?? [])],
      200,
    );
    const perGroup = new Map<string, number>();
    return ranked.filter((it) => {
      const n = (perGroup.get(it.group) ?? 0) + 1;
      perGroup.set(it.group, n);
      return q.trim() !== "" || n <= GROUP_LIMIT;
    });
  }, [items, q]);
  // Keep groups together, in the order they first appear in the ranking.
  const ordered = useMemo(() => {
    const groups: string[] = [];
    for (const it of shown) if (!groups.includes(it.group)) groups.push(it.group);
    return groups.flatMap((g) => shown.filter((it) => it.group === g));
  }, [shown]);

  useEffect(() => setActive(0), [q]);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    input.current?.focus();
    return () => prev?.focus?.({ preventScroll: true });
  }, []);
  useEffect(() => {
    list.current?.querySelector(`[data-i="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const choose = (it: PaletteItem | undefined, how: "run" | "alt" | "mod") => {
    if (!it || it.disabled) return;
    const fn = how === "alt" ? it.alt?.run : how === "mod" ? it.mod?.run : it.run;
    if (!fn) return;
    onClose();
    fn();
  };

  let lastGroup = "";
  return (
    <div
      className="fixed inset-0 z-[80] flex items-start justify-center bg-black/40 p-4 pt-[12vh]"
      onMouseDown={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        className="card w-full max-w-xl overflow-hidden shadow-2xl"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-3 border-b border-outline px-4">
          <Icon name="search" size={20} className="text-muted" />
          <input
            ref={input}
            role="combobox"
            aria-expanded="true"
            aria-controls={id}
            aria-activedescendant={ordered.length ? `${id}-${active}` : undefined}
            aria-autocomplete="list"
            className="h-14 flex-1 bg-transparent text-base outline-none placeholder:text-muted"
            placeholder="Go to a device, folder, tag or page…"
            style={{ outline: "none" }}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setActive((a) => Math.min(a + 1, ordered.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive((a) => Math.max(a - 1, 0));
              } else if (e.key === "Enter") {
                e.preventDefault();
                choose(
                  ordered[active],
                  e.shiftKey ? "alt" : e.metaKey || e.ctrlKey ? "mod" : "run",
                );
              } else if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                onClose();
              }
            }}
          />
          <kbd className="rounded border border-outline px-1.5 text-xs text-muted">Esc</kbd>
        </div>
        <ul
          ref={list}
          id={id}
          role="listbox"
          aria-label="Results"
          className="max-h-[55vh] overflow-auto p-2"
        >
          {ordered.length === 0 && (
            <li className="px-3 py-6 text-center text-sm text-muted">Nothing matches “{q}”.</li>
          )}
          {ordered.map((it, i) => {
            const header = it.group !== lastGroup;
            lastGroup = it.group;
            return (
              <li key={`${it.group}:${it.id}`} role="presentation">
                {header && (
                  <div
                    className="px-3 pt-2 pb-1 text-xs font-medium tracking-wide text-muted uppercase"
                    role="presentation"
                  >
                    {it.group}
                  </div>
                )}
                <div
                  id={`${id}-${i}`}
                  data-i={i}
                  role="option"
                  aria-selected={i === active}
                  aria-disabled={it.disabled}
                  className={`flex cursor-pointer items-center gap-3 rounded-lg px-3 py-2 ${
                    i === active ? "bg-primary-soft" : ""
                  } ${it.disabled ? "opacity-50" : ""}`}
                  onMouseMove={() => setActive(i)}
                  onClick={(e) =>
                    choose(it, e.shiftKey ? "alt" : e.metaKey || e.ctrlKey ? "mod" : "run")
                  }
                >
                  <span className="text-muted">
                    {it.os !== undefined ? (
                      <OsIcon os={it.os} size={18} />
                    ) : (
                      <Icon name={it.icon ?? "chevronRight"} size={18} />
                    )}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm">{it.label}</span>
                    {it.detail && (
                      <span className="block truncate text-xs text-muted">{it.detail}</span>
                    )}
                  </span>
                  {it.badge}
                  {i === active && (it.alt || it.mod) && (
                    <span className="hidden shrink-0 text-xs text-muted sm:inline">
                      {it.alt && <>⇧↵ {it.alt.label}</>}
                      {it.alt && it.mod && " · "}
                      {it.mod && (
                        <>
                          {navigator.platform.startsWith("Mac") ? "⌘" : "Ctrl+"}↵ {it.mod.label}
                        </>
                      )}
                    </span>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
      </div>
    </div>
  );
}
