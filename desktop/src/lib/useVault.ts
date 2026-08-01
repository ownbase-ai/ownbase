import { useCallback, useEffect, useState } from "react";

import * as api from "./api";
import type { VaultStatus } from "./types";

/** What the app should be showing, derived from the vault's state. */
export type VaultPhase =
  | "loading"
  /** No vault exists yet: this machine has never been set up. */
  | "absent"
  /** A vault exists but the agent has forgotten the master password. */
  | "locked"
  | "unlocked"
  /** The CLI itself could not be run — a broken bundle, not a locked vault. */
  | "broken";

export interface Vault {
  phase: VaultPhase;
  status: VaultStatus | null;
  /** Set when phase is "broken". */
  error: string | null;
  refresh: () => Promise<void>;
  unlock: (password: string, idleTimeout?: string) => Promise<void>;
  init: (path: string, password: string) => Promise<void>;
  lock: () => Promise<void>;
}

function phaseFor(status: VaultStatus): VaultPhase {
  if (status.unlocked) return "unlocked";
  // No recorded vault path means nothing has been set up, which is a different
  // screen from "locked": one asks for a location, the other for a password.
  return status.vault_path ? "locked" : "absent";
}

/**
 * Tracks the vault, which decides what the whole app can do.
 *
 * Re-checked on window focus rather than on a timer. The vault auto-locks after
 * an idle period, and the moment that matters is when the user comes back to
 * the window — polling every few seconds would also keep resetting the idle
 * timer it is trying to observe.
 */
export function useVault(): Vault {
  const [status, setStatus] = useState<VaultStatus | null>(null);
  const [phase, setPhase] = useState<VaultPhase>("loading");
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const next = await api.vaultStatus();
      setStatus(next);
      setPhase(phaseFor(next));
      setError(null);
    } catch (err) {
      setStatus(null);
      setPhase("broken");
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const onFocus = () => void refresh();
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [refresh]);

  const unlock = useCallback(
    async (password: string, idleTimeout?: string) => {
      const next = await api.vaultUnlock(password, idleTimeout);
      setStatus(next);
      setPhase(phaseFor(next));
      setError(null);
    },
    [],
  );

  const init = useCallback(async (path: string, password: string) => {
    const result = await api.vaultInit(path, password);
    setStatus(result.status);
    setPhase(phaseFor(result.status));
    setError(null);
  }, []);

  const lock = useCallback(async () => {
    await api.vaultLock();
    await refresh();
  }, [refresh]);

  return { phase, status, error, refresh, unlock, init, lock };
}
