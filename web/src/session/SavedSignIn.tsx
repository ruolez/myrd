import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import type { SavedCredential } from "../api/types";
import { CredentialDialog } from "../components/CredentialDialog";
import { Icon } from "../components/icons";
import { Spinner } from "../components/ui";
import {
  askToSave,
  autoUnlockAction,
  prefillUsername,
  setAskToSave,
  showUnlocking,
  unlockFailure,
} from "../lib/unlock";
import type { RemoteSession, Snapshot } from "./RemoteSession";

// A device that typed the password but is still locked after this long most
// likely got a wrong one.
const STILL_LOCKED_MS = 15_000;

type UnlockStatus =
  | { kind: "idle" }
  | { kind: "unlocking" }
  | { kind: "typed" }
  | { kind: "failed"; message: string };

export type SavedSignIn = {
  deviceId: string;
  canUnlock: boolean;
  /** undefined while loading. */
  saved: SavedCredential | null | undefined;
  status: UnlockStatus;
  /** The lock screen is covered by "Unlocking…". */
  covering: boolean;
  /** Shows the lock screen under "Unlocking…". */
  uncover: () => void;
  dialog: { prompted: boolean } | null;
  openDialog: () => void;
  closeDialog: () => void;
  unlockNow: () => void;
  save: (username: string, password: string) => Promise<void>;
  remove: () => Promise<void>;
};

/**
 * Saved sign-in for one session: loads the device's saved credential while
 * the session connects, types it as soon as the session finds the device
 * locked (once per lock), and offers to save one when none is saved.
 */
export function useSavedSignIn(
  session: RemoteSession,
  snap: Snapshot | null,
  focused: boolean,
): SavedSignIn {
  const deviceId = session.deviceId;
  const canUnlock = snap?.unlockKey != null;
  const locked = snap?.locked ?? false;
  const live = snap?.phase === "connected" || snap?.phase === "reconnecting";
  const [saved, setSaved] = useState<SavedCredential | null | undefined>(undefined);
  const [status, setStatus] = useState<UnlockStatus>({ kind: "idle" });
  const [dialog, setDialog] = useState<{ prompted: boolean } | null>(null);
  const [uncovered, setUncovered] = useState(false);
  const attempted = useRef(false);
  const prompted = useRef(false);

  // Not waiting for the unlock key: the password is at hand when the device says it is locked.
  useEffect(() => {
    let stale = false;
    api.devices
      .credential(deviceId)
      .then((r) => !stale && setSaved(r.credential))
      .catch(() => !stale && setSaved(null));
    return () => {
      stale = true;
    };
  }, [deviceId]);

  // A new lock is a new chance: unlock once more, offer saving once more.
  useEffect(() => {
    if (locked) return;
    attempted.current = false;
    prompted.current = false;
    setStatus({ kind: "idle" });
    setUncovered(false);
  }, [locked]);

  useEffect(() => {
    if (status.kind !== "typed") return;
    const t = setTimeout(
      () => setStatus({ kind: "failed", message: unlockFailure("still_locked") }),
      STILL_LOCKED_MS,
    );
    return () => clearTimeout(t);
  }, [status]);

  const runUnlock = useCallback(
    async (cred: SavedCredential) => {
      attempted.current = true;
      setStatus({ kind: "unlocking" });
      const r = await session.unlock(cred.sealed);
      if (r.ok) setStatus({ kind: "typed" });
      else if (r.code === "not_locked" || r.code === "ended") setStatus({ kind: "idle" });
      else setStatus({ kind: "failed", message: unlockFailure(r.code) });
    },
    [session],
  );

  const action = autoUnlockAction({
    live,
    focused,
    locked,
    canUnlock,
    saved: saved === undefined ? null : saved !== null,
    attempted: attempted.current,
    prompted: prompted.current || dialog !== null,
    askToSave: askToSave(deviceId),
  });
  useEffect(() => {
    if (action === "unlock" && saved) void runUnlock(saved);
    if (action === "prompt") {
      prompted.current = true;
      setDialog({ prompted: true });
    }
  }, [action, saved, runUnlock]);

  const save = async (username: string, password: string) => {
    const sealed = await session.sealCredential(username, password);
    const { credential } = await api.devices.saveCredential(deviceId, username, sealed);
    setSaved(credential);
    setDialog(null);
    if (session.snapshot.locked) void runUnlock(credential);
  };

  const remove = async () => {
    await api.devices.removeCredential(deviceId);
    setSaved(null);
    setDialog(null);
  };

  return {
    deviceId,
    canUnlock,
    saved,
    status,
    covering:
      live &&
      showUnlocking({
        locked,
        due: action === "unlock",
        status: status.kind,
        dismissed: uncovered,
      }),
    uncover: () => setUncovered(true),
    dialog,
    openDialog: () => setDialog({ prompted: false }),
    closeDialog: () => setDialog(null),
    unlockNow: () => saved && void runUnlock(saved),
    save,
    remove,
  };
}

