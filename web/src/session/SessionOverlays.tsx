// What a session shows over its stage while it is not live: connecting,
// waiting for approval, ended (with reconnect), refused for a changed
// device identity. Shared by desktop and terminal sessions.

import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "../components/icons";
import { Spinner } from "../components/ui";
import { keyFingerprint } from "../lib/devicePins";
import { END_REASONS } from "./RemoteSession";
import type { Snapshot } from "./RemoteSession";

const phaseText: Partial<Record<Snapshot["phase"], string>> = {
  preparing: "Preparing this browser's signing key…",
  creating: "Requesting a session…",
  awaiting_approval: "Waiting for approval on the device…",
  connecting: "Connecting to the device…",
};

export function SessionOverlays({
  snap,
  name,
  forceRelay,
  onReconnect: reconnect,
  onClose,
  onBack,
  onTrust,
}: {
  snap: Snapshot | null;
  name: string;
  forceRelay: boolean;
  onReconnect: (relay?: boolean) => void;
  onClose: () => void;
  onBack: () => void;
  onTrust: (key: string) => void;
}) {
  const phase = snap?.phase ?? "preparing";
  return (
    <>
      {phase === "reconnecting" && (
        <div className="absolute top-4 left-1/2 z-20 flex -translate-x-1/2 items-center gap-2 rounded-full bg-black/70 px-4 py-2 text-sm">
          <Spinner size={16} />
          Connection interrupted — restarting ICE…
        </div>
      )}

      {phaseText[phase] && (
        <Overlay>
          <Spinner size={36} />
          <div className="text-lg">{phaseText[phase]}</div>
          {phase === "awaiting_approval" && (
            <p className="max-w-sm text-center text-sm text-white/70">
              Someone at <b>{name}</b> must allow this session on the device's own screen.
            </p>
          )}
          <button
            className="btn-outline mt-2 border-white/30 text-white hover:bg-white/10"
            onClick={onClose}
          >
            Cancel
          </button>
        </Overlay>
      )}

      {phase === "ended" && snap && (
        <Overlay>
          <Icon
            name={snap.endReason === "user_hangup" ? "check" : "warning"}
            size={40}
            className="text-white/80"
          />
          <div className="text-lg">
            {snap.endReason === "user_hangup" ? "Session ended" : "Session closed"}
          </div>
          <p className="max-w-md text-center text-sm text-white/70">
            {END_REASONS[snap.endReason ?? ""] ?? snap.endReason}
            {snap.endDetail && snap.endDetail !== END_REASONS[snap.endReason ?? ""] && (
              <>
                <br />
                <span className="text-white/50">{snap.endDetail}</span>
              </>
            )}
          </p>
          <div className="mt-2 flex flex-wrap justify-center gap-2">
            {snap.canReconnect && (
              <button className="btn-primary" onClick={() => reconnect()}>
                <Icon name="refresh" size={18} />
                Reconnect
              </button>
            )}
            {snap.canReconnect && !forceRelay && (
              <button
                className="btn-outline border-white/30 text-white hover:bg-white/10"
                onClick={() => reconnect(true)}
              >
                Reconnect via relay
              </button>
            )}
            {forceRelay && (
              <button
                className="btn-outline border-white/30 text-white hover:bg-white/10"
                onClick={() => reconnect(false)}
              >
                Reconnect direct
              </button>
            )}
            <button
              className="btn-outline border-white/30 text-white hover:bg-white/10"
              onClick={onClose}
            >
              Close
            </button>
            <button
              className="btn-outline border-white/30 text-white hover:bg-white/10"
              onClick={onBack}
            >
              Back to devices
            </button>
          </div>
        </Overlay>
      )}

      {phase === "pin_mismatch" && snap?.pinMismatch && (
        <PinMismatch
          name={name}
          pinned={snap.pinMismatch.pinned}
          offered={snap.pinMismatch.offered}
          onCancel={onClose}
          onTrust={() => snap.pinMismatch && onTrust(snap.pinMismatch.offered)}
        />
      )}
    </>
  );
}

function Overlay({ children }: { children: ReactNode }) {
  return (
    <div className="absolute inset-0 z-30 flex flex-col items-center justify-center gap-3 bg-black/80 p-6">
      {children}
    </div>
  );
}

function PinMismatch({
  name,
  pinned,
  offered,
  onCancel,
  onTrust,
}: {
  name: string;
  pinned: string;
  offered: string;
  onCancel: () => void;
  onTrust: () => void;
}) {
  const [fps, setFps] = useState<[string, string]>(["…", "…"]);
  const [ack, setAck] = useState(false);
  useEffect(() => {
    void Promise.all([keyFingerprint(pinned), keyFingerprint(offered)]).then(([a, b]) =>
      setFps([a, b]),
    );
  }, [pinned, offered]);
  return (
    <div className="absolute inset-0 z-40 flex items-center justify-center bg-[#3c1f1d]/95 p-6">
      <div className="max-w-lg space-y-4 rounded-2xl border border-[#f28b82]/40 bg-black/60 p-6">
        <div className="flex items-center gap-3 text-[#f28b82]">
          <Icon name="warning" size={32} />
          <h2 className="text-xl font-medium">Device identity changed</h2>
        </div>
        <p className="text-sm text-white/80">
          <b>{name}</b> presented a different identity key than the one this browser pinned on your
          first session. This happens if the agent was reinstalled — or if someone is impersonating
          the device. The session was refused.
        </p>
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 font-mono text-xs">
          <dt className="text-white/50">Pinned</dt>
          <dd>{fps[0]}</dd>
          <dt className="text-white/50">Offered</dt>
          <dd className="text-[#f28b82]">{fps[1]}</dd>
        </dl>
        <label className="flex items-start gap-2 text-sm text-white/80">
          <input
            type="checkbox"
            className="mt-1"
            checked={ack}
            onChange={(e) => setAck(e.target.checked)}
          />
          I reinstalled or re-enrolled this device myself and want to trust the new key.
        </label>
        <div className="flex justify-end gap-2">
          <button className="btn-primary" onClick={onCancel}>
            Keep me safe
          </button>
          <button
            className="btn-outline border-[#f28b82]/50 text-[#f28b82] hover:bg-white/5"
            disabled={!ack}
            onClick={onTrust}
          >
            Trust new key
          </button>
        </div>
      </div>
    </div>
  );
}
