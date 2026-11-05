// Installed-app (PWA) helpers that don't touch the browser: which install
// instructions fit this browser, and the window color for each view.

export type InstallHint =
  | "installed"
  | "prompt"
  | "ios-share"
  | "mac-safari-dock"
  | "android-menu"
  | "chromium-menu"
  | "unsupported";

export type InstallEnv = {
  /** Running as the installed app (display-mode standalone or navigator.standalone). */
  standalone: boolean;
  /** The browser fired beforeinstallprompt and the event is still usable. */
  canPrompt: boolean;
  /** navigator.maxTouchPoints; iPadOS reports a Mac user agent but has touch. */
  maxTouchPoints: number;
};

export function installHint(ua: string, env: InstallEnv): InstallHint {
  if (env.standalone) return "installed";
  if (env.canPrompt) return "prompt";
  const ipadOS = /Macintosh/.test(ua) && env.maxTouchPoints > 1;
  if (/iPhone|iPad|iPod/.test(ua) || ipadOS) return "ios-share";
  if (/Android/.test(ua)) return "android-menu";
  if (/Firefox\//.test(ua)) return "unsupported";
  if (/Chrome\/|Chromium\/|Edg\//.test(ua)) return "chromium-menu";
  if (/Macintosh/.test(ua) && /Version\/[\d.]+.*Safari\//.test(ua)) return "mac-safari-dock";
  return "unsupported";
}

export type ThemeName = "light" | "dark";
export type ChromeView = "app" | "session";

// --c-surface per theme; the session toolbar is #161718 in both themes.
const APP_COLORS: Record<ThemeName, string> = { light: "#ffffff", dark: "#1e1f20" };
const SESSION_COLOR = "#161718";

export function themeColorFor(theme: ThemeName, view: ChromeView): string {
  return view === "session" ? SESSION_COLOR : APP_COLORS[theme];
}
