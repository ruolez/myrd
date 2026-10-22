import { useEffect, useRef, useState } from "react";
import type { DragEvent } from "react";
import { ApiError, api, errorMessage } from "../../api/client";
import type { Release, ReleaseOS } from "../../api/types";
import { Icon, OsIcon } from "../../components/icons";
import {
  Badge,
  EmptyState,
  ErrorBox,
  Loading,
  PageHeader,
  Toggle,
  useConfirm,
  useToast,
} from "../../components/ui";
import { bytes, dateTime, osName } from "../../lib/format";
import { useAsync } from "../../lib/hooks";
import {
  MAX_SIDECAR_BYTES,
  addReleaseFiles,
  formatRollout,
  pairFiles,
  parseSidecar,
  releaseFileMatches,
  uploadErrorMessage,
} from "../../lib/release";
import type { FilePair, ReleaseSidecar, SidecarResult } from "../../lib/release";

const ROLLOUT_DEBOUNCE_MS = 600;

export function AdminReleases() {
  const toast = useToast();
  const confirm = useConfirm();
  const { data, error, loading, reload } = useAsync(() => api.admin.releases(), []);

  const act = async (fn: () => Promise<unknown>, ok: string): Promise<boolean> => {
    let done = false;
    try {
      await fn();
      toast(ok, "success");
      done = true;
    } catch (e) {
      toast(errorMessage(e), "error");
    }
    await reload(true);
    return done;
  };

  const push = async (os: ReleaseOS) => {
    const ok = await confirm({
      title: `Update all outdated ${osName(os)} devices?`,
      message:
        "Every online device older than its newest published release is asked to update, whatever the rollout percentage, spread over up to 10 minutes. Devices in a session wait until it ends.",
      confirm: "Update all",
    });
    if (!ok) return;
    try {
      const { queued } = await api.admin.pushUpdates(os);
      toast(
        queued
          ? `Queued ${queued} ${osName(os)} device${queued === 1 ? "" : "s"}`
          : `No outdated ${osName(os)} devices are online`,
        "success",
      );
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  };

  const items = data?.items ?? [];
  return (
    <>
      <PageHeader
        title="Agent releases"
        subtitle="Signed installers that agents update themselves to. Each agent verifies the release signature with the key compiled into it."
        actions={(["darwin", "windows"] as const).map((os) => (
          <button key={os} className="btn-outline" onClick={() => void push(os)}>
            <OsIcon os={os} size={18} />
            Update all outdated {osName(os)}
          </button>
        ))}
      />
      <UploadRelease onUploaded={() => void reload(true)} />
      {error && <ErrorBox error={error} onRetry={() => void reload()} />}
      {loading && !data ? (
        <Loading />
      ) : items.length === 0 ? (
        <div className="card">
          <EmptyState title="No releases">
            Build an installer, sign it with{" "}
            <code className="font-mono">remotectl-release sign</code>, then upload the installer
            with its <code className="font-mono">.sig</code> above.
          </EmptyState>
        </div>
      ) : (
        <section className="card overflow-x-auto">
          <table className="data-table">
            <thead>
              <tr>
                <th>Version</th>
                <th>Installer</th>
                <th>Devices</th>
                <th>Published</th>
                <th>Rollout</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {items.map((r) => (
                <ReleaseRow
                  key={r.id}
                  r={r}
                  onRollout={(pct) =>
                    act(
                      () => api.admin.updateRelease(r.id, { rollout_pct: pct }),
                      `${r.version}: ${formatRollout(pct).toLowerCase()}`,
                    )
                  }
                  onPublish={(published) =>
                    act(
                      () => api.admin.updateRelease(r.id, { published }),
                      published ? `${r.version} published` : `${r.version} unpublished`,
                    )
                  }
                  onInstaller={() =>
                    act(
                      () => api.admin.setReleaseInstaller(r.id),
                      `New ${osName(r.os)} devices now get ${r.version}`,
                    )
                  }
                  onDelete={async () => {
                    if (
                      await confirm({
                        title: `Delete ${r.version} (${osName(r.os)} ${r.arch})?`,
                        message:
                          "The installer is removed from the server and devices stop being offered it. Devices already running it are not affected.",
                        confirm: "Delete",
                        danger: true,
                      })
                    )
                      await act(() => api.admin.deleteRelease(r.id), "Release deleted");
                  }}
                />
              ))}
            </tbody>
          </table>
        </section>
      )}
    </>
  );
}

function ReleaseRow({
  r,
  onRollout,
  onPublish,
  onInstaller,
  onDelete,
}: {
  r: Release;
  onRollout: (pct: number) => Promise<boolean>;
  onPublish: (published: boolean) => Promise<boolean>;
  onInstaller: () => Promise<boolean>;
  onDelete: () => Promise<void>;
}) {
  return (
    <tr>
      <td>
        <div className="flex items-center gap-2 font-medium whitespace-nowrap">
          <OsIcon os={r.os} size={18} />
          {r.version}
        </div>
        <div className="text-xs whitespace-nowrap text-muted">
          {osName(r.os)} · {r.arch}
        </div>
      </td>
      <td className="max-w-64">
        <a
          className="block truncate font-mono text-xs text-primary hover:underline"
          href={r.url}
          download={r.filename}
          title={`SHA-256 ${r.sha256} · key ${r.key_id}`}
        >
          {r.filename}
        </a>
        <div className="text-xs text-muted">
          {bytes(r.size)} · {dateTime(r.published_at ?? r.created_at)}
        </div>
        {r.installer && (
          <div className="mt-1">
            <Badge tone="primary">Add-device installer</Badge>
          </div>
        )}
      </td>
      <td className="tabular-nums">{r.devices}</td>
      <td>
        <Toggle
          label={`Published ${r.version} ${osName(r.os)} ${r.arch}`}
          checked={!!r.published_at}
          onChange={(v) => void onPublish(v)}
        />
      </td>
      <td>
        <RolloutSlider key={r.rollout_pct} r={r} onCommit={onRollout} />
      </td>
      <td className="text-right whitespace-nowrap">
        {!r.installer && (
          <button
            className="btn-text btn-sm"
            title={`Offer this installer on the Add device page for ${osName(r.os)}`}
            onClick={() => void onInstaller()}
          >
            Use for Add device
          </button>
        )}
        <button className="btn-text btn-sm text-error" onClick={() => void onDelete()}>
          Delete
        </button>
      </td>
    </tr>
  );
}

/** Keyed by the saved value so a reload resets it; a failed save snaps back. */
function RolloutSlider({
  r,
  onCommit,
}: {
  r: Release;
  onCommit: (pct: number) => Promise<boolean>;
}) {
  const [pct, setPct] = useState(r.rollout_pct);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  const change = (v: number) => {
    setPct(v);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      timer.current = null;
      if (v !== r.rollout_pct) void onCommit(v).then((saved) => saved || setPct(r.rollout_pct));
    }, ROLLOUT_DEBOUNCE_MS);
  };

  return (
    <label className="block w-44">
      <span className="flex items-center gap-2">
        <input
          type="range"
          min={0}
          max={100}
          step={5}
          value={pct}
          className="flex-1 accent-[var(--c-primary)]"
          onChange={(e) => change(Number(e.target.value))}
          aria-label={`Rollout percentage for ${r.version} ${osName(r.os)} ${r.arch}`}
        />
        <span className="w-10 text-right tabular-nums">{pct}%</span>
      </span>
      <span className="text-xs text-muted">{formatRollout(pct)}</span>
    </label>
  );
}

