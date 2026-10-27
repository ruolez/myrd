import type { DownloadOS } from "../api/types";

/**
 * The installer an install link's landing page starts by itself. Windows 7
 * browsers still say "Windows NT 6.1"; phones and Linux get none.
 */
export function detectInstallOS(userAgent: string): DownloadOS | null {
  if (/Windows NT 6\.1\b/.test(userAgent)) return "windows7";
  if (/Windows NT/.test(userAgent)) return "windows";
  if (/Macintosh/.test(userAgent) && !/iPhone|iPad/.test(userAgent)) return "macos";
  return null;
}
