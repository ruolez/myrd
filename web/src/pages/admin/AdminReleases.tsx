import { useState } from "react";
import type { FormEvent } from "react";
import { api, errorMessage } from "../../api/client";
import type { OS, Release } from "../../api/types";
import { Icon, OsIcon } from "../../components/icons";
import {
  Badge,
  EmptyState,
  ErrorBox,
  Field,
  Loading,
  Modal,
  PageHeader,
  useConfirm,
  useToast,
} from "../../components/ui";
import { dateTime, osName } from "../../lib/format";
import { useAsync } from "../../lib/hooks";

export function AdminReleases() {
  const toast = useToast();
  const confirm = useConfirm();
  const { data, error, loading, reload } = useAsync(() => api.admin.releases(), []);
  const [creating, setCreating] = useState(false);

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn();
      toast(ok, "success");
      await reload(true);
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  const items = data?.items ?? [];
  return (
    <>
      <PageHeader
        title="Agent releases"
        subtitle="Agents poll every 6 h and verify the Ed25519 manifest signature against the release key compiled into them."
        actions={
          <button className="btn-primary" onClick={() => setCreating(true)}>
            <Icon name="add" size={18} />
            New release
          </button>
        }
      />
      {error && <ErrorBox error={error} onRetry={() => void reload()} />}
      {loading && !data ? (
        <Loading />
      ) : items.length === 0 ? (
        <div className="card">
          <EmptyState title="No releases">
            Build and sign an agent in CI, then register it here with its CDN URL, SHA-256 and
            signature (<code className="font-mono">remotectl-release sign</code>).
          </EmptyState>
        </div>
      ) : (
        <ul className="space-y-3">
          {items.map((r) => (
            <ReleaseRow
              key={r.id}
              r={r}
              onRollout={(pct) =>
                act(
                  () => api.admin.updateRelease(r.id, { rollout_pct: pct }),
                  `Rollout set to ${pct}%`,
                )
              }
              onPublish={(pub) =>
                act(
                  () => api.admin.updateRelease(r.id, { published: pub }),
                  pub ? "Published" : "Unpublished",
                )
              }
              onDelete={async () => {
                if (
                  await confirm({
                    title: `Delete ${r.version} (${osName(r.os)} ${r.arch})?`,
                    message: "Agents stop being offered this build.",
                    confirm: "Delete",
                    danger: true,
                  })
                )
                  await act(() => api.admin.deleteRelease(r.id), "Release deleted");
              }}
            />
          ))}
        </ul>
      )}
      {creating && (
        <CreateRelease
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            toast("Release created", "success");
            void reload();
          }}
        />
      )}
    </>
  );
}

function ReleaseRow({
  r,
  onRollout,
  onPublish,
  onDelete,
}: {
  r: Release;
  onRollout: (pct: number) => Promise<void>;
  onPublish: (pub: boolean) => Promise<void>;
  onDelete: () => Promise<void>;
}) {
  const [pct, setPct] = useState(r.rollout_pct);
  return (
    <li className="card flex flex-wrap items-center gap-4 p-4">
      <OsIcon os={r.os} size={24} />
      <div className="min-w-48 flex-1">
        <div className="flex items-center gap-2 font-medium">
          {r.version}{" "}
          <span className="text-sm font-normal text-muted">
            {osName(r.os)} · {r.arch}
          </span>
          {r.published_at ? <Badge tone="success">published</Badge> : <Badge>draft</Badge>}
        </div>
        <div className="truncate font-mono text-xs text-muted" title={r.url}>
          {r.url}
        </div>
        <div className="truncate font-mono text-xs text-muted" title={r.sha256}>
          sha256 {r.sha256}
        </div>
        <div className="text-xs text-muted">
          {r.published_at
            ? `Published ${dateTime(r.published_at)}`
            : `Created ${dateTime(r.created_at)}`}
        </div>
      </div>
      <label className="flex w-full items-center gap-3 text-sm sm:w-72">
        <span className="w-16 shrink-0 text-muted">Rollout</span>
        <input
          type="range"
          min={0}
          max={100}
          step={5}
          value={pct}
          className="flex-1 accent-[var(--c-primary)]"
          onChange={(e) => setPct(Number(e.target.value))}
          onPointerUp={() => pct !== r.rollout_pct && void onRollout(pct)}
          onKeyUp={() => pct !== r.rollout_pct && void onRollout(pct)}
          aria-label={`Rollout percentage for ${r.version}`}
        />
        <span className="w-10 text-right tabular-nums">{pct}%</span>
      </label>
      <div className="flex gap-1">
        <button className="btn-outline btn-sm" onClick={() => void onPublish(!r.published_at)}>
          {r.published_at ? "Unpublish" : "Publish"}
        </button>
        <button className="btn-text btn-sm text-error" onClick={() => void onDelete()}>
          Delete
        </button>
      </div>
    </li>
  );
}

