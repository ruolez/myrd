import { createContext, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { themeColorFor } from "../lib/pwa";
import type { ChromeView } from "../lib/pwa";

export type ThemePref = "system" | "light" | "dark";
const KEY = "remotectl-theme";

function readPref(): ThemePref {
  try {
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

const media = () => window.matchMedia?.("(prefers-color-scheme: dark)");

let chromeView: ChromeView = "app";

export function applyTheme(pref: ThemePref = readPref()) {
  const dark = pref === "dark" || (pref === "system" && !!media()?.matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  syncThemeColor();
}

/** The browser / installed-app window color: the page surface, or the session toolbar. */
export function setChromeView(view: ChromeView) {
  chromeView = view;
  syncThemeColor();
}

function syncThemeColor() {
  const theme = document.documentElement.dataset.theme === "dark" ? "dark" : "light";
  document
    .querySelector('meta[name="theme-color"]')
    ?.setAttribute("content", themeColorFor(theme, chromeView));
}

const Ctx = createContext<[ThemePref, (p: ThemePref) => void]>(["system", () => undefined]);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [pref, setPref] = useState<ThemePref>(readPref);
  useEffect(() => {
    applyTheme(pref);
    try {
      if (pref === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, pref);
    } catch {
      // storage unavailable
    }
    const m = media();
    if (pref !== "system" || !m) return;
    const on = () => applyTheme("system");
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, [pref]);
  return <Ctx.Provider value={[pref, setPref]}>{children}</Ctx.Provider>;
}

export const useTheme = () => useContext(Ctx);
