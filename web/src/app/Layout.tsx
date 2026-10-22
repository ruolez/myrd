import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation, useMatches, useNavigate } from "react-router";
import { Icon } from "../components/icons";
import type { IconName } from "../components/icons";
import { Menu } from "../components/menu";
import { RailTip } from "../components/RailTip";
import { Brand, ErrorBox } from "../components/ui";
import { DEFAULT_SIDEBAR, isSidebarToggle, navMode, parseSidebarPrefs } from "../lib/nav";
import type { NavMode, SidebarPrefs } from "../lib/nav";
import { signaling } from "../lib/signaling";
import type { ConnState } from "../lib/signaling";
import { useAuth, useUser } from "./auth";
import { usePalette } from "./palette";
import { DeviceViewsNav } from "../pages/devices/DashboardPane";
import { SessionCount, SessionDock } from "./SessionDock";
import { useTheme } from "./theme";

type NavItem = { to: string; label: string; icon: IconName; end?: boolean };

const devicesItem: NavItem = { to: "/devices", label: "Devices", icon: "devices" };
const activityItem: NavItem = { to: "/activity", label: "Activity", icon: "activity" };

const adminNav: { label?: string; items: NavItem[] }[] = [
  { items: [{ to: "/admin", label: "Overview", icon: "dashboard", end: true }] },
  {
    label: "Manage",
    items: [
      { to: "/admin/users", label: "Users", icon: "users" },
      { to: "/admin/devices", label: "All devices", icon: "devices" },
    ],
  },
  {
    label: "Monitor",
    items: [
      { to: "/admin/sessions", label: "Sessions", icon: "sessions" },
      { to: "/admin/audit", label: "Audit log", icon: "audit" },
    ],
  },
  {
    label: "Platform",
    items: [
      { to: "/admin/releases", label: "Releases", icon: "release" },
      { to: "/admin/settings", label: "System settings", icon: "settings" },
    ],
  },
];

const modes: { mode: NavMode; label: string; icon: IconName }[] = [
  { mode: "workspace", label: "Workspace", icon: "person" },
  { mode: "admin", label: "Admin", icon: "admin" },
];

/** Where each half of the app was left, so switching back returns there. */
const lastPath = new Map<NavMode, string>([
  ["workspace", "/devices"],
  ["admin", "/admin"],
]);

const SIDEBAR_KEY = "remotectl-sidebar";

function loadPrefs(): SidebarPrefs {
  try {
    return parseSidebarPrefs(localStorage.getItem(SIDEBAR_KEY));
  } catch {
    return DEFAULT_SIDEBAR;
  }
}

function useSidebarPrefs() {
  const [prefs, setPrefs] = useState(loadPrefs);
  const update = useCallback((patch: (p: SidebarPrefs) => Partial<SidebarPrefs>) => {
    setPrefs((p) => {
      const next = { ...p, ...patch(p) };
      try {
        localStorage.setItem(SIDEBAR_KEY, JSON.stringify(next));
      } catch {
        // storage unavailable
      }
      return next;
    });
  }, []);
  return [prefs, update] as const;
}

function useSignalState(): ConnState {
  const [s, setS] = useState<ConnState>(signaling.state);
  useEffect(() => signaling.onState(setS), []);
  return s;
}

const isMac = typeof navigator !== "undefined" && navigator.platform.startsWith("Mac");

function NavRow({
  item,
  compact,
  onNavigate,
  quiet,
  trailing,
  children,
}: {
  item: NavItem;
  compact: boolean;
  onNavigate: () => void;
  /** Active, but a sub-item below carries the highlight. */
  quiet?: boolean;
  trailing?: ReactNode;
  children?: ReactNode;
}) {
  const link = (
    <NavLink
      to={item.to}
      end={item.end}
      onClick={onNavigate}
      aria-label={compact ? item.label : undefined}
      className={({ isActive }) =>
        `flex items-center gap-3 rounded-full text-sm font-medium transition-colors ${
          compact ? "mx-auto h-10 w-10 justify-center" : `px-4 py-2 ${trailing ? "pr-10" : ""}`
        } ${
          isActive
            ? quiet && !compact
              ? "text-primary hover:bg-surface-2"
              : "bg-primary-soft text-primary"
            : "text-text hover:bg-surface-2"
        }`
      }
    >
      <Icon name={item.icon} size={20} className="shrink-0" />
      {!compact && <span className="truncate">{item.label}</span>}
    </NavLink>
  );
  return (
    <li className="relative">
      <RailTip show={compact} label={item.label}>
        {link}
      </RailTip>
      {!compact && trailing}
      {!compact && children}
    </li>
  );
}

