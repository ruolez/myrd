import { Fragment, useCallback, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api, errorMessage } from "../api/client";
import type { Device, DevicePatch } from "../api/types";
import { useAuth } from "../app/auth";
import { useDirectory } from "../app/directory";
import {
  DeviceStatusBadge,
  DeviceUpdateBadge,
  UpdateButton,
  useLiveDeviceStatus,
  useLiveUpdates,
} from "../components/devices";
import { Icon, OsIcon } from "../components/icons";
import { AuditTable, SessionsTable } from "../components/tables";
import {
  Badge,
  ErrorBox,
  Loading,
  Meter,
  PageHeader,
  TagChip,
  Toggle,
  useConfirm,
  useToast,
} from "../components/ui";
import { folderPath } from "../lib/folders";
import { bytes, dateTime, osName, timeAgo } from "../lib/format";
import {
  diskLabel,
  diskSeverity,
  formatIdle,
  formatUptime,
  freeFraction,
  infoStale,
  memSeverity,
} from "../lib/health";
import { useAsync, useNow, usePaged } from "../lib/hooks";
import { updateBadge } from "../lib/update";
import { DeviceDialogs, NOTES_MAX, useOrgDialog } from "./devices/dialogs";

export function DeviceDetail() {
  return (
    <DeviceDialogs>
      <DeviceDetailPage />
    </DeviceDialogs>
  );
}

function DeviceDetailPage() {
  const { id = "" } = useParams();
  const dir = useDirectory();
  const dialog = useOrgDialog();
  const { config } = useAuth();
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
  const kick = useLiveUpdates(data ? [data.device] : [], () => void reload(true), setDevices);

  if (loading && !data) return <Loading />;
  if (error || !data)
    return <ErrorBox error={error ?? "Device not found."} onRetry={() => void reload()} />;
  // Organization edits go through the shared directory (dialogs, other tabs).
  const live = dir.devices.find((x) => x.id === data.device.id);
  const d = live
    ? {
        ...data.device,
        folder_id: live.folder_id,
        tag_ids: live.tag_ids,
        favorite: live.favorite,
        notes: live.notes,
        name: live.name,
        info: live.info ?? data.device.info,
        info_at: live.info_at ?? data.device.info_at,
      }
    : data.device;

  const save = async (patch: DevicePatch, quiet = false) => {
    try {
      const r = await api.devices.update(d.id, patch);
      setData({ device: r.device });
      dir.setDevices((list) => list.map((x) => (x.id === r.device.id ? r.device : x)));
      if (!quiet) toast("Saved", "success");
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };
  const path = folderPath(dir.folders, d.folder_id);
  const previewsOffered =
    (config?.features?.includes("previews") ?? false) && config?.screen_previews !== false;
  const deviceTags = d.tag_ids
    .map((t) => dir.tags.find((x) => x.id === t))
    .filter((t): t is NonNullable<typeof t> => !!t);

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
        title={
          <span className="inline-flex items-center gap-2">
            {d.name}
            {dir.organization && (
              <button
                className={`btn-text btn-sm px-1.5 ${d.favorite ? "text-warning" : "text-muted"}`}
                aria-pressed={d.favorite}
                aria-label={d.favorite ? "Remove from favorites" : "Add to favorites"}
                title={d.favorite ? "Favorite" : "Add to favorites"}
                onClick={() => void save({ favorite: !d.favorite }, true)}
              >
                <Icon name={d.favorite ? "star" : "starOutline"} size={22} />
              </button>
            )}
          </span>
        }
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
              <div className="text-sm font-medium">Ask every time</div>
              <p className="text-sm text-muted">
                Show the prompt on the device for every session, even for remembered browsers.
                Otherwise your first connection is allowed and remembered without a prompt, unless
                “Ask before new browsers connect” is turned on in the agent's menu on the device.
              </p>
            </div>
            <Toggle
              label="Ask every time"
              checked={!d.auto_accept}
              onChange={(v) => void save({ auto_accept: !v })}
            />
          </div>
          {previewsOffered && (
            <div className="flex items-start justify-between gap-4">
              <div>
                <div className="text-sm font-medium">Screen previews</div>
                <p className="text-sm text-muted">
                  Show a small picture of this device's screen on your dashboard, refreshed about
                  once a minute while it is open. Pictures are encrypted on the device for your
                  approved browsers; the server cannot see them. The device can turn them off too,
                  and sends none while “Ask every time” is on.
                </p>
              </div>
              <Toggle
                label="Screen previews"
                checked={d.previews}
                onChange={(v) => void save({ previews: v })}
              />
            </div>
          )}
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
                No browsers remembered yet. The device keeps the authoritative list.
              </p>
            )}
            <p className="mt-2 text-xs text-muted">
              To remove a browser everywhere, revoke its key in Settings → Browser keys.
            </p>
          </div>
        </section>

        <div className="space-y-6">
          {dir.organization && (
            <section className="card space-y-4 p-6" aria-labelledby="organize">
              <h2 id="organize" className="font-medium">
                Organize
              </h2>
              <div>
                <div className="label">Folder</div>
                <div className="flex items-center justify-between gap-2">
                  <span
                    className={`flex min-w-0 items-center gap-2 text-sm ${path.length ? "" : "text-muted"}`}
                  >
                    <Icon
                      name={path.length ? "folder" : "inbox"}
                      size={18}
                      className="shrink-0 text-muted"
                    />
                    <span className="truncate">{path.length ? path.join(" / ") : "No folder"}</span>
                  </span>
                  <button
                    className="btn-text btn-sm"
                    onClick={() => dialog({ kind: "move", ids: [d.id] })}
                  >
                    Move…
                  </button>
                </div>
              </div>
              <div>
                <div className="label">Tags</div>
                <div className="flex flex-wrap items-center gap-1.5">
                  {deviceTags.map((t) => (
                    <TagChip
                      key={t.id}
                      name={t.name}
                      color={t.color}
                      onRemove={() =>
                        void save({ tag_ids: d.tag_ids.filter((x) => x !== t.id) }, true)
                      }
                    />
                  ))}
                  <button
                    className="btn-text btn-sm"
                    onClick={() => dialog({ kind: "tags", ids: [d.id] })}
                  >
                    <Icon name="tag" size={16} />
                    {deviceTags.length ? "Edit" : "Add tags"}
                  </button>
                </div>
              </div>
              <NotesField
                key={d.notes}
                notes={d.notes}
                onSave={(notes) => void save({ notes }, true)}
              />
            </section>
          )}
          <section className="card space-y-4 p-6">
            <h2 className="font-medium">Agent</h2>
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
              <dt className="text-muted">Version</dt>
              <dd>
                {d.agent_version || "—"}
                {d.latest_version && d.latest_version !== d.agent_version && (
                  <span className="text-muted"> · latest {d.latest_version}</span>
                )}
              </dd>
              <dt className="text-muted">Updates</dt>
              <dd className="min-w-0">
                <DeviceUpdateBadge device={d} />
                {!updateBadge(d) && "—"}
                {d.update_at && (
                  <div className="text-xs text-muted">last attempt {timeAgo(d.update_at)}</div>
                )}
              </dd>
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
            <UpdateButton
              device={d}
              request={(force) => api.devices.requestUpdate(d.id, force)}
              onRequested={kick}
              className="btn-tonal w-full"
              label={d.latest_version ? `Update to ${d.latest_version}` : "Update now"}
            />
            <button className="btn-outline w-full text-error" onClick={() => void remove()}>
              Remove device
            </button>
          </section>
        </div>
      </div>

      {d.info && <HealthCard device={d} />}

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

