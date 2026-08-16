import { useState } from "react";

import { CopyButton } from "../components/CopyButton";
import {
  Badge,
  Button,
  CommandLine,
  Dot,
  ErrorNote,
  Field,
  Input,
  Panel,
  Row,
} from "../components/ui";
import * as api from "../lib/api";
import { absolute, ago, span, until } from "../lib/format";
import type { BaseSummary, VersionCheck, VersionComponent, VersionStatus } from "../lib/types";
import type { Vault } from "../lib/useVault";

/**
 * What the vault holds and how it behaves.
 *
 * The honest version of a credential screen: it says where the file is, what is
 * in it, when it will lock, and how to open it without OwnBase. It never shows a
 * secret, because it never has one — the private keys live in the agent's memory
 * and the app only ever asks the CLI to use them.
 */
export function VaultView({
  vault,
  bases,
  versionCheck,
  versionError,
  onRefreshVersions,
}: {
  vault: Vault;
  bases: BaseSummary[];
  versionCheck?: VersionCheck | null;
  versionError?: string | null;
  onRefreshVersions?: () => void;
}) {
  const status = vault.status;
  const withKeys = bases.filter((b) => b.has_key);

  return (
    <div className="mx-auto w-full max-w-3xl space-y-5 overflow-y-auto px-8 py-8">
      <header>
        <h1 className="text-lg font-medium text-zinc-100">Vault</h1>
        <p className="mt-1 text-sm leading-relaxed text-zinc-500">
          One encrypted file holds every credential that reaches your Bases. You chose
          where it lives; nothing but your master password opens it.
        </p>
      </header>

      <Panel
        title="Status"
        action={
          <Button variant="secondary" onClick={() => void vault.lock()}>
            Lock now
          </Button>
        }
      >
        <Row label="State">
          <Badge tone={status?.unlocked ? "good" : "warn"}>
            <Dot tone={status?.unlocked ? "good" : "warn"} />
            {status?.unlocked ? "unlocked" : "locked"}
          </Badge>
        </Row>
        <Row label="File" title={status?.vault_path}>
          <span className="font-mono text-xs">{status?.vault_path ?? "—"}</span>
        </Row>
        <Row label="Holds">
          {status?.bases ?? 0} Base{(status?.bases ?? 0) === 1 ? "" : "s"},{" "}
          {status?.keys ?? 0} owner key{(status?.keys ?? 0) === 1 ? "" : "s"}
        </Row>
        <Row label="Unlocked" title={absolute(status?.unlocked_at)}>
          {ago(status?.unlocked_at, "—")}
        </Row>
        <Row label="Auto-locks" title={absolute(status?.locks_at)}>
          {status?.idle_timeout_seconds
            ? `${until(status.locks_at, "—")} unless something uses it`
            : "never — no idle timeout set"}
        </Row>
        {status?.idle_timeout_seconds ? (
          <Row label="Idle timeout">{span(status.idle_timeout_seconds)}</Row>
        ) : null}
        {status?.vault_path && (
          <div className="mt-3 flex justify-end">
            <CopyButton value={status.vault_path} label="Copy path" />
          </div>
        )}
      </Panel>

      <Panel
        title="The credential agent"
        subtitle="A small resident process, like ssh-agent, holding the unlocked vault in memory."
      >
        <Row label="Running">
          {status?.running ? (
            <span className="text-emerald-300">yes (pid {status.pid})</span>
          ) : (
            <span className="text-zinc-500">no</span>
          )}
        </Row>
        {status?.ssh_agent_socket && (
          <Row label="Signs SSH on" title={status.ssh_agent_socket}>
            <span className="font-mono text-xs">{status.ssh_agent_socket}</span>
          </Row>
        )}
        {status?.version && <Row label="Version">{status.version}</Row>}
        <p className="mt-3 text-xs leading-relaxed text-zinc-500">
          Private keys never leave the agent and are never written to disk. When
          something needs to authenticate, it asks the agent for a signature — which
          is also why a coding agent can reach your Bases without ever being handed a
          key.
        </p>
      </Panel>

      <Panel
        title="Owner keys"
        subtitle="One per Base, so retiring a Base revokes exactly one credential."
      >
        {withKeys.length === 0 ? (
          <p className="text-sm text-zinc-500">No keys yet.</p>
        ) : (
          <ul className="divide-y divide-zinc-800">
            {withKeys.map((base) => (
              <li
                key={base.name}
                className="flex items-center justify-between gap-3 py-2.5 text-sm first:pt-0 last:pb-0"
              >
                <span className="text-zinc-200">{base.name}</span>
                <span className="text-xs text-zinc-500">
                  {base.host ? base.host : "no machine yet"}
                  {base.has_token ? " · API token stored" : ""}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Panel>

      <ChangePassword />

      <RecoveryStringPanel />

      <AboutPanel
        check={versionCheck}
        error={versionError}
        onRefresh={onRefreshVersions}
      />

      <Panel
        title="If OwnBase disappeared tomorrow"
        subtitle="The escape hatch is not a promise, it is the file format."
      >
        <p className="text-sm leading-relaxed text-zinc-400">
          The vault is a standard KDBX 4 database — the KeePass format. KeePassXC and
          every other KeePass client can open it with your master password, read the
          hosts and tokens, and export the private keys. Nothing here depends on this
          app, or on us.
        </p>
        <p className="mt-3 text-xs leading-relaxed text-zinc-500">
          Back the file up. It holds the only copy of the keys that reach your Bases,
          and there is no recovery: lose both it and your master password and no one
          can get in, which is the same property that makes it safe to keep in cloud
          storage.
        </p>
      </Panel>
    </div>
  );
}

function RecoveryStringPanel() {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState<string | null>(null);
  const [location, setLocation] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function reveal() {
    if (open) {
      setOpen(false);
      setValue(null);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const r = await api.vaultRecoveryString();
      setValue(r.recovery_string);
      setLocation(r.location);
      setOpen(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel
      title="Recovery string"
      subtitle="For remote vaults only. Store offline with your master password so a fresh machine can reopen the same vault."
      action={
        <Button variant="secondary" busy={busy} onClick={() => void reveal()}>
          {open ? "Hide" : "Reveal"}
        </Button>
      }
    >
      {error && (
        <p className="text-sm text-zinc-500">
          {error.includes("local file")
            ? "This vault is a local file — there is no recovery string. Back up the file itself."
            : error}
        </p>
      )}
      {open && value && (
        <div className="space-y-2">
          {location && (
            <Row label="Location">
              <span className="font-mono text-xs">{location}</span>
            </Row>
          )}
          <p className="selectable break-all font-mono text-xs text-zinc-300">{value}</p>
          <CopyButton value={value} label="Copy recovery string" />
        </div>
      )}
    </Panel>
  );
}

function AboutPanel({
  check,
  error,
  onRefresh,
}: {
  check?: VersionCheck | null;
  error?: string | null;
  onRefresh?: () => void;
}) {
  const components = check?.components ?? [];

  return (
    <Panel
      title="About & updates"
      subtitle="This app and the CLI bundled beside it. Client updates are guided, not applied."
      action={
        onRefresh ? (
          <Button variant="secondary" onClick={() => onRefresh()}>
            Check again
          </Button>
        ) : undefined
      }
    >
      {error && <p className="mb-3 text-xs text-red-300">{error}</p>}
      {components.length === 0 && !error ? (
        <p className="text-sm text-zinc-500">Checking versions…</p>
      ) : (
        <ul className="divide-y divide-zinc-800">
          {components.map((c) => (
            <VersionRow key={c.name} component={c} />
          ))}
        </ul>
      )}
      {check?.manifest?.error && (
        <p className="mt-3 text-xs leading-relaxed text-zinc-500">
          Could not reach the release channel: {check.manifest.error}
        </p>
      )}
      <p className="mt-3 text-xs leading-relaxed text-zinc-500">
        Every action in this app is a call to the CLI bundled beside it. Base
        daemon updates show on each Base&apos;s Overview when a checkup finds one.
      </p>
    </Panel>
  );
}

function VersionRow({ component }: { component: VersionComponent }) {
  const label =
    component.name === "cli"
      ? "ownbasectl"
      : component.name === "app"
        ? "App"
        : component.name;
  return (
    <li className="flex flex-col gap-1.5 py-2.5 first:pt-0 last:pb-0">
      <div className="flex items-center justify-between gap-3 text-sm">
        <span className="text-zinc-200">{label}</span>
        <span className="flex items-center gap-2">
          <span className="font-mono text-xs text-zinc-400">{component.current}</span>
          <VersionBadge status={component.status} />
        </span>
      </div>
      {component.status === "behind" && component.latest && (
        <p className="text-xs text-zinc-500">Latest {component.latest}</p>
      )}
      {component.guide && (
        <div className="flex flex-wrap items-center gap-2">
          <CommandLine>{component.guide}</CommandLine>
          <CopyButton value={component.guide} label="Copy" />
        </div>
      )}
    </li>
  );
}

function VersionBadge({ status }: { status: VersionStatus }) {
  switch (status) {
    case "current":
      return <Badge tone="good">current</Badge>;
    case "behind":
      return <Badge tone="warn">update</Badge>;
    case "ahead":
      return <Badge tone="info">ahead</Badge>;
    case "dev":
      return <Badge tone="unknown">dev</Badge>;
    default:
      return <Badge tone="unknown">unknown</Badge>;
  }
}

function ChangePassword() {
  const [open, setOpen] = useState(false);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  const mismatch =
    confirm.length > 0 && password !== confirm ? "The two passwords do not match." : null;

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!password || mismatch) return;
    setBusy(true);
    setError(null);
    try {
      await api.vaultChangePassword(password);
      setDone(true);
      setOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
      setPassword("");
      setConfirm("");
    }
  }

  return (
    <Panel
      title="Master password"
      subtitle="Re-encrypts the vault. The old password is not needed — it is already unlocked."
      action={
        !open && (
          <Button variant="secondary" onClick={() => setOpen(true)}>
            Change
          </Button>
        )
      }
    >
      {done && !open && (
        <p className="text-sm text-emerald-300">
          Changed. Every machine that syncs this file will need the new password.
        </p>
      )}
      {!open && !done && (
        <p className="text-sm leading-relaxed text-zinc-500">
          You can also do this from a terminal with{" "}
          <CommandLine>ownbasectl vault passwd</CommandLine>.
        </p>
      )}
      {open && (
        <form onSubmit={submit} className="space-y-4">
          <Field label="New master password">
            <Input
              type="password"
              autoFocus
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
            />
          </Field>
          <Field label="Confirm" error={mismatch}>
            <Input
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              autoComplete="new-password"
            />
          </Field>
          {error && <ErrorNote title="Could not change the password" detail={error} />}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="primary"
              busy={busy}
              disabled={!password || Boolean(mismatch)}
            >
              Change password
            </Button>
          </div>
        </form>
      )}
    </Panel>
  );
}
