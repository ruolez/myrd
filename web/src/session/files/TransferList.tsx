import { useSyncExternalStore } from "react";
import { Icon } from "../../components/icons";
import { describeFileError } from "../../lib/files";
import type { TransferId } from "../../lib/files";
import { bytes, duration } from "../../lib/format";
import { isFinal } from "../FileChannel";
import type { FileChannel, Transfer } from "../FileChannel";

const EMPTY: readonly Transfer[] = [];
const noop = () => () => undefined;

/** The transfer store of a FileChannel (empty while there is none). */
export function useTransfers(fc: FileChannel | null): readonly Transfer[] {
  return useSyncExternalStore(fc?.subscribe ?? noop, fc?.getTransfers ?? (() => EMPTY));
}

const STATUS: Record<Transfer["status"], string> = {
  queued: "Queued",
  waiting: "Starting…",
  active: "",
  verifying: "Verifying…",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
};

function detail(t: Transfer) {
  if (t.status === "failed" || t.status === "cancelled") {
    return t.error && t.error.code !== "cancelled" ? describeFileError(t.error.code) : "Cancelled";
  }
  if (t.status === "done") {
    return `${bytes(t.size)}${t.direction === "upload" && t.path ? ` → ${t.path}` : ""}`;
  }
  const parts = [`${bytes(t.bytes)} of ${bytes(t.size)}`];
  if (t.status === "active" && t.speed) parts.push(`${bytes(t.speed)}/s`);
  if (t.status === "active" && t.eta !== null && t.eta > 0)
    parts.push(`${duration(Math.ceil(t.eta))} left`);
  if (STATUS[t.status]) parts.push(STATUS[t.status]);
  return parts.join(" · ");
}

export function TransferList({
  transfers,
  onCancel,
  onClear,
}: {
  transfers: readonly Transfer[];
  onCancel: (xfer: TransferId) => void;
  onClear: () => void;
}) {
  if (transfers.length === 0) return null;
  const finished = transfers.some(isFinal);
  return (
    <section aria-label="Transfers" className="space-y-2">
      <div className="flex items-center text-xs text-white/60">
        Transfers
        {finished && (
          <button className="ml-auto text-[#8ab4f8] hover:underline" onClick={onClear}>
            Clear finished
          </button>
        )}
      </div>
      <ul className="space-y-2">
        {[...transfers].reverse().map((t) => {
          const pct = t.size > 0 ? Math.min(1, t.bytes / t.size) : t.status === "done" ? 1 : 0;
          const failed = t.status === "failed";
          return (
            <li key={t.xfer} className="rounded-lg bg-white/5 px-3 py-2">
              <div className="flex items-center gap-2">
                <Icon
                  name={t.direction === "upload" ? "upload" : "download"}
                  size={16}
                  className="shrink-0 text-white/60"
                />
                <span className="min-w-0 flex-1 truncate text-sm" title={t.path ?? t.name}>
                  {t.name}
                </span>
                {t.status === "done" && <Icon name="check" size={16} className="text-[#81c995]" />}
                {failed && <Icon name="warning" size={16} className="text-[#f28b82]" />}
                {!isFinal(t) && (
                  <button
                    className="rounded-full p-0.5 text-white/60 hover:bg-white/10 hover:text-white"
                    aria-label={`Cancel ${t.name}`}
                    title="Cancel"
                    onClick={() => onCancel(t.xfer)}
                  >
                    <Icon name="cancel" size={16} />
                  </button>
                )}
              </div>
              {!isFinal(t) && (
                <div className="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-white/10">
                  <div
                    className="h-full bg-[#8ab4f8] transition-[width]"
                    style={{ width: `${Math.round(pct * 100)}%` }}
                  />
                </div>
              )}
              <p className={`mt-1 text-xs ${failed ? "text-[#f28b82]" : "text-white/50"}`}>
                {detail(t)}
              </p>
              {t.sink === "memory" && !isFinal(t) && (
                <p className="mt-1 text-xs text-[#fdd663]">
                  This browser holds the download in memory until it finishes; very large files may
                  fail.
                </p>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
