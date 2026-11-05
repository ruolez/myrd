// The phone keyboard's target: a hidden field that always holds SENTINEL.
// Every edit is diffed against it (autocorrect replacements, dictation,
// Android's keyCode 229 keys) and turned into terminal bytes; keys that
// type nothing (arrows, Esc, Ctrl+C on a hardware keyboard) come from
// keydown.

import { useEffect } from "react";
import type { RefObject } from "react";
import { ctrlChar, diffInput, editBytes, keySequence, SENTINEL } from "../../lib/termKeys";
import type { Mods, NamedKey } from "../../lib/termKeys";

const KEYS: Record<string, NamedKey> = {
  ArrowUp: "up",
  ArrowDown: "down",
  ArrowLeft: "left",
  ArrowRight: "right",
  Escape: "esc",
  Tab: "tab",
  Home: "home",
  End: "end",
  PageUp: "pageUp",
  PageDown: "pageDown",
  Delete: "delete",
  Insert: "insert",
  F1: "f1",
  F2: "f2",
  F3: "f3",
  F4: "f4",
  F5: "f5",
  F6: "f6",
  F7: "f7",
  F8: "f8",
  F9: "f9",
  F10: "f10",
  F11: "f11",
  F12: "f12",
};

type Props = {
  inputRef: RefObject<HTMLTextAreaElement | null>;
  /** Armed modifiers of the key bar (consumed by the next key). */
  mods: () => Mods;
  appCursor: () => boolean;
  /** Bytes to type; usedMods: the key bar's modifiers were applied. */
  onBytes: (s: string, usedMods: boolean) => void;
  onPaste: (text: string) => void;
  onFocusChange: (focused: boolean) => void;
};

export function TermKeyboard({
  inputRef,
  mods,
  appCursor,
  onBytes,
  onPaste,
  onFocusChange,
}: Props) {
  useEffect(() => {
    const el = inputRef.current;
    if (!el) return;
    let composing = false;
    const reset = () => {
      el.value = SENTINEL;
      el.setSelectionRange(SENTINEL.length, SENTINEL.length);
    };
    const flush = () => {
      const { deleted, inserted } = diffInput(SENTINEL, el.value);
      reset();
      if (!deleted && !inserted) return;
      const m = mods();
      onBytes(editBytes(deleted, inserted, m), inserted.length > 0 && (m.ctrl || m.alt));
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229 || e.key === "Process") return;
      const bar = mods();
      const m: Mods = { ctrl: e.ctrlKey || bar.ctrl, alt: e.altKey || bar.alt, shift: e.shiftKey };
      const named = KEYS[e.key];
      if (named) {
        e.preventDefault();
        onBytes(keySequence(named, m, appCursor()), bar.ctrl || bar.alt);
        return;
      }
      if (e.key === "Enter") {
        e.preventDefault();
        onBytes(keySequence("enter", m), bar.alt);
        return;
      }
      // Hardware Ctrl / Alt combos type nothing into the field.
      if ((e.ctrlKey || (e.altKey && !e.metaKey && e.key.length === 1)) && !e.metaKey) {
        const c = e.ctrlKey ? ctrlChar(e.key) : e.key;
        if (c === null) return;
        e.preventDefault();
        onBytes((e.altKey ? "\x1b" : "") + c, false);
      }
    };
    const onBeforeInput = (e: InputEvent) => {
      if (e.inputType === "insertLineBreak" || e.inputType === "insertParagraph") {
        e.preventDefault();
        onBytes("\r", false);
      }
    };
    const onInput = () => {
      if (!composing) flush();
    };
    const onCompStart = () => {
      composing = true;
    };
    const onCompEnd = () => {
      composing = false;
      flush();
    };
    const onPasteEv = (e: ClipboardEvent) => {
      const text = e.clipboardData?.getData("text/plain") ?? "";
      e.preventDefault();
      if (text) onPaste(text);
    };
    const onFocus = () => {
      reset();
      onFocusChange(true);
    };
    const onBlur = () => onFocusChange(false);
    reset();
    el.addEventListener("keydown", onKeyDown);
    el.addEventListener("beforeinput", onBeforeInput);
    el.addEventListener("input", onInput);
    el.addEventListener("compositionstart", onCompStart);
    el.addEventListener("compositionend", onCompEnd);
    el.addEventListener("paste", onPasteEv);
    el.addEventListener("focus", onFocus);
    el.addEventListener("blur", onBlur);
    return () => {
      el.removeEventListener("keydown", onKeyDown);
      el.removeEventListener("beforeinput", onBeforeInput);
      el.removeEventListener("input", onInput);
      el.removeEventListener("compositionstart", onCompStart);
      el.removeEventListener("compositionend", onCompEnd);
      el.removeEventListener("paste", onPasteEv);
      el.removeEventListener("focus", onFocus);
      el.removeEventListener("blur", onBlur);
    };
  }, [inputRef, mods, appCursor, onBytes, onPaste, onFocusChange]);

  return (
    <textarea
      ref={inputRef}
      aria-label="Type in the terminal"
      className="pointer-events-none absolute top-0 left-0 h-px w-px resize-none overflow-hidden border-0 bg-transparent p-0 text-base opacity-0"
      autoCapitalize="none"
      autoCorrect="off"
      autoComplete="off"
      spellCheck={false}
      enterKeyHint="enter"
      inputMode="text"
      defaultValue={SENTINEL}
    />
  );
}
