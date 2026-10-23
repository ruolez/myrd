import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { SavedCredential } from "../api/types";
import { Icon } from "../components/icons";
import { ErrorBox, Field, Modal } from "../components/ui";
import {
  askToSave,
  autoUnlockAction,
  prefillUsername,
  setAskToSave,
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
  dialog: { prompted: boolean } | null;
  openDialog: () => void;
  closeDialog: () => void;
  unlockNow: () => void;
  save: (username: string, password: string) => Promise<void>;
  remove: () => Promise<void>;
};

/**
 * Saved sign-in for one session: loads the device's saved credential, types
 * it when the session is opened on a locked device (once per lock), and
 * offers to save one when none is saved.
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
  const attempted = useRef(false);
  const prompted = useRef(false);

  useEffect(() => {
    if (!canUnlock) return;
    let stale = false;
    api.devices
      .credential(deviceId)
      .then((r) => !stale && setSaved(r.credential))
      .catch(() => !stale && setSaved(null));
    return () => {
      stale = true;
    };
  }, [canUnlock, deviceId]);

  // A new lock is a new chance: unlock once more, offer saving once more.
  useEffect(() => {
    if (locked) return;
    attempted.current = false;
    prompted.current = false;
    setStatus({ kind: "idle" });
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
  const [username, setUsername] = useState(() =>
    prefillUsername(signIn.saved?.username, consoleUser),
  );
  const [password, setPassword] = useState("");
  const [neverAsk, setNeverAsk] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const prompted = signIn.dialog?.prompted ?? false;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await signIn.save(username.trim(), password);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  const dismiss = () => {
    if (neverAsk) setAskToSave(signIn.deviceId, false);
    signIn.closeDialog();
  };
  const remove = async () => {
    setBusy(true);
    try {
      await signIn.remove();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  return (
    <Modal title={`Sign-in for ${deviceName}`} onClose={dismiss}>
      <form className="space-y-4" onSubmit={(e) => void submit(e)}>
        <p className="text-muted">
          {prompted ? `${deviceName} is locked. ` : ""}
          Save its sign-in and opening it while locked unlocks it for you. The password is encrypted
          so that only {deviceName} can read it, and it types it only on its own lock screen.
        </p>
        {error && <ErrorBox error={error} />}
        <Field label="Username" hint="Shown for reference: the device types only the password.">
          <input
            className="input"
            autoComplete="off"
            spellCheck={false}
            maxLength={256}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </Field>
        <Field
          label="Password"
          hint={signIn.saved ? "Saving replaces the stored password." : undefined}
        >
          <input
            className="input"
            type="password"
            autoComplete="new-password"
            required
            maxLength={256}
            value={password}
            autoFocus
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        {prompted && !signIn.saved && (
          <label className="flex items-center gap-2 text-muted">
            <input
              type="checkbox"
              checked={neverAsk}
              onChange={(e) => setNeverAsk(e.target.checked)}
            />
            Don't offer this again for {deviceName}
          </label>
        )}
        <div className="flex flex-wrap items-center justify-end gap-2">
          {signIn.saved && (
            <button
              type="button"
              className="btn-text mr-auto text-error"
              disabled={busy}
              onClick={() => void remove()}
            >
              Remove saved sign-in
            </button>
          )}
          <button type="button" className="btn-text" onClick={dismiss}>
            {prompted ? "Not now" : "Cancel"}
          </button>
          <button className="btn-primary" disabled={busy || password === ""}>
            {locked ? "Save and unlock" : "Save"}
          </button>
        </div>
      </form>
    </Modal>
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
