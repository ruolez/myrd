import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { DragEvent, KeyboardEvent, MouseEvent, ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import type { Device, Tag } from "../api/types";
import { useDirectory } from "../app/directory";
import { useWorkspace, useWorkspaceState } from "../app/workspace";
import { Icon } from "../components/icons";
import { Popover } from "../components/menu";
import { EmptyState, ErrorBox, Loading, SegmentedControl } from "../components/ui";
import { applyFilter } from "../lib/deviceFilters";
import type { Chip, DashView, FilterCtx, SortKey } from "../lib/deviceFilters";
import {
  DEFAULT_PREFS,
  PREFS_KEY,
  TILE_MIN_WIDTH,
  parseDashParams,
  parseDashPrefs,
  serializeDashPrefs,
  toDashParams,
} from "../lib/dashboardState";
import type { DashParams, DashPrefs } from "../lib/dashboardState";
import { folderPath } from "../lib/folders";
import { useNow } from "../lib/hooks";
import { gridNav, selectRange, toggleId } from "../lib/selection";
import { LiveThumb } from "../session/LiveThumb";
import { useDeviceActions } from "./devices/actions";
import { DeviceViewsNav, viewTitle } from "./devices/DashboardPane";
import { DeviceTable } from "./devices/DeviceTable";
import { DEVICE_DRAG, DeviceTile } from "./devices/DeviceTile";
import type { TileProps } from "./devices/DeviceTile";

const CHIP_GROUPS: { title: string; chips: { chip: Chip; label: string }[] }[] = [
  {
    title: "Status",
    chips: [
      { chip: "online", label: "Online" },
      { chip: "offline", label: "Offline" },
      { chip: "in_session", label: "In session" },
      { chip: "attention", label: "Needs attention" },
    ],
  },
  {
    title: "System",
    chips: [
      { chip: "darwin", label: "macOS" },
      { chip: "windows", label: "Windows" },
      { chip: "linux", label: "Linux" },
    ],
  },
];
const CHIPS = CHIP_GROUPS.flatMap((g) => g.chips);

// Below this many devices a Favorites row would only repeat the grid.
const SECTIONS_FROM = 6;

const SORTS: { value: SortKey; label: string }[] = [
  { value: "status", label: "Status" },
  { value: "name", label: "Name" },
  { value: "last_connected", label: "Recently used" },
  { value: "last_seen", label: "Last seen" },
];

function usePrefs(): [DashPrefs, (fn: (p: DashPrefs) => DashPrefs) => void] {
  const [prefs, setPrefs] = useState<DashPrefs>(() => {
    try {
      return parseDashPrefs(localStorage.getItem(PREFS_KEY));
    } catch {
      return DEFAULT_PREFS;
    }
  });
  const update = useCallback((fn: (p: DashPrefs) => DashPrefs) => {
    setPrefs((cur) => {
      const next = fn(cur);
      try {
        localStorage.setItem(PREFS_KEY, serializeDashPrefs(next));
      } catch {
        // storage unavailable
      }
      return next;
    });
  }, []);
  return [prefs, update];
}

/** Focus moves between tiles with the arrow keys, Home and End. */
function onGridKey(e: KeyboardEvent<HTMLElement>) {
  const target = e.target as HTMLElement;
  if (!target.matches("[data-tile-primary]")) return;
  const tiles = [...e.currentTarget.querySelectorAll<HTMLElement>("[data-tile-primary]")];
  const top = tiles[0]?.getBoundingClientRect().top;
  const columns = tiles.filter((t) => t.getBoundingClientRect().top === top).length;
  const next = gridNav(tiles.indexOf(target), e.key, columns, tiles.length);
  if (next === null) return;
  e.preventDefault();
  tiles[next]?.focus();
}

type TileOverrides = Pick<TileProps, "media" | "status" | "primary">;

function FilterChip({ label, on, onClick }: { label: string; on: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      className={`rounded-full border px-3 py-1 text-xs font-medium transition-colors ${
        on
          ? "border-primary bg-primary-soft text-primary"
          : "border-outline text-muted hover:text-text"
      }`}
    >
      {on && <Icon name="check" size={14} className="mr-1 -ml-1 inline" />}
      {label}
    </button>
  );
}

function OptionRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="text-sm text-muted">{label}</span>
      {children}
    </div>
  );
}