export function SignInDialog({
  deviceName,
  consoleUser,
  signIn,
  locked,
}: {
  deviceName: string;
  consoleUser: string | null;
  signIn: SavedSignIn;
  locked: boolean;
}) {
  return (
    <CredentialDialog
      deviceName={deviceName}
      initialUsername={prefillUsername(signIn.saved?.username, consoleUser)}
      saved={!!signIn.saved}
      prompted={signIn.dialog?.prompted ?? false}
      locked={locked}
      onSave={signIn.save}
      onRemove={signIn.remove}
      onClose={signIn.closeDialog}
      onNeverAsk={() => setAskToSave(signIn.deviceId, false)}
    />
  );
}

/** The bar shown while the device is locked. */
export function LockedBanner({ deviceName, signIn }: { deviceName: string; signIn: SavedSignIn }) {
  const { status, saved, canUnlock } = signIn;
  let text: string;
  if (status.kind === "unlocking" || status.kind === "typed") text = `Unlocking ${deviceName}…`;
  else if (status.kind === "failed") text = status.message;
  else if (canUnlock && saved) text = `${deviceName} is locked.`;
  else
    text = `${deviceName} is locked. Click the screen, type its password and press Enter, even if the screen stays black.`;
  const busy = status.kind === "unlocking" || status.kind === "typed";
  return (
    <div
      role="status"
      className="absolute bottom-4 left-1/2 z-20 flex max-w-[calc(100%-2rem)] -translate-x-1/2 items-center gap-2 rounded-full bg-black/80 px-4 py-2 text-sm text-white shadow-lg"
    >
      <Icon name="lock" size={16} />
      <span>{text}</span>
      {canUnlock && saved && !busy && (
        <button
          className="rounded-full bg-white/15 px-3 py-0.5 hover:bg-white/25"
          onClick={signIn.unlockNow}
        >
          Unlock
        </button>
      )}
      {canUnlock && saved !== undefined && !busy && (
        <button
          className="rounded-full px-2 py-0.5 text-white/80 hover:bg-white/15"
          onClick={signIn.openDialog}
        >
          {saved ? "Edit" : "Save sign-in…"}
        </button>
      )}
    </div>
  );
}

/** Covers the lock screen while the saved password goes in. */
export function UnlockingOverlay({
  deviceName,
  signIn,
}: {
  deviceName: string;
  signIn: SavedSignIn;
}) {
  return (
    <div
      role="status"
      aria-hidden={!signIn.covering}
      className={`absolute inset-0 z-20 flex flex-col items-center justify-center gap-4 bg-black/90 text-white transition-opacity duration-300 motion-reduce:transition-none ${
        signIn.covering ? "opacity-100" : "pointer-events-none opacity-0"
      }`}
    >
      <Spinner size={36} label={`Unlocking ${deviceName}`} />
      <div className="text-lg">Unlocking {deviceName}…</div>
      <button
        className="rounded-full px-3 py-1 text-sm text-white/70 hover:bg-white/15 hover:text-white"
        onClick={signIn.uncover}
        tabIndex={signIn.covering ? 0 : -1}
      >
        Show lock screen
      </button>
    </div>
  );
}
