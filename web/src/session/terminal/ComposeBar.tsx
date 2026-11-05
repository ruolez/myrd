// A command line with the phone's own editing: autocorrect, predictions,
// dictation, the space-bar cursor. Return sends the line (with Enter);
// recent commands of this device are a tap away.

import { useEffect, useRef, useState } from "react";
import { Icon } from "../../components/icons";
import { historyKey, pushHistory, readList, writeList } from "../../lib/termHistory";

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
  const ref = useRef<HTMLTextAreaElement>(null);

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

  return (
    <div className="shrink-0 border-t border-white/10 bg-[#1b1c1d] px-2 pt-1.5 pb-1.5">
      {!text && history.length > 0 && (
        <div className="mb-1.5 flex gap-1.5 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          {history.slice(0, 12).map((h) => (
            <button
              key={h}
              type="button"
              className="max-w-[60vw] shrink-0 truncate rounded-full bg-white/10 px-3 py-1.5 font-mono text-xs text-white/85"
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
