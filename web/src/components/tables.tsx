import { Fragment, useState } from "react";
import type { ReactNode } from "react";
import type { AuditEvent, Session } from "../api/types";
import { bytes, dateTime, duration, timeAgo } from "../lib/format";
import { PathBadge, SessionStateBadge } from "./devices";
import { Badge, EmptyState, ErrorBox, Loading, Pager } from "./ui";

type Paged<T> = {
  items: T[];
  loading: boolean;
  error: string | null;
  reload: () => Promise<void>;
  hasPrev: boolean;
  hasNext: boolean;
  next: () => void;
  prev: () => void;
};

function Frame<T>({ p, empty, children }: { p: Paged<T>; empty: string; children: ReactNode }) {
  if (p.error)
    return (
      <div className="p-4">
        <ErrorBox error={p.error} onRetry={() => void p.reload()} />
      </div>
    );
  if (p.loading && p.items.length === 0) return <Loading />;
  if (p.items.length === 0) return <EmptyState title={empty} />;
  return (
    <>
      <div className="overflow-x-auto">{children}</div>
      <Pager hasPrev={p.hasPrev} hasNext={p.hasNext} onPrev={p.prev} onNext={p.next} />
    </>
  );
}

export function SessionsTable({
  p,
  showDevice = true,
  showUser = false,
  actions,
}: {
  p: Paged<Session>;
  showDevice?: boolean;
  showUser?: boolean;
  actions?: (s: Session) => ReactNode;
}) {
  return (
    <Frame p={p} empty="No sessions yet">
      <table className="data-table">
        <thead>
          <tr>
            <th>Started</th>
            {showDevice && <th>Device</th>}
            {showUser && <th>User</th>}
            <th>Browser</th>
            <th>State</th>
            <th>Path</th>
            <th>Duration</th>
            <th>Traffic</th>
            <th>IPs</th>
            {actions && <th />}
          </tr>
        </thead>
        <tbody>
          {p.items.map((s) => (
            <tr key={s.id}>
              <td className="whitespace-nowrap" title={dateTime(s.created_at)}>
                {timeAgo(s.created_at)}
              </td>
              {showDevice && <td>{s.device_name}</td>}
              {showUser && <td>{s.user_email}</td>}
              <td className="text-muted">{s.client_key_label ?? "—"}</td>
              <td>
                <div className="flex flex-col gap-0.5">
                  <SessionStateBadge state={s.state} />
                  {s.end_reason && <span className="text-xs text-muted">{s.end_reason}</span>}
                </div>
              </td>
              <td>
                <PathBadge path={s.ice_path} />
              </td>
              <td className="whitespace-nowrap">{duration(s.duration_s)}</td>
              <td className="text-xs whitespace-nowrap text-muted">
                ↑ {bytes(s.bytes_sent)} · ↓ {bytes(s.bytes_recv)}
              </td>
              <td className="font-mono text-xs text-muted">
                {s.client_ip ?? "—"}
                <br />
                {s.agent_ip ?? "—"}
              </td>
              {actions && <td className="text-right">{actions(s)}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </Frame>
  );
}

const actorTone = {
  user: "primary",
  agent: "success",
  admin: "warning",
  system: "neutral",
} as const;

export function AuditTable({ p, showUser = false }: { p: Paged<AuditEvent>; showUser?: boolean }) {
  const [open, setOpen] = useState<number | null>(null);
  return (
    <Frame p={p} empty="No events">
      <table className="data-table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Event</th>
            <th>Actor</th>
            {showUser && <th>User</th>}
            <th>Device</th>
            <th>IP</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {p.items.map((e) => (
            <Fragment key={e.id}>
              <tr>
                <td className="whitespace-nowrap" title={dateTime(e.ts)}>
                  {dateTime(e.ts)}
                </td>
                <td className="font-mono text-xs">{e.event}</td>
                <td>
                  <Badge tone={actorTone[e.actor] ?? "neutral"}>{e.actor}</Badge>
                </td>
                {showUser && <td>{e.user_email ?? "—"}</td>}
                <td>{e.device_name ?? "—"}</td>
                <td className="font-mono text-xs text-muted">{e.ip ?? "—"}</td>
                <td className="text-right">
                  {e.meta && Object.keys(e.meta).length > 0 && (
                    <button
                      className="btn-text btn-sm"
                      aria-expanded={open === e.id}
                      onClick={() => setOpen(open === e.id ? null : e.id)}
                    >
                      {open === e.id ? "Hide" : "Details"}
                    </button>
                  )}
                </td>
              </tr>
              {open === e.id && (
                <tr>
                  <td colSpan={showUser ? 7 : 6} className="bg-surface-2">
                    <pre className="overflow-x-auto font-mono text-xs">
                      {JSON.stringify({ session_id: e.session_id, ...e.meta }, null, 2)}
                    </pre>
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </table>
    </Frame>
  );
}
