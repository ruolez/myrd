// Switch between terminals, take over one that is already running on the
// device, or start a new shell. A bottom sheet on phones, a popover on
// larger screens.

import { useEffect, useRef, useState } from "react";
import { Icon } from "../../components/icons";
import { Spinner } from "../../components/ui";
import { kindLabel } from "../../lib/terminal";
import type { TermBlocked, TermShell, TermSource } from "../../lib/terminal";
import type { Term } from "../TerminalChannel";

export type PickerTab = { term: Term; title: string; ended: boolean };

type Props = {
  sheet: boolean;
  tabs: PickerTab[];
  active: Term | null;
  /** null while loading. */
  sources: TermSource[] | null;
  /** Apps with terminals open that the device does not let the agent show yet. */
  blocked: TermBlocked[];
  shells: TermShell[];
  admin: boolean;
  error: string | null;
  busy: string | null;
  onSwitch: (t: Term) => void;
  onCloseTab: (t: Term) => void;
  onAttach: (s: TermSource) => void;
  /** Ends a shell the agent keeps (and whatever runs in it). */
  onEnd: (s: TermSource) => void;
  onNew: (shell?: string, admin?: boolean) => void;
  onRefresh: () => void;
  /** Shows the device's permission prompt again. */
  onAskAgain: () => void;
  onDismiss: () => void;
};

function ago(ms: number): string {
  if (!ms) return "";
  const s = Math.max(0, (Date.now() - ms) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

function Badge({ s }: { s: Pick<TermSource, "kind" | "fidelity" | "admin"> }) {
  return (
    <span className="flex shrink-0 items-center gap-1">
      <span className="rounded bg-white/10 px-1.5 py-0.5 text-[10px] font-medium tracking-wide text-white/70 uppercase">
        {kindLabel(s)}
      </span>
      {s.fidelity === "mirror" && (
        <span className="rounded bg-[#fdd663]/15 px-1.5 py-0.5 text-[10px] font-medium text-[#fdd663]">
          mirror
        </span>
      )}
      {s.admin && (
        <span className="rounded bg-[#f28b82]/15 px-1.5 py-0.5 text-[10px] font-medium text-[#f28b82]">
          admin
        </span>
      )}
    </span>
  );
}

/** Ends a kept shell; the first tap asks, the second (within 4 s) ends it. */
function EndButton({
  s,
  busy,
  onEnd,
}: {
  s: TermSource;
  busy: boolean;
  onEnd: (s: TermSource) => void;
}) {
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const t = setTimeout(() => setArmed(false), 4000);
    return () => clearTimeout(t);
  }, [armed]);
  return (
    <button
      className={`ml-1 flex h-11 shrink-0 items-center justify-center rounded-xl ${
        armed
          ? "bg-[#d93025] px-3 text-sm font-medium text-white"
          : "w-11 text-white/55 hover:bg-white/10"
      }`}
      disabled={busy}
      aria-label={
        armed ? `Confirm: end ${s.title} and what runs in it` : `End ${s.title} on the device`
      }
      title={armed ? "Tap again to end it" : "End this shell on the device"}
      onClick={() => (armed ? onEnd(s) : setArmed(true))}
    >
      {armed ? "End" : <Icon name="trash" size={18} />}
    </button>
  );
}

