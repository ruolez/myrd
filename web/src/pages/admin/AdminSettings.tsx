import { useEffect, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { api, errorMessage } from "../../api/client";
import type { AdminSettings } from "../../api/types";
import { ErrorBox, Field, Loading, PageHeader, Toggle, useToast } from "../../components/ui";
import { useAsync } from "../../lib/hooks";

export function AdminSettingsPage() {
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => api.admin.settings(), []);
  const [f, setF] = useState<AdminSettings | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (data) setF(data);
  }, [data]);

  if (loading && !data) return <Loading />;
  if (!f)
    return <ErrorBox error={error ?? "Could not load settings."} onRetry={() => void reload()} />;

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      setF(await api.admin.saveSettings(f));
      toast("Settings saved", "success");
    } catch (err) {
      toast(errorMessage(err), "error");
    } finally {
      setBusy(false);
    }
  };

  const dirty = JSON.stringify(f) !== JSON.stringify(data);

  return (
    <>
      <PageHeader title="System settings" />
      <form className="card max-w-2xl space-y-6 p-6" onSubmit={(e) => void save(e)}>
        <Row
          title="Open sign-up"
          description="Anyone who can reach the site may create an account. Off means admins create users."
        >
          <Toggle
            label="Open sign-up"
            checked={f.signup_allowed}
            onChange={(v) => setF({ ...f, signup_allowed: v })}
          />
        </Row>
        <Row
          title="Auto-accept pinned browsers by default"
          description="Initial value for newly enrolled devices. Owners can change it per device; unknown browsers always need on-device approval."
        >
          <Toggle
            label="Default auto-accept"
            checked={f.default_auto_accept}
            onChange={(v) => setF({ ...f, default_auto_accept: v })}
          />
        </Row>
        <Field
          label="Maximum session length (hours)"
          hint="The server ends sessions that run longer. 0 disables the limit."
        >
          <input
            className="input max-w-32"
            type="number"
            min={0}
            max={168}
            value={f.max_session_hours}
            onChange={(e) =>
              setF({ ...f, max_session_hours: Math.max(0, Number(e.target.value) || 0) })
            }
          />
        </Field>
        <Field label="macOS installer URL" hint="Shown on the Add device page.">
          <input
            className="input font-mono"
            type="url"
            placeholder="https://dl.example.com/RemoteAgent.pkg"
            value={f.download_macos_url}
            onChange={(e) => setF({ ...f, download_macos_url: e.target.value })}
          />
        </Field>
        <Field label="Windows installer URL">
          <input
            className="input font-mono"
            type="url"
            placeholder="https://dl.example.com/RemoteAgent.msi"
            value={f.download_windows_url}
            onChange={(e) => setF({ ...f, download_windows_url: e.target.value })}
          />
        </Field>
        <div className="flex justify-end gap-2">
          <button
            type="button"
            className="btn-text"
            disabled={!dirty}
            onClick={() => data && setF(data)}
          >
            Discard
          </button>
          <button className="btn-primary" disabled={busy || !dirty}>
            Save
          </button>
        </div>
      </form>
    </>
  );
}

function Row({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-6">
      <div>
        <div className="text-sm font-medium">{title}</div>
        <p className="text-sm text-muted">{description}</p>
      </div>
      {children}
    </div>
  );
}
