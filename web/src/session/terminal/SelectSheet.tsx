// The terminal's text in a plain read-only field, so the phone's own
// selection handles and copy menu work on it.

import { useState } from "react";
import { Icon } from "../../components/icons";

export function SelectSheet({ text, onClose }: { text: string; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <div
      className="absolute inset-0 z-40 flex flex-col bg-[#131314] text-white"
      role="dialog"
      aria-label="Select text"
    >
      <div className="flex h-11 shrink-0 items-center gap-2 border-b border-white/10 px-2">
        <button
          className="btn btn-sm text-white/85 hover:bg-white/10"
          onClick={onClose}
          aria-label="Back to the terminal"
        >
          <Icon name="back" size={18} />
        </button>
        <span className="flex-1 text-sm text-white/70">Select text to copy</span>
        <button
          className="btn btn-sm text-white/85 hover:bg-white/10"
          onClick={() => {
            void navigator.clipboard?.writeText(text).then(() => setCopied(true));
          }}
        >
          <Icon name={copied ? "check" : "copy"} size={16} />
          {copied ? "Copied" : "Copy all"}
        </button>
      </div>
      <textarea
        readOnly
        value={text}
        aria-label="Terminal text"
        className="min-h-0 flex-1 resize-none bg-transparent p-3 font-mono text-[13px] leading-snug whitespace-pre text-white/90 focus:outline-none"
        ref={(el) => {
          // Start at the bottom, where the latest output is.
          if (el) el.scrollTop = el.scrollHeight;
        }}
      />
    </div>
  );
}
