import { useCallback, useState } from "react";

import { Sidebar } from "./components/Sidebar";
import type { Route } from "./components/Sidebar";
import { ErrorNote, Spinner } from "./components/ui";
import * as api from "./lib/api";
import type { BaseSummary } from "./lib/types";
import { useAsync } from "./lib/useAsync";
import { useVault } from "./lib/useVault";
import type { Vault } from "./lib/useVault";
import { BaseDetail } from "./views/BaseDetail";
import { SessionsView } from "./views/SessionsView";
import { SetupWizard } from "./views/SetupWizard";
import { UnlockView } from "./views/UnlockView";
import { VaultView } from "./views/VaultView";

/**
 * What the window shows, decided by one question: can we reach the vault?
 *
 * Everything past the unlock screen assumes an open vault, which is why this is
 * a gate rather than a banner — a dashboard full of "locked" errors would be
 * worse than not showing it.
 */
export function App() {
  const vault = useVault();

  if (vault.phase === "loading") {
    return (
      <div className="flex h-full items-center justify-center bg-zinc-950">
        <Spinner className="h-5 w-5 text-zinc-600" />
      </div>
    );
  }

  if (vault.phase === "broken") {
    return (
      <div className="flex h-full items-center justify-center bg-zinc-950 p-8">
        <div className="w-full max-w-lg">
          <ErrorNote
            title="OwnBase could not run its command-line tool"
            detail={vault.error}
            onRetry={() => void vault.refresh()}
          />
          <p className="mt-4 text-sm leading-relaxed text-zinc-500">
            The app bundles <code className="font-mono">ownbasectl</code> and does
            everything through it, so this is a problem with the install rather
            than with any of your Bases — they are unaffected.
          </p>
        </div>
      </div>
    );
  }

  if (vault.phase !== "unlocked") {
    return <UnlockView vault={vault} />;
  }

  return <Shell vault={vault} />;
}

function Shell({ vault }: { vault: Vault }) {
  const load = useCallback(() => api.listBases(), []);
  const bases = useAsync<BaseSummary[]>(load);
  const list = bases.data ?? [];

  // Null means "wherever makes sense", which is the first Base if there is one
  // and the wizard if there is not. Derived rather than stored, so a reload of
  // the list cannot move a user who has already navigated somewhere.
  const [chosen, setChosen] = useState<Route | null>(null);
  const first = list[0];
  const landing: Route = first ? { view: "base", name: first.name } : { view: "wizard" };
  const route = chosen ?? landing;

  const navigate = useCallback((next: Route) => setChosen(next), []);

  const selected =
    route.view === "base" ? (list.find((b) => b.name === route.name) ?? null) : null;

  return (
    <div className="flex h-full bg-zinc-950">
      <Sidebar
        bases={list}
        loading={bases.loading}
        route={route}
        onNavigate={navigate}
        vault={vault.status}
        onLock={() => void vault.lock()}
      />

      <main className="min-h-0 min-w-0 flex-1">
        {bases.error && route.view !== "vault" ? (
          <div className="p-8">
            <ErrorNote
              title="Could not list your Bases"
              detail={bases.error}
              onRetry={bases.reload}
            />
          </div>
        ) : route.view === "wizard" ? (
          <SetupWizard
            existingNames={list.map((b) => b.name)}
            onCancel={() => setChosen(null)}
            onFinished={(name) => {
              void bases.reload();
              void vault.refresh();
              navigate({ view: "base", name });
            }}
          />
        ) : route.view === "sessions" ? (
          <SessionsView />
        ) : route.view === "vault" ? (
          <VaultView vault={vault} bases={list} />
        ) : selected ? (
          <BaseDetail
            key={selected.name}
            base={selected}
            onRemoved={() => {
              // Navigate away before reload finishes — otherwise landing still
              // points at this Base from the stale list and detail stays mounted.
              const rest = list.filter((b) => b.name !== selected.name);
              const next: Route = rest[0]
                ? { view: "base", name: rest[0].name }
                : { view: "wizard" };
              setChosen(next);
              void bases.reload();
              void vault.refresh();
            }}
          />
        ) : (
          <div className="flex h-full items-center justify-center">
            <Spinner className="text-zinc-700" />
          </div>
        )}
      </main>
    </div>
  );
}
