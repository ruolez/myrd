// The remote restart dialog (control `power`), shared by sessions, the
// dashboard menu and the device page. Running the restart is the caller's.

import { useState } from "react";
import { errorMessage } from "../api/client";
import type { PowerAction } from "../lib/power";
import { ErrorBox, Modal } from "./ui";

function describe(action: PowerAction, os: string | null): string {
  if (action === "restart_agent")
    return "Only RemoteAgent restarts. The session ends; reconnect in a few seconds.";
  return os === "darwin"
    ? "The Mac restarts right away. Open apps are quit, and unsaved work may be lost."
    : "Windows restarts in 5 seconds and closes open apps without asking, so unsaved work is lost. It comes back at its sign-in screen.";
}

export function PowerDialog({
  deviceName,
  os,
  actions,
  pending,
  onRun,
  onClose,
}: {
  deviceName: string;
  os: string | null;
  /** The actions the device offers; null when not known until it is connected. */
  actions: PowerAction[] | null;
  /** What is happening while onRun runs (e.g. connecting first). */
  pending?: string;
  onRun: (action: PowerAction) => Promise<void>;
  onClose: () => void;
}) {
  const offered = (a: PowerAction) => actions === null || actions.includes(a);
  const [action, setAction] = useState<PowerAction>(
    offered("restart") ? "restart" : "restart_agent",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      await onRun(action);
    } catch (e) {
      setError(errorMessage(e));
      setBusy(false);
    }
  };

  const choice = (a: PowerAction, title: string) => (
    <label
      className={`flex cursor-pointer gap-3 rounded-xl border p-3 ${action === a ? "border-primary" : "border-outline"} ${offered(a) ? "" : "pointer-events-none opacity-40"}`}
    >
      <input
        type="radio"
        name="power"
        className="mt-1"
        checked={action === a}
        disabled={!offered(a) || busy}
        onChange={() => setAction(a)}
      />
      <span>
        <span className="block font-medium">{title}</span>
        <span className="block text-sm text-muted">{describe(a, os)}</span>
      </span>
    </label>
  );

  return (
    <Modal
      title={`Restart ${deviceName}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn-text" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn-danger"
            disabled={busy || !offered(action)}
            onClick={() => void run()}
          >
            {busy
              ? (pending ?? "Restarting…")
              : action === "restart"
                ? "Restart device"
                : "Restart agent"}
          </button>
        </>
      }
    >
      <div className="space-y-3">
        {error && <ErrorBox error={error} />}
        {choice("restart", "Restart the device")}
        {choice("restart_agent", "Restart the agent")}
      </div>
    </Modal>
  );
}
