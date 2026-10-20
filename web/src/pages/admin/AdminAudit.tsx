import { useState } from "react";
import type { FormEvent } from "react";
import { api } from "../../api/client";
import { AuditTable } from "../../components/tables";
import { Field, PageHeader } from "../../components/ui";
import { usePaged } from "../../lib/hooks";

type Filters = { user_id: string; device_id: string; event: string; actor: string };
const empty: Filters = { user_id: "", device_id: "", event: "", actor: "" };

export function AdminAudit() {
  const [draft, setDraft] = useState<Filters>(empty);
  const [filters, setFilters] = useState<Filters>(empty);
  const p = usePaged((before) => api.admin.audit({ ...filters, before, limit: 50 }), [filters]);

  const apply = (e: FormEvent) => {
    e.preventDefault();
    setFilters({
      user_id: draft.user_id.trim(),
      device_id: draft.device_id.trim(),
      event: draft.event.trim(),
      actor: draft.actor,
    });
  };

  return (
    <>
      <PageHeader
        title="Audit log"
        subtitle="Append-only. Every login, enrollment, session and admin action."
      />
      <section className="card">
        <form
          className="grid gap-3 border-b border-outline p-4 sm:grid-cols-2 lg:grid-cols-5"
          onSubmit={apply}
        >
          <Field label="Event">
            <input
              className="input"
              placeholder="e.g. session.connected"
              value={draft.event}
              onChange={(e) => setDraft({ ...draft, event: e.target.value })}
            />
          </Field>
          <Field label="Actor">
            <select
              className="input"
              value={draft.actor}
              onChange={(e) => setDraft({ ...draft, actor: e.target.value })}
            >
              <option value="">Any</option>
              <option value="user">user</option>
              <option value="agent">agent</option>
              <option value="admin">admin</option>
              <option value="system">system</option>
            </select>
          </Field>
          <Field label="User ID">
            <input
              className="input font-mono"
              value={draft.user_id}
              onChange={(e) => setDraft({ ...draft, user_id: e.target.value })}
            />
          </Field>
          <Field label="Device ID">
            <input
              className="input font-mono"
              value={draft.device_id}
              onChange={(e) => setDraft({ ...draft, device_id: e.target.value })}
            />
          </Field>
          <div className="flex items-end gap-2">
            <button className="btn-primary">Apply</button>
            <button
              type="button"
              className="btn-text"
              onClick={() => {
                setDraft(empty);
                setFilters(empty);
              }}
            >
              Clear
            </button>
          </div>
        </form>
        <AuditTable p={p} showUser />
      </section>
    </>
  );
}
