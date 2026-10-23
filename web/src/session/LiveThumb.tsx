import { useEffect, useRef } from "react";
import { useSessionSnap } from "../app/workspace";
import { OsIcon } from "../components/icons";
import type { RemoteSession } from "./RemoteSession";

/**
 * A small live view of an open session: another <video> on the same
 * MediaStream (the browser decodes once, whatever the number of sinks).
 * Unfocused sessions stream at background quality, about 5 fps.
 */
export function LiveThumb({
  session,
  os,
  live = true,
  className = "",
}: {
  session: RemoteSession | null;
  os?: string;
  /** false shows the OS icon (the session in view needs no second picture). */
  live?: boolean;
  className?: string;
}) {
  const snap = useSessionSnap(session);
  const ref = useRef<HTMLVideoElement>(null);
  const stream = (live && snap?.stream) || null;

  useEffect(() => {
    const v = ref.current;
    if (!v) return;
    if (v.srcObject !== stream) v.srcObject = stream;
    if (stream) void v.play().catch(() => undefined);
    // A detached element would hold its player until garbage collection.
    return () => {
      v.srcObject = null;
    };
  }, [stream]);

  return (
    <span className={`relative block overflow-hidden bg-black ${className}`}>
      {stream ? (
        <video
          ref={ref}
          className="absolute inset-0 h-full w-full object-contain"
          muted
          playsInline
          autoPlay
          disablePictureInPicture
          aria-hidden="true"
        />
      ) : (
        <span className="absolute inset-0 flex items-center justify-center text-white/40">
          <OsIcon os={os} size={18} />
        </span>
      )}
    </span>
  );
}
