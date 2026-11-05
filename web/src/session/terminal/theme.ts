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

/** A theme from a background, a foreground, an accent and the 16 colors. */
function scheme(
  background: string,
  foreground: string,
  accent: string,
  ansi: readonly string[],
  dark = true,
): ITheme {
  const [black, red, green, yellow, blue, magenta, cyan, white] = ansi;
  const [bBlack, bRed, bGreen, bYellow, bBlue, bMagenta, bCyan, bWhite] = ansi.slice(8);
  const ink = dark ? "#ffffff" : "#000000";
  return {
    background,
    foreground,
    cursor: accent,
    cursorAccent: background,
    selectionBackground: `${accent}55`,
    selectionInactiveBackground: `${accent}2a`,
    scrollbarSliderBackground: `${ink}22`,
    scrollbarSliderHoverBackground: `${ink}33`,
    scrollbarSliderActiveBackground: `${ink}44`,
    black,
    red,
    green,
    yellow,
    blue,
    magenta,
    cyan,
    white,
    brightBlack: bBlack,
    brightRed: bRed,
    brightGreen: bGreen,
    brightYellow: bYellow,
    brightBlue: bBlue,
    brightMagenta: bMagenta,
    brightCyan: bCyan,
    brightWhite: bWhite,
  };
}

// Widely used open color schemes (their published palettes).
// prettier-ignore
const SOLARIZED_LIGHT = scheme(
  "#fdf6e3",
  "#586e75",
  "#268bd2",
  [
    "#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5",
    "#002b36", "#cb4b16", "#586e75", "#657b83", "#839496", "#6c71c4", "#93a1a1", "#fdf6e3",
  ],
  false,
);
// prettier-ignore
const DRACULA = scheme("#282a36", "#f8f8f2", "#bd93f9", [
  "#21222c", "#ff5555", "#50fa7b", "#f1fa8c", "#bd93f9", "#ff79c6", "#8be9fd", "#f8f8f2",
  "#6272a4", "#ff6e6e", "#69ff94", "#ffffa5", "#d6acff", "#ff92df", "#a4ffff", "#ffffff",
]);
// prettier-ignore
const NORD = scheme("#2e3440", "#d8dee9", "#88c0d0", [
  "#3b4252", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#88c0d0", "#e5e9f0",
  "#4c566a", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#8fbcbb", "#eceff4",
]);
// prettier-ignore
const GRUVBOX = scheme("#282828", "#ebdbb2", "#fabd2f", [
  "#282828", "#cc241d", "#98971a", "#d79921", "#458588", "#b16286", "#689d6a", "#a89984",
  "#928374", "#fb4934", "#b8bb26", "#fabd2f", "#83a598", "#d3869b", "#8ec07c", "#ebdbb2",
]);
// prettier-ignore
const ONE_DARK = scheme("#282c34", "#abb2bf", "#61afef", [
  "#282c34", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#abb2bf",
  "#5c6370", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#ffffff",
]);
// prettier-ignore
const MONOKAI = scheme("#272822", "#f8f8f2", "#f8f8f0", [
  "#272822", "#f92672", "#a6e22e", "#f4bf75", "#66d9ef", "#ae81ff", "#a1efe4", "#f8f8f2",
  "#75715e", "#f92672", "#a6e22e", "#f4bf75", "#66d9ef", "#ae81ff", "#a1efe4", "#f9f8f5",
]);
// prettier-ignore
const TOKYO_NIGHT = scheme("#1a1b26", "#c0caf5", "#7aa2f7", [
  "#15161e", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6",
  "#414868", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#c0caf5",
]);
// prettier-ignore
const HIGH_CONTRAST = scheme("#000000", "#ffffff", "#ffff00", [
  "#000000", "#ff5f5f", "#5fff5f", "#ffff5f", "#5fafff", "#ff5fff", "#5fffff", "#e4e4e4",
  "#808080", "#ff8787", "#87ff87", "#ffff87", "#87d7ff", "#ff87ff", "#87ffff", "#ffffff",
]);

export const TERMINAL_THEMES = {
  dark: { name: "Dark", theme: TERMINAL_THEME },
  light: { name: "Light", theme: LIGHT },
  solarized: { name: "Solarized Dark", theme: SOLARIZED },
  "solarized-light": { name: "Solarized Light", theme: SOLARIZED_LIGHT },
  dracula: { name: "Dracula", theme: DRACULA },
  nord: { name: "Nord", theme: NORD },
  gruvbox: { name: "Gruvbox", theme: GRUVBOX },
  "one-dark": { name: "One Dark", theme: ONE_DARK },
  monokai: { name: "Monokai", theme: MONOKAI },
  "tokyo-night": { name: "Tokyo Night", theme: TOKYO_NIGHT },
  "high-contrast": { name: "High contrast", theme: HIGH_CONTRAST },
} satisfies Record<string, { name: string; theme: ITheme }>;

export type TermThemeId = keyof typeof TERMINAL_THEMES;

export const parseThemeId = (v: string | undefined): TermThemeId =>
  v !== undefined && Object.hasOwn(TERMINAL_THEMES, v) ? (v as TermThemeId) : "dark";

export const TERMINAL_FONT =
  'ui-monospace, "SF Mono", SFMono-Regular, Menlo, "Cascadia Mono", Consolas, "Roboto Mono", "DejaVu Sans Mono", monospace';

/** Fonts the appearance panel offers, where installed ("system" = the stack above). */
export const TERMINAL_FONTS = [
  { id: "system", name: "System", family: TERMINAL_FONT },
  { id: "sf-mono", name: "SF Mono", family: '"SF Mono", SFMono-Regular, monospace' },
  { id: "menlo", name: "Menlo", family: "Menlo, monospace" },
  { id: "monaco", name: "Monaco", family: "Monaco, monospace" },
  { id: "cascadia", name: "Cascadia Mono", family: '"Cascadia Mono", "Cascadia Code", monospace' },
  { id: "consolas", name: "Consolas", family: "Consolas, monospace" },
  { id: "jetbrains", name: "JetBrains Mono", family: '"JetBrains Mono", monospace' },
  { id: "fira", name: "Fira Code", family: '"Fira Code", "Fira Mono", monospace' },
  { id: "dejavu", name: "DejaVu Sans Mono", family: '"DejaVu Sans Mono", monospace' },
  { id: "ubuntu", name: "Ubuntu Mono", family: '"Ubuntu Mono", monospace' },
  { id: "courier", name: "Courier New", family: '"Courier New", Courier, monospace' },
] as const;

export type TermFontId = (typeof TERMINAL_FONTS)[number]["id"];

export const fontFamily = (id: TermFontId): string =>
  TERMINAL_FONTS.find((f) => f.id === id)?.family ?? TERMINAL_FONT;