type PairState = {
  key: string;
  installer: File | null;
  sig: File | null;
  sidecar: ReleaseSidecar | null;
  /** Why this pair cannot be uploaded yet; null when it can. */
  issue: string | null;
  /** Why its last upload failed; uploading again retries it. */
  failed: string | null;
};

const target = (s: ReleaseSidecar) => `${osName(s.os)} ${s.arch}`;

function pairState(
  p: FilePair<File>,
  sigs: ReadonlyMap<File, SidecarResult>,
  failed: Readonly<Record<string, string>>,
): PairState {
  const key = (p.installer ?? p.sig)!.name;
  const result = p.sig ? sigs.get(p.sig) : undefined;
  const sidecar = result?.ok ? result.sidecar : null;
  const issue = !p.installer
    ? `No installer for ${p.sig?.name}`
    : !p.sig
      ? `No .sig for ${p.installer.name}`
      : !result
        ? "Reading the signature…"
        : !result.ok
          ? `${p.sig.name} is not a usable signature: ${result.error}`
          : releaseFileMatches(result.sidecar, p.installer);
  return { key, installer: p.installer, sig: p.sig, sidecar, issue, failed: failed[key] ?? null };
}

function UploadRelease({ onUploaded }: { onUploaded: () => void }) {
  const toast = useToast();
  const input = useRef<HTMLInputElement>(null);
  const [files, setFiles] = useState<File[]>([]);
  const [sigs, setSigs] = useState<ReadonlyMap<File, SidecarResult>>(new Map());
  const [failed, setFailed] = useState<Record<string, string>>({});
  const [rollout, setRollout] = useState(100);
  const [dragging, setDragging] = useState(false);
  const [uploading, setUploading] = useState<{ key: string; progress: number } | null>(null);
  const busy = uploading !== null;

  const readSig = async (file: File) => {
    let result: SidecarResult;
    if (file.size > MAX_SIDECAR_BYTES) result = { ok: false, error: "signature file is too large" };
    else {
      try {
        result = parseSidecar(await file.text());
      } catch {
        result = { ok: false, error: "signature file could not be read" };
      }
    }
    setSigs((prev) => new Map(prev).set(file, result));
  };

  const forget = (names: readonly string[]) =>
    setFailed((prev) => {
      const next = { ...prev };
      for (const n of names) delete next[n];
      return next;
    });

  const addFiles = (added: File[]) => {
    setFiles((prev) => addReleaseFiles(prev, added));
    forget(added.map((f) => f.name));
    for (const f of pairFiles(added).pairs) if (f.sig) void readSig(f.sig);
  };

  const remove = (gone: readonly (File | null)[]) =>
    setFiles((prev) => prev.filter((f) => !gone.includes(f)));

  const clear = () => {
    setFiles([]);
    setFailed({});
    if (input.current) input.current.value = "";
  };

  const { pairs: filePairs, ignored } = pairFiles(files);
  const pairs = filePairs.map((p) => pairState(p, sigs, failed));
  const ready = pairs.filter((p) => !p.issue);

  const upload = async () => {
    const uploaded: string[] = [];
    let version = "";
    for (const p of ready) {
      if (!p.installer || !p.sig || !p.sidecar) continue;
      const key = p.key;
      setUploading({ key, progress: 0 });
      forget([key]);
      try {
        await api.admin.uploadRelease(
          { sig: p.sig, file: p.installer, rolloutPct: rollout },
          (progress) => setUploading({ key, progress }),
        );
        uploaded.push(target(p.sidecar));
        version = p.sidecar.version;
        remove([p.installer, p.sig]);
      } catch (e) {
        const msg = e instanceof ApiError ? uploadErrorMessage(e.code, e.message) : errorMessage(e);
        setFailed((prev) => ({ ...prev, [key]: msg }));
      }
    }
    setUploading(null);
    if (uploaded.length === pairs.length) clear();
    if (uploaded.length) {
      toast(
        `Uploaded ${version} for ${uploaded.join(", ")} as ${uploaded.length === 1 ? "a draft" : "drafts"}`,
        "success",
      );
      onUploaded();
    }
  };

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setDragging(false);
    if (!busy) addFiles(Array.from(e.dataTransfer.files));
  };

  const at = uploading ? ready.findIndex((p) => p.key === uploading.key) + 1 : 0;
  return (
    <section className="card mb-6 space-y-4 p-6">
      <div>
        <h2 className="font-medium">Upload releases</h2>
        <p className="text-sm text-muted">
          Drop the installers (<code className="font-mono">.pkg</code>,{" "}
          <code className="font-mono">.msi</code>) together with the{" "}
          <code className="font-mono">.sig</code> files that{" "}
          <code className="font-mono">remotectl-release sign</code> wrote next to them: one release
          or a whole release folder (macOS, Windows and Windows 7). Uploads start as drafts;
          publishing rolls them out.
        </p>
      </div>
      <div
        className={`flex flex-col items-center gap-2 rounded-xl border-2 border-dashed p-6 text-center text-sm transition-colors ${
          dragging ? "border-primary bg-primary-soft" : "border-outline"
        }`}
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
      >
        <Icon name="upload" size={28} className="text-muted" />
        <div>
          Drop installers and their signatures here, or{" "}
          <button
            type="button"
            className="font-medium text-primary hover:underline"
            disabled={busy}
            onClick={() => input.current?.click()}
          >
            choose files
          </button>
        </div>
        <input
          ref={input}
          type="file"
          multiple
          accept=".pkg,.msi,.sig"
          className="hidden"
          aria-label="Release installer and signature files"
          onChange={(e) => {
            addFiles(Array.from(e.target.files ?? []));
            e.target.value = "";
          }}
        />
      </div>

      {pairs.length > 0 && (
        <ul className="divide-y divide-outline rounded-xl border border-outline text-sm">
          {pairs.map((p) => (
            <PairRow
              key={p.key}
              p={p}
              progress={uploading?.key === p.key ? uploading.progress : null}
              busy={busy}
              onRemove={() => {
                remove([p.installer, p.sig]);
                forget([p.key]);
              }}
            />
          ))}
        </ul>
      )}
      {ignored.length > 0 && (
        <p className="text-xs text-warning">
          Ignored {ignored.map((f) => f.name).join(", ")}: only .pkg and .msi installers and their
          .sig files are uploaded.
        </p>
      )}

      {pairs.length > 0 && (
        <label className="flex flex-wrap items-center gap-3 text-sm">
          <span className="text-muted">Rollout once published</span>
          <input
            type="range"
            min={0}
            max={100}
            step={5}
            value={rollout}
            className="w-44 accent-[var(--c-primary)]"
            onChange={(e) => setRollout(Number(e.target.value))}
            aria-label="Initial rollout percentage"
          />
          <span className="tabular-nums">{rollout}%</span>
          <span className="text-xs text-muted">{formatRollout(rollout)}</span>
        </label>
      )}
      {pairs.length > 0 && (
        <div className="flex justify-end gap-2">
          <button className="btn-text" disabled={busy} onClick={clear}>
            Clear
          </button>
          <button
            className="btn-primary"
            disabled={busy || ready.length === 0}
            onClick={() => void upload()}
          >
            <Icon name="upload" size={18} />
            {busy
              ? `Uploading ${at} of ${ready.length}`
              : ready.length > 1
                ? `Upload ${ready.length} drafts`
                : "Upload draft"}
          </button>
        </div>
      )}
    </section>
  );
}

