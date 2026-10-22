import { useEffect, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { api, errorMessage } from "../../api/client";
import type { AdminSettings, DownloadLink, DownloadOS } from "../../api/types";
import { OsIcon } from "../../components/icons";
import {
  ErrorBox,
  Field,
  Loading,
  PageHeader,
  Toggle,
  useConfirm,
  useToast,
} from "../../components/ui";
import { bytes, timeAgo } from "../../lib/format";
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
      <ClientDownloads />
      <form className="card mt-6 max-w-2xl space-y-6 p-6" onSubmit={(e) => void save(e)}>
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
          title="Ask every time by default"
          description="Initial value for newly enrolled devices: prompt on the device for every session, even for remembered browsers. Owners can change it per device. Without it, a device allows and remembers the first connection from its owner's account, unless “Ask before new browsers connect” is on in the agent's menu on that device."
        >
          <Toggle
            label="Ask every time by default"
            checked={!f.default_auto_accept}
            onChange={(v) => setF({ ...f, default_auto_accept: !v })}
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
        <Field
          label="External macOS installer URL"
          hint="Used on the Add device page only when no installer is uploaded above."
        >
          <input
            className="input font-mono"
            type="url"
            placeholder="https://dl.example.com/RemoteAgent.pkg"
            value={f.download_macos_url}
            onChange={(e) => setF({ ...f, download_macos_url: e.target.value })}
          />
        </Field>
        <Field label="External Windows installer URL">
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

const downloadTargets: { os: DownloadOS; label: string; accept: string; agentOs: string }[] = [
  { os: "macos", label: "macOS", accept: ".pkg,.dmg,.zip", agentOs: "darwin" },
  { os: "windows", label: "Windows", accept: ".msi,.exe,.zip", agentOs: "windows" },
  { os: "windows7", label: "Windows 7", accept: ".msi,.exe,.zip", agentOs: "windows" },
];

function ClientDownloads() {
  const { data, error, reload } = useAsync(() => api.config(), []);
  const origin = data?.webauthn.origin ?? window.location.origin;
  return (
    <section className="card max-w-2xl space-y-5 p-6">
      <div>
        <h2 className="font-medium">Client downloads</h2>
        <p className="text-sm text-muted">
          Installers uploaded here are offered on the Add device page, at a link that works without
          signing in. Build the macOS app for this server with{" "}
          <code className="font-mono text-xs">SERVER_URL={origin}</code>; the Windows installer gets
          the server URL from the install command shown on Add device.
        </p>
      </div>
      {error && <ErrorBox error={error} onRetry={() => void reload()} />}
      {downloadTargets.map((t) => (
        <DownloadRow
          key={t.os}
          target={t}
          link={data?.downloads[t.os] ?? null}
          onChange={() => void reload()}
        />
      ))}
    </section>
  );
}

function DownloadRow({
  target,
  link,
  onChange,
}: {
  target: (typeof downloadTargets)[number];
  link: DownloadLink | null;
  onChange: () => void;
}) {
  const toast = useToast();
  const confirm = useConfirm();
  const input = useRef<HTMLInputElement>(null);
  const [progress, setProgress] = useState<number | null>(null);
  const file = link?.file ?? null;

  const upload = async (f: File) => {
    setProgress(0);
    try {
      await api.admin.uploadDownload(target.os, f, setProgress);
      toast(`${target.label} installer uploaded`, "success");
      onChange();
    } catch (err) {
      toast(errorMessage(err), "error");
    } finally {
      setProgress(null);
      if (input.current) input.current.value = "";
    }
  };

  const remove = async () => {
    if (
      !(await confirm({
        title: `Remove the ${target.label} installer?`,
        message: "The Add device page stops offering it. Installed agents are not affected.",
        confirm: "Remove",
        danger: true,
      }))
    )
      return;
    try {
      await api.admin.deleteDownload(target.os);
      toast(`${target.label} installer removed`, "success");
      onChange();
    } catch (err) {
      toast(errorMessage(err), "error");
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-4 rounded-xl border border-outline p-4">
      <OsIcon os={target.agentOs} size={28} />
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{target.label}</div>
        {progress !== null ? (
          <div className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-surface-2">
            <div
              className="h-full bg-primary transition-[width]"
              style={{ width: `${Math.round(progress * 100)}%` }}
            />
          </div>
        ) : file ? (
          <p className="truncate text-xs text-muted" title={`SHA-256 ${file.sha256}`}>
            {file.filename} · {bytes(file.size)} · uploaded {timeAgo(file.uploaded_at)}
          </p>
        ) : (
          <p className="text-xs text-muted">
            {link ? `External URL: ${link.url}` : "No installer uploaded"}
          </p>
        )}
      </div>
      <input
        ref={input}
        type="file"
        accept={target.accept}
        className="hidden"
        aria-label={`${target.label} installer file`}
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) void upload(f);
        }}
      />
      <div className="flex gap-2">
        {file && (
          <a className="btn-text btn-sm" href={link?.url} download={file.filename}>
            Download
          </a>
        )}
        {file && (
          <button
            type="button"
            className="btn-text btn-sm"
            disabled={progress !== null}
            onClick={() => void remove()}
          >
            Remove
          </button>
        )}
        <button
          type="button"
          className="btn-tonal btn-sm"
          disabled={progress !== null}
          onClick={() => input.current?.click()}
        >
          {file ? "Replace" : "Upload"}
        </button>
      </div>
    </div>
  );
}
