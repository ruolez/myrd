import type { ITheme } from "@xterm/xterm";

// Dark like the session stage; the 16 colors are tuned for contrast on it
// (the app's tag palette, dark variants).
export const TERMINAL_THEME: ITheme = {
  background: "#131314",
  foreground: "#e3e3e3",
  cursor: "#8ab4f8",
  cursorAccent: "#131314",
  selectionBackground: "#8ab4f866",
  selectionInactiveBackground: "#8ab4f833",
  scrollbarSliderBackground: "#ffffff22",
  scrollbarSliderHoverBackground: "#ffffff33",
  scrollbarSliderActiveBackground: "#ffffff44",
  black: "#1e1f20",
  red: "#f28b82",
  green: "#81c995",
  yellow: "#fdd663",
  blue: "#8ab4f8",
  magenta: "#c58af9",
  cyan: "#78d9ec",
  white: "#e3e3e3",
  brightBlack: "#5f6368",
  brightRed: "#f6aea9",
  brightGreen: "#a8dab5",
  brightYellow: "#fde293",
  brightBlue: "#aecbfa",
  brightMagenta: "#d7aefb",
  brightCyan: "#a1e4f2",
  brightWhite: "#ffffff",
};

const LIGHT: ITheme = {
  background: "#fbfbfb",
  foreground: "#1f1f1f",
  cursor: "#1a73e8",
  cursorAccent: "#fbfbfb",
  selectionBackground: "#1a73e844",
  selectionInactiveBackground: "#1a73e822",
  scrollbarSliderBackground: "#00000022",
  scrollbarSliderHoverBackground: "#00000033",
  scrollbarSliderActiveBackground: "#00000044",
  black: "#1f1f1f",
  red: "#c5221f",
  green: "#137333",
  yellow: "#a05a00",
  blue: "#1a5fb4",
  magenta: "#8430ce",
  cyan: "#007b83",
  white: "#9aa0a6",
  brightBlack: "#5f6368",
  brightRed: "#d93025",
  brightGreen: "#188038",
  brightYellow: "#b06000",
  brightBlue: "#1a73e8",
  brightMagenta: "#9334e6",
  brightCyan: "#12858f",
  brightWhite: "#3c4043",
};

const SOLARIZED: ITheme = {
  background: "#002b36",
  foreground: "#93a1a1",
  cursor: "#93a1a1",
  cursorAccent: "#002b36",
  selectionBackground: "#586e7566",
  selectionInactiveBackground: "#586e7533",
  scrollbarSliderBackground: "#ffffff22",
  scrollbarSliderHoverBackground: "#ffffff33",
  scrollbarSliderActiveBackground: "#ffffff44",
  black: "#073642",
  red: "#dc322f",
  green: "#859900",
  yellow: "#b58900",
  blue: "#268bd2",
  magenta: "#d33682",
  cyan: "#2aa198",
  white: "#eee8d5",
  brightBlack: "#586e75",
  brightRed: "#cb4b16",
  brightGreen: "#9aad00",
  brightYellow: "#d4a400",
  brightBlue: "#839496",
  brightMagenta: "#6c71c4",
  brightCyan: "#93a1a1",
  brightWhite: "#fdf6e3",
};

export type TermThemeId = "dark" | "light" | "solarized";

export const TERMINAL_THEMES: Record<TermThemeId, { name: string; theme: ITheme }> = {
  dark: { name: "Dark", theme: TERMINAL_THEME },
  light: { name: "Light", theme: LIGHT },
  solarized: { name: "Solarized", theme: SOLARIZED },
};

const ORDER: TermThemeId[] = ["dark", "light", "solarized"];

/** The theme after id, round and round (the menu's Theme item). */
export const nextTheme = (id: TermThemeId): TermThemeId =>
  ORDER[(ORDER.indexOf(id) + 1) % ORDER.length]!;

export const parseThemeId = (v: string | undefined): TermThemeId =>
  v === "light" || v === "solarized" ? v : "dark";

export const TERMINAL_FONT =
  'ui-monospace, "SF Mono", SFMono-Regular, Menlo, "Cascadia Mono", Consolas, "Roboto Mono", "DejaVu Sans Mono", monospace';
