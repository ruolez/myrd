import { useEffect, useRef } from "react";
import { Icon } from "../components/icons";
import type { Combo } from "../lib/combos";
import { combosFor } from "../lib/combos";

/** Sends key combinations the controller's own OS would keep for itself. */
export function KeysMenu({
  os,
  onSend,
  onClose,
  className = "",
}: {
  os: string | null;
  onSend: (c: Combo) => void;
  onClose: () => void;
  className?: string;
}) {
  const root = useRef<HTMLDivElement>(null);
  // Taking focus blurs the remote keyboard, which releases held keys on the device.
  useEffect(() => {
    root.current?.focus({ preventScroll: true });
  }, []);

  return (
    <div
      ref={root}
      role="dialog"
      aria-label="Send keys"
      tabIndex={-1}
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      className={`absolute z-30 w-[min(20rem,calc(100%-1.5rem))] rounded-2xl border border-white/10 bg-[#1e1f20] p-3 text-sm text-white shadow-xl outline-none ${className}`}
    >
      <div className="mb-2 flex items-center gap-2 pl-1">
        <h2 className="font-medium">Send keys</h2>
        <button
          className="btn btn-sm ml-auto px-2 text-white/80 hover:bg-white/10"
          aria-label="Close send keys"
          onClick={onClose}
        >
          <Icon name="close" size={18} />
        </button>
      </div>
      <ul className="space-y-0.5">
        {combosFor(os).map((c) => (
          <li key={c.label}>
            <button
              className="flex w-full items-baseline gap-3 rounded-lg px-2 py-1.5 text-left hover:bg-white/10"
              onClick={() => onSend(c)}
            >
              <span className="min-w-[7.5rem] font-mono text-xs text-[#8ab4f8]">{c.label}</span>
              <span className="text-white/70">{c.hint}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
