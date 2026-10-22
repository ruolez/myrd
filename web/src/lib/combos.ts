// Key combinations the controller's own OS would catch (Ctrl+Alt+Del, Alt+Tab,
// Cmd+Space…), sent to the device from the session's Keys menu.

import { hidUsage, KeyState } from "./hid";
import { encodeKey } from "./input";

export type Combo = {
  /** Shown as the button text, in the device's own key names. */
  label: string;
  /** What it does on the device. */
  hint: string;
  /** KeyboardEvent.code names, pressed in order and released in reverse. */
  keys: string[];
};

const windows: Combo[] = [
  {
    label: "Ctrl+Alt+Del",
    hint: "Lock, switch user, sign out, Task Manager",
    keys: ["ControlLeft", "AltLeft", "Delete"],
  },
  { label: "Ctrl+Shift+Esc", hint: "Task Manager", keys: ["ControlLeft", "ShiftLeft", "Escape"] },
  { label: "Alt+Tab", hint: "Switch windows", keys: ["AltLeft", "Tab"] },
  { label: "Alt+F4", hint: "Close the window", keys: ["AltLeft", "F4"] },
  { label: "Win", hint: "Start menu", keys: ["MetaLeft"] },
  { label: "Win+D", hint: "Show the desktop", keys: ["MetaLeft", "KeyD"] },
  { label: "Win+E", hint: "File Explorer", keys: ["MetaLeft", "KeyE"] },
  { label: "Win+R", hint: "Run", keys: ["MetaLeft", "KeyR"] },
  { label: "PrtScn", hint: "Screenshot", keys: ["PrintScreen"] },
];

const macos: Combo[] = [
  { label: "⌘ Tab", hint: "Switch apps", keys: ["MetaLeft", "Tab"] },
  { label: "⌘ Space", hint: "Spotlight", keys: ["MetaLeft", "Space"] },
  { label: "⌥ ⌘ Esc", hint: "Force Quit", keys: ["AltLeft", "MetaLeft", "Escape"] },
  { label: "⌃ ⌘ Q", hint: "Lock the screen", keys: ["ControlLeft", "MetaLeft", "KeyQ"] },
  { label: "⌘ Q", hint: "Quit the app", keys: ["MetaLeft", "KeyQ"] },
  { label: "⌘ W", hint: "Close the window", keys: ["MetaLeft", "KeyW"] },
  { label: "⌃ ↑", hint: "Mission Control", keys: ["ControlLeft", "ArrowUp"] },
  { label: "⇧ ⌘ 3", hint: "Screenshot", keys: ["ShiftLeft", "MetaLeft", "Digit3"] },
  { label: "⇧ ⌘ 4", hint: "Screenshot of an area", keys: ["ShiftLeft", "MetaLeft", "Digit4"] },
];

const linux: Combo[] = [
  { label: "Ctrl+Alt+Del", hint: "Log out or restart", keys: ["ControlLeft", "AltLeft", "Delete"] },
  { label: "Alt+Tab", hint: "Switch windows", keys: ["AltLeft", "Tab"] },
  { label: "Alt+F4", hint: "Close the window", keys: ["AltLeft", "F4"] },
  { label: "Super", hint: "Activities or menu", keys: ["MetaLeft"] },
  { label: "Ctrl+Alt+T", hint: "Terminal", keys: ["ControlLeft", "AltLeft", "KeyT"] },
];

/** The combos for the device's OS (hello.os); Windows' when it is unknown. */
export function combosFor(os: string | null): Combo[] {
  return os === "darwin" ? macos : os === "linux" ? linux : windows;
}

/** Key messages for a combo: each key down in order, then up in reverse, each
 * carrying the modifier byte as it stands after that key, like typed keys. */
export function comboInput(keys: string[]): Uint8Array[] {
  const ks = new KeyState();
  const usages = keys.map((k) => {
    const u = hidUsage(k);
    if (u === undefined) throw new Error(`no HID usage for ${k}`);
    return u;
  });
  const down = usages.map((u) => encodeKey(u, true, ks.down(u)));
  const up = [...usages].reverse().map((u) => encodeKey(u, false, ks.up(u)));
  return [...down, ...up];
}
