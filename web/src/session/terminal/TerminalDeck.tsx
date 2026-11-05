// The terminals of one session: tabs (or, on phones, one terminal at a time
// with a switcher), the picker to take over or start terminals, and the
// phone key bar, keyboard and command line. After a reconnect it reattaches
// the terminals this tab had open.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "../../components/icons";
import { Spinner } from "../../components/ui";
import type { Hotkey } from "../../lib/shortcuts";
import { defaultFontSize, MAX_FONT, MIN_FONT } from "../../lib/termGestures";
import { describeTermError, TermError } from "../../lib/terminal";
import type {
  TerminalAvailability,
  TermId,
  TermShell,
  TermSize,
  TermSource,
  TermSourceId,
} from "../../lib/terminal";
import { fontKey, openKey, readList, writeList } from "../../lib/termHistory";
import { afterKey, isOn, keySequence, pasteBytes, tapMod, withMods } from "../../lib/termKeys";
import type { ModState, Mods, NamedKey } from "../../lib/termKeys";
import type { Term, TerminalChannel } from "../TerminalChannel";
import { AccessoryBar } from "./AccessoryBar";
import { ComposeBar } from "./ComposeBar";
import { SelectSheet } from "./SelectSheet";
import { TerminalPicker } from "./TerminalPicker";
import type { PickerTab } from "./TerminalPicker";
import { TermKeyboard } from "./TermKeyboard";
import { TerminalView } from "./TerminalView";
import type { TerminalViewHandle } from "./TerminalView";

/** `lost`: it ended only because the connection did; it keeps running on the device. */
type Tab = { term: Term; title: string; ended: boolean; lost: boolean };

export type DeckHandle = {
  newShell: () => void;
  openPicker: () => void;
  paste: () => Promise<void>;
  selectText: () => void;
  zoom: (step: 1 | -1) => void;
};

type Props = {
  deviceId: string;
  channel: TerminalChannel | null;
  state: TerminalAvailability;
  /** In view (the pane is focused and, inside a desktop session, the panel is open). */
  visible: boolean;
  touch: boolean;
  /** Open or reattach a terminal as soon as the channel is ready. */
  autoStart: boolean;
  /** The session is connected; otherwise its own overlay explains, and the deck shows no placeholder. */
  connected?: boolean;
  onHotkey?: (h: Hotkey) => void;
  /** Extra controls at the end of the deck's header. */
  headerEnd?: ReactNode;
  deckRef?: (h: DeckHandle | null) => void;
};

const readFont = (deviceId: string) => {
  const n = Number(readList(globalThis.localStorage, fontKey(deviceId))[0]);
  return Number.isFinite(n) && n >= MIN_FONT && n <= MAX_FONT
    ? n
    : defaultFontSize(window.innerWidth);
};

