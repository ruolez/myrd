import { useState } from "react";
import { api, errorMessage } from "../../api/client";
import { SessionsTable } from "../../components/tables";
import { PageHeader, useConfirm, useToast } from "../../components/ui";
import { useInterval, usePaged } from "../../lib/hooks";

const states = [
  { v: "", label: "All" },
  { v: "active", label: "Active" },
  { v: "pending", label: "Pending" },
  { v: "ended", label: "Ended" },
  { v: "failed", label: "Failed" },
];

export function AdminSessions() {
  const toast = useToast();
  const confirm = useConfirm();
  const [state, setState] = useState("");
  const p = usePaged((before) => api.admin.sessions({ state, before, limit: 25 }), [state]);
  useInterval(() => !p.hasPrev && void p.reload(true), 10_000);

  return (
    <>
      <PageHeader
        title="Sessions"
        subtitle="Session content is end-to-end encrypted; admins see metadata only and can end a session."
      />
      <section className="card">
        <div className="flex flex-wrap gap-2 border-b border-outline p-3">
          <div
            className="inline-flex rounded-full border border-outline p-1"
            role="radiogroup"
            aria-label="State filter"
          >
            {states.map((s) => (
              <button
                key={s.v}
                role="radio"
                aria-checked={state === s.v}
                onClick={() => setState(s.v)}
                className={`rounded-full px-3 py-1 text-xs ${state === s.v ? "bg-primary text-on-primary" : "hover:bg-surface-2"}`}
              >
                {s.label}
              </button>
            ))}
          </div>
        </div>
        <SessionsTable
          p={p}
          showUser
          actions={(s) =>
            s.state === "active" || s.state === "pending" ? (
              <button
                className="btn-text btn-sm text-error"
                onClick={async () => {
                  if (
                    !(await confirm({
                      title: "End this session?",
                      message: `${s.user_email} → ${s.device_name}. Both ends are told the session was terminated by an administrator.`,
                      confirm: "End session",
                      danger: true,
                    }))
                  )
                    return;
                  try {
                    await api.admin.endSession(s.id);
                    toast("Session ended", "success");
                    await p.reload(true);
                  } catch (e) {
                    toast(errorMessage(e), "error");
                  }
                }}
              >
                End
              </button>
            ) : null
          }
        />
      </section>
    </>
  );
}