function CreateRelease({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [f, setF] = useState({
    version: "",
    os: "darwin" as OS,
    arch: "arm64",
    url: "",
    sha256: "",
    signature: "",
    rollout_pct: 10,
  });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) =>
    setF({ ...f, [k]: k === "rollout_pct" ? Number(e.target.value) : e.target.value });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!/^[0-9a-f]{64}$/i.test(f.sha256.trim()))
      return setError("SHA-256 must be 64 hex characters.");
    setBusy(true);
    setError(null);
    try {
      await api.admin.createRelease({
        ...f,
        sha256: f.sha256.trim().toLowerCase(),
        signature: f.signature.trim(),
        url: f.url.trim(),
        version: f.version.trim(),
      });
      onCreated();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="New release" onClose={onClose} wide>
      <form className="grid gap-4 sm:grid-cols-3" onSubmit={(e) => void submit(e)}>
        {error && (
          <div className="sm:col-span-3">
            <ErrorBox error={error} />
          </div>
        )}
        <Field label="Version">
          <input
            className="input font-mono"
            placeholder="1.2.0"
            required
            value={f.version}
            onChange={set("version")}
          />
        </Field>
        <Field label="OS">
          <select className="input" value={f.os} onChange={set("os")}>
            <option value="darwin">macOS</option>
            <option value="windows">Windows</option>
            <option value="linux">Linux</option>
          </select>
        </Field>
        <Field label="Arch">
          <select className="input" value={f.arch} onChange={set("arch")}>
            <option value="arm64">arm64</option>
            <option value="amd64">amd64</option>
            <option value="universal">universal</option>
          </select>
        </Field>
        <div className="sm:col-span-3">
          <Field label="Download URL (CDN)">
            <input
              className="input font-mono"
              type="url"
              required
              placeholder="https://dl.example.com/RemoteAgent-1.2.0.pkg"
              value={f.url}
              onChange={set("url")}
            />
          </Field>
        </div>
        <div className="sm:col-span-3">
          <Field label="SHA-256">
            <input className="input font-mono" required value={f.sha256} onChange={set("sha256")} />
          </Field>
        </div>
        <div className="sm:col-span-3">
          <Field
            label="Signature"
            hint="Base64 Ed25519 signature from the offline release key over the manifest (remotectl-release sign)."
          >
            <textarea
              className="input font-mono"
              rows={2}
              required
              value={f.signature}
              onChange={set("signature")}
            />
          </Field>
        </div>
        <div className="sm:col-span-3">
          <Field label={`Initial rollout: ${f.rollout_pct}%`}>
            <input
              type="range"
              min={0}
              max={100}
              step={5}
              className="w-full accent-[var(--c-primary)]"
              value={f.rollout_pct}
              onChange={set("rollout_pct")}
            />
          </Field>
        </div>
        <div className="flex justify-end gap-2 sm:col-span-3">
          <button type="button" className="btn-text" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary" disabled={busy}>
            Create draft
          </button>
        </div>
      </form>
    </Modal>
  );
}