export function TerminalDeck({
  deviceId,
  channel,
  state,
  visible,
  touch,
  autoStart,
  connected = true,
  onHotkey,
  headerEnd,
  deckRef,
}: Props) {
  const [tabs, setTabs] = useState<Tab[]>([]);
  const [activeId, setActiveId] = useState<TermId | null>(null);
  const [picker, setPicker] = useState(false);
  const [sources, setSources] = useState<TermSource[] | null>(null);
  const [shells, setShells] = useState<TermShell[]>([]);
  const [admin, setAdmin] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [fontSize, setFontSize] = useState(() => readFont(deviceId));
  const [keyboardOpen, setKeyboardOpen] = useState(false);
  const [compose, setCompose] = useState(false);
  const [ctrl, setCtrl] = useState<ModState>("off");
  const [alt, setAlt] = useState<ModState>("off");
  const [selectText, setSelectText] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const modTaps = useRef({ ctrl: 0, alt: 0 });
  const mods = useRef({ ctrl, alt });
  mods.current = { ctrl, alt };
  const kbRef = useRef<HTMLTextAreaElement>(null);
  const areaRef = useRef<HTMLDivElement>(null);
  const handles = useRef(new Map<TermId, TerminalViewHandle>());
  const lastSize = useRef<TermSize | null>(null);
  const started = useRef<TerminalChannel | null>(null);
  // The channel whose reattaching is done: only then is "what is open here" saved.
  const restored = useRef<TerminalChannel | null>(null);
  const [restoredTick, setRestoredTick] = useState(0);

  const active = tabs.find((t) => t.term.id === activeId) ?? null;
  const live = state === "ready" && !!channel;

  // ---------- tabs ----------
  const addTab = useCallback((term: Term) => {
    setTabs((ts) =>
      ts.some((t) => t.term === term)
        ? ts
        : [...ts, { term, title: term.title, ended: false, lost: false }],
    );
    setActiveId(term.id);
  }, []);

  useEffect(() => {
    const offs = tabs.flatMap((t) => [
      t.term.onTitle((title) =>
        setTabs((ts) => ts.map((x) => (x.term === t.term ? { ...x, title } : x))),
      ),
      // onEnd also fires at once for a terminal that already ended: change
      // nothing then, or this effect would run forever.
      t.term.onEnd((e) =>
        setTabs((ts) =>
          ts.some((x) => x.term === t.term && !x.ended)
            ? ts.map((x) =>
                x.term === t.term ? { ...x, ended: true, lost: e.reason === "session_ended" } : x,
              )
            : ts,
        ),
      ),
    ]);
    return () => offs.forEach((o) => o());
  }, [tabs]);

  // The terminals this tab watches, reattached after a reconnect.
  useEffect(() => {
    if (!channel || restored.current !== channel) return;
    const ids = tabs.filter((t) => !t.ended || t.lost).map((t) => t.term.source.id);
    writeList(globalThis.sessionStorage, openKey(deviceId), ids);
  }, [tabs, channel, deviceId, restoredTick]);

  const size = useCallback((): TermSize => {
    const fitted = activeId !== null ? handles.current.get(activeId)?.fit() : null;
    if (fitted) return fitted;
    if (lastSize.current) return lastSize.current;
    const r = areaRef.current?.getBoundingClientRect();
    const w = r?.width || window.innerWidth;
    const h = r?.height || window.innerHeight - 120;
    return {
      cols: Math.max(20, Math.floor((w - 8) / (fontSize * 0.6))),
      rows: Math.max(5, Math.floor((h - 4) / (fontSize * 1.15 * 1.2))),
    };
  }, [activeId, fontSize]);

  const fail = (e: unknown) => {
    const code = e instanceof TermError ? e.code : "error";
    setError(e instanceof TermError ? describeTermError(code) : String(e));
  };

  const refresh = useCallback(async () => {
    if (!channel) return;
    setSources(null);
    try {
      const r = await channel.list();
      setSources(r.sources);
      setShells(r.shells);
      setAdmin(r.admin);
    } catch (e) {
      setSources([]);
      fail(e);
    }
  }, [channel]);

  const newShell = useCallback(
    async (shell?: string, asAdmin?: boolean) => {
      if (!channel) return;
      setError(null);
      setBusy(asAdmin ? "Starting an administrator shell…" : "Starting a shell…");
      try {
        addTab(await channel.openShell(size(), shell, asAdmin));
        setPicker(false);
      } catch (e) {
        fail(e);
        setPicker(true);
      } finally {
        setBusy(null);
      }
    },
    [channel, addTab, size],
  );

  const attach = useCallback(
    async (s: TermSource) => {
      if (!channel) return;
      setError(null);
      setBusy(`Opening ${s.title}…`);
      try {
        addTab(await channel.attach(s.id, size()));
        setPicker(false);
      } catch (e) {
        fail(e);
        void refresh();
      } finally {
        setBusy(null);
      }
    },
    [channel, addTab, size, refresh],
  );

  /** Ends a kept shell from the list: attach to it, then end its program. */
  const endSource = useCallback(
    async (s: TermSource) => {
      if (!channel) return;
      setError(null);
      setBusy(`Ending ${s.title}…`);
      try {
        const t = await channel.attach(s.id, size());
        await new Promise<void>((resolve) => {
          const off = t.onEnd(() => {
            off();
            resolve();
          });
          t.kill();
          setTimeout(resolve, 5000);
        });
      } catch (e) {
        fail(e);
      } finally {
        setBusy(null);
        void refresh();
      }
    },
    [channel, size, refresh],
  );

  const closeTab = (term: Term) => {
    term.detach();
    setTabs((ts) => {
      const rest = ts.filter((t) => t.term !== term);
      if (activeId === term.id) setActiveId(rest.at(-1)?.term.id ?? null);
      return rest;
    });
  };

  // First ready channel: reattach what this tab had open, else take over or start one.
  useEffect(() => {
    if (!channel || state !== "ready" || started.current === channel) return;
    started.current = channel;
    setTabs([]);
    setActiveId(null);
    void (async () => {
      const remembered = readList(globalThis.sessionStorage, openKey(deviceId));
      let opened = 0;
      for (const id of remembered) {
        try {
          addTab(await channel.attach(id as TermSourceId, size()));
          opened++;
        } catch {
          // gone since
        }
      }
      restored.current = channel;
      setRestoredTick((n) => n + 1);
      if (opened || !autoStart) return;
      try {
        const r = await channel.list();
        setSources(r.sources);
        setShells(r.shells);
        setAdmin(r.admin);
        if (r.sources.length) setPicker(true);
        else await newShell();
      } catch (e) {
        fail(e);
      }
    })();
  }, [channel, state, deviceId, autoStart, addTab, size, newShell]);

  useEffect(() => {
    if (picker) void refresh();
  }, [picker, refresh]);

  // ---------- typing ----------
  const type = useCallback(
    (s: string) => {
      if (!s) return;
      const t = tabs.find((x) => x.term.id === activeId);
      if (!t || t.ended) return;
      if (!t.term.write(s)) setNotice("Not connected: that input was not sent.");
      handles.current.get(t.term.id)?.scrollToBottom();
    },
    [tabs, activeId],
  );
  const spendMods = useCallback(() => {
    setCtrl((c) => afterKey(c));
    setAlt((a) => afterKey(a));
  }, []);
  const currentMods = useCallback(
    (): Mods => ({ ctrl: isOn(mods.current.ctrl), alt: isOn(mods.current.alt) }),
    [],
  );
  const appCursor = useCallback(
    () => (activeId !== null && handles.current.get(activeId)?.modes().appCursor) || false,
    [activeId],
  );
  const onKey = (k: NamedKey) => {
    type(keySequence(k, currentMods(), appCursor()));
    spendMods();
  };
  const onText = (s: string) => {
    type(withMods(s, currentMods()));
    spendMods();
  };
  const onMod = (which: "ctrl" | "alt") => {
    const now = performance.now();
    const since = now - modTaps.current[which];
    modTaps.current[which] = now;
    (which === "ctrl" ? setCtrl : setAlt)((s) => tapMod(s, since));
  };
  const onKbBytes = useCallback(
    (s: string, used: boolean) => {
      type(s);
      if (used) spendMods();
    },
    [type, spendMods],
  );
  const pasteText = useCallback(
    (text: string) => {
      if (activeId === null) return;
      const h = handles.current.get(activeId);
      if (h) h.paste(text);
      else type(pasteBytes(text, false));
    },
    [activeId, type],
  );
  const toggleKeyboard = () => {
    const kb = kbRef.current;
    if (compose) {
      setCompose(false);
      return;
    }
    if (!kb) return;
    if (document.activeElement === kb) kb.blur();
    else kb.focus({ preventScroll: true });
  };
  const onTap = () => {
    if (!touch || compose) return;
    kbRef.current?.focus({ preventScroll: true });
  };
  const sendLine = (line: string) => {
    const bracketed = activeId !== null && handles.current.get(activeId)?.modes().bracketed;
    type((line.includes("\n") ? pasteBytes(line, !!bracketed) : line) + "\r");
  };

  const zoom = useCallback(
    (step: 1 | -1) =>
      setFontSize((f) => {
        const n = Math.min(MAX_FONT, Math.max(MIN_FONT, f + step));
        writeList(globalThis.localStorage, fontKey(deviceId), [String(n)]);
        return n;
      }),
    [deviceId],
  );
  const pinch = useCallback(
    (n: number) => {
      setFontSize(n);
      writeList(globalThis.localStorage, fontKey(deviceId), [String(n)]);
    },
    [deviceId],
  );

  useEffect(() => {
    if (!notice) return;
    const t = setTimeout(() => setNotice(null), 3000);
    return () => clearTimeout(t);
  }, [notice]);

  const handle = useMemo<DeckHandle>(
    () => ({
      newShell: () => void newShell(),
      openPicker: () => setPicker(true),
      paste: async () => {
        const text = await navigator.clipboard?.readText().catch(() => "");
        if (text) pasteText(text);
      },
      selectText: () => {
        const h = activeId !== null ? handles.current.get(activeId) : null;
        if (h) setSelectText(h.text());
      },
      zoom,
    }),
    [newShell, pasteText, activeId, zoom],
  );
  useEffect(() => {
    deckRef?.(handle);
    return () => deckRef?.(null);
  }, [deckRef, handle]);

  // ---------- render ----------
  const pickerTabs: PickerTab[] = tabs.map((t) => ({
    term: t.term,
    title: t.title,
    ended: t.ended,
  }));
  const header = (
    <div className="flex h-10 shrink-0 items-center gap-1 border-b border-white/10 bg-[#161718] px-1.5">
      {touch ? (
        <button
          className="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-lg px-2 text-left hover:bg-white/10"
          onClick={() => setPicker(true)}
          disabled={!live}
          aria-haspopup="dialog"
          aria-label={`Terminals: ${active?.title ?? "none open"}. Switch or open another`}
        >
          <Icon name="terminal" size={18} className="shrink-0 text-[#81c995]" />
          <span className="min-w-0 truncate text-sm">{active?.title ?? "Terminal"}</span>
          {tabs.length > 1 && (
            <span className="shrink-0 rounded-full bg-white/15 px-1.5 text-[11px] tabular-nums">
              {tabs.length}
            </span>
          )}
          <Icon name="unfoldMore" size={16} className="shrink-0 text-white/60" />
        </button>
      ) : (
        <div
          role="tablist"
          aria-label="Terminals"
          className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
        >
          {tabs.map((t) => (
            <div
              key={t.term.id}
              className={`group flex h-8 max-w-56 shrink-0 items-center rounded-lg text-xs ${t.term.id === activeId ? "bg-white/[0.12] text-white" : "text-white/65 hover:bg-white/[0.06]"}`}
            >
              <button
                role="tab"
                aria-selected={t.term.id === activeId}
                className="flex min-w-0 items-center gap-1.5 py-1 pl-2"
                onClick={() => setActiveId(t.term.id)}
                title={t.title}
              >
                <span
                  className={`h-1.5 w-1.5 shrink-0 rounded-full ${t.ended ? "bg-white/30" : "bg-[#81c995]"}`}
                />
                <span className="truncate">{t.title}</span>
              </button>
              <button
                className="ml-1 rounded p-1 text-white/50 opacity-0 group-hover:opacity-100 hover:bg-white/15 hover:text-white focus-visible:opacity-100"
                aria-label={`Close ${t.title} here (it keeps running on the device)`}
                title="Close here (keeps running on the device)"
                onClick={() => closeTab(t.term)}
              >
                <Icon name="close" size={12} />
              </button>
            </div>
          ))}
          <button
            className="btn btn-sm shrink-0 px-2 text-white/80 hover:bg-white/10"
            aria-label="New shell"
            title="New shell (Ctrl+Alt+Shift+T)"
            disabled={!live}
            onClick={() => void newShell()}
          >
            <Icon name="add" size={18} />
          </button>
          <button
            className="btn btn-sm shrink-0 px-2 text-white/80 hover:bg-white/10"
            aria-label="Terminals on the device"
            title="Terminals on the device"
            aria-haspopup="dialog"
            disabled={!live}
            onClick={() => setPicker((p) => !p)}
          >
            <Icon name="chevronDown" size={18} />
          </button>
        </div>
      )}
      {touch && (
        <button
          className="btn btn-sm shrink-0 px-2 text-white/85 hover:bg-white/10"
          aria-label="New shell"
          disabled={!live}
          onClick={() => void newShell()}
        >
          <Icon name="add" size={20} />
        </button>
      )}
      {headerEnd}
    </div>
  );

  let body: ReactNode;
  if (!connected) body = null;
  else if (state === "pending" || !channel)
    body =
      state === "unsupported" ? (
        <Empty icon="terminal" title="No terminal on this device">
          Its agent doesn't offer terminals. Update the agent, check that{" "}
          <b>Allow remote terminal</b> is on in its menu, and that your organization allows
          terminals.
        </Empty>
      ) : (
        <Empty icon={null} title="Connecting the terminal…" />
      );
  else if (tabs.length === 0)
    body = (
      <Empty icon="terminal" title={busy ?? "No terminal open"}>
        <div className="mt-3 flex flex-wrap justify-center gap-2">
          <button className="btn-primary" disabled={!!busy} onClick={() => void newShell()}>
            <Icon name="add" size={18} />
            New shell
          </button>
          <button
            className="btn-outline border-white/30 text-white hover:bg-white/10"
            onClick={() => setPicker(true)}
          >
            Terminals on the device
          </button>
        </div>
        {error && <p className="mt-3 text-sm text-[#f6aea9]">{error}</p>}
      </Empty>
    );

  return (
    <div className="relative flex min-h-0 flex-1 flex-col bg-[#131314] text-white">
      {header}
      <div ref={areaRef} className="relative min-h-0 flex-1 overflow-hidden">
        {body}
        {tabs.map((t) => (
          <TerminalView
            key={t.term.id}
            term={t.term}
            visible={visible && t.term.id === activeId}
            touch={touch}
            fontSize={fontSize}
            onFontSize={pinch}
            onSize={(s) => (lastSize.current = s)}
            onTap={onTap}
            onHotkey={onHotkey}
            handleRef={(h) => {
              if (h) handles.current.set(t.term.id, h);
              else handles.current.delete(t.term.id);
            }}
          />
        ))}
        {active?.ended && (
          <div className="absolute inset-x-2 bottom-2 z-10 flex flex-wrap items-center gap-2 rounded-xl bg-[#2d2e30] px-3 py-2 text-sm shadow-lg ring-1 ring-white/10">
            <span className="flex-1 text-white/75">This terminal ended.</span>
            {active.term.source.kind === "shell" && (
              <button
                className="btn btn-sm bg-white/10 text-white hover:bg-white/15"
                onClick={() => void newShell()}
              >
                New shell
              </button>
            )}
            <button
              className="btn btn-sm text-white/80 hover:bg-white/10"
              onClick={() => closeTab(active.term)}
            >
              Close
            </button>
          </div>
        )}
        {notice && (
          <div
            role="status"
            className="absolute top-2 left-1/2 z-20 -translate-x-1/2 rounded-lg bg-[#f9ab00] px-3 py-1.5 text-sm text-black shadow"
          >
            {notice}
          </div>
        )}
        {busy && tabs.length > 0 && !picker && (
          <div className="absolute top-2 right-2 z-20 flex items-center gap-2 rounded-full bg-black/70 px-3 py-1.5 text-xs">
            <Spinner size={14} /> {busy}
          </div>
        )}
      </div>
      {touch && compose && live && (
        <ComposeBar deviceId={deviceId} onSend={sendLine} onClose={() => setCompose(false)} />
      )}
      {touch && live && tabs.length > 0 && (
        <div className={keyboardOpen || compose ? "" : "pb-[env(safe-area-inset-bottom)]"}>
          <AccessoryBar
            ctrl={ctrl}
            alt={alt}
            onMod={onMod}
            onKey={onKey}
            onText={onText}
            keyboardOpen={keyboardOpen || compose}
            onToggleKeyboard={toggleKeyboard}
            composeOpen={compose}
            onToggleCompose={() => setCompose((c) => !c)}
          />
        </div>
      )}
      {touch && (
        <TermKeyboard
          inputRef={kbRef}
          mods={currentMods}
          appCursor={appCursor}
          onBytes={onKbBytes}
          onPaste={pasteText}
          onFocusChange={setKeyboardOpen}
        />
      )}
      {picker && live && (
        <TerminalPicker
          sheet={touch}
          tabs={pickerTabs}
          active={active?.term ?? null}
          sources={sources}
          shells={shells}
          admin={admin}
          error={error}
          busy={busy}
          onSwitch={(t) => {
            setActiveId(t.id);
            setPicker(false);
          }}
          onCloseTab={closeTab}
          onAttach={(s) => void attach(s)}
          onEnd={(s) => void endSource(s)}
          onNew={(sh, ad) => void newShell(sh, ad)}
          onRefresh={() => void refresh()}
          onDismiss={() => {
            setPicker(false);
            setError(null);
          }}
        />
      )}
      {selectText !== null && <SelectSheet text={selectText} onClose={() => setSelectText(null)} />}
    </div>
  );
}

function Empty({
  icon,
  title,
  children,
}: {
  icon: "terminal" | null;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 p-6 text-center">
      {icon ? <Icon name={icon} size={40} className="text-white/40" /> : <Spinner size={32} />}
      <div className="text-base">{title}</div>
      {children && <div className="max-w-sm text-sm text-white/65">{children}</div>}
    </div>
  );
}
