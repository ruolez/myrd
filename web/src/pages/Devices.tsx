import { useCallback } from "react";
import { Link, useNavigate } from "react-router";
import { api } from "../api/client";
import type { Device } from "../api/types";
import { DeviceStatusBadge, useLiveDeviceStatus } from "../components/devices";
import { Icon, OsIcon } from "../components/icons";
import { EmptyState, ErrorBox, Loading, PageHeader } from "../components/ui";
import { osName, timeAgo } from "../lib/format";
import { useAsync, useNow } from "../lib/hooks";

export function Devices() {
  const { data, error, loading, reload, setData } = useAsync(() => api.devices.list(), []);
  const navigate = useNavigate();
  useNow(30_000);

  const setDevices = useCallback(
    (fn: (d: Device[]) => Device[]) => setData((cur) => (cur ? { items: fn(cur.items) } : cur)),
    [setData],
  );
  useLiveDeviceStatus(setDevices);

  const devices = data?.items ?? [];
  const sorted = [...devices].sort(
    (a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name),
  );

  return (
    <>
      <PageHeader
        title="Devices"
        subtitle={
          devices.length
            ? `${devices.filter((d) => d.online).length} of ${devices.length} online`
            : undefined
        }
        actions={
          <>
            <button className="btn-text" onClick={() => void reload()} aria-label="Refresh">
              <Icon name="refresh" size={18} />
            </button>
            <Link to="/devices/add" className="btn-primary">
              <Icon name="add" size={18} />
              Add device
            </Link>
          </>
        }
      />
      {error && <ErrorBox error={error} onRetry={() => void reload()} />}
      {loading && !data ? (
        <Loading />
      ) : devices.length === 0 && !error ? (
        <div className="card">
          <EmptyState
            title="No devices yet"
            action={
              <Link to="/devices/add" className="btn-primary">
                <Icon name="add" size={18} />
                Add your first device
              </Link>
            }
          >
            Install the agent on a Mac or Windows PC, or start the Docker demo device, then enroll
            it with a one-time token.
          </EmptyState>
        </div>
      ) : (
        <ul className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {sorted.map((d) => {
            const canConnect = d.online && d.status !== "disabled" && !d.busy;
            return (
              <li key={d.id} className="card flex flex-col gap-4 p-5">
                <div className="flex items-start gap-3">
                  <div
                    className={`rounded-xl p-2.5 ${d.online ? "bg-primary-soft text-primary" : "bg-surface-2 text-muted"}`}
                  >
                    <OsIcon os={d.os} size={24} />
                  </div>
                  <div className="min-w-0 flex-1">
                    <Link
                      to={`/devices/${d.id}`}
                      className="block truncate font-medium hover:underline"
                    >
                      {d.name}
                    </Link>
                    <div className="truncate text-xs text-muted">
                      {osName(d.os)} · {d.arch} · {d.hostname}
                    </div>
                  </div>
                  <DeviceStatusBadge device={d} />
                </div>
                <div className="flex items-center justify-between gap-2 text-xs text-muted">
                  <span>
                    {d.online ? `Agent ${d.agent_version}` : `Last seen ${timeAgo(d.last_seen_at)}`}
                  </span>
                  {d.permissions && (!d.permissions.screen || !d.permissions.input) && (
                    <span className="text-warning">Permissions missing</span>
                  )}
                </div>
                <div className="mt-auto flex gap-2">
                  <button
                    className="btn-primary flex-1"
                    disabled={!canConnect}
                    onClick={() => navigate(`/session/${d.id}`)}
                    title={
                      canConnect
                        ? "Start a remote session"
                        : d.busy
                          ? "Device is in another session"
                          : "Device is offline"
                    }
                  >
                    <Icon name="sessions" size={18} />
                    Connect
                  </button>
                  <Link to={`/devices/${d.id}`} className="btn-outline">
                    Details
                  </Link>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </>
  );
}
