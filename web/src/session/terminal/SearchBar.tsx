// Find in the terminal's scrollback: Enter for the next match, Shift+Enter
// for the previous one, Escape to close.

import { useEffect, useRef, useState } from "react";
import { Icon } from "../../components/icons";
import type { FindResults } from "./TerminalView";

export function SearchBar({
  results,
  onFind,
  onClose,
}: {
  results: FindResults | null;
  /** Finds query; back: the previous match. */
  onFind: (query: string, back: boolean) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    ref.current?.focus();
  }, []);

  const count =
    !query || !results
      ? ""
      : results.count === 0
        ? "No matches"
        : `${results.index + 1} of ${results.count}`;
  const btn =
    "flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-white/80 hover:bg-white/10 disabled:opacity-40";
  return (
    <div
      role="search"
      className="absolute top-2 right-2 left-2 z-30 ml-auto flex max-w-md items-center gap-1 rounded-xl bg-[#2d2e30] p-1 pl-2 shadow-lg ring-1 ring-white/10"
    >
      <Icon name="search" size={18} className="shrink-0 text-white/50" />
      <input
        ref={ref}
        value={query}
        aria-label="Find in the terminal"
        placeholder="Find"
        className="h-9 min-w-0 flex-1 bg-transparent px-1 text-base text-white placeholder:text-white/40 focus:outline-none sm:text-sm"
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        enterKeyHint="search"
        onChange={(e) => {
          setQuery(e.target.value);
          onFind(e.target.value, false);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            onFind(query, e.shiftKey);
          } else if (e.key === "Escape") {
            e.preventDefault();
            onClose();
          }
        }}
      />
      <span className="shrink-0 px-1 text-xs text-white/55 tabular-nums" aria-live="polite">
        {count}
      </span>
      <button
        type="button"
        className={btn}
        aria-label="Previous match"
        disabled={!query}
        onClick={() => onFind(query, true)}
      >
        <Icon name="chevronUp" size={18} />
      </button>
      <button
        type="button"
        className={btn}
        aria-label="Next match"
        disabled={!query}
        onClick={() => onFind(query, false)}
      >
        <Icon name="chevronDown" size={18} />
      </button>
      <button type="button" className={btn} aria-label="Close find" onClick={onClose}>
        <Icon name="close" size={18} />
      </button>
    </div>
  );
}
