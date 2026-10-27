import { describe, expect, test } from "vitest";
import { detectInstallOS } from "./installLink";

const ua = {
  win11Chrome:
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
  win7Chrome:
    "Mozilla/5.0 (Windows NT 6.1; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/109.0.0.0 Safari/537.36",
  win7Firefox: "Mozilla/5.0 (Windows NT 6.1; Win64; x64; rv:115.0) Gecko/20100101 Firefox/115.0",
  win81Edge:
    "Mozilla/5.0 (Windows NT 6.3; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/109.0.0.0 Safari/537.36 Edg/109.0.1518.140",
  macSafari:
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
  iPhone:
    "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
  android:
    "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36",
  linux: "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0",
};

describe("detectInstallOS", () => {
  test.each([
    ["Windows 10/11", ua.win11Chrome, "windows"],
    ["Windows 8.1 takes the normal build", ua.win81Edge, "windows"],
    ["Windows 7 (Chrome)", ua.win7Chrome, "windows7"],
    ["Windows 7 (Firefox)", ua.win7Firefox, "windows7"],
    ["macOS", ua.macSafari, "macos"],
    ["iPhone has no agent", ua.iPhone, null],
    ["Android has no agent", ua.android, null],
    ["Linux has no agent", ua.linux, null],
    ["empty", "", null],
  ])("%s", (_name, userAgent, want) => {
    expect(detectInstallOS(userAgent)).toBe(want);
  });
});
