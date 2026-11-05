import { describe, expect, test } from "vitest";
import { installHint, themeColorFor } from "./pwa";
import type { InstallEnv } from "./pwa";

const UA = {
  iphoneSafari:
    "Mozilla/5.0 (iPhone; CPU iPhone OS 19_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.1 Mobile/15E148 Safari/604.1",
  iphoneChrome:
    "Mozilla/5.0 (iPhone; CPU iPhone OS 19_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/142.0.7444.0 Mobile/15E148 Safari/604.1",
  ipadOS:
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.1 Safari/605.1.15",
  macSafari:
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.1 Safari/605.1.15",
  macChrome:
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
  winEdge:
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 Edg/142.0.0.0",
  winFirefox: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:145.0) Gecko/20100101 Firefox/145.0",
  androidChrome:
    "Mozilla/5.0 (Linux; Android 16; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Mobile Safari/537.36",
  androidFirefox: "Mozilla/5.0 (Android 16; Mobile; rv:145.0) Gecko/145.0 Firefox/145.0",
};

const browser: InstallEnv = { standalone: false, canPrompt: false, maxTouchPoints: 0 };

describe("installHint", () => {
  test.each([
    ["iPhone Safari", UA.iphoneSafari, { maxTouchPoints: 5 }, "ios-share"],
    ["iPhone Chrome", UA.iphoneChrome, { maxTouchPoints: 5 }, "ios-share"],
    ["iPadOS (desktop UA, touch)", UA.ipadOS, { maxTouchPoints: 5 }, "ios-share"],
    ["macOS Safari", UA.macSafari, {}, "mac-safari-dock"],
    ["macOS Chrome without a prompt", UA.macChrome, {}, "chromium-menu"],
    ["macOS Chrome with a prompt", UA.macChrome, { canPrompt: true }, "prompt"],
    ["Windows Edge without a prompt", UA.winEdge, {}, "chromium-menu"],
    ["Windows Firefox", UA.winFirefox, {}, "unsupported"],
    ["Android Chrome without a prompt", UA.androidChrome, { maxTouchPoints: 5 }, "android-menu"],
    ["Android Firefox", UA.androidFirefox, { maxTouchPoints: 5 }, "android-menu"],
    [
      "installed app, even with a prompt",
      UA.macChrome,
      { standalone: true, canPrompt: true },
      "installed",
    ],
    ["installed on iPhone", UA.iphoneSafari, { standalone: true, maxTouchPoints: 5 }, "installed"],
    ["unknown browser", "curl/8.10", {}, "unsupported"],
  ] as const)("%s → %s", (_name, ua, env, want) => {
    expect(installHint(ua, { ...browser, ...env })).toBe(want);
  });
});

describe("themeColorFor", () => {
  test.each([
    ["light", "app", "#ffffff"],
    ["dark", "app", "#1e1f20"],
    ["light", "session", "#161718"],
    ["dark", "session", "#161718"],
  ] as const)("%s %s → %s", (theme, view, want) => {
    expect(themeColorFor(theme, view)).toBe(want);
  });
});
