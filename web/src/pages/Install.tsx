import { useEffect, useRef } from "react";
import { useParams } from "react-router";
import { api } from "../api/client";
import type { DownloadOS } from "../api/types";
import { OsIcon } from "../components/icons";
import { Loading } from "../components/ui";
import { bytes } from "../lib/format";
import { useAsync } from "../lib/hooks";
import { detectInstallOS } from "../lib/installLink";
import { AuthCard } from "./Login";

const osLabel: Record<DownloadOS, string> = {
  windows: "Windows",
  windows7: "Windows 7",
  macos: "macOS",
};
const osOrder: DownloadOS[] = ["windows", "macos", "windows7"];
const installerUrl = (code: string, os: DownloadOS) =>
  `/v1/install/${encodeURIComponent(code)}/${os}`;

/**
 * Public landing page of an install link (/i/:code): starts the download for
 * this computer. The installer's file name carries the code, so it enrolls
 * into the link owner's account without asking anything.
 */
export function Install() {
  const { code = "" } = useParams();
  const { data, error, loading } = useAsync(() => api.install(code), [code]);
  const detected = detectInstallOS(navigator.userAgent);

  const started = useRef(false);
  useEffect(() => {
    if (started.current || !data || !detected || !data.downloads[detected]) return;
    started.current = true;
    window.location.assign(installerUrl(code, detected));
  }, [code, data, detected]);

  if (loading) return <Loading />;
  if (error || !data) {
    return (
      <AuthCard
        title="This link doesn't work"
        subtitle="It was replaced or turned off. Ask the person who sent it for a new one."
      >
        <></>
      </AuthCard>
    );
  }

  const mine = detected && data.downloads[detected] ? detected : null;
  const others = osOrder.filter((os) => os !== mine && data.downloads[os]);
  return (
    <AuthCard
      title="Add this computer"
      subtitle={
        mine
          ? "The download has started. Open it when it finishes."
          : "Open this link on the Windows PC or Mac you want to add."
      }
    >
      <div className="space-y-5 text-sm">
        {mine && (
          <>
            <a className="btn-primary w-full justify-center" href={installerUrl(code, mine)}>
              <OsIcon os={mine === "macos" ? "darwin" : "windows"} size={18} />
              Download for {osLabel[mine]}
            </a>
            <ol className="list-decimal space-y-2 pl-5">
              {mine === "macos" ? (
                <>
                  <li>Open the downloaded installer and click through it.</li>
                  <li>
                    Allow <strong>Screen Recording</strong> and <strong>Accessibility</strong> when
                    macOS asks. The agent opens the right settings page.
                  </li>
                </>
              ) : (
                <>
                  <li>Open the downloaded installer.</li>
                  <li>
                    Click <strong>Yes</strong> when Windows asks to allow changes. There is nothing
                    to fill in.
                  </li>
                </>
              )}
              <li>This computer shows up in the owner's device list a few seconds later.</li>
            </ol>
            <p className="text-xs text-muted">
              {data.downloads[mine]?.filename} · {bytes(data.downloads[mine]?.size ?? 0)}
            </p>
          </>
        )}
        {others.length > 0 && (
          <div className="space-y-2">
            {mine && <div className="text-xs text-muted">Other systems</div>}
            {others.map((os) => (
              <a
                key={os}
                className="btn-outline w-full justify-center"
                href={installerUrl(code, os)}
              >
                <OsIcon os={os === "macos" ? "darwin" : "windows"} size={18} />
                Download for {osLabel[os]}
              </a>
            ))}
          </div>
        )}
        {!mine && others.length === 0 && (
          <p className="text-center text-muted">No installer is available yet.</p>
        )}
      </div>
    </AuthCard>
  );
}
