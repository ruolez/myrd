import { useEffect, useState } from "react";
import type { RefObject } from "react";
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

export function Hud({
  stats,
  encoder,
  video,
}: {
  stats: HudStats;
  encoder: string | null;
  video: RefObject<HTMLVideoElement | null>;
}) {
  const frame = useFrameTiming(video, true);
  const rows: [string, string][] = [
    ["RTT", fmt(stats.rttMs, "ms")],
    ["Receive→display", fmt(frame.renderMs, "ms", 1)],
    ["Decode", fmt(frame.processingMs ?? stats.decodeMs, "ms", 1)],
    [
      "FPS",
      stats.fps === null
        ? "—"
        : `${Math.round(stats.fps)}${stats.agentFps ? ` / ${Math.round(stats.agentFps)}` : ""}`,
    ],
    ["Bitrate", stats.bitrateKbps === null ? "—" : `${(stats.bitrateKbps / 1000).toFixed(2)} Mbps`],
    ["Loss", fmt(stats.packetLossPct, "%", 2)],
    ["Jitter", fmt(stats.jitterMs, "ms")],
    ["Resolution", stats.frameWidth ? `${stats.frameWidth}×${stats.frameHeight}` : "—"],
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
