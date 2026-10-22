import { useState } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";

/**
 * A label beside an icon-only control of the collapsed sidebar. Rendered in a
 * portal so the rail's scrolling does not clip it; off when `show` is false.
 */
export function RailTip({
  label,
  show,
  children,
}: {
  label: ReactNode;
  show: boolean;
  children: ReactNode;
}) {
  const [at, setAt] = useState<DOMRect | null>(null);
  if (!show) return children;
  const place = (e: { currentTarget: HTMLElement }) =>
    setAt(e.currentTarget.getBoundingClientRect());
  return (
    <div
      onMouseEnter={place}
      onMouseLeave={() => setAt(null)}
      onFocus={place}
      onBlur={() => setAt(null)}
      onClick={() => setAt(null)}
    >
      {children}
      {at &&
        createPortal(
          <div
            role="tooltip"
            className="pointer-events-none fixed z-[80] -translate-y-1/2 rounded-lg bg-text px-2.5 py-1.5 text-xs font-medium whitespace-nowrap text-surface shadow-lg"
            style={{ left: at.right + 10, top: at.top + at.height / 2 }}
          >
            {label}
          </div>,
          document.body,
        )}
    </div>
  );
}
