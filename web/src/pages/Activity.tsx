import { useState } from "react";
import { api } from "../api/client";
import { AuditTable, SessionsTable } from "../components/tables";
import { PageHeader } from "../components/ui";
import { usePaged } from "../lib/hooks";

export function Activity() {
  const [tab, setTab] = useState<"sessions" | "audit">("sessions");
  const sessions = usePaged((before) => api.sessions({ before, limit: 25 }), []);
  const audit = usePaged((before) => api.audit({ before, limit: 50 }), []);
  return (
    <>
      <PageHeader
        title="Activity"
        subtitle="Every session and security event on your account and devices."
      />
      <section className="card">
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
              {t === "sessions" ? "Sessions" : "Audit log"}
            </button>
          ))}
        </div>
        {tab === "sessions" ? <SessionsTable p={sessions} /> : <AuditTable p={audit} />}
      </section>
    </>
  );
}
