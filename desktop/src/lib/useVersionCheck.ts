import { useCallback } from "react";

import * as api from "./api";
import type { VersionCheck } from "./types";
import { useAsync } from "./useAsync";

/**
 * Global CLI + app version check (no Base). Used for the sidebar badge and
 * the Vault About/Updates panel. Daemon staleness lives on each Base's
 * checkup findings — opening N tunnels on launch would be too expensive.
 */
export function useVersionCheck() {
  const load = useCallback(async (): Promise<VersionCheck> => {
    let appVersion: string | undefined;
    try {
      appVersion = await api.appVersion();
    } catch {
      // Browser/e2e without Tauri: still check the CLI.
    }
    return api.versionCheck({ appVersion });
  }, []);
  return useAsync(load);
}

/** How many components are behind the newest release. */
export function behindCount(check: VersionCheck | null | undefined): number {
  if (!check) return 0;
  let n = check.components.filter((c) => c.status === "behind").length;
  if (check.skew) n += 1;
  return n;
}