export function Devices() {
  const dir = useDirectory();
  const { devices, folders, tags, organization, loading, error } = dir;
  const actions = useDeviceActions();
  const now = useNow(30_000);
  const [params, setParams] = useSearchParams();
  const state = useMemo(() => parseDashParams(params), [params]);
  const [prefs, setPrefs] = usePrefs();
  const sort = state.sort ?? prefs.sort;
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const anchor = useRef<string | null>(null);
  const [queryDraft, setQueryDraft] = useState(state.query);
  const searchRef = useRef<HTMLInputElement>(null);
  const [paneOpen, setPaneOpen] = useState(false);
  const paneBtn = useRef<HTMLButtonElement>(null);
  const [filterOpen, setFilterOpen] = useState(false);
  const filterBtn = useRef<HTMLButtonElement>(null);
  const [displayOpen, setDisplayOpen] = useState(false);
  const displayBtn = useRef<HTMLButtonElement>(null);
  const zen = prefs.zen && prefs.layout === "grid";

  const setState = useCallback(
    (patch: Partial<DashParams>) =>
      setParams((p) => toDashParams({ ...parseDashParams(p), ...patch }), { replace: true }),
    [setParams],
  );

  // The search box writes the URL a moment after typing stops.
  useEffect(() => setQueryDraft(state.query), [state.query]);
  useEffect(() => {
    if (queryDraft === state.query) return;
    const t = setTimeout(() => setState({ query: queryDraft }), 200);
    return () => clearTimeout(t);
  }, [queryDraft, state.query, setState]);

  // "/" focuses search, "z" toggles zen mode; Escape clears the selection.
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      const t = e.target as HTMLElement;
      const typing = t.closest("input, textarea, select, [contenteditable=true]");
      const plain = !typing && !e.metaKey && !e.ctrlKey && !e.altKey;
      if (e.key === "/" && plain) {
        e.preventDefault();
        searchRef.current?.focus();
      } else if (e.key === "z" && plain) {
        setPrefs((p) => ({ ...p, layout: "grid", zen: !(p.zen && p.layout === "grid") }));
      } else if (e.key === "Escape" && !typing) {
        setSelected(new Set());
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [setPrefs]);

  // Views of folders or tags that no longer exist fall back to all devices.
  const view: DashView = useMemo(() => {
    const v = state.view;
    if (typeof v === "object" && !dir.loading) {
      if ("folder" in v && !folders.some((f) => f.id === v.folder)) return "all";
      if ("tag" in v && !tags.some((t) => t.id === v.tag)) return "all";
    }
    return v;
  }, [state.view, folders, tags, dir.loading]);

  const ws = useWorkspace();
  const wsState = useWorkspaceState();
  const openHere = useMemo(() => new Set<string>(wsState.order), [wsState.order]);
  const ctx: FilterCtx = useMemo(
    () => ({ folders, tags, now, openHere }),
    [folders, tags, now, openHere],
  );
  const shown = useMemo(
    () => applyFilter(devices, { view, query: state.query, chips: state.chips, sort }, ctx),
    [devices, view, state.query, state.chips, sort, ctx],
  );
  const tagMap = useMemo(() => new Map<string, Tag>(tags.map((t) => [t.id, t])), [tags]);
  const order = useMemo<string[]>(() => shown.map((d) => d.id), [shown]);

  // Drop selected devices that disappeared from view.
  useEffect(() => {
    setSelected((s) => {
      const keep = new Set([...s].filter((id) => order.includes(id)));
      return keep.size === s.size ? s : keep;
    });
  }, [order]);

  const onSelect = (d: Device, e: MouseEvent | KeyboardEvent) => {
    if (e.shiftKey && anchor.current)
      setSelected((s) => selectRange(s, order, anchor.current as string, d.id));
    else setSelected((s) => toggleId(s, d.id));
    anchor.current = d.id;
  };
  const onDragStart = (d: Device, e: DragEvent) => {
    const ids = selected.has(d.id) ? [...selected] : [d.id];
    e.dataTransfer.setData(DEVICE_DRAG, JSON.stringify(ids));
    e.dataTransfer.setData(
      "text/plain",
      ids.map((id) => devices.find((x) => x.id === id)?.name ?? id).join(", "),
    );
    e.dataTransfer.effectAllowed = "move";
  };

  const overrides = (d: Device): TileOverrides =>
    actions.openHere(d)
      ? {
          media: (
            <span className="absolute inset-0">
              <LiveThumb session={ws.session(d.id)} os={d.os} className="h-full w-full" />
            </span>
          ),
          status: { label: "Open here", tone: "here" },
          primary: {
            label: "Switch to",
            onClick: () => actions.connect(d),
            enabled: true,
            title: `Switch to the session with ${d.name}`,
          },
        }
      : {};

  const filtering = state.query !== "" || state.chips.size > 0;
  const sections =
    view === "all" && !filtering && prefs.layout === "grid" && devices.length > SECTIONS_FROM
      ? [
          organization && {
            key: "fav",
            title: "Favorites",
            items: shown.filter((d) => d.favorite),
          },
        ].filter(
          (s): s is { key: string; title: string; items: Device[] } => !!s && s.items.length > 0,
        )
      : [];

  const grid = (items: Device[], key: string) => (
    <ul
      key={key}
      className={`grid ${zen ? "gap-5" : "gap-4"}`}
      style={{
        gridTemplateColumns: `repeat(auto-fill, minmax(min(100%, ${TILE_MIN_WIDTH[prefs.size]}px), 1fr))`,
      }}
      onKeyDown={onGridKey}
    >
      {items.map((d) => (
        <li key={d.id}>
          <DeviceTile
            device={d}
            tags={tagMap}
            now={now}
            size={prefs.size}
            zen={zen}
            selected={selected.has(d.id)}
            selecting={selected.size > 0}
            organization={organization}
            onSelect={(e) => onSelect(d, e)}
            onDragStart={(e) => onDragStart(d, e)}
            onKick={dir.kickUpdates}
            {...overrides(d)}
          />
        </li>
      ))}
    </ul>
  );

  const title = viewTitle(view, folders, tags);
  const crumbs =
    typeof view === "object" && "folder" in view ? folderPath(folders, view.folder) : [];
  const online = devices.filter((d) => d.online).length;
  const heading = crumbs.length > 1 ? crumbs.join(" / ") : title;
  const count =
    view === "all" && !filtering
      ? `${online} of ${devices.length} online`
      : `${shown.length} of ${devices.length}`;
  const selIds = [...selected];

  let body: ReactNode;
  if (loading && devices.length === 0) body = <Loading />;
  else if (devices.length === 0 && !error)
    body = (
      <div className="card">
        <EmptyState
          title="No devices yet"
          action={
            <Link to="/devices/add" className="btn-primary">
              <Icon name="add" size={18} />
              Add your first device
            </Link>
          }
        >
          Install the agent on a Mac or Windows PC, or start the Docker demo device, then enroll it
          with a one-time token.
        </EmptyState>
      </div>
    );
  else if (shown.length === 0)
    body = (
      <div className="card">
        {filtering ? (
          <EmptyState
            title="No devices match"
            action={
              <button
                className="btn-tonal"
                onClick={() => {
                  setQueryDraft("");
                  setState({ query: "", chips: new Set() });
                }}
              >
                Clear search and filters
              </button>
            }
          >
            Search looks at names, host names, notes, tags and folders. Try <code>tag:</code>,{" "}
            <code>folder:</code>, <code>os:mac</code> or <code>is:online</code>.
          </EmptyState>
        ) : view === "attention" ? (
          <EmptyState title="All good">
            No device is offline for long, failing updates or missing permissions.
          </EmptyState>
        ) : view === "favorites" ? (
          <EmptyState title="No favorites yet">
            Star a device to keep it at the top of the dashboard.
          </EmptyState>
        ) : typeof view === "object" && "folder" in view ? (
          <EmptyState title="This folder is empty">
            Drag devices onto the folder in the list, or use “Move to folder…” in a device's menu.
          </EmptyState>
        ) : (
          <EmptyState title="Nothing here">No device fits this view right now.</EmptyState>
        )}
      </div>
    );
  else if (prefs.layout === "list")
    body = (
      <DeviceTable
        devices={shown}
        tags={tagMap}
        folders={folders}
        now={now}
        selected={selected}
        organization={organization}
        sort={sort}
        onSort={(s) => {
          setPrefs((p) => ({ ...p, sort: s }));
          setState({ sort: null });
        }}
        onSelect={onSelect}
        onDragStart={onDragStart}
        statusOf={(d) => overrides(d).status}
        primaryOf={(d) => overrides(d).primary}
      />
    );
  else
    body = (
      <div className="space-y-8">
        {sections.map((s) => (
          <section key={s.key} aria-labelledby={`sec-${s.key}`}>
            <h2 id={`sec-${s.key}`} className="mb-3 text-sm font-medium text-muted">
              {s.title}
            </h2>
            {grid(s.items, s.key)}
          </section>
        ))}
        <section aria-label={title}>
          {sections.length > 0 && (
            <h2 className="mb-3 text-sm font-medium text-muted">All devices</h2>
          )}
          {grid(shown, "all")}
        </section>
      </div>
    );

  return (
    <>
      {devices.length === 0 && <h1 className="mb-5 text-xl font-normal tracking-tight">Devices</h1>}
      {devices.length > 0 && (
        <div className="mb-5 flex flex-wrap items-center gap-x-2 gap-y-3">
          <div className="flex min-w-0 flex-1 items-baseline gap-3">
            <h1 className="min-w-0 truncate text-xl font-normal tracking-tight">
              <button
                ref={paneBtn}
                className="inline-flex max-w-full items-center gap-1 rounded-lg lg:hidden"
                aria-haspopup="dialog"
                aria-expanded={paneOpen}
                onClick={() => setPaneOpen((o) => !o)}
              >
                <span className="truncate">{heading}</span>
                <Icon name="chevronDown" size={18} className="shrink-0 text-muted" />
              </button>
              <span className="hidden lg:inline">{heading}</span>
            </h1>
            <span className="shrink-0 text-sm text-muted tabular-nums">{count}</span>
            {paneOpen && (
              <Popover
                anchor={paneBtn}
                onClose={() => setPaneOpen(false)}
                className="max-h-[70vh] w-72 overflow-auto p-2"
                role="dialog"
                aria-label="Device views"
              >
                <DeviceViewsNav onNavigate={() => setPaneOpen(false)} />
              </Popover>
            )}
          </div>
          <div className="flex w-full items-center gap-1 sm:w-auto">
            <label className="relative min-w-0 flex-1 sm:w-56 sm:flex-none sm:transition-[width] sm:focus-within:w-72 motion-reduce:transition-none">
              <span className="sr-only">Search devices</span>
              <Icon
                name="search"
                size={18}
                className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted"
              />
              <input
                ref={searchRef}
                className="peer input h-9 rounded-full border-transparent bg-surface-2 py-1.5 pr-8 pl-9 focus:bg-surface"
                type="search"
                placeholder="Search"
                value={queryDraft}
                onChange={(e) => setQueryDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    setQueryDraft("");
                    setState({ query: "" });
                    e.currentTarget.blur();
                  }
                }}
              />
              {!queryDraft && (
                <kbd className="pointer-events-none absolute top-1/2 right-3 max-sm:hidden -translate-y-1/2 rounded border border-outline px-1.5 font-sans text-[11px] text-muted peer-focus:hidden">
                  /
                </kbd>
              )}
            </label>
            <button
              ref={filterBtn}
              className={`btn h-9 shrink-0 px-3 ${
                state.chips.size
                  ? "bg-primary-soft text-primary"
                  : "text-muted hover:bg-surface-2 hover:text-text"
              }`}
              aria-haspopup="dialog"
              aria-expanded={filterOpen}
              title="Filter"
              onClick={() => setFilterOpen((o) => !o)}
            >
              <Icon name="filter" size={18} />
              <span className="sr-only sm:not-sr-only">Filter</span>
              {state.chips.size > 0 && (
                <span className="rounded-full bg-primary px-1.5 text-[11px] leading-4 text-on-primary tabular-nums">
                  {state.chips.size}
                </span>
              )}
            </button>
            {filterOpen && (
              <Popover
                anchor={filterBtn}
                align="end"
                onClose={() => setFilterOpen(false)}
                className="w-72 space-y-3 p-3"
                role="dialog"
                aria-label="Filters"
              >
                {CHIP_GROUPS.map((g) => (
                  <div key={g.title}>
                    <div className="mb-1.5 text-[11px] font-medium tracking-wide text-muted uppercase">
                      {g.title}
                    </div>
                    <div className="flex flex-wrap gap-1.5" role="group" aria-label={g.title}>
                      {g.chips.map(({ chip, label }) => (
                        <FilterChip
                          key={chip}
                          label={label}
                          on={state.chips.has(chip)}
                          onClick={() => setState({ chips: toggleId(state.chips, chip) })}
                        />
                      ))}
                    </div>
                  </div>
                ))}
                {state.chips.size > 0 && (
                  <div className="flex justify-end border-t border-outline pt-2">
                    <button
                      className="btn-text btn-sm"
                      onClick={() => setState({ chips: new Set() })}
                    >
                      Clear filters
                    </button>
                  </div>
                )}
              </Popover>
            )}
            {!zen && (
              <button
                ref={displayBtn}
                className="btn h-9 w-9 shrink-0 px-0 text-muted hover:bg-surface-2 hover:text-text"
                aria-haspopup="dialog"
                aria-expanded={displayOpen}
                aria-label="View options"
                title="View options"
                onClick={() => setDisplayOpen((o) => !o)}
              >
                <Icon name="tune" size={18} />
              </button>
            )}
            {displayOpen && !zen && (
              <Popover
                anchor={displayBtn}
                align="end"
                onClose={() => setDisplayOpen(false)}
                className="w-64 space-y-3 p-3"
                role="dialog"
                aria-label="View options"
              >
                <OptionRow label="Layout">
                  <SegmentedControl
                    label="Layout"
                    value={prefs.layout}
                    onChange={(layout) => setPrefs((p) => ({ ...p, layout }))}
                    options={[
                      { value: "grid", label: "Tiles", icon: "gridView" },
                      { value: "list", label: "List", icon: "listView" },
                    ]}
                  />
                </OptionRow>
                {prefs.layout === "grid" && (
                  <OptionRow label="Tile size">
                    <SegmentedControl
                      label="Tile size"
                      value={prefs.size}
                      onChange={(size) => setPrefs((p) => ({ ...p, size }))}
                      options={[
                        { value: "s", label: "S" },
                        { value: "m", label: "M" },
                        { value: "l", label: "L" },
                      ]}
                    />
                  </OptionRow>
                )}
                <OptionRow label="Sort by">
                  <select
                    className="input w-auto rounded-full py-1"
                    value={sort}
                    onChange={(e) => {
                      setPrefs((p) => ({ ...p, sort: e.target.value as SortKey }));
                      setState({ sort: null });
                    }}
                    aria-label="Sort devices"
                  >
                    {SORTS.map((s) => (
                      <option key={s.value} value={s.value}>
                        {s.label}
                      </option>
                    ))}
                  </select>
                </OptionRow>
                <div className="flex justify-end border-t border-outline pt-2">
                  <button
                    className="btn-text btn-sm"
                    onClick={() => {
                      setDisplayOpen(false);
                      void dir.reload();
                    }}
                  >
                    <Icon name="refresh" size={16} />
                    Refresh now
                  </button>
                </div>
              </Popover>
            )}
            <button
              className={`btn h-9 w-9 shrink-0 px-0 ${
                zen
                  ? "bg-primary-soft text-primary"
                  : "text-muted hover:bg-surface-2 hover:text-text"
              }`}
              aria-pressed={zen}
              aria-label="Zen mode"
              title={zen ? "Leave zen mode (Z)" : "Zen mode: screens only (Z)"}
              onClick={() => setPrefs((p) => ({ ...p, layout: "grid", zen: !zen }))}
            >
              <Icon name="zen" size={18} />
            </button>
            <Link
              to="/devices/add"
              className="btn-primary ml-1 h-9 shrink-0 px-3 sm:px-4"
              aria-label="Add device"
              title="Add device"
            >
              <Icon name="add" size={18} />
              <span className="hidden sm:inline">Add device</span>
            </Link>
          </div>
          {state.chips.size > 0 && (
            <div className="flex w-full flex-wrap gap-1.5" role="group" aria-label="Active filters">
              {CHIPS.filter((c) => state.chips.has(c.chip)).map(({ chip, label }) => (
                <button
                  key={chip}
                  type="button"
                  className="inline-flex items-center gap-1 rounded-full bg-primary-soft py-0.5 pr-1.5 pl-2.5 text-xs font-medium text-primary hover:brightness-110"
                  aria-label={`Remove filter ${label}`}
                  onClick={() => setState({ chips: toggleId(state.chips, chip) })}
                >
                  {label}
                  <Icon name="close" size={14} />
                </button>
              ))}
            </div>
          )}
        </div>
      )}
      {error && (
        <div className="mb-4">
          <ErrorBox error={error} onRetry={() => void dir.reload()} />
        </div>
      )}
      <div>
        <div className="min-w-0 space-y-4">
          {selected.size > 0 && (
            <div
              className="sticky top-2 z-20 flex flex-wrap items-center gap-2 rounded-full border border-primary bg-surface px-4 py-2 shadow-md"
              role="toolbar"
              aria-label="Selected devices"
            >
              <span className="text-sm font-medium">{selected.size} selected</span>
              <span className="mx-1 h-5 w-px bg-outline" />
              <button
                className="btn-text btn-sm"
                onClick={() => actions.dialog({ kind: "move", ids: selIds })}
              >
                <Icon name="folder" size={16} />
                Move to…
              </button>
              <button
                className="btn-text btn-sm"
                onClick={() => actions.dialog({ kind: "tags", ids: selIds })}
              >
                <Icon name="tag" size={16} />
                Tags…
              </button>
              <button
                className="btn-text btn-sm"
                onClick={() => void actions.setFavorite(selIds, true)}
              >
                <Icon name="star" size={16} />
                Favorite
              </button>
              <button
                className="btn-text btn-sm"
                onClick={() => void actions.setFavorite(selIds, false)}
              >
                <Icon name="starOutline" size={16} />
                Unfavorite
              </button>
              <span className="ml-auto flex gap-1">
                <button className="btn-text btn-sm" onClick={() => setSelected(new Set(order))}>
                  Select all {order.length}
                </button>
                <button
                  className="btn-text btn-sm"
                  onClick={() => setSelected(new Set())}
                  aria-label="Clear selection"
                >
                  <Icon name="close" size={16} />
                </button>
              </span>
            </div>
          )}

          {body}
        </div>
      </div>
    </>
  );
}
