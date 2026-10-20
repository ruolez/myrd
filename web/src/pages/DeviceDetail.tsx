import { useCallback, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api, errorMessage } from "../api/client";
import type { Device } from "../api/types";
import { DeviceStatusBadge, useLiveDeviceStatus } from "../components/devices";
import { Icon, OsIcon } from "../components/icons";
import { AuditTable, SessionsTable } from "../components/tables";
import {
  Badge,
  ErrorBox,
  Loading,
  PageHeader,
  Toggle,
  useConfirm,
  useToast,
} from "../components/ui";
import { dateTime, osName, timeAgo } from "../lib/format";
import { useAsync, usePaged } from "../lib/hooks";

export function DeviceDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const confirm = useConfirm();
  const toast = useToast();
  const { data, error, loading, reload, setData } = useAsync(() => api.devices.get(id), [id]);
  const [name, setName] = useState<string | null>(null);
  const [tab, setTab] = useState<"sessions" | "audit">("sessions");
  const sessions = usePaged((before) => api.sessions({ device_id: id, before, limit: 20 }), [id]);
  const audit = usePaged((before) => api.audit({ device_id: id, before, limit: 25 }), [id]);

  const setDevices = useCallback(
    (fn: (d: Device[]) => Device[]) =>
      setData((cur) => {
        if (!cur) return cur;
        const [next] = fn([cur.device]);
        return next ? { device: next } : cur;
      }),
    [setData],
  );
  useLiveDeviceStatus(
    setDevices,
    useCallback(() => navigate("/devices", { replace: true }), [navigate]),
  );

  if (loading && !data) return <Loading />;
  if (error || !data)
    return <ErrorBox error={error ?? "Device not found."} onRetry={() => void reload()} />;
  const d = data.device;

  const save = async (patch: { name?: string; auto_accept?: boolean }) => {
    try {
      const r = await api.devices.update(d.id, patch);
      setData({ device: r.device });
      toast("Saved", "success");
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  const remove = async () => {
    const ok = await confirm({
      title: `Remove ${d.name}?`,
      message:
        "The agent will be signed out and must be enrolled again with a new token. Its session history stays in the audit log.",
      confirm: "Remove device",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.devices.remove(d.id);
      navigate("/devices", { replace: true });
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  const canConnect = d.online && d.status !== "disabled" && !d.busy;
  const editing = name !== null;

  return (
    <>
      <PageHeader
        title={d.name}
        subtitle={
          <span className="inline-flex items-center gap-2">
            <OsIcon os={d.os} size={16} /> {osName(d.os)} · {d.arch} · {d.hostname}{" "}
            <DeviceStatusBadge device={d} />
          </span>
        }
        actions={
          <>
            <Link to="/devices" className="btn-text">
              <Icon name="back" size={18} />
              Devices
            </Link>
            <button
              className="btn-primary"
              disabled={!canConnect}
              onClick={() => navigate(`/session/${d.id}`)}
            >
              <Icon name="sessions" size={18} />
              Connect
            </button>
          </>
        }
      />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3 [&>*]:min-w-0">
        <section className="card space-y-5 p-6 lg:col-span-2">
          <h2 className="font-medium">Settings</h2>
          <div>
            <div className="label">Name</div>
            {editing ? (
              <form
                className="flex gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  const n = name.trim();
                  if (n) void save({ name: n }).then(() => setName(null));
                }}
              >
                <input
                  className="input"
                  value={name}
                  maxLength={64}
                  autoFocus
                  onChange={(e) => setName(e.target.value)}
                />
                <button className="btn-primary">Save</button>
                <button type="button" className="btn-text" onClick={() => setName(null)}>
                  Cancel
                </button>
              </form>
            ) : (
              <div className="flex items-center gap-3">
                <span>{d.name}</span>
                <button className="btn-text btn-sm" onClick={() => setName(d.name)}>
                  Rename
                </button>
              </div>
            )}
          </div>
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="text-sm font-medium">Auto-accept pinned browsers</div>
              <p className="text-sm text-muted">
                Browsers already approved on the device connect without a prompt. New browsers
                always need approval on the device's own screen.
              </p>
            </div>
            <Toggle
              label="Auto-accept pinned browsers"
              checked={d.auto_accept}
              onChange={(v) => void save({ auto_accept: v })}
            />
          </div>
          <div>
            <div className="mb-2 text-sm font-medium">Approved browsers</div>
            {d.pinned_clients && d.pinned_clients.length > 0 ? (
              <ul className="divide-y divide-outline rounded-xl border border-outline">
                {d.pinned_clients.map((p) => (
                  <li
                    key={p.client_key_id}
                    className="flex items-center justify-between px-3 py-2 text-sm"
                  >
                    <span>{p.label}</span>
                    <span className="text-xs text-muted">approved {timeAgo(p.approved_at)}</span>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-sm text-muted">
                No browsers approved yet. The device keeps the authoritative list.
              </p>
            )}
            <p className="mt-2 text-xs text-muted">
              To remove a browser everywhere, revoke its key in Settings → Browser keys.
            </p>
          </div>
        </section>

        <section className="card space-y-4 p-6">
          <h2 className="font-medium">Agent</h2>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-muted">Version</dt>
            <dd>{d.agent_version || "—"}</dd>
            <dt className="text-muted">Encoder</dt>
            <dd>{d.encoder ?? "—"}</dd>
            <dt className="text-muted">Last seen</dt>
            <dd>{d.online ? "now" : timeAgo(d.last_seen_at)}</dd>
            <dt className="text-muted">Enrolled</dt>
            <dd>{dateTime(d.created_at)}</dd>
            <dt className="text-muted">Screen</dt>
            <dd>{d.permissions ? <Perm ok={d.permissions.screen} /> : "—"}</dd>
            <dt className="text-muted">Input</dt>
            <dd>{d.permissions ? <Perm ok={d.permissions.input} /> : "—"}</dd>
          </dl>
          {d.monitors && d.monitors.length > 0 && (
            <div>
              <div className="mb-2 text-sm font-medium">Monitors</div>
              <ul className="space-y-1 text-sm">
                {d.monitors.map((m) => (
                  <li key={m.id} className="flex justify-between gap-2">
                    <span className="truncate">
                      {m.name || `Display ${m.id}`}{" "}
                      {m.primary && <Badge tone="primary">primary</Badge>}
                    </span>
                    <span className="text-muted">
                      {m.width}×{m.height}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          <button className="btn-outline w-full text-error" onClick={() => void remove()}>
            Remove device
          </button>
        </section>
      </div>

      <section className="card mt-6">
        <div className="flex gap-1 border-b border-outline px-3 pt-3" role="tablist">
          {(["sessions", "audit"] as const).map((t) => (
            <button
              key={t}
              role="tab"
              aria-selected={tab === t}
              onClick={() => setTab(t)}
              className={`border-b-2 px-4 py-2 text-sm font-medium ${
                tab === t
                  ? "border-primary text-primary"
                  : "border-transparent text-muted hover:text-text"
              }`}
            >
              {t === "sessions" ? "Session history" : "Audit log"}
            </button>
          ))}
        </div>
        {tab === "sessions" ? (
          <SessionsTable p={sessions} showDevice={false} />
        ) : (
          <AuditTable p={audit} />
        )}
      </section>
    </>
  );
}

function Perm({ ok }: { ok: boolean }) {
  return ok ? <Badge tone="success">granted</Badge> : <Badge tone="error">missing</Badge>;
}
