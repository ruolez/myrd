// A command line with the phone's own editing: autocorrect, predictions,
// dictation, the space-bar cursor. Return sends the line (with Enter);
// saved commands (every device) and recent ones (this device) are a tap away.

import { useEffect, useRef, useState } from "react";
import { Icon } from "../../components/icons";
import {
  historyKey,
  pushHistory,
  readList,
  snippetsKey,
  toggleSnippet,
  writeList,
} from "../../lib/termHistory";

export function ComposeBar({
  deviceId,
  onSend,
  onClose,
}: {
  deviceId: string;
  /** The line to type, without the Enter (the caller adds it). */
  onSend: (line: string) => void;
  onClose: () => void;
}) {
  const [text, setText] = useState("");
  const [history, setHistory] = useState(() =>
    readList(globalThis.localStorage, historyKey(deviceId)),
  );
  const [snippets, setSnippets] = useState(() => readList(globalThis.localStorage, snippetsKey));
  const ref = useRef<HTMLTextAreaElement>(null);
  const saved = snippets.includes(text.trim());

  useEffect(() => {
    ref.current?.focus({ preventScroll: true });
  }, []);
  // One line tall, growing to four.
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 4 * 22 + 12)}px`;
  }, [text]);

  const send = () => {
    onSend(text);
    const next = pushHistory(history, text);
    setHistory(next);
    writeList(globalThis.localStorage, historyKey(deviceId), next);
    setText("");
  };

  const toggleSaved = () => {
    const next = toggleSnippet(snippets, text);
    setSnippets(next);
    writeList(globalThis.localStorage, snippetsKey, next);
  };
  const recent = history.filter((h) => !snippets.includes(h)).slice(0, 12);
  const chip = "max-w-[60vw] shrink-0 truncate rounded-full px-3 py-1.5 font-mono text-xs";

  return (
    <div className="shrink-0 border-t border-white/10 bg-[#1b1c1d] px-2 pt-1.5 pb-1.5">
      {!text && (snippets.length > 0 || recent.length > 0) && (
        <div className="mb-1.5 flex gap-1.5 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          {snippets.map((h) => (
            <button
              key={`s:${h}`}
              type="button"
              aria-label={`Saved: ${h}`}
              className={`${chip} flex items-center gap-1 bg-[#8ab4f8]/20 text-[#d2e3fc]`}
              onPointerDown={(e) => e.preventDefault()}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => setText(h)}
            >
              <Icon name="star" size={12} className="shrink-0" />
              <span className="truncate">{h}</span>
            </button>
          ))}
          {recent.map((h) => (
            <button
              key={h}
              type="button"
              className={`${chip} bg-white/10 text-white/85`}
              onPointerDown={(e) => e.preventDefault()}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => setText(h)}
            >
              {h}
            </button>
          ))}
        </div>
      )}
      <div className="flex items-end gap-1.5">
        <textarea
          ref={ref}
          rows={1}
          value={text}
          aria-label="Command"
          placeholder="Type a command…"
          className="min-h-10 flex-1 resize-none rounded-xl border border-white/15 bg-[#131314] px-3 py-2 font-mono text-base leading-[22px] text-white placeholder:text-white/35 focus:border-[#8ab4f8] focus:outline-none"
          autoCapitalize="none"
          autoCorrect="on"
          spellCheck
          enterKeyHint="send"
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              send();
            } else if (e.key === "Escape") {
              e.preventDefault();
              onClose();
            }
          }}
        />
        {text.trim() && (
          <button
            type="button"
            aria-label={saved ? "Remove from saved commands" : "Save this command"}
            aria-pressed={saved}
            className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-full hover:bg-white/10 ${saved ? "text-[#fdd663]" : "text-white/60"}`}
            onPointerDown={(e) => e.preventDefault()}
            onMouseDown={(e) => e.preventDefault()}
            onClick={toggleSaved}
          >
            <Icon name={saved ? "star" : "starOutline"} size={20} />
          </button>
        )}
        <button
          type="button"
          aria-label="Send the command"
          className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-[#8ab4f8] text-[#131314] disabled:opacity-40"
          onPointerDown={(e) => e.preventDefault()}
          onMouseDown={(e) => e.preventDefault()}
          onClick={send}
        >
          <Icon name="send" size={18} />
        </button>
      </div>
    </div>
  );
}
