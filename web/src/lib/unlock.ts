// Saved sign-in in a session: when to unlock a locked device on its own, when
// to offer saving a password, and what a refusal from the device means.

export type AutoUnlockInput = {
  live: boolean;
  focused: boolean;
  locked: boolean;
  /** The device advertised `unlock` and its unlock key verified. */
  canUnlock: boolean;
  /** A credential is saved for the device; null while loading. */
  saved: boolean | null;
  /** An unlock already ran while the device has been locked. */
  attempted: boolean;
  /** The save form was already offered while the device has been locked. */
  prompted: boolean;
  /** The user did not turn the offer off for this device. */
  askToSave: boolean;
};

/**
 * What opening a locked session does by itself: type the saved password
 * once per lock (a wrong one must not lock the account out by retrying), or
 * offer to save one.
 */
export function autoUnlockAction(s: AutoUnlockInput): "unlock" | "prompt" | null {
  if (!s.live || !s.focused || !s.locked || !s.canUnlock || s.saved === null) return null;
  if (s.saved) return s.attempted ? null : "unlock";
  return s.prompted || !s.askToSave ? null : "prompt";
}

/** Devices whose agent can type a saved password (macOS and Windows, agent 0.5.0 and later). */
export const canSaveSignIn = (d: { os: string }) => d.os === "darwin" || d.os === "windows";

/** The username the save form starts with: the saved one, else the device's signed-in user. */
export function prefillUsername(
  saved: string | null | undefined,
  consoleUser: string | null | undefined,
) {
  return saved || consoleUser || "";
}

const failures: Record<string, string> = {
  still_locked: "Still locked. The saved password may be wrong: update it or type it yourself.",
  untrusted:
    "The device does not trust the browser that saved this password. Save it again from this browser.",
  stale_key: "The device's identity changed since the password was saved. Save it again.",
  bad_credential: "The device could not open the saved password. Save it again.",
  busy: "An unlock is already running. Try again in a few seconds.",
  too_soon: "An unlock just ran. Try again in a few seconds.",
  not_locked: "The device is no longer locked.",
  input_failed: "The device could not type the password.",
  unsupported: "This device cannot unlock itself. Update its agent.",
  timeout: "The device did not answer.",
};

export function unlockFailure(code: string | null): string {
  return (code && failures[code]) || "The device could not be unlocked.";
}

const NO_PROMPT_KEY = "remotectl-unlock-no-prompt";

function noPromptSet(): Set<string> {
  try {
    const raw = localStorage.getItem(NO_PROMPT_KEY);
    return new Set(raw ? (JSON.parse(raw) as string[]) : []);
  } catch {
    return new Set();
  }
}

/** Whether to offer saving a password when this device is found locked. */
export function askToSave(deviceId: string): boolean {
  return !noPromptSet().has(deviceId);
}

export function setAskToSave(deviceId: string, ask: boolean) {
  const s = noPromptSet();
  if (ask) s.delete(deviceId);
  else s.add(deviceId);
  try {
    localStorage.setItem(NO_PROMPT_KEY, JSON.stringify([...s]));
  } catch {
    // private window: the offer comes back next time
  }
}
