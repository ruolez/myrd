import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { api, errorMessage } from "../api/client";
import { useAuth } from "../app/auth";
import { Icon, OsIcon } from "../components/icons";
import { CopyButton, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { bytes } from "../lib/format";
import { installSnippet, macEnrollSnippet } from "../lib/install";
import { signaling } from "../lib/signaling";
import { useAsync, useNow } from "../lib/hooks";

type Target = "darwin" | "windows" | "windows7";

const targets: { id: Target; label: string; note: string }[] = [
  { id: "darwin", label: "macOS", note: "macOS 13 or later, Apple silicon or Intel" },
  { id: "windows", label: "Windows", note: "Windows 10 1903 or later, x64" },
  {
    id: "windows7",
    label: "Windows 7",
    note: "Windows 7 SP1, x64: a separate build with slower screen capture",
  },
];

function Snippet({ code }: { code: string }) {
  return (
    <div className="rounded-lg bg-surface-2">
      <div className="flex justify-end px-2 pt-2">
        <CopyButton text={code} />
      </div>
      <pre className="overflow-x-auto px-3 pt-1 pb-3 font-mono text-xs leading-relaxed">{code}</pre>
    </div>
  );
}

export function AddDevice() {
  const { config: bootConfig, user } = useAuth();
  // Fresh copy: an installer uploaded since sign-in should show up here.
  const { data: freshConfig } = useAsync(() => api.config(), []);
  const config = freshConfig ?? bootConfig;
  const [target, setTarget] = useState<Target>("darwin");
  const [token, setToken] = useState<{ token: string; expires_at: string } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [enrolled, setEnrolled] = useState(false);
  const now = useNow(1000);

  const mint = async () => {
    setBusy(true);
    setError(null);
    setEnrolled(false);
    try {
      setToken(await api.devices.enrollToken());
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const minted = useRef(false);
  useEffect(() => {
    if (minted.current) return;
    minted.current = true;
    void mint();
  }, []);

  // A device that comes online while this page is open is almost certainly the new one.
  useEffect(
    () => signaling.on("device.status", (m) => m.online && token && setEnrolled(true)),
    [token],
  );

  const remaining = token
    ? Math.max(0, Math.floor((Date.parse(token.expires_at) - now) / 1000))
    : 0;
  const expired = !!token && remaining === 0;
  const mmss = `${Math.floor(remaining / 60)}:${String(remaining % 60).padStart(2, "0")}`;
  const t = token?.token ?? "XXXX-XXXX";
  const link =
    target === "darwin"
      ? config?.downloads.macos
      : target === "windows"
        ? config?.downloads.windows
        : config?.downloads.windows7;
  const downloadUrl = link ? new URL(link.url, window.location.origin).toString() : null;
  const osLabel = target === "darwin" ? "macOS" : target === "windows7" ? "Windows 7" : "Windows";
  const serverUrl = config?.webauthn.origin ?? window.location.origin;
  // The MSI asks for the token itself; a bare .exe or .zip needs the command.
  const windowsMsi = !link?.file || link.file.filename.toLowerCase().endsWith(".msi");
  const snippet = installSnippet({
    target: target === "windows7" ? "windows" : target,
    token: t,
    serverUrl,
    downloadUrl,
    filename: link?.file?.filename ?? null,
  });

  return (
    <>
      <PageHeader
        title="Add device"
        subtitle="Enrollment needs physical access to the computer and a one-time token that expires in 15 minutes."
        actions={
          <Link to="/devices" className="btn-text">
            <Icon name="back" size={18} />
            Back to devices
          </Link>
        }
      />
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[1fr_1.4fr] [&>*]:min-w-0">
        <section className="card p-6">
          <h2 className="mb-4 font-medium">1. Choose the operating system</h2>
          <div className="space-y-2" role="radiogroup" aria-label="Operating system">
            {targets.map((o) => (
              <button
                key={o.id}
                role="radio"
                aria-checked={target === o.id}
                onClick={() => setTarget(o.id)}
                className={`flex w-full items-center gap-3 rounded-xl border p-3 text-left transition-colors ${
                  target === o.id
                    ? "border-primary bg-primary-soft"
                    : "border-outline hover:bg-surface-2"
                }`}
              >
                <OsIcon os={o.id === "windows7" ? "windows" : o.id} size={24} />
                <div>
                  <div className="text-sm font-medium">{o.label}</div>
                  <div className="text-xs text-muted">{o.note}</div>
                </div>
              </button>
            ))}
          </div>

          <h2 className="mt-8 mb-3 font-medium">2. Enrollment token</h2>
          {error && <ErrorBox error={error} onRetry={() => void mint()} />}
          <div className="flex flex-col items-center gap-3 rounded-xl bg-surface-2 p-5">
            {busy && !token ? (
              <Spinner />
            ) : (
              <>
                <div
                  className={`font-mono text-3xl tracking-widest ${expired ? "text-muted line-through" : ""}`}
                >
                  {t}
                </div>
                <div
                  className={`text-sm ${remaining < 60 ? "text-error" : "text-muted"}`}
                  aria-live="polite"
                >
                  {expired ? "Expired" : `Expires in ${mmss}`}
                </div>
                <div className="flex gap-2">
                  {token && !expired && <CopyButton text={token.token} label="Copy token" />}
                  <button
                    className="btn-outline btn-sm"
                    onClick={() => void mint()}
                    disabled={busy}
                  >
                    New token
                  </button>
                </div>
              </>
            )}
          </div>
          {enrolled && (
            <div className="mt-4 rounded-xl bg-success-soft p-3 text-sm text-success">
              A device just came online.{" "}
              <Link to="/devices" className="font-medium underline">
                View devices
              </Link>
            </div>
          )}
        </section>

        <section className="card space-y-5 p-6">
          <h2 className="font-medium">3. Install and enroll</h2>
          {target === "darwin" ? (
            <ol className="list-decimal space-y-2 pl-5 text-sm">
              <li>
                On the Mac you are adding, download and run the macOS installer
                {link?.file ? ` (${link.file.filename})` : ""}.
              </li>
              <li>
                On first launch, enter the token above when the agent asks for it. Missed the
                dialog? Choose <strong>Enroll this device…</strong> from the agent's menu bar icon,
                or run the command at the bottom of this page.
              </li>
              <li>
                Grant Screen Recording and Accessibility when prompted; the agent opens the exact
                System Settings pane.
              </li>
            </ol>
          ) : windowsMsi ? (
            <ol className="list-decimal space-y-2 pl-5 text-sm">
              <li>
                On the PC you are adding, download and run the Windows installer
                {link?.file ? ` (${link.file.filename})` : ""}.
              </li>
              <li>
                On the <strong>Add this PC</strong> page, check the server address (
                <span className="font-mono">{serverUrl}</span>) and enter the token above, then
                click Install.
              </li>
              <li>
                The PC appears here a few seconds after setup finishes. No extra permissions are
                needed.
              </li>
            </ol>
          ) : (
            <ol className="list-decimal space-y-2 pl-5 text-sm">
              <li>
                On the PC you are adding, open <strong>PowerShell as Administrator</strong> (Start →
                type PowerShell → Run as administrator).
              </li>
              <li>
                Paste the command below. It downloads the agent, enrolls with the token above and
                installs the service; the PC appears here within seconds.
              </li>
            </ol>
          )}
          {target === "windows7" && (
            <p className="rounded-xl bg-warning-soft p-3 text-sm text-warning">
              Windows 7 has had no security updates since January 2023, so anyone who gets into that
              PC another way can use this connection too. Its build captures the screen through GDI:
              expect up to 1080p and a lower frame rate than on Windows 10.
            </p>
          )}
          {link && downloadUrl ? (
            <div className="space-y-2">
              <div className="flex flex-wrap items-center gap-2">
                <a className="btn-primary" href={link.url} download={link.file?.filename}>
                  <Icon name="release" size={18} />
                  Download for {osLabel}
                </a>
                <CopyButton text={downloadUrl} label="Copy link" />
              </div>
              {link.file && (
                <p className="text-xs text-muted">
                  {link.file.filename} · {bytes(link.file.size)} · SHA-256{" "}
                  <span className="font-mono" title={link.file.sha256}>
                    {link.file.sha256.slice(0, 12)}…
                  </span>
                  <br />
                  The link works without signing in, so you can open it directly on that computer.
                </p>
              )}
            </div>
          ) : (
            <p className="text-sm text-muted">
              No {osLabel} installer has been uploaded yet.{" "}
              {user?.is_admin ? (
                <Link to="/admin/settings" className="text-primary underline">
                  Upload one under Admin → System
                </Link>
              ) : (
                "Ask an administrator to upload one under Admin → System."
              )}
            </p>
          )}
          <div>
            <div className="mb-2 text-sm font-medium">
              {target === "darwin"
                ? "Scripted install"
                : windowsMsi
                  ? "Or install from PowerShell as Administrator, without clicks"
                  : "Install command"}
            </div>
            <Snippet code={snippet} />
          </div>
          {target === "darwin" && (
            <div>
              <div className="mb-2 text-sm font-medium">
                Already installed but not in your devices
              </div>
              <p className="mb-2 text-xs text-muted">
                In Terminal on that Mac, as the signed-in user (not sudo). The agent connects within
                seconds.
              </p>
              <Snippet code={macEnrollSnippet(t, serverUrl)} />
            </div>
          )}
        </section>
      </div>
    </>
  );
}
