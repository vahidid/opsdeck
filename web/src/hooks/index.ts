import { useTabActive } from "../context/tabs";
import { useCallback, useEffect, useRef, useState } from "react";
import { errMsg } from "../api/client";

export type AsyncState<T> = {
  data: T | undefined;
  error: string | null;
  loading: boolean;
  reload: () => Promise<void>;
  setData: (fn: T | ((prev: T | undefined) => T)) => void;
};

/** Runs `fn` whenever deps change; keeps previous data while reloading. */
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[], opts: { enabled?: boolean } = {}): AsyncState<T> {
  const enabled = opts.enabled ?? true;
  const [data, setDataState] = useState<T | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState<boolean>(enabled);
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const seq = useRef(0);

  const reload = useCallback(async () => {
    const my = ++seq.current;
    setLoading(true);
    try {
      const v = await fnRef.current();
      if (my === seq.current) {
        setDataState(v);
        setError(null);
      }
    } catch (e) {
      if (my === seq.current) setError(errMsg(e));
    } finally {
      if (my === seq.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!enabled) {
      setLoading(false);
      return;
    }
    void reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, ...deps]);

  const setData = useCallback((v: T | ((prev: T | undefined) => T)) => {
    setDataState((prev) => (typeof v === "function" ? (v as (p: T | undefined) => T)(prev) : v));
  }, []);

  return { data, error, loading, reload, setData };
}

// useInterval runs fn every ms milliseconds while the page's tab is visible
// (pages in background tabs stay mounted but stop polling).
export function useInterval(fn: () => void, ms: number | null) {
  const ref = useRef(fn);
  ref.current = fn;
  const visible = useTabActive();
  useEffect(() => {
    if (ms === null || !visible) return;
    const t = setInterval(() => ref.current(), ms);
    return () => clearInterval(t);
  }, [ms, visible]);
}

export function useLocalStorage<T>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      const s = localStorage.getItem(key);
      return s === null ? initial : (JSON.parse(s) as T);
    } catch {
      return initial;
    }
  });
  const set = useCallback(
    (nv: T) => {
      setV(nv);
      try {
        localStorage.setItem(key, JSON.stringify(nv));
      } catch {
        /* ignore */
      }
    },
    [key],
  );
  return [v, set];
}

/** Run an action with busy flag; errors go to onError. */
export function useAction() {
  const [busy, setBusy] = useState<string | null>(null);
  const run = useCallback(async <T,>(key: string, fn: () => Promise<T>, onError?: (msg: string) => void): Promise<T | undefined> => {
    setBusy(key);
    try {
      return await fn();
    } catch (e) {
      onError?.(errMsg(e));
      return undefined;
    } finally {
      setBusy(null);
    }
  }, []);
  return { busy, run };
}
