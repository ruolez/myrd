import { createContext, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";

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

export function applyTheme(pref: ThemePref = readPref()) {
  const dark = pref === "dark" || (pref === "system" && !!media()?.matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
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