function GroupLabel({ compact, children }: { compact: boolean; children: ReactNode }) {
  if (compact) return <div className="mx-auto my-2 h-px w-8 bg-outline" role="presentation" />;
  return (
    <div className="mb-1 px-4 pt-1 text-[11px] font-medium tracking-wide text-muted uppercase">
      {children}
    </div>
  );
}

/** Admins move between their own workspace and the organization's admin console. */
function ModeSwitch({ mode, compact }: { mode: NavMode; compact: boolean }) {
  return (
    <nav
      aria-label="Area"
      className={`rounded-full bg-surface-2 p-1 ${compact ? "mx-auto flex w-fit flex-col gap-1" : "grid grid-cols-2 gap-1"}`}
    >
      {modes.map((m) => {
        const active = m.mode === mode;
        return (
          <RailTip
            key={m.mode}
            show={compact}
            label={m.mode === "admin" ? "Admin console" : "My workspace"}
          >
            <Link
              to={lastPath.get(m.mode) ?? "/"}
              aria-current={active ? "true" : undefined}
              aria-label={compact ? m.label : undefined}
              className={`flex items-center justify-center gap-1.5 rounded-full text-xs font-medium transition-colors ${
                compact ? "h-8 w-8" : "py-1.5"
              } ${active ? "bg-surface text-text shadow-sm" : "text-muted hover:text-text"}`}
            >
              <Icon name={m.icon} size={16} className={active ? "text-primary" : ""} />
              {!compact && m.label}
            </Link>
          </RailTip>
        );
      })}
    </nav>
  );
}

function SearchButton({ compact }: { compact: boolean }) {
  const palette = usePalette();
  const keys = isMac ? "⌘K" : "Ctrl K";
  if (compact)
    return (
      <RailTip show label={`Search · ${keys}`}>
        <button
          type="button"
          className="mx-auto flex h-10 w-10 items-center justify-center rounded-full text-muted hover:bg-surface-2 hover:text-text"
          aria-label="Search"
          onClick={palette.open}
        >
          <Icon name="search" size={20} />
        </button>
      </RailTip>
    );
  return (
    <button
      type="button"
      className="flex w-full items-center gap-2 rounded-full border border-outline px-3 py-1.5 text-sm text-muted transition-colors hover:bg-surface-2 hover:text-text"
      onClick={palette.open}
    >
      <Icon name="search" size={18} />
      <span className="flex-1 truncate text-left">Search…</span>
      <kbd className="rounded border border-outline px-1.5 font-sans text-[11px]">{keys}</kbd>
    </button>
  );
}

