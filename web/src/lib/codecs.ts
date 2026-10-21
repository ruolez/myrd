// Video codec preferences for the agent's track (docs/PROTOCOL.md §5).

type Codec = { mimeType: string; sdpFmtpLine?: string };

const fmtp = (c: Codec) => c.sdpFmtpLine ?? "";

// The agent encodes whatever H.264 profile is negotiated; high (CABAC, 8x8
// transform; no B-frames, so constrained) costs ~10-15% fewer bits than baseline.
function h264Score(c: Codec): number {
  if (c.mimeType.toLowerCase() !== "video/h264") return -1;
  const f = fmtp(c);
  let s = 1;
  if (/packetization-mode=1/.test(f)) s += 8;
  if (/profile-level-id=64/i.test(f)) s += 4;
  else if (/profile-level-id=4d/i.test(f)) s += 3;
  else if (/profile-level-id=42e0/i.test(f)) s += 2;
  return s;
}

/** VP9 profile 1 (4:4:4): the agent's full-color mode. */
const isFullColor = (c: Codec) =>
  c.mimeType.toLowerCase() === "video/vp9" && /(^|;)\s*profile-id=1(;|$)/.test(fmtp(c));

/**
 * H.264 best first, then full-color VP9 and the repair formats (RTX, RED,
 * ULPFEC, FlexFEC); null when the browser has no H.264 (keep its defaults).
 */
export function preferredCodecs<C extends Codec>(codecs: readonly C[]): C[] | null {
  const h264 = codecs.filter((c) => h264Score(c) > 0).sort((a, b) => h264Score(b) - h264Score(a));
  if (h264.length === 0) return null;
  const support = codecs.filter((c) => /rtx|red|ulpfec|flexfec/i.test(c.mimeType));
  return [...h264, ...codecs.filter(isFullColor), ...support];
}

/** A codec stats entry as the HUD shows it. */
export function codecName(mimeType: string | undefined, sdpFmtpLine?: string): string {
  const c = { mimeType: mimeType ?? "", sdpFmtpLine };
  if (isFullColor(c)) return "VP9 4:4:4";
  if (c.mimeType.toLowerCase() === "video/h264") return "H.264";
  return c.mimeType.replace(/^video\//i, "") || "—";
}
