// The terminals of one session: a tab for every terminal on the device (or,
// on phones, one terminal at a time with a switcher), attached when first
// shown; the picker to start shells or take over others; and the phone key
// bar, keyboard and command line. After a reconnect it reattaches the
// terminals this tab had open.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "../../components/icons";
import { Spinner } from "../../components/ui";
import type { Hotkey } from "../../lib/shortcuts";
import { defaultFontSize, MAX_FONT, MIN_FONT } from "../../lib/termGestures";
import { describeTermError, TermError, typedPath } from "../../lib/terminal";
import type {
  TerminalAvailability,
  TermBlocked,
  TermId,
  TermShell,
  TermSize,
  TermSource,
  TermSourceId,
} from "../../lib/terminal";
import { fontKey, openKey, readList, writeList } from "../../lib/termHistory";
import { stripItems } from "../../lib/termStrip";
import { afterKey, isOn, keySequence, pasteBytes, tapMod, withMods } from "../../lib/termKeys";
import type { ModState, Mods, NamedKey } from "../../lib/termKeys";
import { isFinal } from "../FileChannel";
import type { FileChannel, Transfer } from "../FileChannel";
import type { Term, TerminalChannel } from "../TerminalChannel";
import { AccessoryBar } from "./AccessoryBar";
import { ComposeBar } from "./ComposeBar";
import { SearchBar } from "./SearchBar";
import { SelectSheet } from "./SelectSheet";
import { TerminalPicker } from "./TerminalPicker";
import type { PickerTab } from "./TerminalPicker";
import { TermKeyboard } from "./TermKeyboard";
import { TerminalView } from "./TerminalView";
import type { FindResults, TerminalViewHandle } from "./TerminalView";
import { TERMINAL_THEMES } from "./theme";
import { AppearancePanel } from "./AppearancePanel";
import { useTermAppearance } from "./appearance";
import { useVisualViewport } from "./useVisualViewport";

/** `lost`: it ended only because the connection did; it keeps running on the device. */
type Tab = { term: Term; title: string; ended: boolean; lost: boolean };

/** Terminals kept attached at once (the agent allows 8 per channel); switching
 * to another detaches the one used least recently, which keeps running. */
const MAX_ATTACHED = 6;
/** How often the list of terminals on the device is refreshed while in view. */
const LIST_EVERY_MS = 8000;