function Account({ compact, sig }: { compact: boolean; sig: ConnState }) {
  const user = useUser();
  const { logout } = useAuth();
  const [theme, setTheme] = useTheme();
  const navigate = useNavigate();
  const onSettings = useLocation().pathname === "/settings";
  const status = sig === "open" ? "Connected" : sig === "connecting" ? "Connecting…" : "Offline";
  const dot = sig === "open" ? "bg-success" : sig === "connecting" ? "bg-warning" : "bg-error";
  return (
    <Menu
      label="Account"
      title={`${user.email} · ${status}`}
      align="start"
      className={`flex items-center gap-3 text-left transition-colors hover:bg-surface-2 ${
        compact ? "mx-auto rounded-full p-1" : "w-full rounded-2xl p-2"
      } ${onSettings ? "bg-surface-2" : ""}`}
      items={[
        { label: "Account settings", icon: "settings", onSelect: () => navigate("/settings") },
        "separator",
        { label: "System theme", checked: theme === "system", onSelect: () => setTheme("system") },
        { label: "Light theme", checked: theme === "light", onSelect: () => setTheme("light") },
        { label: "Dark theme", checked: theme === "dark", onSelect: () => setTheme("dark") },
        "separator",
        { label: "Sign out", icon: "logout", onSelect: () => void logout() },
      ]}
    >
      <span
        className="relative flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-sm font-semibold text-on-primary"
        style={{ background: "linear-gradient(135deg, var(--c-brand-from), var(--c-brand-to))" }}
      >
        {user.email.slice(0, 1).toUpperCase()}
        <span
          className={`absolute -right-0.5 -bottom-0.5 h-3 w-3 rounded-full ring-2 ring-surface ${dot}`}
        />
      </span>
      {!compact && (
        <>
          <span className="min-w-0 flex-1">
            <span className="block truncate text-sm font-medium text-text">{user.email}</span>
            <span className="block truncate text-xs text-muted">
              {user.is_admin ? "Administrator · " : ""}
              {status}
            </span>
          </span>
          <Icon name="unfoldMore" size={18} className="shrink-0 text-muted" />
        </>
      )}
    </Menu>
  );
}

function Sidebar({
  compact,
  prefs,
  setPrefs,
  onToggle,
  onNavigate,
  drawer = false,
}: {
  compact: boolean;
  prefs: SidebarPrefs;
  setPrefs: (patch: (p: SidebarPrefs) => Partial<SidebarPrefs>) => void;
  /** Collapses the desktop sidebar, or closes the mobile drawer. */
  onToggle: () => void;
  onNavigate: () => void;
  drawer?: boolean;
}) {
  const user = useUser();
  const loc = useLocation();
  const sig = useSignalState();
  const mode = user.is_admin ? navMode(loc.pathname) : "workspace";
  const toggleLabel = drawer
    ? "Close menu"
    : `${compact ? "Expand" : "Collapse"} sidebar (${isMac ? "⌘" : "Ctrl+"}B)`;
  const toggle = (
    <button
      type="button"
      className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-muted hover:bg-surface-2 hover:text-text"
      aria-label={toggleLabel}
      title={compact ? undefined : toggleLabel}
      aria-expanded={drawer ? undefined : !compact}
      onClick={onToggle}
    >
      <Icon
        name={drawer ? "close" : "menuOpen"}
        size={20}
        className={compact ? "-scale-x-100" : ""}
      />
    </button>
  );

  return (
    <div className={`flex h-full flex-col ${compact ? "w-[4.5rem]" : drawer ? "w-72" : "w-68"}`}>
      <div className={`space-y-3 px-3 pt-3 pb-2 ${compact ? "flex flex-col items-center" : ""}`}>
        {compact ? (
          <>
            <Link to="/devices" aria-label="Devices" className="p-1.5">
              <img src="/favicon.svg" alt="" width={30} height={30} />
            </Link>
            <RailTip show label={toggleLabel}>
              {toggle}
            </RailTip>
          </>
        ) : (
          <div className="flex items-center justify-between gap-2 pl-2">
            <Link to="/devices" className="min-w-0">
              <Brand size={28} />
            </Link>
            {toggle}
          </div>
        )}
        <SearchButton compact={compact} />
        {user.is_admin && <ModeSwitch mode={mode} compact={compact} />}
      </div>

      <nav
        aria-label={mode === "admin" ? "Admin console" : "Main"}
        className="flex-1 space-y-4 overflow-x-hidden overflow-y-auto px-3 py-2"
      >
        {mode === "admin" ? (
          adminNav.map((g, i) => (
            <div key={g.label ?? i}>
              {g.label && <GroupLabel compact={compact}>{g.label}</GroupLabel>}
              <ul className="space-y-0.5">
                {g.items.map((it) => (
                  <NavRow key={it.to} item={it} compact={compact} onNavigate={onNavigate} />
                ))}
              </ul>
            </div>
          ))
        ) : (
          <ul className="space-y-0.5">
            <NavRow
              item={devicesItem}
              compact={compact}
              onNavigate={onNavigate}
              quiet={prefs.devices && loc.pathname === "/devices"}
              trailing={
                <button
                  type="button"
                  className="absolute top-1 right-1 flex h-8 w-8 items-center justify-center rounded-full text-muted hover:bg-surface-2 hover:text-text"
                  aria-label={prefs.devices ? "Hide device views" : "Show device views"}
                  title={prefs.devices ? "Hide device views" : "Show device views"}
                  aria-expanded={prefs.devices}
                  onClick={() => setPrefs((p) => ({ devices: !p.devices }))}
                >
                  <Icon
                    name="chevronDown"
                    size={18}
                    className={`transition-transform motion-reduce:transition-none ${prefs.devices ? "" : "-rotate-90"}`}
                  />
                </button>
              }
            >
              {prefs.devices && (
                <div className="mt-1 mb-3 ml-[1.625rem] border-l border-outline pl-1.5">
                  <DeviceViewsNav onNavigate={onNavigate} />
                </div>
              )}
            </NavRow>
            <NavRow item={activityItem} compact={compact} onNavigate={onNavigate} />
          </ul>
        )}

        <div className="border-t border-outline pt-3 empty:hidden">
          <SessionDock
            compact={compact}
            open={prefs.sessions}
            onToggle={() => setPrefs((p) => ({ sessions: !p.sessions }))}
          />
        </div>
      </nav>

      <div className={`border-t border-outline p-2 ${compact ? "flex justify-center" : ""}`}>
        <Account compact={compact} sig={sig} />
      </div>
    </div>
  );
}

