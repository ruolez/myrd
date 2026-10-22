import { useState } from "react";
import type { DeviceId } from "../api/types";
import { useDirectory } from "../app/directory";
import { useSessionSnap, useWorkspace, useWorkspaceState } from "../app/workspace";
import { Icon } from "../components/icons";
import type { Phase } from "./RemoteSession";
import { LiveThumb } from "./LiveThumb";

const TAB_DRAG = "application/x-remotectl-session";

export const phaseDot: Record<Phase, { cls: string; label: string }> = {
  preparing: { cls: "bg-white/40", label: "Starting" },
  creating: { cls: "bg-white/40", label: "Starting" },
  awaiting_approval: {
    cls: "bg-[#f9ab00] animate-pulse",
    label: "Waiting for approval on the device",
  },
  connecting: { cls: "bg-[#f9ab00]", label: "Connecting" },
  connected: { cls: "bg-[#81c995]", label: "Live" },
  reconnecting: { cls: "bg-[#f9ab00] animate-pulse", label: "Reconnecting" },
  pin_mismatch: { cls: "bg-[#f28b82]", label: "Refused: device identity changed" },
  ended: { cls: "bg-white/30", label: "Ended" },
};

function Tab({
  id,
  index,
  focused,
  onSelect,
  onClose,
}: {
  id: DeviceId;
  index: number;
  focused: boolean;
  onSelect: () => void;
  onClose: () => void;
}) {
  const ws = useWorkspace();
  const { devices } = useDirectory();
  const session = ws.session(id);
  const snap = useSessionSnap(session);
  const device = devices.find((d) => d.id === id);
  const name = snap?.deviceName || device?.name || "Device";
  const dot = phaseDot[snap?.phase ?? "preparing"];
  const transfers = snap?.files?.activeCount ?? 0;
  const [over, setOver] = useState(false);
  const shortcut = index < 9 ? ` (Ctrl+Alt+Shift+${index + 1})` : "";

  return (
    <li
      className={`group relative flex shrink-0 items-center rounded-lg ${over ? "ring-2 ring-[#8ab4f8]" : ""}`}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData(TAB_DRAG, id);
        e.dataTransfer.effectAllowed = "move";
      }}
      onDragOver={(e) => {
        if (!e.dataTransfer.types.includes(TAB_DRAG)) return;
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        setOver(false);
        const from = e.dataTransfer.getData(TAB_DRAG) as DeviceId;
        if (from && from !== id) ws.reorder(from, index);
      }}
    >
      <button
        type="button"
        className={`flex items-center gap-2 rounded-lg py-1 pr-7 pl-1 text-left text-xs ${
          focused ? "bg-white/15 ring-2 ring-[#8ab4f8]" : "hover:bg-white/10"
        } ${snap?.phase === "ended" ? "opacity-60" : ""}`}
        aria-current={focused ? "page" : undefined}
        title={`${name}: ${dot.label}${shortcut}`}
        onClick={onSelect}
      >
        <LiveThumb session={session} os={device?.os} className="h-9 w-16 rounded" />
        <span className="flex max-w-36 min-w-0 flex-col">
          <span className="truncate font-medium text-white">{name}</span>
          <span className="flex items-center gap-1.5 text-white/60">
            <span className={`h-1.5 w-1.5 rounded-full ${dot.cls}`} />
            {transfers > 0 ? (
              <>
                <Icon name="upload" size={12} /> {transfers}
              </>
            ) : (
              dot.label.split(":")[0]
            )}
          </span>
        </span>
      </button>
      <button
        type="button"
        className="absolute top-1 right-1 rounded p-0.5 text-white/50 hover:bg-white/15 hover:text-white"
        aria-label={`Close ${name}`}
        title="Close (ends the session)"
        onClick={onClose}
      >
        <Icon name="close" size={14} />
      </button>
    </li>
  );
}

/** The open sessions of this tab, live, in their workspace order. */
export function SessionTabs({
  onSelect,
  onClose,
  onAdd,
}: {
  onSelect: (id: DeviceId) => void;
  onClose: (id: DeviceId) => void;
  onAdd: () => void;
}) {
  const st = useWorkspaceState();
  return (
    <nav aria-label="Open sessions" className="flex min-w-0 items-center gap-1">
      <ul className="flex min-w-0 items-center gap-1 overflow-x-auto py-1">
        {st.order.map((id, i) => (
          <Tab
            key={id}
            id={id}
            index={i}
            focused={st.focused === id}
            onSelect={() => onSelect(id)}
            onClose={() => onClose(id)}
          />
        ))}
      </ul>
      <button
        type="button"
        className="btn btn-sm shrink-0 text-white/80 hover:bg-white/10"
        aria-label="Open another device (Ctrl+Alt+Shift+K)"
        title="Open another device (Ctrl+Alt+Shift+K)"
        onClick={onAdd}
      >
        <Icon name="add" size={18} />
      </button>
    </nav>
  );
}
