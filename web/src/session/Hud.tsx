import { useEffect, useState } from "react";
import type { RefObject } from "react";
import { describeAuto } from "../lib/stream";
import type { StreamState } from "../lib/stream";
import type { HudStats } from "./RemoteSession";

type FrameTiming = { renderMs: number | null; processingMs: number | null };

type VideoFrameMeta = {
  presentationTime: number;
  expectedDisplayTime: number;
  receiveTime?: number;
  processingDuration?: number;
};

type RVFCVideo = HTMLVideoElement & {
  requestVideoFrameCallback?: (cb: (now: number, meta: VideoFrameMeta) => void) => number;
  cancelVideoFrameCallback?: (id: number) => void;
};

/** Per-frame timing from requestVideoFrameCallback, averaged over ~30 frames. */
function useFrameTiming(video: RefObject<HTMLVideoElement | null>, active: boolean): FrameTiming {
  const [t, setT] = useState<FrameTiming>({ renderMs: null, processingMs: null });
  useEffect(() => {
    const v = video.current as RVFCVideo | null;
    if (!active || !v?.requestVideoFrameCallback) return;
    let id = 0;
    let n = 0;
    let render = 0;
    let renderN = 0;
    let proc = 0;
    let procN = 0;
    const cb = (_now: number, meta: VideoFrameMeta) => {
      if (meta.receiveTime !== undefined) {
        const d = meta.expectedDisplayTime - meta.receiveTime;
        // Headless or throttled tabs report display times with no real vsync.
        if (d >= 0 && d < 1000) {
          render += d;
          renderN++;
        }
      }
      if (meta.processingDuration !== undefined) {
        proc += meta.processingDuration * 1000;
        procN++;
      }
      if (++n >= 30) {
        setT({
          renderMs: renderN ? render / renderN : null,
          processingMs: procN ? proc / procN : null,
        });
        n = render = renderN = proc = procN = 0;
      }
      id = v.requestVideoFrameCallback?.(cb) ?? 0;
    };
    id = v.requestVideoFrameCallback(cb);
    return () => v.cancelVideoFrameCallback?.(id);
  }, [video, active]);
  return t;
}

const fmt = (v: number | null, unit: string, digits = 0) =>
  v === null || Number.isNaN(v) ? "—" : `${v.toFixed(digits)} ${unit}`;

const int = (v: number | null | undefined) =>
  v === null || v === undefined || Number.isNaN(v) ? "—" : String(Math.round(v));

const mbps = (kbps: number | null | undefined) =>
  kbps === null || kbps === undefined ? "—" : (kbps / 1000).toFixed(kbps < 10000 ? 2 : 1);

function bitrateText(stats: HudStats) {
  if (stats.bitrateKbps === null) return "—";
  const extra = [
    stats.targetKbps !== null ? `target ${mbps(stats.targetKbps)}` : "",
    stats.bweKbps !== null ? `BWE ${mbps(stats.bweKbps)}` : "",
  ].filter(Boolean);
  return `${mbps(stats.bitrateKbps)} Mbps${extra.length ? ` (${extra.join(", ")})` : ""}`;
}

function encodeText(stats: HudStats, stream: StreamState | null) {
  const w = stream?.width ?? stats.encodeWidth;
  const h = stream?.height ?? stats.encodeHeight;
  if (!w || !h) return "—";
  const native = stream ? ` of ${stream.native_width}×${stream.native_height}` : "";
  return `${w}×${h}${native}`;
}

export function Hud({
  stats,
  encoder,
  video,
  stream,
}: {
  stats: HudStats;
  encoder: string | null;
  video: RefObject<HTMLVideoElement | null>;
  stream: StreamState | null;
}) {
  const frame = useFrameTiming(video, true);
  const rows: [string, string][] = [
    ["RTT", fmt(stats.rttMs, "ms")],
    ["Receive→display", fmt(frame.renderMs, "ms", 1)],
    ["Decode", fmt(frame.processingMs ?? stats.decodeMs, "ms", 1)],
    [
      "FPS recv/sent/target",
      `${int(stats.fps)} / ${int(stats.agentFps)} / ${int(stats.targetFps ?? stream?.target_fps)}`,
    ],
    ["Bitrate", bitrateText(stats)],
    ["Loss", fmt(stats.packetLossPct, "%", 2)],
    ["Jitter", fmt(stats.jitterMs, "ms")],
    ["Decoded", stats.frameWidth ? `${stats.frameWidth}×${stats.frameHeight}` : "—"],
    ["Encoded", encodeText(stats, stream)],
    ...(stream?.auto ? [["Auto", describeAuto(stream) ?? "—"] as [string, string]] : []),
    ["Path", stats.path ?? "—"],
    ["Encoder", encoder ?? "—"],
  ];
  return (
    <div
      className="pointer-events-none absolute top-3 right-3 z-20 rounded-lg bg-black/70 px-3 py-2 font-mono text-[11px] leading-5 text-white shadow-lg"
      aria-label="Latency HUD"
    >
      <table>
        <tbody>
          {rows.map(([k, v]) => (
            <tr key={k}>
              <td className="pr-3 text-white/60">{k}</td>
              <td className={k === "Path" && v === "relay" ? "text-[#c58af9]" : ""}>{v}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {stats.remoteCandidate && (
        <div className="mt-1 max-w-56 truncate text-white/50">{stats.remoteCandidate}</div>
      )}
    </div>
  );
}
