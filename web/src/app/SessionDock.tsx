import { Link } from "react-router";
import type { DeviceId } from "../api/types";
import { Icon } from "../components/icons";
import { LiveThumb } from "../session/LiveThumb";
import { phaseDot } from "../session/SessionTabs";
import { useDirectory } from "./directory";
import { useSessionSnap, useWorkspace, useWorkspaceState } from "./workspace";

function DockRow({ id, index }: { id: DeviceId; index: number }) {
  const ws = useWorkspace();
  const { devices } = useDirectory();
  const session = ws.session(id);
  const snap = useSessionSnap(session);
  const device = devices.find((d) => d.id === id);
  const name = snap?.deviceName || device?.name || "Device";
  const dot = phaseDot[snap?.phase ?? "preparing"];
  return (
    <li className="group relative">
      <Link
        to={`/session/${id}`}
        className="flex items-center gap-2.5 rounded-xl p-1.5 pr-7 hover:bg-surface-2"
        title={`${name}: ${dot.label}${index < 9 ? ` (Ctrl+Alt+Shift+${index + 1})` : ""}`}
      >
        <LiveThumb
          session={session}
          os={device?.os}
          className="h-10 w-[72px] shrink-0 rounded-md"
        />
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
export function SessionDock() {
  const st = useWorkspaceState();
  if (!st.order.length) return null;
  return (
    <section aria-labelledby="dock-title">
      <div
        id="dock-title"
        className="mb-1 px-4 text-xs font-medium tracking-wide text-muted uppercase"
      >
        Open sessions · {st.order.length}
      </div>
      <ul className="space-y-0.5 px-1">
        {st.order.map((id, i) => (
          <DockRow key={id} id={id} index={i} />
        ))}
      </ul>
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
