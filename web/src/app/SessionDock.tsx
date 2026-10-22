import { Link } from "react-router";
import type { DeviceId } from "../api/types";
import { Icon } from "../components/icons";
import { RailTip } from "../components/RailTip";
import { LiveThumb } from "../session/LiveThumb";
import { phaseDot } from "../session/SessionTabs";
import { useDirectory } from "./directory";
import { useSessionSnap, useWorkspace, useWorkspaceState } from "./workspace";

function DockRow({ id, index, compact }: { id: DeviceId; index: number; compact: boolean }) {
  const ws = useWorkspace();
  const { devices } = useDirectory();
  const session = ws.session(id);
  const snap = useSessionSnap(session);
  const device = devices.find((d) => d.id === id);
  const name = snap?.deviceName || device?.name || "Device";
  const dot = phaseDot[snap?.phase ?? "preparing"];
  const shortcut = index < 9 ? `Ctrl+Alt+Shift+${index + 1}` : "";
  if (compact)
    return (
      <li>
        <RailTip
          show
          label={
            <span className="flex flex-col">
              <span>{name}</span>
              <span className="font-normal opacity-70">
                {dot.label.split(":")[0]}
                {shortcut && ` · ${shortcut}`}
              </span>
            </span>
          }
        >
          <Link
            to={`/session/${id}`}
            className="relative mx-auto block w-fit rounded-lg p-1 hover:bg-surface-2"
            aria-label={`${name}: ${dot.label}`}
          >
            <LiveThumb session={session} os={device?.os} className="h-7 w-10 rounded" />
            <span
              className={`absolute right-0.5 bottom-0.5 h-2.5 w-2.5 rounded-full ring-2 ring-surface ${dot.cls}`}
            />
          </Link>
        </RailTip>
      </li>
    );
  return (
    <li className="group relative">
      <Link
        to={`/session/${id}`}
        className="flex items-center gap-2.5 rounded-xl p-1.5 pr-7 hover:bg-surface-2"
        title={`${name}: ${dot.label}${shortcut ? ` (${shortcut})` : ""}`}
      >
        <LiveThumb session={session} os={device?.os} className="h-9 w-[64px] shrink-0 rounded-md" />
        <span className="min-w-0">
          <span className="block truncate text-sm font-medium">{name}</span>
          <span className="flex items-center gap-1.5 text-xs text-muted">
            <span className={`h-1.5 w-1.5 rounded-full ${dot.cls}`} />
            {dot.label.split(":")[0]}
          </span>
        </span>
      </Link>
      <button
        type="button"
        className="absolute top-1/2 right-1 -translate-y-1/2 rounded p-1 text-muted opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-surface-2 hover:text-text"
        aria-label={`End session with ${name}`}
        title="End session"
        onClick={() => ws.close(id)}
      >
        <Icon name="close" size={16} />
      </button>
    </li>
  );
}

/** The sessions still connected in this tab, shown while browsing other pages. */
export function SessionDock({
  compact = false,
  open = true,
  onToggle,
}: {
  /** Thumbnails only, for the collapsed sidebar. */
  compact?: boolean;
  open?: boolean;
  onToggle?: () => void;
}) {
  const st = useWorkspaceState();
  if (!st.order.length) return null;
  if (compact)
    return (
      <section aria-label={`Open sessions (${st.order.length})`}>
        <ul className="space-y-1">
          {st.order.map((id, i) => (
            <DockRow key={id} id={id} index={i} compact />
          ))}
        </ul>
      </section>
    );
  return (
    <section aria-labelledby="dock-title">
      <button
        type="button"
        className="group mb-1 flex w-full items-center gap-2 rounded-lg px-4 py-1 text-left text-[11px] font-medium tracking-wide text-muted uppercase hover:text-text"
        aria-expanded={open}
        onClick={onToggle}
      >
        <span id="dock-title" className="flex-1">
          Open sessions
        </span>
        <span className="rounded-full bg-primary-soft px-1.5 text-[11px] text-primary tabular-nums">
          {st.order.length}
        </span>
        <Icon
          name="chevronDown"
          size={16}
          className={`transition-transform motion-reduce:transition-none ${open ? "" : "-rotate-90"}`}
        />
      </button>
      {open && (
        <ul className="space-y-0.5">
          {st.order.map((id, i) => (
            <DockRow key={id} id={id} index={i} compact={false} />
          ))}
        </ul>
      )}
    </section>
  );
}

/** A count badge for the mobile header. */
export function SessionCount() {
  const st = useWorkspaceState();
  if (!st.order.length) return null;
  const to = st.order[0];
  return (
    <Link
      to={`/session/${to}`}
      className="btn-tonal btn-sm ml-auto"
      aria-label={`${st.order.length} open session(s)`}
    >
      <Icon name="sessions" size={16} />
      {st.order.length}
    </Link>
  );
}
