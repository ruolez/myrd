// Local echo: what you type shows at once, before the device echoes it back
// (a slow link or a mirrored window can take a few hundred ms). A line is
// predicted only once the device has echoed typing on it, so a password
// prompt (no echo) never shows what is typed; anything that may move the
// cursor or redraw the line (Enter, arrows, Tab, Ctrl keys, a paste) stops
// predicting until the next echo; output that doesn't match drops the
// prediction and the real screen wins.

/** A character shown before its echo, and when it was typed. */
export type Guess = { ch: string; at: number };

export type Predict = {
  /** The device echoed typing on this line: typing shows at once. */
  trusted: boolean;
  /** Shown, not yet echoed; oldest first. */
  pending: Guess[];
  /** Typed while not trusted, and when: its echo makes the line trusted. */
  probe: Guess | null;
};

export const NO_PREDICT: Predict = { trusted: false, pending: [], probe: null };

/** A guess not echoed in this long is dropped (the line stops being trusted). */
export const ECHO_TIMEOUT_MS = 1000;
/** Output this much longer than the typing it would echo is not an echo. */
const ECHO_SLACK = 16;

const isPrintable = (c: string) => c >= " " && c <= "~";

/** Typing went out: what shows now. `alt`: a full-screen program runs (no prediction). */
export function typed(s: Predict, text: string, now: number, alt: boolean): Predict {
  if (alt) return NO_PREDICT;
  let next = s;
  for (const c of text) {
    if (isPrintable(c)) {
      next = next.trusted
        ? { ...next, pending: [...next.pending, { ch: c, at: now }] }
        : { ...next, probe: next.probe ?? { ch: c, at: now } };
    } else if (c === "\x7f" || c === "\b") {
      if (next.pending.length) next = { ...next, pending: next.pending.slice(0, -1) };
      else if (next.trusted) next = { ...next, trusted: false }; // deleting real text: wait for the echo
    } else {
      return NO_PREDICT; // Enter, Tab, Escape (arrows, paste), Ctrl keys
    }
  }
  return next;
}

/** Strips escape sequences and control characters, keeping what prints. */
export function printed(text: string): string {
  /* eslint-disable no-control-regex -- matching terminal control sequences */
  return text
    .replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, "") // OSC
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "") // CSI
    .replace(/\x1b[@-_]/g, "") // other escapes
    .replace(/[\x00-\x1f\x7f]/g, "");
  /* eslint-enable no-control-regex */
}

/** Output arrived: confirm what it echoed, drop what it contradicts. */
export function output(s: Predict, text: string, now: number): Predict {
  const out = printed(text);
  if (!out) return s;
  if (!s.trusted) {
    const p = s.probe;
    const echo =
      p !== null && now - p.at <= ECHO_TIMEOUT_MS && out.length <= ECHO_SLACK && out.includes(p.ch);
    return echo ? { trusted: true, pending: [], probe: null } : s;
  }
  let n = 0;
  while (n < s.pending.length && n < out.length && out[n] === s.pending[n]!.ch) n++;
  if (n === 0 || (n < s.pending.length && n < out.length)) return { ...s, pending: [] };
  return { ...s, pending: s.pending.slice(n) };
}

/** Drops guesses the device never echoed (the line is not trusted any more). */
export function expire(s: Predict, now: number): Predict {
  const oldest = s.pending[0];
  if (oldest && now - oldest.at > ECHO_TIMEOUT_MS) return NO_PREDICT;
  if (s.probe && now - s.probe.at > ECHO_TIMEOUT_MS) return { ...s, probe: null };
  return s;
}

/** The text to draw at the cursor. */
export const shown = (s: Predict) => s.pending.map((g) => g.ch).join("");
