// remotectl streamed downloads (scope /__dl/). The page registers a download
// with a MessagePort, then navigates a hidden iframe to /__dl/<id>/<name>; the
// response body pulls chunks from the page, so nothing is buffered whole.
// Page side: web/src/session/downloadSinks.ts.

const pending = new Map();

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

self.addEventListener("message", (e) => {
  const d = e.data;
  if (!d || typeof d !== "object") return;
  if (d.type === "register" && typeof d.id === "string" && e.ports[0]) {
    pending.set(d.id, { port: e.ports[0], disposition: String(d.disposition) });
    e.ports[0].postMessage({ type: "registered" });
  }
  // "keepalive" needs no reply: receiving it extends the worker's lifetime.
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  const m = /^\/__dl\/([A-Za-z0-9-]+)(?:\/|$)/.exec(url.pathname);
  if (!m || url.origin !== self.location.origin) return;
  const entry = pending.get(m[1]);
  if (!entry) {
    e.respondWith(new Response("This download link has expired.", { status: 404 }));
    return;
  }
  pending.delete(m[1]);
  const { port } = entry;
  const body = new ReadableStream(
    {
      start(controller) {
        port.onmessage = (ev) => {
          const msg = ev.data || {};
          if (msg.type === "chunk") {
            controller.enqueue(new Uint8Array(msg.chunk));
          } else if (msg.type === "end") {
            controller.close();
            port.close();
          } else if (msg.type === "abort") {
            controller.error(new Error(String(msg.reason || "aborted")));
            port.close();
          }
        };
      },
      pull() {
        port.postMessage({ type: "pull" });
      },
      cancel(reason) {
        port.postMessage({ type: "cancel", reason: String(reason || "cancelled") });
        port.close();
      },
    },
    new CountQueuingStrategy({ highWaterMark: 8 }),
  );
  // No Content-Length: the file may change size between listing and download.
  const headers = {
    "Content-Type": "application/octet-stream",
    "Content-Disposition": entry.disposition,
    "X-Content-Type-Options": "nosniff",
    "Cache-Control": "no-store",
  };
  e.respondWith(new Response(body, { headers }));
});
