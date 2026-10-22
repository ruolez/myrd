// Dashboard screen previews (PROTOCOL §3 "Screen previews"): tells the server
// which devices are on screen (preview.watch), opens the sealed previews it
// relays and keeps them as object URLs in memory only, never in storage.

import { api } from "../api/client";
import type { Device, UserId } from "../api/types";
import { checkDevicePin } from "./devicePins";
import { openPreview } from "./previewCrypto";
import type { SealedPreview } from "./previewCrypto";
import { ensurePreviewKey } from "./previewKey";
import type { PreviewKey } from "./previewKey";
import { signaling } from "./signaling";

export type PreviewState = {
  /** Object URL of the latest picture. */
  url: string | null;
  capturedAt: number | null;
  width: number;
  height: number;
  /** Why there is no new picture (PROTOCOL reasons, plus "unpinned" and "identity_changed"). */
  reason: string | null;
};

const EMPTY: PreviewState = { url: null, capturedAt: null, width: 0, height: 0, reason: null };
const WATCH_DEBOUNCE_MS = 300;
const MAX_IMAGES = 150;
const MAX_DECRYPTS = 4;

export class PreviewManager {
  private states = new Map<string, PreviewState>();
  private listeners = new Map<string, Set<() => void>>();
  private visible = new Map<Element, string>();
  private fetched = new Set<string>();
  private observer: IntersectionObserver | null = null;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private key: Promise<PreviewKey> | null = null;
  private offs: (() => void)[] = [];
  private queue: (() => Promise<void>)[] = [];
  private running = 0;
  private disposed = false;
  private lastSent = "";

  constructor(
    private readonly userId: UserId,
    /** The device as the dashboard knows it (public key, previews switch). */
    private readonly device: (id: string) => Device | undefined,
    /** Devices this browser should ask previews for right now. */
    private readonly eligible: (d: Device) => boolean,
  ) {
    if (typeof IntersectionObserver !== "undefined")
      this.observer = new IntersectionObserver((entries) => this.onIntersect(entries), {
        rootMargin: "200px",
      });
    const onVis = () => this.scheduleWatch(0);
    document.addEventListener("visibilitychange", onVis);
    this.offs.push(() => document.removeEventListener("visibilitychange", onVis));
    this.offs.push(signaling.on("preview", (m) => this.enqueue(m as unknown as SealedPreview)));
    this.offs.push(
      signaling.on("preview.unavailable", (m) =>
        this.patch(String(m.device_id), { reason: String(m.reason) }),
      ),
    );
    this.offs.push(
      signaling.onState((s) => {
        if (s === "open") {
          this.lastSent = "";
          this.scheduleWatch(0);
        }
      }),
    );
  }

  get(id: string): PreviewState {
    return this.states.get(id) ?? EMPTY;
  }

  subscribe(id: string, fn: () => void): () => void {
    let set = this.listeners.get(id);
    if (!set) this.listeners.set(id, (set = new Set()));
    set.add(fn);
    return () => set.delete(fn);
  }

  /** Watches el (a tile) for device id while it is on screen. */
  observe(el: Element, id: string): () => void {
    this.visible.set(el, "");
    this.observer?.observe(el);
    if (!this.observer) this.visible.set(el, id);
    (el as HTMLElement).dataset.previewId = id;
    this.scheduleWatch();
    return () => {
      this.observer?.unobserve(el);
      this.visible.delete(el);
      this.scheduleWatch();
    };
  }

  /** Re-evaluates which devices to watch (devices or switches changed). */
  refresh() {
    this.scheduleWatch();
  }

  dispose() {
    this.disposed = true;
    if (this.timer) clearTimeout(this.timer);
    this.observer?.disconnect();
    for (const off of this.offs) off();
    if (this.lastSent && this.lastSent !== "[]") this.sendWatch([]);
    for (const s of this.states.values()) if (s.url) URL.revokeObjectURL(s.url);
    this.states.clear();
  }

  private onIntersect(entries: IntersectionObserverEntry[]) {
    for (const e of entries) {
      if (!this.visible.has(e.target)) continue;
      this.visible.set(
        e.target,
        e.isIntersecting ? ((e.target as HTMLElement).dataset.previewId ?? "") : "",
      );
    }
    this.scheduleWatch();
  }