/** Why an app's tabs are missing, and how the person at the device lets them show. */
function BlockedNotice({ b, onAskAgain }: { b: TermBlocked; onAskAgain: () => void }) {
  const [asked, setAsked] = useState(false);
  if (b.reason === "asking")
    return (
      <div role="status" className="mx-2 mb-2 flex gap-2.5 rounded-lg bg-white/[0.06] px-3 py-2.5">
        <span className="mt-0.5 shrink-0">
          <Spinner size={16} label="Waiting" />
        </span>
        <p className="text-sm text-white/80">
          Waiting for permission to show {b.app} tabs. Someone at the Mac needs to click{" "}
          <strong className="font-medium text-white">Allow</strong> on “RemoteAgent wants access to
          control {b.app}”.
        </p>
      </div>
    );
  return (
    <div className="mx-2 mb-2 rounded-lg bg-[#fdd663]/10 px-3 py-2.5 ring-1 ring-[#fdd663]/25">
      <p className="text-sm text-white/85">
        {b.app} tabs are hidden: this Mac doesn’t allow RemoteAgent to control {b.app}.
      </p>
      <p className="mt-1 text-xs text-white/55">
        Ask again shows the prompt on the Mac’s screen. It can also be turned on there in System
        Settings › Privacy &amp; Security › Automation › RemoteAgent.
      </p>
      <button
        className="mt-2 min-h-10 rounded-lg bg-white/10 px-3 text-sm font-medium text-white hover:bg-white/15 disabled:opacity-50"
        disabled={asked}
        onClick={() => {
          setAsked(true);
          onAskAgain();
        }}
      >
        Ask again
      </button>
    </div>
  );
}

const row =
  "flex min-h-14 min-w-0 flex-1 items-center gap-3 rounded-xl px-3 py-2 text-left hover:bg-white/[0.06] focus-visible:bg-white/[0.08] focus-visible:outline-none";