function HealthCard({ device: d }: { device: Device }) {
  const now = useNow(15_000);
  const info = d.info;
  if (!info) return null;
  const stale = infoStale(d.info_at, d.online, now);
  const mem = info.mem_total > 0 ? info.mem_used / info.mem_total : 0;
  const rows: [string, string][] = [
    [
      "System",
      [info.os_name, info.os_version, info.os_build && `(${info.os_build})`]
        .filter(Boolean)
        .join(" "),
    ],
    ["Signed in", info.user === null ? "—" : info.user === "" ? "Nobody" : info.user],
    ["Activity", info.locked ? "Locked" : formatIdle(info.idle_s) || "—"],
    ["Up for", formatUptime(info.uptime_s)],
    [
      "CPU",
      `${info.cpu_pct === null ? "—" : `${Math.round(info.cpu_pct)}%`} · ${info.cpu_count} cores`,
    ],
  ];
  return (
    <section className="card mt-6 p-6" aria-labelledby="health">
      <div className="mb-4 flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="health" className="font-medium">
          Health
        </h2>
        <span className={`text-xs ${stale ? "text-warning" : "text-muted"}`}>
          {stale ? `Last report ${timeAgo(d.info_at, now)}` : `Updated ${timeAgo(d.info_at, now)}`}
        </span>
      </div>
      <div className={`grid gap-6 md:grid-cols-2 ${stale ? "opacity-70" : ""}`}>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
          {rows.map(([k, v]) => (
            <Fragment key={k}>
              <dt className="text-muted">{k}</dt>
              <dd className="min-w-0 truncate" title={v}>
                {v}
              </dd>
            </Fragment>
          ))}
        </dl>
        <div className="space-y-4">
          {info.mem_total > 0 && (
            <Meter
              label="Memory"
              value={mem}
              tone={memSeverity(info)}
              detail={`${bytes(info.mem_used)} used of ${bytes(info.mem_total)}`}
            />
          )}
          {info.disks.map((k) => (
            <Meter
              key={k.mount}
              label={`${diskLabel(k)}${k.fs ? ` · ${k.fs}` : ""}`}
              value={1 - freeFraction(k)}
              tone={diskSeverity(k)}
              detail={`${bytes(k.free)} free of ${bytes(k.total)}`}
            />
          ))}
        </div>
      </div>
    </section>
  );
}

/** Notes edited in place, saved when the field loses focus or on Ctrl/Cmd+Enter. */
function NotesField({ notes, onSave }: { notes: string; onSave: (notes: string) => void }) {
  const [draft, setDraft] = useState(notes);
  const commit = () => {
    if (draft.trim() !== notes) onSave(draft.trim());
  };
  return (
    <label className="block">
      <span className="label">Notes</span>
      <textarea
        className="input min-h-24"
        value={draft}
        maxLength={NOTES_MAX}
        placeholder="Who uses it, where it is, what to watch out for…"
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) e.currentTarget.blur();
        }}
      />
      <span className="mt-1 block text-xs text-muted">
        Saved when you leave the field. Searchable from the dashboard.
      </span>
    </label>
  );
}

function Perm({ ok }: { ok: boolean }) {
  return ok ? <Badge tone="success">granted</Badge> : <Badge tone="error">missing</Badge>;
}
