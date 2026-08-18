import wordmark from "../assets/wordmark.png";
import { Badge, Button, Dot, Spinner } from "./ui";
import type { Tone } from "./ui";
import { cx } from "../lib/cx";
import { until } from "../lib/format";
import type { BaseSummary, VaultStatus } from "../lib/types";

export type Route =
  | { view: "base"; name: string }
  | { view: "wizard" }
  | { view: "sessions" }
  | { view: "vault" };

export function Sidebar({
  bases,
  loading,
  route,
  onNavigate,
  vault,
  onLock,
  updatesBehind = 0,
}: {
  bases: BaseSummary[];
  loading: boolean;
  route: Route;
  onNavigate: (route: Route) => void;
  vault: VaultStatus | null;
  onLock: () => void;
  /** CLI/app components behind the newest release (from version --check). */
  updatesBehind?: number;
}) {
  return (
    <nav className="flex w-60 shrink-0 flex-col border-r border-zinc-800 bg-zinc-950">
      {/* Clears the transparent title bar, which the window draws over. */}
      <div className="h-11 shrink-0" />

      <div className="px-3 pb-3">
        <div className="flex h-7 items-center px-2">
          <img
            src={wordmark}
            alt="OwnBase"
            className="h-5 w-auto max-w-[9.5rem] object-contain object-left"
            draggable={false}
          />
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        <Group label="Bases">
          {loading && bases.length === 0 ? (
            <div className="px-2 py-2">
              <Spinner className="text-zinc-600" />
            </div>
          ) : bases.length === 0 ? (
            <p className="px-2 py-1.5 text-xs leading-relaxed text-zinc-600">
              None yet.
            </p>
          ) : (
            bases.map((base) => (
              <Item
                key={base.name}
                active={route.view === "base" && route.name === base.name}
                onClick={() => onNavigate({ view: "base", name: base.name })}
              >
                <Dot tone={baseTone(base)} />
                <span className="truncate">{base.name}</span>
              </Item>
            ))
          )}
          <Item
            active={route.view === "wizard"}
            onClick={() => onNavigate({ view: "wizard" })}
          >
            <span aria-hidden className="w-2 text-center text-zinc-500">
              +
            </span>
            <span className="text-zinc-400">Set up a Base</span>
          </Item>
        </Group>

        <Group label="Audit">
          <Item
            active={route.view === "sessions"}
            onClick={() => onNavigate({ view: "sessions" })}
          >
            <span aria-hidden className="w-2" />
            Sessions
          </Item>
        </Group>

        <Group label="Credentials">
          <Item
            active={route.view === "vault"}
            onClick={() => onNavigate({ view: "vault" })}
          >
            <span aria-hidden className="w-2" />
            <span className="flex min-w-0 flex-1 items-center justify-between gap-2">
              <span>Vault</span>
              {updatesBehind > 0 && <Badge tone="warn">{updatesBehind}</Badge>}
            </span>
          </Item>
        </Group>
      </div>

      <VaultFooter vault={vault} onLock={onLock} updatesBehind={updatesBehind} />
    </nav>
  );
}

/**
 * The colour beside a Base's name.
 *
 * Deliberately not health: knowing whether a Base is healthy costs an SSH
 * tunnel per Base, and a sidebar that opens six of them on launch would make
 * the app slow for information the detail view is about to fetch anyway. So this
 * says what is knowable locally — whether there is a machine behind the name —
 * and stays grey rather than pretending to a verdict it does not have.
 */
function baseTone(base: BaseSummary): Tone {
  if (base.kind === "unregistered-vm") return "warn";
  if (base.kind === "key-only") return "unknown";
  return "info";
}

function Group({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="mb-4">
      <p className="px-2 pb-1 text-[0.6875rem] font-medium uppercase tracking-wider text-zinc-600">
        {label}
      </p>
      <ul className="space-y-0.5">{children}</ul>
    </div>
  );
}

function Item({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <li>
      <button
        onClick={onClick}
        className={cx(
          "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors",
          active ? "bg-zinc-800 text-zinc-100" : "text-zinc-300 hover:bg-zinc-900",
        )}
      >
        {children}
      </button>
    </li>
  );
}

function VaultFooter({
  vault,
  onLock,
  updatesBehind,
}: {
  vault: VaultStatus | null;
  onLock: () => void;
  updatesBehind: number;
}) {
  return (
    <div className="shrink-0 border-t border-zinc-800 px-3 py-3">
      <div className="flex items-center justify-between gap-2">
        <Badge tone="good">
          <Dot tone="good" />
          Unlocked
        </Badge>
        <Button variant="ghost" className="px-2 py-1 text-xs" onClick={onLock}>
          Lock
        </Button>
      </div>
      {updatesBehind > 0 && (
        <p className="mt-1.5 px-0.5 text-[0.6875rem] leading-relaxed text-amber-200/80">
          {updatesBehind === 1
            ? "1 OwnBase update available — open Vault."
            : `${updatesBehind} OwnBase updates available — open Vault.`}
        </p>
      )}
      {vault?.locks_at && (
        <p className="mt-1.5 px-0.5 text-[0.6875rem] leading-relaxed text-zinc-600">
          Locks {until(vault.locks_at)} unless something uses it.
        </p>
      )}
    </div>
  );
}
