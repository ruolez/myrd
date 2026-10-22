// Workspace shortcuts that never reach the remote. Browsers keep Ctrl+W/T/N
// and Ctrl+Tab for themselves unless the keyboard is locked (fullscreen,
// Chromium), so the always-available chords use Ctrl+Alt+Shift, which
// applications rarely bind. Keys are matched by physical code, so layouts
// do not matter.

export type Hotkey =
  | { t: "streamMenu" }
  | { t: "palette" }
  | { t: "dashboard" }
  | { t: "prev" }
  | { t: "next" }
  | { t: "focus"; n: number };

export type HotkeyEvent = {
  code: string;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  metaKey: boolean;
};

/** The workspace action for a keydown, or null for keys that go to the remote. */
export function workspaceHotkey(e: HotkeyEvent, keyboardLocked = false): Hotkey | null {
  if (keyboardLocked && e.code === "Tab" && e.ctrlKey && !e.altKey && !e.metaKey)
    return { t: e.shiftKey ? "prev" : "next" };
  if (!(e.ctrlKey && e.altKey && e.shiftKey) || e.metaKey) return null;
  const digit = /^Digit([1-9])$/.exec(e.code);
  if (digit) return { t: "focus", n: Number(digit[1]) };
  switch (e.code) {
    case "KeyS":
      return { t: "streamMenu" };
    case "KeyK":
      return { t: "palette" };
    case "KeyD":
      return { t: "dashboard" };
    case "ArrowLeft":
      return { t: "prev" };
    case "ArrowRight":
      return { t: "next" };
  }
  return null;
}

/** Shown in tooltips and the palette's help. */
export const HOTKEY_HELP: [string, string][] = [
  ["Ctrl+Alt+Shift+1…9", "Switch to session 1…9"],
  ["Ctrl+Alt+Shift+← / →", "Previous / next session"],
  ["Ctrl+Alt+Shift+D", "Devices dashboard"],
  ["Ctrl+Alt+Shift+K", "Command palette"],
  ["Ctrl+Alt+Shift+S", "Stream settings"],
  ["Ctrl+Tab", "Next session (fullscreen with keyboard lock)"],
];
