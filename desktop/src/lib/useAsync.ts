import { useCallback, useEffect, useRef, useState } from "react";

import { CliError } from "./cli";

export interface Async<T> {
  data: T | null;
  /** True only for the first load; a refresh keeps the old data on screen. */
  loading: boolean;
  /** True while a refresh is in flight over data that is already showing. */
  refreshing: boolean;
  error: string | null;
  /** Set when the failure was the vault locking, which the shell handles. */
  locked: boolean;
  reload: () => Promise<void>;
}

/**
 * Loads something from the CLI and keeps it fresh.
 *
 * `load` must be memoized with `useCallback`; its identity is what decides when
 * this reloads. That is a real constraint rather than a stylistic one — most of
 * these calls open an SSH tunnel to a Base and take a second or two, so a loader
 * that is a new function every render would be a request every render.
 *
 * Two other things here earn their keep. A refresh does not blank the screen:
 * flashing a spinner over data that is still true makes the app feel
 * unreliable. And a stale response is discarded — switching Bases quickly would
 * otherwise let a slow first request overwrite the second one's answer, showing
 * one Base's health under another Base's name.
 */
export function useAsync<T>(load: () => Promise<T>): Async<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [locked, setLocked] = useState(false);

  // Bumped on every run; a response whose generation is no longer current is an
  // answer to a question nobody is asking any more.
  const generation = useRef(0);

  const run = useCallback(
    async (isRefresh: boolean) => {
      const mine = ++generation.current;
      if (isRefresh) setRefreshing(true);
      else setLoading(true);
      try {
        const next = await load();
        if (generation.current !== mine) return;
        setData(next);
        setError(null);
        setLocked(false);
      } catch (err) {
        if (generation.current !== mine) return;
        setLocked(err instanceof CliError && err.isLocked);
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        if (generation.current === mine) {
          setLoading(false);
          setRefreshing(false);
        }
      }
    },
    [load],
  );

  useEffect(() => {
    void run(false);
  }, [run]);

  const reload = useCallback(() => run(true), [run]);

  return { data, loading, refreshing, error, locked, reload };
}
