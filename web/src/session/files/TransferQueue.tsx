// The file manager's queue: one row per batch with its combined progress,
// and what failed inside it.

import { useSyncExternalStore } from "react";
import { Icon } from "../../components/icons";
import { describeFileError } from "../../lib/files";
import { bytes, duration } from "../../lib/format";
import { batchView } from "../../lib/transferPlan";
import type { BatchView } from "../../lib/transferPlan";
import type { Batch, FileChannel, Transfer } from "../FileChannel";

const EMPTY_T: readonly Transfer[] = [];
const EMPTY_B: readonly Batch[] = [];
const noop = () => () => undefined;

/** The transfer and batch stores of a FileChannel (empty while there is none). */
export function useTransfers(fc: FileChannel | null): readonly Transfer[] {
  return useSyncExternalStore(fc?.subscribe ?? noop, fc?.getTransfers ?? (() => EMPTY_T));
}

export function useBatches(fc: FileChannel | null): readonly Batch[] {
  return useSyncExternalStore(fc?.subscribe ?? noop, fc?.getBatches ?? (() => EMPTY_B));
}

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;

function statusLine(v: BatchView): string {
  switch (v.status) {
    case "scanning":
      return "Looking through folders…";
    case "asking":
      return "Waiting for your answer…";
    case "cancelled":
      return "Cancelled";
  }
  if (v.files === 0 && v.skipped > 0 && v.failures.length === 0) return "Skipped";
  const parts: string[] = [];
  if (v.status === "running") {
    parts.push(
      `${v.filesDone} of ${plural(v.files, "file")}`,
      `${bytes(v.bytesDone)} of ${bytes(v.bytes)}`,
    );
    if (v.speed) parts.push(`${bytes(v.speed)}/s`);
    if (v.eta !== null && v.eta > 0) parts.push(`${duration(Math.ceil(v.eta))} left`);
  } else {
    parts.push(
      v.files === 1 && v.filesDone === 1
        ? bytes(v.bytes)
        : `${plural(v.filesDone, "file")}, ${bytes(v.bytesDone)}`,
    );
    if (v.failures.length > 0) parts.push(`${v.failures.length} failed`);
  }
  if (v.skipped > 0) parts.push(`${v.skipped} skipped`);
  return parts.join(" · ");
}

const DONE: readonly BatchView["status"][] = ["done", "partial", "failed", "cancelled"];

export function TransferQueue({ files }: { files: FileChannel }) {
  const batches = useBatches(files);
  const transfers = useTransfers(files);
  // Transfers without a batch come from older code paths; show them as their own rows.
  const loose = transfers.filter((t) => t.batch === null);
  if (batches.length === 0 && loose.length === 0) {
    return <p className="px-4 py-3 text-xs text-white/40">Transfers show up here.</p>;
  }
  const views = batches.map((b) => ({ b, v: batchView(b, transfers) }));
  const finished =
    views.some(({ v }) => DONE.includes(v.status)) || loose.some((t) => t.status === "done");
  return (
    <section aria-label="Transfers" className="flex min-h-0 flex-col">
      <div className="flex items-center px-4 pt-2 pb-1 text-xs text-white/60">
        Transfers
        {finished && (
          <button
            className="ml-auto text-[#8ab4f8] hover:underline"
            onClick={() => files.clearFinished()}
          >
            Clear finished
          </button>
        )}
      </div>
      <ul className="min-h-0 space-y-1.5 overflow-y-auto px-3 pb-3">
        {[...views].reverse().map(({ b, v }) => {
          const done = DONE.includes(v.status);
          const pct =
            v.bytes > 0 ? Math.min(1, v.bytesDone / v.bytes) : v.status === "done" ? 1 : 0;
          const bad = v.status === "failed" || v.status === "partial";
          return (
            <li key={b.id} className="rounded-lg bg-white/5 px-3 py-2">
              <div className="flex items-center gap-2 text-sm">
                <Icon
                  name={b.direction === "upload" ? "upload" : "download"}
                  size={16}
                  className="shrink-0 text-white/60"
                />
                <span className="min-w-0 truncate" title={`${b.name} → ${b.dest}`}>
                  {b.name}
                  <span className="text-white/40"> → {b.dest}</span>
                </span>
                <span className="ml-auto flex shrink-0 items-center">
                  {v.status === "done" && (
                    <Icon name="check" size={16} className="text-[#81c995]" />
                  )}
                  {bad && <Icon name="warning" size={16} className="text-[#f28b82]" />}
                  {!done && (
                    <button
                      className="rounded-full p-0.5 text-white/60 hover:bg-white/10 hover:text-white"
                      aria-label={`Cancel ${b.name}`}
                      title="Cancel"
                      onClick={() => files.cancelBatch(b.id)}
                    >
                      <Icon name="cancel" size={16} />
                    </button>
                  )}
                </span>
              </div>
              {!done && (
                <div className="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-white/10">
                  <div
                    className={`h-full bg-[#8ab4f8] transition-[width] ${v.status === "scanning" ? "animate-pulse" : ""}`}
                    style={{ width: `${v.status === "scanning" ? 100 : Math.round(pct * 100)}%` }}
                  />
                </div>
              )}
              <p className={`mt-1 truncate text-xs ${bad ? "text-[#f28b82]" : "text-white/50"}`}>
                {statusLine(v)}
                {v.status === "running" && v.current && (
                  <span className="text-white/40"> · {v.current}</span>
                )}
              </p>
              {v.failures.length > 0 && (
                <details className="mt-1 text-xs text-white/60">
                  <summary className="cursor-pointer text-[#f28b82]">
                    {v.failures.length === 1
                      ? "Show what failed"
                      : `Show the ${v.failures.length} that failed`}
                  </summary>
                  <ul className="mt-1 max-h-32 space-y-0.5 overflow-y-auto">
                    {v.failures.map((f, i) => (
                      <li key={i} className="break-all">
                        <span className="text-white/80">{f.name}</span> —{" "}
                        {describeFileError(f.code)}
                      </li>
                    ))}
                  </ul>
                </details>
              )}
              {transfers.some(
                (t) => t.batch === b.id && t.sink === "memory" && t.status === "active",
              ) && (
                <p className="mt-1 text-xs text-[#fdd663]">
                  This browser holds downloads in memory until they finish; very large files may
                  fail.
                </p>
              )}
            </li>
          );
        })}
        {[...loose].reverse().map((t) => (
          <li
            key={`t${t.xfer}`}
            className="flex items-center gap-2 rounded-lg bg-white/5 px-3 py-2 text-sm"
          >
            <Icon
              name={t.direction === "upload" ? "upload" : "download"}
              size={16}
              className="text-white/60"
            />
            <span className="min-w-0 flex-1 truncate">{t.name}</span>
            <span className="text-xs text-white/50">
              {t.status === "done"
                ? bytes(t.size)
                : t.error
                  ? describeFileError(t.error.code)
                  : `${bytes(t.bytes)} of ${bytes(t.size)}`}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
