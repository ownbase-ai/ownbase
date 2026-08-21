import { useCallback, useEffect, useRef } from "react";

import * as api from "./api";
import type { VersionCheck } from "./types";
import { useAsync } from "./useAsync";

/**
 * Global CLI + app version check (no Base). Used for the sidebar badge and
 * the Vault About/Updates panel. Daemon staleness lives on each Base's
 * checkup findings — opening N tunnels on launch would be too expensive.
 *
 * Launch is not stalled on the network: the first paint uses the 24h
 * manifest cache. Once that lands, a single background `--refresh` runs so
 * a just-cut release shows up without waiting for the cache TTL or a manual
 * "Check again". Vault → Check again still forces another live fetch.
 */
export function useVersionCheck() {
  const refreshNext = useRef(false);
  const didBackgroundRefresh = useRef(false);
  const load = useCallback(async (): Promise<VersionCheck> => {
    const refresh = refreshNext.current;
    refreshNext.current = false;
    let appVersion: string | undefined;
    try {
      appVersion = await api.appVersion();
    } catch {
      // Browser/e2e without Tauri: still check the CLI.
    }
    return api.versionCheck({ appVersion, refresh });
  }, []);
  const { reload: reloadAsync, loading, ...rest } = useAsync(load);
  const reload = useCallback(
    (opts?: { refresh?: boolean }) => {
      refreshNext.current = opts?.refresh === true;
      return reloadAsync();
    },
    [reloadAsync],
  );

  // After the cached first load finishes (success or soft failure), pull a
  // live manifest once. useAsync keeps the cached badge on screen while
  // refreshing, so this never blanks the shell.
  useEffect(() => {
    if (loading || didBackgroundRefresh.current) return;
    didBackgroundRefresh.current = true;
    void reload({ refresh: true });
  }, [loading, reload]);

  return { ...rest, loading, reload };
}

/** How many components are behind the newest release. */
export function behindCount(check: VersionCheck | null | undefined): number {
  if (!check) return 0;
  let n = check.components.filter((c) => c.status === "behind").length;
  if (check.skew) n += 1;
  return n;
}
