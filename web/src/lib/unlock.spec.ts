import { afterEach, describe, expect, test, vi } from "vitest";
import {
  askToSave,
  autoUnlockAction,
  agentTooOld,
  canSaveSignIn,
  prefillUsername,
  setAskToSave,
  showUnlocking,
  unlockFailure,
  unlockKeyFailure,
} from "./unlock";
import type { AutoUnlockInput, UnlockingInput } from "./unlock";

const lockedWithSaved: AutoUnlockInput = {
  live: true,
  focused: true,
  locked: true,
  canUnlock: true,
  saved: true,
  attempted: false,
  prompted: false,
  askToSave: true,
};

describe("autoUnlockAction", () => {
  test("unlocks a locked device in view with a saved password", () => {
    expect(autoUnlockAction(lockedWithSaved)).toBe("unlock");
  });

  test("offers to save a password when none is saved", () => {
    expect(autoUnlockAction({ ...lockedWithSaved, saved: false })).toBe("prompt");
  });

  test.each<[string, Partial<AutoUnlockInput>]>([
    ["the device is not locked", { locked: false }],
    ["the session is in the background", { focused: false }],
    ["the session is not connected", { live: false }],
    ["the device cannot unlock itself", { canUnlock: false }],
    ["the saved credential is still loading", { saved: null }],
    ["an unlock already ran during this lock", { attempted: true }],
  ])("does nothing when %s", (_name, patch) => {
    expect(autoUnlockAction({ ...lockedWithSaved, ...patch })).toBeNull();
  });

  test.each<[string, Partial<AutoUnlockInput>]>([
    ["it was already offered during this lock", { prompted: true }],
    ["the user turned the offer off for this device", { askToSave: false }],
  ])("does not offer to save when %s", (_name, patch) => {
    expect(autoUnlockAction({ ...lockedWithSaved, saved: false, ...patch })).toBeNull();
  });
});

describe("showUnlocking", () => {
  const unlocking: UnlockingInput = {
    locked: true,
    due: false,
    status: "unlocking",
    dismissed: false,
  };

  test.each<[string, Partial<UnlockingInput>]>([
    ["the saved password is about to be sent", { due: true, status: "idle" }],
    ["the device is typing it", { status: "unlocking" }],
    ["it was typed and the device is signing in", { status: "typed" }],
  ])("hides the lock screen while %s", (_name, patch) => {
    expect(showUnlocking({ ...unlocking, ...patch })).toBe(true);
  });

  test.each<[string, Partial<UnlockingInput>]>([
    ["the device unlocked", { locked: false }],
    ["the unlock failed", { status: "failed" }],
    ["nothing is under way", { status: "idle" }],
    ["the user asked to see the screen", { dismissed: true }],
  ])("shows the screen when %s", (_name, patch) => {
    expect(showUnlocking({ ...unlocking, ...patch })).toBe(false);
  });
});

describe("canSaveSignIn", () => {
  test.each([
    ["darwin", true],
    ["windows", true],
    ["linux", false],
  ])("%s → %s", (os, want) => {
    expect(canSaveSignIn({ os })).toBe(want);
  });
});

describe("agentTooOld", () => {
  test.each([
    ["0.4.0", true],
    ["0.5.0-rc.1", true],
    ["0.5.0", false],
    ["0.10.2", false],
  ])("%s → %s", (v, want) => {
    expect(agentTooOld(v)).toBe(want);
  });
});

describe("unlockKeyFailure", () => {
  const live = {
    phase: "connected",
    features: ["stream", "unlock"],
    endDetail: null,
    agentVersion: "0.5.0",
  };

  test("keeps waiting while the session can still deliver the key", () => {
    expect([
      unlockKeyFailure({ ...live, phase: "awaiting_approval", features: null }, "PC"),
      unlockKeyFailure(live, "PC"),
    ]).toEqual([null, null]);
  });

  test("names an agent that does not offer saved sign-in", () => {
    expect(unlockKeyFailure({ ...live, features: ["stream"], agentVersion: "0.4.0" }, "PC")).toBe(
      "PC's agent (0.4.0) did not offer saved sign-in. It needs 0.5.0 or later on macOS or Windows.",
    );
  });

  test("passes on why the session ended", () => {
    expect([
      unlockKeyFailure({ ...live, phase: "ended", endDetail: "Not approved on the device." }, "PC"),
      unlockKeyFailure({ ...live, phase: "ended" }, "PC"),
    ]).toEqual(["Not approved on the device.", "The session to PC ended."]);
  });
});

describe("prefillUsername", () => {
  test.each([
    ["alice", "bob", "alice"],
    [null, `CORP\\bob`, `CORP\\bob`],
    ["", "bob", "bob"],
    [undefined, null, ""],
  ])("saved %j, console user %j → %j", (saved, user, want) => {
    expect(prefillUsername(saved, user)).toBe(want);
  });
});

describe("unlockFailure", () => {
  test("explains a known code and falls back for an unknown one", () => {
    expect([
      unlockFailure("still_locked"),
      unlockFailure("no_prompt"),
      unlockFailure("nonsense"),
      unlockFailure(null),
    ]).toEqual([
      "Still locked. The saved password may be wrong: update it or type it yourself.",
      "The password box did not appear, so nothing was typed. Click the screen, then press Unlock.",
      "The device could not be unlocked.",
      "The device could not be unlocked.",
    ]);
  });
});

describe("askToSave", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  test("remembers turning the offer off per device, and back on", () => {
    const store = new Map<string, string>();
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => store.set(k, v),
    });
    const [a, b] = ["device-a", "device-b"];
    setAskToSave(a, false);
    const off = [askToSave(a), askToSave(b)];
    setAskToSave(a, true);
    expect([...off, askToSave(a)]).toEqual([false, true, true]);
  });
});