export type DeckHandle = {
  newShell: () => void;
  openPicker: () => void;
  paste: () => Promise<void>;
  selectText: () => void;
  zoom: (step: 1 | -1) => void;
  find: () => void;
  /** Picks files to upload into the terminal's working directory. */
  upload: () => void;
  /** Opens the Appearance panel. */
  appearance: () => void;
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
  /** The session's file channel: files dropped on the terminal go to its working directory. */
  files?: FileChannel | null;
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
  files = null,
}: Props) {
  const [tabs, setTabs] = useState<Tab[]>([]);
  const [activeId, setActiveId] = useState<TermId | null>(null);
  const [picker, setPicker] = useState(false);
  const [sources, setSources] = useState<TermSource[] | null>(null);
  const [blocked, setBlocked] = useState<TermBlocked[]>([]);
  const [order, setOrder] = useState<TermSourceId[]>([]);
  const [hidden, setHidden] = useState<ReadonlySet<TermSourceId>>(new Set());
  const [opening, setOpening] = useState<TermSourceId | null>(null);
  const [shells, setShells] = useState<TermShell[]>([]);
  const [admin, setAdmin] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [fontSize, setFontSize] = useState(() => readFont(deviceId));
  // The key field has focus; kbUp: and the on-screen keyboard shows (Android's
  // Back hides it without taking the focus away).
  const [keyboardOpen, setKeyboardOpen] = useState(false);
  const port = useVisualViewport(touch && visible);
  const kbUp = keyboardOpen && port?.keyboard !== false;
  const [compose, setCompose] = useState(false);
  const [ctrl, setCtrl] = useState<ModState>("off");
  const [alt, setAlt] = useState<ModState>("off");
  const [selectText, setSelectText] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [finding, setFinding] = useState(false);
  const [findResults, setFindResults] = useState<FindResults | null>(null);
  const [dropping, setDropping] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const appearance = useTermAppearance();
  const theme = TERMINAL_THEMES[appearance.theme].theme;
  const [styling, setStyling] = useState(false);
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

  const tabsRef = useRef(tabs);
  tabsRef.current = tabs;
  const lastUsed = useRef(new Map<TermId, number>());
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

  useEffect(() => {
    if (activeId !== null) lastUsed.current.set(activeId, Date.now());
  }, [activeId]);

  // Tabs keep the place they got when first seen.
  useEffect(() => {
    setOrder((o) => {
      const seen = new Set(o);
      const add = [
        ...tabs.map((t) => t.term.source.id),
        ...(sources ?? []).map((s) => s.id),
      ].filter((id) => !seen.has(id) && seen.add(id));
      return add.length ? [...o, ...add] : o;
    });
  }, [tabs, sources]);

  const strip = useMemo(
    () => stripItems(order, tabs, (t) => t.term.source.id, sources ?? [], hidden),
    [order, tabs, sources, hidden],
  );

  const setHiddenId = (id: TermSourceId, hide: boolean) =>
    setHidden((h) => {
      if (h.has(id) === hide) return h;
      const next = new Set(h);
      if (hide) next.add(id);
      else next.delete(id);
      return next;
    });

  /** Detaches the attached terminal used least recently when no more may be attached. */
  const makeRoom = useCallback(() => {
    const open = tabsRef.current.filter((t) => !t.ended);
    if (open.length < MAX_ATTACHED) return;
    const [victim] = open
      .filter((t) => t.term.id !== activeId)
      .sort(
        (a, b) => (lastUsed.current.get(a.term.id) ?? 0) - (lastUsed.current.get(b.term.id) ?? 0),
      );
    if (!victim) return;
    victim.term.detach();
    setTabs((ts) => ts.filter((t) => t.term !== victim.term));
  }, [activeId]);

  // The terminals this tab watches, reattached after a reconnect.
  useEffect(() => {
    if (!channel || restored.current !== channel) return;
    // The tab in view goes last: reattaching shows the last one opened.
    const open = tabs.filter((t) => !t.ended || t.lost);
    const ids = [
      ...open.filter((t) => t.term.id !== activeId),
      ...open.filter((t) => t.term.id === activeId),
    ].map((t) => t.term.source.id);
    writeList(globalThis.sessionStorage, openKey(deviceId), ids);
  }, [tabs, channel, deviceId, restoredTick, activeId]);

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

  /** quiet: keep showing the current list while asking (a poll). */
  const refresh = useCallback(
    async ({ ask = false, quiet = false } = {}) => {
      if (!channel) return;
      if (!quiet) setSources(null);
      try {
        const r = await channel.list(ask);
        setSources(r.sources);
        setShells(r.shells);
        setAdmin(r.admin);
        setBlocked(r.blocked);
      } catch (e) {
        if (quiet) return;
        setSources([]);
        fail(e);
      }
    },
    [channel],
  );

  const newShell = useCallback(
    async (shell?: string, asAdmin?: boolean) => {
      if (!channel) return;
      setError(null);
      setBusy(asAdmin ? "Starting an administrator shell…" : "Starting a shell…");
      makeRoom();
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
    [channel, addTab, size, makeRoom],
  );

  const attach = useCallback(
    async (s: TermSource) => {
      if (!channel) return;
      const open = tabsRef.current.find((t) => t.term.source.id === s.id && !t.ended);
      if (open) {
        setActiveId(open.term.id);
        setPicker(false);
        return;
      }
      setError(null);
      setHiddenId(s.id, false);
      setOpening(s.id);
      setBusy(`Opening ${s.title}…`);
      makeRoom();
      try {
        addTab(await channel.attach(s.id, size()));
        setPicker(false);
      } catch (e) {
        fail(e);
        // From a tab the picker isn't open to show the error.
        setNotice(`${s.title}: ${e instanceof TermError ? describeTermError(e.code) : String(e)}`);
        void refresh({ quiet: true });
      } finally {
        setBusy(null);
        setOpening(null);
      }
    },
    [channel, addTab, size, refresh, makeRoom],
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

  /** Closes a tab here; the terminal keeps running on the device (and stays in the picker). */
  const closeTab = (term: Term) => {
    setHiddenId(term.source.id, true);
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
      try {
        const r = await channel.list();
        setSources(r.sources);
        setShells(r.shells);
        setAdmin(r.admin);
        setBlocked(r.blocked);
        if (r.blocked.length) setPicker(true);
        if (opened || !autoStart) return;
        // Every terminal on the device is a tab; show the first, or start a shell.
        if (r.sources[0]) await attach(r.sources[0]);
        else await newShell();
      } catch (e) {
        fail(e);
      }
    })();
  }, [channel, state, deviceId, autoStart, addTab, size, newShell, attach]);

  // Typing goes to the terminal in view once the picker closes or a tab is picked.
  useEffect(() => {
    if (touch || picker || !visible || activeId === null) return;
    handles.current.get(activeId)?.focus();
  }, [touch, picker, visible, activeId]);

  // Terminals opened or closed on the device come and go as tabs.
  useEffect(() => {
    if (!live || !visible) return;
    const t = setInterval(() => void refresh({ quiet: true }), LIST_EVERY_MS);
    return () => clearInterval(t);
  }, [live, visible, refresh]);

  useEffect(() => {
    if (picker) void refresh();
  }, [picker, refresh]);

  // While the device shows a permission prompt, pick up the answer.
  const waiting = picker && blocked.some((b) => b.reason === "asking");
  useEffect(() => {
    if (!waiting) return;
    const t = setInterval(() => void refresh({ quiet: true }), 2000);
    return () => clearInterval(t);
  }, [waiting, refresh]);

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
  /** Must run inside the tap: phones raise the keyboard only for a focus the user asked for. */
  const raiseKeyboard = () => {
    const kb = kbRef.current;
    if (!kb) return;
    // Focusing the field it already has shows no keyboard: focus it anew.
    if (document.activeElement === kb) kb.blur();
    kb.focus({ preventScroll: true });
  };
  const toggleKeyboard = () => {
    if (compose) {
      setCompose(false);
      return;
    }
    if (kbUp) kbRef.current?.blur();
    else raiseKeyboard();
  };
  const onTap = () => {
    if (!touch || compose || kbUp) return;
    raiseKeyboard();
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

  // ---------- find ----------
  const find = (query: string, back: boolean) => {
    const h = activeId !== null ? handles.current.get(activeId) : null;
    if (!h) return;
    if (!query) {
      h.clearFind();
      setFindResults(null);
    } else if (!h.find(query, back)) setFindResults({ index: -1, count: 0 });
  };
  const closeFind = () => {
    if (activeId !== null) handles.current.get(activeId)?.clearFind();
    setFinding(false);
    setFindResults(null);
    if (!touch && activeId !== null) handles.current.get(activeId)?.focus();
  };

  // ---------- upload into the working directory ----------
  const upload = useCallback(
    async (list: File[]) => {
      if (!list.length) return;
      if (!files) {
        setNotice("This device can't receive files.");
        return;
      }
      const t = tabs.find((x) => x.term.id === activeId && !x.ended) ?? null;
      let dir: string | null = null;
      if (t && channel) dir = await channel.cwd(t.term.id).catch(() => null);
      setBusy(
        list.length === 1 ? `Uploading ${list[0]!.name}…` : `Uploading ${list.length} files…`,
      );
      try {
        const done = await uploadAll(files, list, dir);
        const ok = done.filter((x) => x.status === "done" && x.path);
        // The names go on the command line, ready to use.
        if (ok.length && t) type(ok.map((x) => typedPath(x.path!, dir)).join(" ") + " ");
        setNotice(
          ok.length < done.length
            ? `${done.length - ok.length} of ${done.length} files were not uploaded.`
            : `Uploaded to ${dir ?? "the Downloads folder"}`,
        );
      } finally {
        setBusy(null);
      }
    },
    [files, tabs, activeId, channel, type],
  );

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
      find: () => setFinding(true),
      upload: () => fileInput.current?.click(),
      appearance: () => setStyling(true),
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
          {strip.length > 1 && (
            <span className="shrink-0 rounded-full bg-white/15 px-1.5 text-[11px] tabular-nums">
              {strip.length}
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
          {strip.map(({ id, tab: t, source }) => {
            const selected = t !== null && t.term.id === activeId;
            const title = t?.title ?? source?.title ?? id;
            return (
              <div
                key={id}
                className={`group flex h-8 max-w-56 shrink-0 items-center rounded-lg text-xs ${selected ? "bg-white/[0.12] text-white" : t ? "text-white/70 hover:bg-white/[0.06]" : "text-white/50 hover:bg-white/[0.06] hover:text-white/80"}`}
              >
                <button
                  role="tab"
                  aria-selected={selected}
                  data-attached={t !== null}
                  className="flex min-w-0 items-center gap-1.5 py-1 pl-2"
                  disabled={!t && (!live || opening !== null)}
                  onClick={() => {
                    if (!t) return source && void attach(source);
                    setActiveId(t.term.id);
                    handles.current.get(t.term.id)?.focus(); // also when it was already in view
                  }}
                  title={[title, source?.detail].filter(Boolean).join("\n")}
                >
                  {opening === id ? (
                    <Spinner size={10} label="Opening" />
                  ) : (
                    <span
                      className={`h-1.5 w-1.5 shrink-0 rounded-full ${!t ? "ring-1 ring-white/45" : t.ended ? "bg-white/30" : "bg-[#81c995]"}`}
                    />
                  )}
                  <span className="truncate">{title}</span>
                </button>
                <button
                  className="ml-1 rounded p-1 text-white/50 opacity-0 group-hover:opacity-100 hover:bg-white/15 hover:text-white focus-visible:opacity-100"
                  aria-label={`Close ${title} here (it keeps running on the device)`}
                  title="Close here (keeps running on the device)"
                  onClick={() => (t ? closeTab(t.term) : setHiddenId(id, true))}
                >
                  <Icon name="close" size={12} />
                </button>
              </div>
            );
          })}
          {blocked.length > 0 && (
            <button
              className="flex h-8 shrink-0 items-center gap-1 rounded-lg px-2 text-xs text-[#fdd663] hover:bg-white/[0.06]"
              onClick={() => setPicker(true)}
              title={blocked.map((b) => `${b.app} tabs need permission on the device`).join("\n")}
            >
              <Icon name="warning" size={14} />
              {blocked.map((b) => b.app).join(", ")} hidden
            </button>
          )}
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
      <button
        className="btn btn-sm shrink-0 px-2 text-white/80 hover:bg-white/10"
        aria-label="Appearance"
        title="Appearance: theme, text size, font, cursor"
        aria-haspopup="dialog"
        onClick={() => setStyling((v) => !v)}
      >
        <Icon name="palette" size={touch ? 20 : 18} />
      </button>
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
      <div
        ref={areaRef}
        className="relative min-h-0 flex-1 overflow-hidden"
        onDragOver={(e) => {
          if (!live || !e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          e.dataTransfer.dropEffect = "copy";
          setDropping(true);
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDropping(false);
        }}
        onDrop={(e) => {
          if (!e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          setDropping(false);
          void upload(droppedFiles(e.dataTransfer));
        }}
      >
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
            theme={theme}
            appearance={appearance}
            onFind={() => setFinding(true)}
            onFindResults={t.term.id === activeId ? setFindResults : undefined}
            handleRef={(h) => {
              if (h) handles.current.set(t.term.id, h);
              else handles.current.delete(t.term.id);
            }}
          />
        ))}
        {/* A terminal cut off by a dropped connection comes back with the connection. */}
        {active?.ended && !active.lost && (
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
        {finding && tabs.length > 0 && (
          <SearchBar results={findResults} onFind={find} onClose={closeFind} />
        )}
        {dropping && (
          <div className="pointer-events-none absolute inset-2 z-30 flex items-center justify-center rounded-xl border-2 border-dashed border-[#8ab4f8] bg-[#131314]/80 text-sm text-white">
            Drop to upload into this terminal's folder
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
        <div className={kbUp || compose ? "" : "pb-[env(safe-area-inset-bottom)]"}>
          <AccessoryBar
            ctrl={ctrl}
            alt={alt}
            onMod={onMod}
            onKey={onKey}
            onText={onText}
            keyboardOpen={kbUp || compose}
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
          blocked={blocked}
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
          onAskAgain={() => void refresh({ ask: true, quiet: true })}
          onDismiss={() => {
            setPicker(false);
            setError(null);
          }}
        />
      )}
      {styling && (
        <AppearancePanel
          sheet={touch}
          fontSize={fontSize}
          onFontSize={(n) => pinch(Math.min(MAX_FONT, Math.max(MIN_FONT, n)))}
          onClose={() => setStyling(false)}
        />
      )}
      {selectText !== null && <SelectSheet text={selectText} onClose={() => setSelectText(null)} />}
      <input
        ref={fileInput}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          const list = [...(e.target.files ?? [])];
          e.target.value = "";
          void upload(list);
        }}
      />
    </div>
  );
}

/** Uploads files into dir (null: the agent's default) and resolves once all of them finished. */
function uploadAll(fc: FileChannel, list: File[], dir: string | null): Promise<Transfer[]> {
  const ids = list.map((f) => fc.upload(f, dir));
  return new Promise((resolve) => {
    let off = () => {};
    const check = () => {
      const ts = fc.getTransfers().filter((t) => ids.includes(t.xfer));
      if (ts.length === ids.length && ts.every(isFinal)) {
        off();
        resolve(ts);
      }
    };
    off = fc.subscribe(check);
    check();
  });
}

/** The files of a drop, without folders. */
function droppedFiles(dt: DataTransfer): File[] {
  const items = [...dt.items];
  if (!items.length) return [...dt.files];
  return items
    .filter((it) => it.kind === "file" && !it.webkitGetAsEntry()?.isDirectory)
    .map((it) => it.getAsFile())
    .filter((f): f is File => f !== null);
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