  private scheduleWatch(delay = WATCH_DEBOUNCE_MS) {
    if (this.disposed) return;
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      this.timer = null;
      void this.flushWatch();
    }, delay);
  }

  private wanted(): string[] {
    if (document.hidden) return [];
    const ids = new Set<string>();
    for (const id of this.visible.values()) {
      const d = id ? this.device(id) : undefined;
      if (d && this.eligible(d)) ids.add(id);
    }
    return [...ids].sort().slice(0, 100);
  }

  private async flushWatch() {
    const ids = this.wanted();
    // First paint: the latest stored previews, before the next live ones.
    const fresh = ids.filter((id) => !this.fetched.has(id));
    if (fresh.length) {
      for (const id of fresh) this.fetched.add(id);
      void this.withKey(async (k) => {
        const { items } = await api.previews(k.clientKeyId, fresh);
        for (const p of items) this.enqueue(p);
      });
    }
    this.sendWatch(ids);
  }

  private sendWatch(ids: string[]) {
    const sig = JSON.stringify(ids);
    if (sig === this.lastSent) return;
    void this.withKey(async (k) => {
      if (signaling.state !== "open") return;
      this.lastSent = sig;
      signaling.send({ type: "preview.watch", client_key_id: k.clientKeyId, device_ids: ids });
    });
  }

  private async withKey(fn: (k: PreviewKey) => Promise<void>) {
    try {
      this.key ??= ensurePreviewKey(this.userId);
      await fn(await this.key);
    } catch {
      this.key = null; // retried on the next change
    }
  }

  private enqueue(p: SealedPreview) {
    this.queue.push(() => this.open(p));
    this.pump();
  }

  private pump() {
    while (this.running < MAX_DECRYPTS && this.queue.length) {
      const job = this.queue.shift()!;
      this.running++;
      void job().finally(() => {
        this.running--;
        this.pump();
      });
    }
  }

  private async open(p: SealedPreview) {
    const id = p.device_id;
    const d = this.device(id);
    if (!d || this.disposed) return;
    const cur = this.get(id);
    if (cur.capturedAt !== null && p.captured_at_ms <= cur.capturedAt) return; // never older
    // Trust comes from this browser's own pin of the device key, made on the
    // first session; a preview never pins a key.
    const pin = await checkDevicePin(id, d.public_key).catch(() => ({ status: "new" as const }));
    if (pin.status === "new") return this.patch(id, { reason: "unpinned" });
    if (pin.status === "mismatch") return this.patch(id, { reason: "identity_changed" });
    let bytes: Uint8Array<ArrayBuffer>;
    try {
      const k = await (this.key ??= ensurePreviewKey(this.userId));
      bytes = await openPreview(id, d.public_key, k.privateKey, p, k.ckSha, k.pkSha);
    } catch {
      return; // not for this key, or tampered with: keep what is shown
    }
    if (this.disposed) return;
    const url = URL.createObjectURL(new Blob([bytes], { type: "image/jpeg" }));
    this.patch(id, {
      url,
      capturedAt: p.captured_at_ms,
      width: p.width,
      height: p.height,
      reason: null,
    });
    this.evict();
  }

  private patch(id: string, change: Partial<PreviewState>) {
    const cur = this.get(id);
    if (change.url && cur.url && change.url !== cur.url) URL.revokeObjectURL(cur.url);
    this.states.delete(id); // re-insert: Map order is recency for eviction
    this.states.set(id, { ...cur, ...change });
    for (const fn of this.listeners.get(id) ?? []) fn();
  }

  private evict() {
    let n = 0;
    for (const s of this.states.values()) if (s.url) n++;
    for (const [id, s] of this.states) {
      if (n <= MAX_IMAGES) break;
      if (!s.url) continue;
      URL.revokeObjectURL(s.url);
      this.states.set(id, { ...EMPTY, reason: s.reason });
      n--;
      for (const fn of this.listeners.get(id) ?? []) fn();
    }
  }
}
