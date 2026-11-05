// What a terminal key sends (xterm conventions), the sticky Ctrl/Alt of the
// phone key bar, and turning soft-keyboard edits into terminal bytes. Pure.

export type ArrowKey = "up" | "down" | "left" | "right";
export type NamedKey =
  | ArrowKey
  | "esc"
  | "tab"
  | "enter"
  | "backspace"
  | "home"
  | "end"
  | "pageUp"
  | "pageDown"
  | "delete"
  | "insert"
  | `f${1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 | 11 | 12}`;

export type Mods = { ctrl: boolean; alt: boolean; shift?: boolean };
export const NO_MODS: Mods = { ctrl: false, alt: false };

const ARROW_FINAL: Record<ArrowKey, string> = { up: "A", down: "B", right: "C", left: "D" };
const TILDE: Partial<Record<NamedKey, number>> = {
  insert: 2,
  delete: 3,
  pageUp: 5,
  pageDown: 6,
  f5: 15,
  f6: 17,
  f7: 18,
  f8: 19,
  f9: 20,
  f10: 21,
  f11: 23,
  f12: 24,
};
const SS3: Partial<Record<NamedKey, string>> = {
  f1: "P",
  f2: "Q",
  f3: "R",
  f4: "S",
  home: "H",
  end: "F",
};

/** xterm's modifier parameter: 1 + shift + 2·alt + 4·ctrl. */
function modParam(m: Mods): number {
  return 1 + (m.shift ? 1 : 0) + (m.alt ? 2 : 0) + (m.ctrl ? 4 : 0);
}

/**
 * The bytes of a named key. appCursor is DECCKM (full-screen programs ask
 * for SS3 arrows).
 */
export function keySequence(key: NamedKey, mods: Mods = NO_MODS, appCursor = false): string {
  const p = modParam(mods);
  switch (key) {
    case "esc":
      return mods.alt ? "\x1b\x1b" : "\x1b";
    case "tab":
      return mods.shift ? "\x1b[Z" : mods.alt ? "\x1b\t" : "\t";
    case "enter":
      return mods.alt ? "\x1b\r" : "\r";
    case "backspace":
      return (mods.alt ? "\x1b" : "") + (mods.ctrl ? "\x08" : "\x7f");
  }
  if (key in ARROW_FINAL) {
    const f = ARROW_FINAL[key as ArrowKey];
    if (p > 1) return `\x1b[1;${p}${f}`;
    return appCursor ? `\x1bO${f}` : `\x1b[${f}`;
  }
  const t = TILDE[key];
  if (t !== undefined) return p > 1 ? `\x1b[${t};${p}~` : `\x1b[${t}~`;
  const s = SS3[key]!;
  if (p > 1) return `\x1b[1;${p}${s}`;
  return key === "home" || key === "end" ? (appCursor ? `\x1bO${s}` : `\x1b[${s}`) : `\x1bO${s}`;
}

/** Ctrl+<char>: the C0 control character, or null when Ctrl does nothing to it. */
export function ctrlChar(ch: string): string | null {
  if (ch.length !== 1) return null;
  const c = ch.toLowerCase();
  if (c >= "a" && c <= "z") return String.fromCharCode(c.charCodeAt(0) - 96);
  switch (ch) {
    case " ":
    case "@":
    case "2":
      return "\x00";
    case "[":
    case "3":
      return "\x1b";
    case "\\":
    case "4":
      return "\x1c";
    case "]":
    case "5":
      return "\x1d";
    case "^":
    case "6":
      return "\x1e";
    case "_":
    case "-":
    case "7":
      return "\x1f";
    case "?":
    case "8":
      return "\x7f";
  }
  return null;
}

/**
 * Typed text with the armed modifiers: Ctrl applies to the first character
 * (Ctrl+C), Alt prefixes it with ESC (Meta). The rest goes as is.
 */
export function withMods(text: string, mods: Mods): string {
  if (!text || (!mods.ctrl && !mods.alt)) return text;
  const first = [...text][0]!;
  const rest = text.slice(first.length);
  let head = first;
  if (mods.ctrl) head = ctrlChar(first) ?? first;
  if (mods.alt) head = "\x1b" + head;
  return head + rest;
}

// ---------- sticky modifiers (phone key bar) ----------

/** off → armed (next key only) → locked (until tapped again). */
export type ModState = "off" | "armed" | "locked";

export const DOUBLE_TAP_MS = 350;

/** A tap on a modifier key; a second tap soon after the first locks it. */
export function tapMod(s: ModState, sinceLastTapMs: number): ModState {
  switch (s) {
    case "off":
      return "armed";
    case "armed":
      return sinceLastTapMs <= DOUBLE_TAP_MS ? "locked" : "off";
    case "locked":
      return "off";
  }
}

/** After a key was sent: an armed modifier is spent, a locked one stays. */
export const afterKey = (s: ModState): ModState => (s === "armed" ? "off" : s);

export const isOn = (s: ModState) => s !== "off";

// ---------- soft keyboard ----------

/**
 * The hidden input always holds SENTINEL, so a phone keyboard's backspace
 * has something to delete (Android sends nothing on an empty field).
 */
export const SENTINEL = "  ";

/**
 * What changed between the field's value before and after an edit, assuming
 * the caret sat at the end: characters deleted from the end and text added.
 */
export function diffInput(before: string, after: string): { deleted: number; inserted: string } {
  let common = 0;
  const max = Math.min(before.length, after.length);
  while (common < max && before[common] === after[common]) common++;
  // Never split a surrogate pair (😀 → 😃 share their first half).
  const hi = before.charCodeAt(common - 1);
  if (common > 0 && hi >= 0xd800 && hi <= 0xdbff) common--;
  return { deleted: [...before.slice(common)].length, inserted: after.slice(common) };
}

/** Bytes for an edit of the hidden field: backspaces, then the text (newlines as Enter). */
export function editBytes(deleted: number, inserted: string, mods: Mods): string {
  const text = inserted.replace(/\r\n|\n|\r/g, "\r");
  return "\x7f".repeat(deleted) + withMods(text, mods);
}

// ---------- pasting ----------

/** Text for the shell: newlines as Enter, and bracketed when the program asked for it. */
export function pasteBytes(text: string, bracketed: boolean): string {
  const t = text.replace(/\r\n|\n/g, "\r");
  // A pasted end marker would end the paste early and run the rest.
  return bracketed ? `\x1b[200~${t.split("\x1b[201~").join("")}\x1b[201~` : t;
}
