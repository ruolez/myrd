// How the app service worker (sw.ts) treats a request. Anything the worker
// must never touch (API, signaling, streamed downloads, ACME, the workers
// themselves) is "bypass": no respondWith, the browser goes to the network.

export type SwRoute = "bypass" | "shell" | "asset" | "static";

const BYPASS_PREFIXES = ["/v1/", "/__dl/", "/.well-known/"];
const BYPASS_PATHS = new Set(["/v1", "/healthz", "/sw.js", "/dl-sw.js"]);
const STATIC_PATHS = new Set(["/favicon.svg", "/manifest.webmanifest"]);

export function classifyRequest(
  url: URL,
  method: string,
  mode: RequestMode,
  origin: string,
): SwRoute {
  if (method !== "GET" || url.origin !== origin) return "bypass";
  const path = url.pathname;
  if (BYPASS_PATHS.has(path) || BYPASS_PREFIXES.some((p) => path.startsWith(p))) return "bypass";
  if (mode === "navigate") return "shell";
  if (path.startsWith("/assets/")) return "asset";
  if (STATIC_PATHS.has(path) || path.startsWith("/icons/")) return "static";
  return "bypass";
}

export const CACHE_PREFIX = "shell-";

/**
 * Caches to delete on activate. `names` is in creation order (CacheStorage
 * keys() order); the newest older build is kept so tabs still running it can
 * load their lazy chunks after a deploy removed them from the server.
 */
export function staleCaches(names: readonly string[], current: string): string[] {
  const builds = names.filter((n) => n.startsWith(CACHE_PREFIX) && n !== current);
  return builds.slice(0, -1);
}
