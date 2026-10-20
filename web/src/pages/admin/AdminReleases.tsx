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
  formatRollout,
  parseSidecar,
  pickPair,
  releaseFileMatches,
  uploadErrorMessage,
} from "../../lib/release";
import type { SidecarResult } from "../../lib/release";

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

type SigState = { file: File; result: SidecarResult };

function UploadRelease({ onUploaded }: { onUploaded: () => void }) {
  const toast = useToast();
  const input = useRef<HTMLInputElement>(null);
  const readSeq = useRef(0);
  const [installer, setInstaller] = useState<File | null>(null);
  const [sig, setSig] = useState<SigState | null>(null);
  const [ignored, setIgnored] = useState<string[]>([]);
  const [rollout, setRollout] = useState(100);
  const [dragging, setDragging] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);

  const readSig = async (file: File) => {
    const my = ++readSeq.current;
    let result: SidecarResult;
    if (file.size > MAX_SIDECAR_BYTES) result = { ok: false, error: "signature file is too large" };
    else {
      try {
        result = parseSidecar(await file.text());
      } catch {
        result = { ok: false, error: "signature file could not be read" };
      }
    }
    if (my === readSeq.current) setSig({ file, result });
  };

  const addFiles = (files: File[]) => {
    const p = pickPair(files);
    setError(null);
    setIgnored(p.ignored.map((f) => f.name));
    if (p.installer) setInstaller(p.installer);
    if (p.sig) void readSig(p.sig);
  };

  const clear = () => {
    readSeq.current++;
    setInstaller(null);
    setSig(null);
    setIgnored([]);
    setError(null);
    if (input.current) input.current.value = "";
  };

  const sidecar = sig?.result.ok ? sig.result.sidecar : null;
  const sigError = sig && !sig.result.ok ? sig.result.error : null;
  const mismatch = sidecar && installer ? releaseFileMatches(sidecar, installer) : null;
  const ready = !!installer && !!sidecar && !mismatch && progress === null;

  const upload = async () => {
    if (!installer || !sig || !sidecar) return;
    setProgress(0);
    setError(null);
    try {
      await api.admin.uploadRelease(
        { sig: sig.file, file: installer, rolloutPct: rollout },
        setProgress,
      );
      toast(
        `Uploaded ${sidecar.version} for ${osName(sidecar.os)} ${sidecar.arch} as a draft`,
        "success",
      );
      clear();
      onUploaded();
    } catch (e) {
      setError(e instanceof ApiError ? uploadErrorMessage(e.code, e.message) : errorMessage(e));
    } finally {
      setProgress(null);
    }
  };

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setDragging(false);
    if (progress === null) addFiles(Array.from(e.dataTransfer.files));
  };

  return (
    <section className="card mb-6 space-y-4 p-6">
      <div>
        <h2 className="font-medium">Upload a release</h2>
        <p className="text-sm text-muted">
          Drop the installer (<code className="font-mono">.pkg</code> or{" "}
          <code className="font-mono">.msi</code>) together with the{" "}
          <code className="font-mono">.sig</code> that{" "}
          <code className="font-mono">remotectl-release sign</code> wrote next to it. Uploads start
          as drafts; publishing rolls them out.
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
          Drop the installer and its signature here, or{" "}
          <button
            type="button"
            className="font-medium text-primary hover:underline"
            disabled={progress !== null}
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

      {(installer || sig) && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
          <dt className="text-muted">Installer</dt>
          <dd className="truncate">
            {installer ? `${installer.name} · ${bytes(installer.size)}` : "missing"}
          </dd>
          <dt className="text-muted">Signature</dt>
          <dd className="truncate">{sig ? sig.file.name : "missing"}</dd>
          {sidecar && (
            <>
              <dt className="text-muted">Version</dt>
              <dd className="font-medium">{sidecar.version}</dd>
              <dt className="text-muted">Target</dt>
              <dd className="inline-flex items-center gap-1.5">
                <OsIcon os={sidecar.os} size={16} />
                {osName(sidecar.os)} · {sidecar.arch}
              </dd>
              <dt className="text-muted">Signed file</dt>
              <dd className="truncate">
                {sidecar.filename} · {bytes(sidecar.size)}
              </dd>
              <dt className="text-muted">Key id</dt>
              <dd className="font-mono text-xs leading-5">{sidecar.key_id}</dd>
            </>
          )}
        </dl>
      )}
      {sigError && <ErrorBox error={`${sig?.file.name} is not a usable signature: ${sigError}`} />}
      {mismatch && <ErrorBox error={mismatch} />}
      {ignored.length > 0 && (
        <p className="text-xs text-warning">
          Ignored {ignored.join(", ")}: only one .pkg or .msi and one .sig are used.
        </p>
      )}
      {error && <ErrorBox error={error} />}

      {(installer || sig) && (
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
      {progress !== null && (
        <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-2">
          <div
            className="h-full bg-primary transition-[width]"
            style={{ width: `${Math.round(progress * 100)}%` }}
          />
        </div>
      )}
      {(installer || sig) && (
        <div className="flex justify-end gap-2">
          <button className="btn-text" disabled={progress !== null} onClick={clear}>
            Clear
          </button>
          <button className="btn-primary" disabled={!ready} onClick={() => void upload()}>
            <Icon name="upload" size={18} />
            {progress !== null ? `Uploading ${Math.round(progress * 100)}%` : "Upload draft"}
          </button>
        </div>
      )}
    </section>
  );
}
