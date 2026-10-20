import { Link } from "react-router";
import { api } from "../../api/client";
import type { AdminOverview } from "../../api/types";
import { SessionsBarChart, ShareBar } from "../../components/charts";
import { Icon } from "../../components/icons";
import { Badge, ErrorBox, Loading, PageHeader } from "../../components/ui";
import { dateTime, duration, pct, timeAgo } from "../../lib/format";
import { useAsync, useInterval } from "../../lib/hooks";

function Tile({
  label,
  value,
  sub,
  tone,
  to,
}: {
  label: string;
  value: string | number;
  sub?: string;
  tone?: "error" | "warning" | "success";
  to?: string;
}) {
  const body = (
    <>
      <div className="text-xs font-medium tracking-wide text-muted uppercase">{label}</div>
      <div
        className={`mt-1 text-3xl font-normal tabular-nums ${
          tone === "error"
            ? "text-error"
            : tone === "warning"
              ? "text-warning"
              : tone === "success"
                ? "text-success"
                : ""
        }`}
      >
        {value}
      </div>
      {sub && <div className="mt-1 text-xs text-muted">{sub}</div>}
    </>
  );
  return to ? (
    <Link to={to} className="card block p-4 transition-colors hover:border-primary">
      {body}
    </Link>
  ) : (
    <div className="card p-4">{body}</div>
  );
}

export function AdminOverviewPage() {
  const { data, error, loading, reload } = useAsync(() => api.admin.overview(), []);
  useInterval(() => void reload(true), 5000);

  if (loading && !data) return <Loading />;
  if (!data) return <ErrorBox error={error ?? "No data"} onRetry={() => void reload()} />;
  return <OverviewBody o={data} stale={error} />;
}

function OverviewBody({ o, stale }: { o: AdminOverview; stale: string | null }) {
  const relayShare = o.sessions.relay_share_24h;
  const paths = o.sessions.by_path_24h;
  return (
    <>
      <PageHeader
        title="Overview"
        subtitle={
          <span className="inline-flex flex-wrap items-center gap-2">
            Server {o.server.version} · {o.server.go_version} · up {duration(o.server.uptime_s)}
            {o.server.db_ok ? (
              <Badge tone="success" dot>
                database ok
              </Badge>
            ) : (
              <Badge tone="error" dot>
                database down
              </Badge>
            )}
            <span className="text-xs">refreshes every 5 s</span>
          </span>
        }
      />
      {stale && (
        <div className="mb-4">
          <ErrorBox error={`Showing the last good data — ${stale}`} />
        </div>
      )}

      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <Tile
          label="Agents online"
          value={o.devices.online}
          sub={`of ${o.devices.total} devices · ${o.devices.disabled} disabled`}
          to="/admin/devices"
        />
        <Tile
          label="Active sessions"
          value={o.sessions.active}
          sub={`${o.sessions.pending} pending`}
          tone={o.sessions.active ? "success" : undefined}
          to="/admin/sessions"
        />
        <Tile
          label="Sessions today"
          value={o.sessions.today}
          sub={`avg ${duration(o.sessions.avg_duration_s_24h)} (24 h)`}
        />
        <Tile
          label="Relay share 24 h"
          value={pct(relayShare)}
          sub={relayShare > 0.4 ? "above 40 % alert threshold" : "TURN-relayed sessions"}
          tone={relayShare > 0.4 ? "warning" : undefined}
        />
        <Tile
          label="Failed 24 h"
          value={o.sessions.failed_24h}
          tone={o.sessions.failed_24h ? "error" : undefined}
          to="/admin/sessions"
        />
        <Tile
          label="Browsers connected"
          value={o.ws.browsers}
          sub={`${o.ws.agents} agent sockets`}
        />
        <Tile
          label="Reconnects / min"
          value={o.ws.reconnects_per_min.toFixed(1)}
          sub={
            o.ws.reconnects_per_min > 50
              ? "flapping — check server/network"
              : "agent WebSocket reconnects"
          }
          tone={o.ws.reconnects_per_min > 50 ? "warning" : undefined}
        />
        <Tile
          label="Users"
          value={o.users.total}
          sub={`${o.users.admins} admins · ${o.users.disabled} disabled`}
          to="/admin/users"
        />
      </div>

      <div className="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-3 [&>*]:min-w-0">
        <section className="card p-5 lg:col-span-2">
          <h2 className="mb-4 font-medium">Sessions per hour · last 24 h</h2>
          <SessionsBarChart series={o.series} />
        </section>
        <section className="card space-y-6 p-5">
          <div>
            <h2 className="mb-3 font-medium">ICE path · 24 h</h2>
            <ShareBar
              parts={[
                { label: "host", value: paths.host ?? 0, color: "var(--c-success)" },
                { label: "srflx", value: paths.srflx ?? 0, color: "var(--c-primary)" },
                { label: "relay", value: paths.relay ?? 0, color: "var(--c-relay)" },
              ]}
            />
          </div>
          <div>
            <h2 className="mb-3 font-medium">Devices by OS</h2>
            <ShareBar
              parts={[
                { label: "macOS", value: o.devices.by_os.darwin ?? 0, color: "var(--c-primary)" },
                {
                  label: "Windows",
                  value: o.devices.by_os.windows ?? 0,
                  color: "var(--c-warning)",
                },
                { label: "Linux", value: o.devices.by_os.linux ?? 0, color: "var(--c-success)" },
              ]}
            />
          </div>
          <div className="text-xs text-muted">Server started {dateTime(o.server.started_at)}</div>
        </section>
      </div>

      <section className="card mt-6">
        <div className="flex items-center justify-between p-5 pb-2">
          <h2 className="font-medium">Recent events</h2>
          <Link to="/admin/audit" className="btn-text btn-sm">
            Audit log <Icon name="chevronRight" size={16} />
          </Link>
        </div>
        {o.recent_audit.length === 0 ? (
          <p className="px-5 pb-5 text-sm text-muted">No events yet.</p>
        ) : (
          <ul className="divide-y divide-outline">
            {o.recent_audit.map((e) => (
              <li
                key={e.id}
                className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-2.5 text-sm"
              >
                <span className="w-20 text-xs text-muted" title={dateTime(e.ts)}>
                  {timeAgo(e.ts)}
                </span>
                <span className="font-mono text-xs">{e.event}</span>
                <span className="text-muted">{e.user_email ?? e.actor}</span>
                {e.device_name && <span className="text-muted">· {e.device_name}</span>}
              </li>
            ))}
          </ul>
        )}
      </section>
    </>
  );
}