export function Layout() {
  const { keyError } = useAuth();
  const [open, setOpen] = useState(false);
  const [prefs, setPrefs] = useSidebarPrefs();
  const loc = useLocation();
  const wide = useMatches().some((m) => (m.handle as { wide?: boolean } | undefined)?.wide);

  useEffect(() => setOpen(false), [loc.pathname]);
  useEffect(() => {
    lastPath.set(navMode(loc.pathname), loc.pathname + loc.search);
  }, [loc.pathname, loc.search]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isSidebarToggle(e) || !window.matchMedia?.("(min-width: 1024px)").matches) return;
      e.preventDefault();
      setPrefs((p) => ({ collapsed: !p.collapsed }));
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [setPrefs]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  const closeDrawer = () => setOpen(false);

  return (
    <div className="flex min-h-full">
      <aside
        className={`sticky top-0 hidden h-screen shrink-0 overflow-hidden border-r border-outline bg-surface transition-[width] duration-200 ease-out motion-reduce:transition-none lg:block ${
          prefs.collapsed ? "w-[4.5rem]" : "w-68"
        }`}
      >
        <Sidebar
          compact={prefs.collapsed}
          prefs={prefs}
          setPrefs={setPrefs}
          onToggle={() => setPrefs((p) => ({ collapsed: !p.collapsed }))}
          onNavigate={() => undefined}
        />
      </aside>
      {open && (
        <div className="fixed inset-0 z-40 lg:hidden" onClick={closeDrawer}>
          <div className="absolute inset-0 bg-black/40" />
          <aside
            className="absolute top-0 left-0 h-full overflow-hidden bg-surface shadow-xl"
            onClick={(e) => e.stopPropagation()}
          >
            <Sidebar
              compact={false}
              drawer
              prefs={prefs}
              setPrefs={setPrefs}
              onToggle={closeDrawer}
              onNavigate={closeDrawer}
            />
          </aside>
        </div>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex items-center gap-2 border-b border-outline bg-surface px-4 py-2 lg:hidden">
          <button className="btn-text btn-sm" aria-label="Open menu" onClick={() => setOpen(true)}>
            <Icon name="menu" />
          </button>
          <Brand size={24} className="text-base" />
          <SessionCount />
        </header>
        <main
          className={`mx-auto w-full flex-1 px-4 py-6 sm:px-6 lg:py-8 ${wide ? "max-w-[110rem]" : "max-w-6xl"}`}
        >
          {keyError && (
            <div className="mb-4">
              <ErrorBox error={keyError} />
            </div>
          )}
          <Outlet />
        </main>
      </div>
    </div>
  );
}
