// Installed-app state: the app service worker (/sw.js, built from src/sw/),
// the browser's install prompt, standalone mode and "a new version is live".

import { useSyncExternalStore } from "react";
import { installHint } from "../lib/pwa";
import type { InstallHint } from "../lib/pwa";

type BeforeInstallPromptEvent = Event & {
  prompt: () => Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed" }>;
};

type PwaState = {
  hint: InstallHint;
  /** A newer build took over this page's service worker; a reload shows it. */
  updated: boolean;
  /** Installed from this tab just now (appinstalled). */
  justInstalled: boolean;
};

const UPDATE_CHECK_MS = 60 * 60 * 1000;
const MIN_CHECK_GAP_MS = 5 * 60 * 1000;

let deferred: BeforeInstallPromptEvent | null = null;
let updated = false;
let justInstalled = false;
let state: PwaState = compute();
const listeners = new Set<() => void>();

function standalone(): boolean {
  return (
    !!window.matchMedia?.("(display-mode: standalone)").matches ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true
  );
}

function compute(): PwaState {
  const hint = installHint(navigator.userAgent, {
    standalone: standalone(),
    canPrompt: deferred !== null,
    maxTouchPoints: navigator.maxTouchPoints ?? 0,
  });
  return { hint, updated, justInstalled };
}

function emit() {
  state = compute();
  for (const fn of listeners) fn();
}

const subscribe = (fn: () => void) => {
  listeners.add(fn);
  return () => void listeners.delete(fn);
};

export const usePwa = (): PwaState => useSyncExternalStore(subscribe, () => state);

/** Shows the browser's install dialog; only valid while hint is "prompt". */
export async function promptInstall(): Promise<void> {
  const e = deferred;
  if (!e) return;
  deferred = null;
  await e.prompt();
  await e.userChoice.catch(() => undefined);
  emit();
}

/** Listens for install and display-mode changes; call once before rendering. */
export function initPwa() {
  window.addEventListener("beforeinstallprompt", (e) => {
    e.preventDefault();
    deferred = e as BeforeInstallPromptEvent;
    emit();
  });
  window.addEventListener("appinstalled", () => {
    deferred = null;
    justInstalled = true;
    emit();
  });
  window.matchMedia?.("(display-mode: standalone)").addEventListener("change", emit);
  if (import.meta.env.PROD) registerAppWorker();
}

function registerAppWorker() {
  if (!window.isSecureContext || !("serviceWorker" in navigator)) return;
  const sw = navigator.serviceWorker;
  // The first install claims this page too; only later takeovers are updates.
  let hadController = !!sw.controller;
  sw.addEventListener("controllerchange", () => {
    if (hadController) {
      updated = true;
      emit();
    }
    hadController = true;
  });
  const register = () => {
    sw.register("/sw.js", { scope: "/", updateViaCache: "none" })
      .then((reg) => {
        let lastCheck = Date.now();
        const check = () => {
          if (Date.now() - lastCheck < MIN_CHECK_GAP_MS) return;
          lastCheck = Date.now();
          void reg.update().catch(() => undefined);
        };
        document.addEventListener("visibilitychange", () => {
          if (document.visibilityState === "visible") check();
        });
        setInterval(check, UPDATE_CHECK_MS);
      })
      .catch(() => undefined);
  };
  if (document.readyState === "complete") register();
  else window.addEventListener("load", register, { once: true });
}
