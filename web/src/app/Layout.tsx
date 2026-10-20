import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router";
import { Icon } from "../components/icons";
import type { IconName } from "../components/icons";
import { Brand, ErrorBox } from "../components/ui";
import { signaling } from "../lib/signaling";
import type { ConnState } from "../lib/signaling";
import { useAuth, useUser } from "./auth";
import { useTheme } from "./theme";

type NavItem = { to: string; label: string; icon: IconName; end?: boolean };

const mainNav: NavItem[] = [
  { to: "/devices", label: "Devices", icon: "devices" },
  { to: "/activity", label: "Activity", icon: "activity" },
  { to: "/settings", label: "Settings", icon: "settings" },
];

const adminNav: NavItem[] = [
  { to: "/admin", label: "Overview", icon: "dashboard", end: true },
  { to: "/admin/users", label: "Users", icon: "users" },
  { to: "/admin/devices", label: "All devices", icon: "devices" },
  { to: "/admin/sessions", label: "Sessions", icon: "sessions" },
  { to: "/admin/audit", label: "Audit log", icon: "audit" },
  { to: "/admin/releases", label: "Releases", icon: "release" },
  { to: "/admin/settings", label: "System", icon: "settings" },
];

function useSignalState(): ConnState {
  const [s, setS] = useState<ConnState>(signaling.state);
  useEffect(() => signaling.onState(setS), []);
  return s;
}

function NavList({ items, onNavigate }: { items: NavItem[]; onNavigate: () => void }) {
  return (
    <ul className="space-y-0.5">
      {items.map((it) => (
        <li key={it.to}>
          <NavLink
            to={it.to}
            end={it.end}
            onClick={onNavigate}
            className={({ isActive }) =>
              `flex items-center gap-3 rounded-full px-4 py-2 text-sm font-medium transition-colors ${
                isActive ? "bg-primary-soft text-primary" : "text-text hover:bg-surface-2"
              }`
            }
          >
            <Icon name={it.icon} size={20} />
            {it.label}
          </NavLink>
        </li>
      ))}
    </ul>
  );
}

export function Layout() {
  const user = useUser();
  const { logout, keyError } = useAuth();
  const [theme, setTheme] = useTheme();
  const [open, setOpen] = useState(false);
  const sig = useSignalState();
  const loc = useLocation();

  useEffect(() => setOpen(false), [loc.pathname]);

  const isDark =
    theme === "dark" ||
    (theme === "system" && !!window.matchMedia?.("(prefers-color-scheme: dark)").matches);

  const sidebar = (
    <nav className="flex h-full flex-col gap-6 p-3" aria-label="Main">
      <div className="px-3 pt-2">
        <Brand size={30} />
      </div>
      <NavList items={mainNav} onNavigate={() => setOpen(false)} />
      {user.is_admin && (
        <div>
          <div className="mb-1 px-4 text-xs font-medium tracking-wide text-muted uppercase">
            Admin
          </div>
          <NavList items={adminNav} onNavigate={() => setOpen(false)} />
        </div>
      )}
      <div className="mt-auto space-y-2 px-2 pb-2">
        <div
          className="flex items-center gap-2 px-2 text-xs text-muted"
          title="Signaling connection"
        >
          <span
            className={`h-2 w-2 rounded-full ${
              sig === "open" ? "bg-success" : sig === "connecting" ? "bg-warning" : "bg-error"
            }`}
          />
          {sig === "open" ? "Connected" : sig === "connecting" ? "Connecting…" : "Offline"}
        </div>
        <div className="truncate px-2 text-sm" title={user.email}>
          {user.email}
        </div>
        <div className="flex gap-1">
          <button
            className="btn-text btn-sm"
            onClick={() => setTheme(isDark ? "light" : "dark")}
            aria-label="Toggle theme"
            title="Toggle theme"
          >
            <Icon name={isDark ? "sun" : "moon"} size={18} />
          </button>
          <button className="btn-text btn-sm" onClick={() => void logout()}>
            <Icon name="logout" size={18} />
            Sign out
          </button>
        </div>
      </div>
    </nav>
  );

  return (
    <div className="flex min-h-full">
      <aside className="sticky top-0 hidden h-screen w-64 shrink-0 border-r border-outline bg-surface lg:block">
        {sidebar}
      </aside>
      {open && (
        <div className="fixed inset-0 z-40 lg:hidden" onClick={() => setOpen(false)}>
          <div className="absolute inset-0 bg-black/40" />
          <aside
            className="absolute top-0 left-0 h-full w-72 overflow-auto bg-surface shadow-xl"
            onClick={(e) => e.stopPropagation()}
          >
            {sidebar}
          </aside>
        </div>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex items-center gap-2 border-b border-outline bg-surface px-4 py-2 lg:hidden">
          <button className="btn-text btn-sm" aria-label="Open menu" onClick={() => setOpen(true)}>
            <Icon name="menu" />
          </button>
          <Brand size={24} className="text-base" />
        </header>
        <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6 sm:px-6 lg:py-8">
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
