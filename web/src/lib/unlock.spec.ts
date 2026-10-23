import { afterEach, describe, expect, test, vi } from "vitest";
import {
  askToSave,
  autoUnlockAction,
  prefillUsername,
  setAskToSave,
  unlockFailure,
} from "./unlock";
import type { AutoUnlockInput } from "./unlock";

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
    expect([unlockFailure("still_locked"), unlockFailure("nonsense"), unlockFailure(null)]).toEqual(
      [
        "Still locked. The saved password may be wrong: update it or type it yourself.",
        "The device could not be unlocked.",
        "The device could not be unlocked.",
      ],
    );
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
