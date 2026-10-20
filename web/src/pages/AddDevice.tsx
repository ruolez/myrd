import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { api, errorMessage } from "../api/client";
import { useAuth } from "../app/auth";
import { Icon, OsIcon } from "../components/icons";
import { CopyButton, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { signaling } from "../lib/signaling";
import { useNow } from "../lib/hooks";

type Target = "darwin" | "windows" | "linux";

const targets: { id: Target; label: string; note: string }[] = [
  { id: "darwin", label: "macOS", note: "macOS 13 or later, Apple silicon or Intel" },
  { id: "windows", label: "Windows", note: "Windows 10 1903 or later, x64" },
  { id: "linux", label: "Docker demo", note: "The Linux test device from docker compose" },
];

function Snippet({ code }: { code: string }) {
  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-lg bg-surface-2 p-3 pr-24 font-mono text-xs leading-relaxed">
        {code}
      </pre>
      <div className="absolute top-2 right-2">
        <CopyButton text={code} />
      </div>
    </div>
  );
}

export function AddDevice() {
  const { config } = useAuth();
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
  const download =
    target === "darwin"
      ? config?.downloads.macos
      : target === "windows"
        ? config?.downloads.windows
        : null;

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
                <OsIcon os={o.id} size={24} />
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
          {target === "linux" ? (
            <>
              <ol className="list-decimal space-y-2 pl-5 text-sm">
                <li>
                  Start the demo device:{" "}
                  <code className="font-mono">docker compose --profile demo up -d demo-device</code>
                </li>
                <li>
                  Open the device console at{" "}
                  <a
                    className="text-primary underline"
                    href="http://localhost:5562"
                    target="_blank"
                    rel="noreferrer"
                  >
                    http://localhost:5562
                  </a>{" "}
                  — it stands in for the device's physical screen.
                </li>
                <li>
                  Paste the token above and press Enroll. The device appears online here within
                  seconds.
                </li>
                <li>Session approval prompts for new browsers also appear in that console.</li>
              </ol>
              <Snippet
                code={`DEMO_ENROLL_TOKEN=${t} docker compose --profile demo up -d demo-device`}
              />
            </>
          ) : (
            <>
              <ol className="list-decimal space-y-2 pl-5 text-sm">
                <li>
                  Download and run the{" "}
                  {target === "darwin" ? "macOS installer (.pkg)" : "Windows installer (.msi)"}.
                </li>
                <li>On first launch, enter the token above when the agent asks for it.</li>
                <li>
                  {target === "darwin"
                    ? "Grant Screen Recording and Accessibility when prompted; the agent opens the exact System Settings pane."
                    : "No extra permissions are needed; the agent runs as a Windows service."}
                </li>
              </ol>
              {download ? (
                <a className="btn-primary" href={download}>
                  <Icon name="release" size={18} />
                  Download for {target === "darwin" ? "macOS" : "Windows"}
                </a>
              ) : (
                <p className="text-sm text-muted">
                  No download URL is configured yet. An administrator can set it under Admin →
                  System.
                </p>
              )}
              <div>
                <div className="mb-2 text-sm font-medium">Scripted install</div>
                {target === "windows" ? (
                  <Snippet code={`msiexec /i RemoteAgent.msi ENROLL_TOKEN=${t} /qn`} />
                ) : (
                  <Snippet
                    code={`# token plist read by the installer\ndefaults write /tmp/remotectl-enroll EnrollToken -string ${t}\nsudo installer -pkg RemoteAgent.pkg -applyChoiceChangesXML /tmp/remotectl-enroll.plist -target /`}
                  />
                )}
              </div>
            </>
          )}
        </section>
      </div>
    </>
  );
}
