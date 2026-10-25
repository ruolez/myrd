import { useState } from "react";
import type { DeviceId } from "../api/types";
import { useDeviceLabels } from "../app/directory";
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
  relayForced,
  onSelect,
  onClose,
}: {
  id: DeviceId;
  index: number;
  focused: boolean;
  relayForced: boolean;
  onSelect: () => void;
  onClose: () => void;
}) {
  const ws = useWorkspace();
  const session = ws.session(id);
  const snap = useSessionSnap(session);
  const device = useDeviceLabels().get(id);
  const name = snap?.deviceName || device?.name || "Device";
  const dot = phaseDot[snap?.phase ?? "preparing"];
  const transfers = snap?.files?.activeCount ?? 0;
  // The agent's relay address can reach the browser as a peer-reflexive
  // candidate, so trust the agent when it reports a relayed path.
  const relayed = snap?.phase === "connected" && (snap.streamState?.relay || snap.path === "relay");
  const status = relayed ? `${dot.label} · relay${relayForced ? " (forced)" : ""}` : dot.label;
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
        className={`flex items-center gap-2 rounded-lg py-1 pr-7 pl-1 text-left text-xs transition-colors ${
          focused ? "bg-white/[0.12]" : "text-white/70 hover:bg-white/[0.06]"
        } ${snap?.phase === "ended" ? "opacity-60" : ""}`}
        aria-current={focused ? "page" : undefined}
        title={`${name}: ${status}${shortcut}`}
        onClick={onSelect}
      >
        <LiveThumb
          session={session}
          os={device?.os}
          live={!focused}
          className={`h-7 w-12 rounded ${focused ? "" : "opacity-70"}`}
        />
        <span className="flex max-w-40 min-w-0 flex-col leading-tight">
          <span className={`truncate font-medium ${focused ? "text-white" : ""}`}>{name}</span>
          <span className="flex items-center gap-1.5 text-[11px] text-white/55">
            <span
              className={`h-1.5 w-1.5 shrink-0 rounded-full ${relayed ? "bg-[#c58af9]" : dot.cls}`}
            />
            {transfers > 0 ? (
              <>
                <Icon name="upload" size={12} /> {transfers}
              </>
            ) : (
              <span className="truncate">{status.split(":")[0]}</span>
            )}
          </span>
        </span>
      </button>
      <button
        type="button"
        className={`absolute top-1/2 right-1 -translate-y-1/2 rounded p-0.5 text-white/50 hover:bg-white/15 hover:text-white ${
          focused
            ? ""
            : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 pointer-coarse:opacity-100"
        }`}
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
    <nav aria-label="Open sessions" className="flex min-w-0 flex-1 items-center gap-1">
      <ul className="flex min-w-0 items-center gap-1 overflow-x-auto py-1">
        {st.order.map((id, i) => (
          <Tab
            key={id}
            id={id}
            index={i}
            focused={st.focused === id}
            relayForced={st.entries[id]?.forceRelay ?? false}
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
