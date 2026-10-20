import { Icon } from "../../components/icons";
import { isFinal } from "../FileChannel";
import type { Transfer } from "../FileChannel";

/** Shown in the stage while the Files panel is closed and transfers are running. */
export function TransferPill({
  transfers,
  onOpen,
}: {
  transfers: readonly Transfer[];
  onOpen: () => void;
}) {
  const active = transfers.filter((t) => !isFinal(t));
  if (active.length === 0) return null;
  const size = active.reduce((n, t) => n + t.size, 0);
  const done = active.reduce((n, t) => n + t.bytes, 0);
  const pct = size > 0 ? Math.round((done / size) * 100) : 0;
  const up = active.filter((t) => t.direction === "upload").length;
  const down = active.length - up;
  return (
    <button
      className="absolute right-4 bottom-4 z-20 flex items-center gap-2 rounded-full bg-black/70 px-4 py-2 text-sm text-white shadow hover:bg-black/80"
      onClick={onOpen}
      aria-label={`${active.length} file transfers, ${pct}% done. Open Files`}
    >
      {up > 0 && (
        <span className="flex items-center gap-1">
          <Icon name="upload" size={16} />
          {up}
        </span>
      )}
      {down > 0 && (
        <span className="flex items-center gap-1">
          <Icon name="download" size={16} />
          {down}
        </span>
      )}
      <span className="tabular-nums">{pct}%</span>
    </button>
  );
}