function PairRow({
  p,
  progress,
  busy,
  onRemove,
}: {
  p: PairState;
  progress: number | null;
  busy: boolean;
  onRemove: () => void;
}) {
  const s = p.sidecar;
  return (
    <li className="flex items-start gap-3 px-4 py-3">
      {s ? <OsIcon os={s.os} size={20} /> : <Icon name="file" size={20} className="text-muted" />}
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="font-medium">
          {s ? `${s.version} · ${target(s)}` : (p.installer ?? p.sig)?.name}
        </div>
        <div className="truncate text-xs text-muted">
          {p.installer ? `${p.installer.name} · ${bytes(p.installer.size)}` : "installer missing"}
          {" + "}
          {p.sig ? p.sig.name : ".sig missing"}
        </div>
        {s && <div className="font-mono text-xs text-muted">key {s.key_id}</div>}
        {progress !== null ? (
          <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-surface-2">
            <div
              className="h-full bg-primary transition-[width]"
              style={{ width: `${Math.round(progress * 100)}%` }}
            />
          </div>
        ) : p.issue || p.failed ? (
          <p className="text-xs text-error">{p.issue ?? p.failed}</p>
        ) : (
          <p className="inline-flex items-center gap-1 text-xs text-success">
            <Icon name="check" size={14} />
            Ready
          </p>
        )}
      </div>
      <button
        className="btn-text btn-sm"
        aria-label={`Remove ${p.key}`}
        title="Remove"
        disabled={busy}
        onClick={onRemove}
      >
        <Icon name="close" size={16} />
      </button>
    </li>
  );
}
