// The saved sign-in form (PROTOCOL §4.2), shared by sessions, the dashboard
// menu and the device page. Sealing and storing are the caller's.

import { useState } from "react";
import type { FormEvent } from "react";
import { errorMessage } from "../api/client";
import { ErrorBox, Field, Modal } from "./ui";

export function CredentialDialog({
  deviceName,
  initialUsername,
  saved,
  prompted = false,
  locked = false,
  unavailable,
  onSave,
  onRemove,
  onClose,
  onNeverAsk,
}: {
  deviceName: string;
  initialUsername: string;
  /** A sign-in is saved: offer removing it. */
  saved: boolean;
  /** Opened by itself on a locked device (offers "don't ask again"). */
  prompted?: boolean;
  /** Saving also unlocks the device. */
  locked?: boolean;
  /** Why nothing can be saved from here (the form is not shown). */
  unavailable?: string | null;
  onSave: (username: string, password: string) => Promise<void>;
  onRemove: () => Promise<void>;
  onClose: () => void;
  onNeverAsk?: () => void;
}) {
  const [username, setUsername] = useState(initialUsername);
  const [password, setPassword] = useState("");
  const [neverAsk, setNeverAsk] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void run(() => onSave(username.trim(), password));
  };
  const dismiss = () => {
    if (neverAsk) onNeverAsk?.();
    onClose();
  };

  return (
    <Modal title={`Sign-in for ${deviceName}`} onClose={dismiss}>
      <form className="space-y-4" onSubmit={submit}>
        <p className="text-muted">
          {prompted ? `${deviceName} is locked. ` : ""}
          Save its sign-in and opening it while locked unlocks it for you. The password is encrypted
          so that only {deviceName} can read it, and it types it only on its own lock screen.
        </p>
        {error && <ErrorBox error={error} />}
        {unavailable ? (
          <p className="rounded-lg bg-surface-2 p-3">{unavailable}</p>
        ) : (
          <>
            <Field label="Username" hint="Shown for reference: the device types only the password.">
              <input
                className="input"
                autoComplete="off"
                spellCheck={false}
                maxLength={256}
                value={username}
                autoFocus={username === ""}
                onChange={(e) => setUsername(e.target.value)}
              />
            </Field>
            <Field
              label="Password"
              hint={saved ? "Saving replaces the stored password." : undefined}
            >
              <input
                className="input"
                type="password"
                autoComplete="new-password"
                required
                maxLength={256}
                value={password}
                autoFocus={username !== ""}
                onChange={(e) => setPassword(e.target.value)}
              />
            </Field>
          </>
        )}
        {prompted && !saved && onNeverAsk && (
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
          {saved && (
            <button
              type="button"
              className="btn-text mr-auto text-error"
              disabled={busy}
              onClick={() => void run(onRemove)}
            >
              Remove saved sign-in
            </button>
          )}
          <button type="button" className="btn-text" onClick={dismiss}>
            {prompted ? "Not now" : unavailable ? "Close" : "Cancel"}
          </button>
          {!unavailable && (
            <button className="btn-primary" disabled={busy || password === ""}>
              {locked ? "Save and unlock" : "Save"}
            </button>
          )}
        </div>
      </form>
    </Modal>
  );
}