export function TerminalPicker(p: Props) {
  const box = useRef<HTMLDivElement>(null);
  const openHere = new Set(p.tabs.map((t) => t.term.source.id));
  const elsewhere = (p.sources ?? []).filter((s) => !openHere.has(s.id));
  const def = p.shells[0];

  useEffect(() => {
    box.current
      ?.querySelector<HTMLButtonElement>("button[data-autofocus]")
      ?.focus({ preventScroll: true });
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && p.onDismiss();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const body = (
    <div ref={box} className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 px-4 pt-3 pb-2">
        <h2 className="flex-1 text-base font-medium">Terminals</h2>
        <button
          className="btn btn-sm text-white/80 hover:bg-white/10"
          aria-label="Refresh the list"
          title="Refresh"
          onClick={p.onRefresh}
        >
          <Icon name="refresh" size={18} />
        </button>
        <button
          className="btn btn-sm text-white/80 hover:bg-white/10"
          aria-label="Close"
          onClick={p.onDismiss}
        >
          <Icon name="close" size={18} />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-2 pb-[max(0.75rem,env(safe-area-inset-bottom))]">
        <div className="mb-2 grid grid-cols-1 gap-1.5 px-2 min-[360px]:grid-cols-2">
          <button
            data-autofocus
            className="flex min-h-12 items-center justify-center gap-2 rounded-xl bg-[#8ab4f8] px-3 font-medium text-[#131314] disabled:opacity-50"
            disabled={!!p.busy}
            onClick={() => p.onNew()}
          >
            <Icon name="add" size={18} />
            New {def ? def.name : "shell"}
          </button>
          {p.admin && (
            <button
              className="flex min-h-12 items-center justify-center gap-2 rounded-xl bg-[#f28b82]/15 px-3 font-medium text-[#f6aea9] ring-1 ring-[#f28b82]/40 disabled:opacity-50"
              disabled={!!p.busy}
              onClick={() => p.onNew(def?.id, true)}
            >
              <Icon name="admin" size={18} />
              As administrator
            </button>
          )}
        </div>
        {p.shells.length > 1 && (
          <div className="mb-2 flex flex-wrap gap-1.5 px-2">
            {p.shells.slice(1).map((s) => (
              <button
                key={s.id}
                className="rounded-full bg-white/10 px-3 py-1.5 text-xs text-white/85 disabled:opacity-50"
                disabled={!!p.busy}
                title={s.path}
                onClick={() => p.onNew(s.id)}
              >
                New {s.name}
              </button>
            ))}
          </div>
        )}
        {p.busy && (
          <div className="flex items-center gap-2 px-4 py-2 text-sm text-white/70">
            <Spinner size={16} /> {p.busy}
          </div>
        )}
        {p.error && (
          <p
            role="alert"
            className="mx-2 mb-2 rounded-lg bg-[#f28b82]/15 px-3 py-2 text-sm text-[#f6aea9]"
          >
            {p.error}
          </p>
        )}

        {p.tabs.length > 0 && (
          <section aria-label="Open in this session" className="mb-1">
            <h3 className="px-4 pt-2 pb-1 text-xs font-medium tracking-wide text-white/50 uppercase">
              Open here
            </h3>
            {p.tabs.map((t) => (
              <div key={t.term.id} className="flex items-center">
                <button
                  className={row}
                  aria-current={t.term === p.active}
                  onClick={() => p.onSwitch(t.term)}
                >
                  <Icon
                    name="terminal"
                    size={20}
                    className={
                      t.ended
                        ? "text-white/35"
                        : t.term === p.active
                          ? "text-[#8ab4f8]"
                          : "text-[#81c995]"
                    }
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm">{t.title}</span>
                    <span className="block truncate text-xs text-white/50">
                      {t.ended ? "Ended" : t.term === p.active ? "In view" : "Running"}
                    </span>
                  </span>
                  <Badge s={t.term.source} />
                </button>
                <button
                  className="ml-1 flex h-11 w-11 shrink-0 items-center justify-center rounded-xl text-white/60 hover:bg-white/10"
                  aria-label={`Close ${t.title} here (it keeps running on the device)`}
                  title="Close here"
                  onClick={() => p.onCloseTab(t.term)}
                >
                  <Icon name="close" size={18} />
                </button>
              </div>
            ))}
          </section>
        )}

        <section aria-label="On the device">
          <h3 className="px-4 pt-2 pb-1 text-xs font-medium tracking-wide text-white/50 uppercase">
            On the device
          </h3>
          {p.sources !== null &&
            p.blocked.map((b) => (
              <BlockedNotice key={`${b.app}:${b.reason}`} b={b} onAskAgain={p.onAskAgain} />
            ))}
          {p.sources === null ? (
            <div className="flex items-center gap-2 px-4 py-3 text-sm text-white/60">
              <Spinner size={16} /> Looking for terminals…
            </div>
          ) : elsewhere.length === 0 ? (
            p.blocked.length === 0 && (
              <p className="px-4 py-3 text-sm text-white/55">
                No other terminals are running there. Shells you open stay running after you
                disconnect, and tmux or screen sessions show up here.
              </p>
            )
          ) : (
            elsewhere.map((s) => (
              <div key={s.id} className="flex items-center">
                <button className={row} disabled={!!p.busy} onClick={() => p.onAttach(s)}>
                  <Icon name="terminal" size={20} className="text-white/60" />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm">{s.title}</span>
                    <span className="block truncate text-xs text-white/50">
                      {[s.detail, s.attached > 0 ? `${s.attached} watching` : "", ago(s.created)]
                        .filter(Boolean)
                        .join(" · ")}
                    </span>
                  </span>
                  <Badge s={s} />
                </button>
                {s.kind === "shell" && <EndButton s={s} busy={!!p.busy} onEnd={p.onEnd} />}
              </div>
            ))
          )}
        </section>
      </div>
    </div>
  );

  if (p.sheet)
    return (
      <div
        className="absolute inset-0 z-40 flex flex-col justify-end bg-black/50"
        onClick={p.onDismiss}
      >
        <div
          role="dialog"
          aria-label="Terminals"
          className="flex max-h-[85%] flex-col rounded-t-2xl bg-[#232425] text-white shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <div className="mx-auto mt-2 h-1 w-9 shrink-0 rounded-full bg-white/25" aria-hidden />
          {body}
        </div>
      </div>
    );
  return (
    <>
      <div className="absolute inset-0 z-30" onClick={p.onDismiss} />
      <div
        role="dialog"
        aria-label="Terminals"
        className="absolute top-11 left-2 z-40 flex max-h-[min(36rem,calc(100%-3.5rem))] w-[min(26rem,calc(100%-1rem))] flex-col rounded-2xl bg-[#232425] text-white shadow-2xl ring-1 ring-white/10"
      >
        {body}
      </div>
    </>
  );
}
