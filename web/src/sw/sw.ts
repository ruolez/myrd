// App service worker (scope /). Caches the app shell so the installed app
// starts without the network and keeps the previous build's chunks so tabs
// opened before a deploy can still lazy-load. Never touches the API,
// signaling, streamed downloads (/__dl/, public/dl-sw.js) or ACME: see routes.ts.
// Built by the swPlugin in vite.config.ts into dist/sw.js.

import { CACHE_PREFIX, classifyRequest, staleCaches } from "./routes";

declare const self: ServiceWorkerGlobalScope;
declare const __BUILD_ID__: string;
declare const __PRECACHE__: string[];

const CACHE = CACHE_PREFIX + __BUILD_ID__;
const SHELL_KEY = "/";
const NAV_TIMEOUT_MS = 4000;

// A response that followed a redirect can't answer a navigation; copy it.
function plain(res: Response): Response {
  if (!res.redirected) return res;
  return new Response(res.body, {
    status: res.status,
    statusText: res.statusText,
    headers: res.headers,
  });
}

const cacheable = (res: Response) => res.ok && res.type === "basic";
const isHtml = (res: Response) => (res.headers.get("Content-Type") ?? "").startsWith("text/html");

self.addEventListener("install", (e) => {
  e.waitUntil(
    (async () => {
      const cache = await caches.open(CACHE);
      await Promise.all(
        __PRECACHE__.map(async (path) => {
          const res = await fetch(new Request(path, { cache: "reload" }));
          if (!cacheable(res) || (path !== SHELL_KEY && isHtml(res)))
            throw new Error(`precache ${path}: ${res.status}`);
          await cache.put(path, plain(res));
        }),
      );
      await self.skipWaiting();
    })(),
  );
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    (async () => {
      const stale = staleCaches(await caches.keys(), CACHE);
      await Promise.all(stale.map((n) => caches.delete(n)));
      await self.registration.navigationPreload?.enable().catch(() => undefined);
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  switch (classifyRequest(url, e.request.method, e.request.mode, self.location.origin)) {
    case "shell":
      e.respondWith(shell(e));
      return;
    case "asset":
      e.respondWith(asset(e.request));
      return;
    case "static":
      e.respondWith(staleWhileRevalidate(e));
      return;
    case "bypass":
      return;
  }
});

async function shell(e: FetchEvent): Promise<Response> {
  const network = (async () => {
    const res = ((await e.preloadResponse) as Response | undefined) ?? (await fetch(e.request));
    if (cacheable(res) && isHtml(res)) {
      const copy = plain(res.clone());
      e.waitUntil(caches.open(CACHE).then((c) => c.put(SHELL_KEY, copy)));
    }
    return res;
  })();
  const cached = await caches.match(SHELL_KEY);
  if (!cached) return network;
  const fallback = network.catch(() => cached);
  const timeout = new Promise<Response>((resolve) =>
    setTimeout(() => resolve(cached), NAV_TIMEOUT_MS),
  );
  e.waitUntil(
    network.then(
      () => undefined,
      () => undefined,
    ),
  );
  return Promise.race([fallback, timeout]);
}

async function asset(req: Request): Promise<Response> {
  const cached = await caches.match(req);
  if (cached) return cached;
  const res = await fetch(req);
  // Caddy answers a missing asset with index.html (try_files); never keep that.
  if (cacheable(res) && !isHtml(res)) {
    const copy = res.clone();
    void caches.open(CACHE).then((c) => c.put(req, copy));
  }
  return res;
}

async function staleWhileRevalidate(e: FetchEvent): Promise<Response> {
  const cached = await caches.match(e.request);
  const network = fetch(e.request).then((res) => {
    if (cacheable(res)) {
      const copy = res.clone();
      e.waitUntil(caches.open(CACHE).then((c) => c.put(e.request, copy)));
    }
    return res;
  });
  if (!cached) return network;
  e.waitUntil(
    network.then(
      () => undefined,
      () => undefined,
    ),
  );
  return cached;
}
