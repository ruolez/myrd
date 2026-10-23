import { useCallback, useEffect, useRef, useState } from "react";
import type { Page } from "../api/types";
import { errorMessage } from "../api/client";

export function useAsync<T>(fn: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const seq = useRef(0);

  const reload = useCallback(async (quiet = false) => {
    const my = ++seq.current;
    if (!quiet) setLoading(true);
    try {
      const d = await fnRef.current();
      if (my === seq.current) {
        setData(d);
        setError(null);
      }
    } catch (e) {
      if (my === seq.current) setError(errorMessage(e));
    } finally {
      if (my === seq.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  return { data, error, loading, reload, setData };
}

/** Cursor pagination over `{items, next_before}` pages (PROTOCOL §1). */
export function usePaged<T>(
  fetchPage: (before: string | null) => Promise<Page<T>>,
  deps: unknown[],
) {
  const [stack, setStack] = useState<(string | null)[]>([null]);
  const cursor = stack[stack.length - 1] ?? null;
  const fetchRef = useRef(fetchPage);
  fetchRef.current = fetchPage;
  const result = useAsync(() => fetchRef.current(cursor), [cursor, ...deps]);

  useEffect(() => {
    setStack([null]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  const nextBefore = result.data?.next_before ?? null;
  return {
    items: result.data?.items ?? [],
    loading: result.loading,
    error: result.error,
    reload: result.reload,
    hasPrev: stack.length > 1,
    hasNext: !!nextBefore,
    next: () => nextBefore && setStack((s) => [...s, nextBefore]),
    prev: () => setStack((s) => (s.length > 1 ? s.slice(0, -1) : s)),
  };
}

/** Calls fn every ms while the tab is visible (and once when it becomes visible again). */
export function useInterval(fn: () => void, ms: number | null) {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    if (ms === null) return;
    let missed = false;
    const t = setInterval(() => {
      if (document.hidden) missed = true;
      else ref.current();
    }, ms);
    const onVisible = () => {
      if (document.hidden || !missed) return;
      missed = false;
      ref.current();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(t);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [ms]);
}

export function useNow(ms = 1000) {
  const [now, setNow] = useState(Date.now());
  useInterval(() => setNow(Date.now()), ms);
  return now;
}
