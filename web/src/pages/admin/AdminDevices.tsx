import { useState } from "react";
import { api, errorMessage } from "../../api/client";
import { DeviceStatusBadge } from "../../components/devices";
import { Icon, OsIcon } from "../../components/icons";
import {
  EmptyState,
  ErrorBox,
  Loading,
  PageHeader,
  Pager,
  useConfirm,
  useToast,
} from "../../components/ui";
import { osName, timeAgo } from "../../lib/format";
import { usePaged } from "../../lib/hooks";

const filters = [
  { v: "", label: "All" },
  { v: "online", label: "Online" },
  { v: "offline", label: "Offline" },
  { v: "disabled", label: "Disabled" },
];

export function AdminDevices() {
  const toast = useToast();
  const confirm = useConfirm();
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const p = usePaged(
    (before) => api.admin.devices({ q: query, status, before, limit: 25 }),
    [query, status],
  );

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn();
      toast(ok, "success");
      await p.reload(true);
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  return (
    <>
      <PageHeader title="All devices" subtitle="Every enrolled agent across all users." />
      <section className="card">
        <div className="flex flex-wrap items-center gap-2 border-b border-outline p-3">
          <form
            className="flex flex-1 gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              setQuery(q.trim());
            }}
          >
            <input
              className="input"
              type="search"
              placeholder="Name, hostname or owner"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              aria-label="Search devices"
            />
            <button className="btn-tonal">
              <Icon name="search" size={18} />
            </button>
          </form>
          <div
            className="inline-flex rounded-full border border-outline p-1"
            role="radiogroup"
            aria-label="Status filter"
          >
            {filters.map((f) => (
              <button
                key={f.v}
                role="radio"
                aria-checked={status === f.v}
                onClick={() => setStatus(f.v)}
                className={`rounded-full px-3 py-1 text-xs ${status === f.v ? "bg-primary text-on-primary" : "hover:bg-surface-2"}`}
              >
                {f.label}
              </button>
            ))}
          </div>
        </div>
        {p.error && (
          <div className="p-4">
            <ErrorBox error={p.error} onRetry={() => void p.reload()} />
          </div>
        )}
        {p.loading && p.items.length === 0 ? (
          <Loading />
        ) : p.items.length === 0 ? (
          <EmptyState title="No devices" />
        ) : (
          <div className="overflow-x-auto">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Device</th>
                  <th>Owner</th>
                  <th>OS</th>
                  <th>Agent</th>
                  <th>Status</th>
                  <th>Last seen</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {p.items.map((d) => (
                  <tr key={d.id}>
                    <td>
                      <div className="font-medium">{d.name}</div>
                      <div className="text-xs text-muted">{d.hostname}</div>
                    </td>
                    <td>{d.owner_email ?? "—"}</td>
                    <td>
                      <span className="inline-flex items-center gap-1.5">
                        <OsIcon os={d.os} size={16} />
                        {osName(d.os)} {d.arch}
                      </span>
                    </td>
                    <td className="font-mono text-xs">{d.agent_version || "—"}</td>
                    <td>
                      <DeviceStatusBadge device={d} />
                    </td>
                    <td>{d.online ? "now" : timeAgo(d.last_seen_at)}</td>
                    <td className="text-right whitespace-nowrap">
                      {d.status === "disabled" ? (
                        <button
                          className="btn-text btn-sm"
                          onClick={() =>
                            void act(() => api.admin.updateDevice(d.id, false), "Device enabled")
                          }
                        >
                          Enable
                        </button>
                      ) : (
                        <button
                          className="btn-text btn-sm"
                          onClick={async () => {
                            if (
                              await confirm({
                                title: `Disable ${d.name}?`,
                                message:
                                  "Its agent is disconnected, live sessions end and it cannot sign in until enabled again.",
                                confirm: "Disable",
                                danger: true,
                              })
                            )
                              await act(
                                () => api.admin.updateDevice(d.id, true),
                                "Device disabled",
                              );
                          }}
                        >
                          Disable
                        </button>
                      )}
                      <button
                        className="btn-text btn-sm text-error"
                        onClick={async () => {
                          if (
                            await confirm({
                              title: `Remove ${d.name}?`,
                              message:
                                "The agent is signed out permanently and must be enrolled again.",
                              confirm: "Remove",
                              danger: true,
                            })
                          )
                            await act(() => api.admin.deleteDevice(d.id), "Device removed");
                        }}
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <Pager hasPrev={p.hasPrev} hasNext={p.hasNext} onPrev={p.prev} onNext={p.next} />
      </section>
    </>
  );
}
