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

export const TERMINAL_FONT =
  'ui-monospace, "SF Mono", SFMono-Regular, Menlo, "Cascadia Mono", Consolas, "Roboto Mono", "DejaVu Sans Mono", monospace';
